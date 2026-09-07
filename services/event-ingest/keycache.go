package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mindmons/integration-hub/internal/auth"
	"github.com/mindmons/integration-hub/internal/cryptox"
)

var (
	ErrKeyUnknown   = errors.New("api key tanınmıyor")
	ErrKeyRevoked   = errors.New("api key iptal edilmiş")
	ErrKeyExpired   = errors.New("api key süresi dolmuş")
	ErrToolBlocked  = errors.New("tool aktif değil")
	ErrNotPublisher = errors.New("tool event yayınlama yetkisine sahip değil")
)

// AuthedTool, doğrulanmış bir isteğin kimlik bilgisi.
type AuthedTool struct {
	KeyID      string
	ToolID     string
	ToolSlug   string
	HMACSecret []byte
}

// cacheEntry, önbellekteki tek kayıt.
type cacheEntry struct {
	record    *APIKeyRecord
	secret    []byte
	expiresAt time.Time
}

// KeyCache, API anahtarlarını kısa süreli olarak bellekte tutar.
//
// Neden önbellek? Doğrulama HER ingest isteğinde yapılır. Önbelleksiz her
// istek en az bir DB gidiş-dönüşü + bir AES çözme demektir; p95 < 50 ms
// hedefi (NFR-2.1) yüksek hacimde riske girerdi.
//
// Neden KISA TTL (30 sn)? Anahtar iptali veya rotasyonu en geç TTL kadar
// gecikmeyle etkili olur. Sınırsız önbellek, iptal edilmiş bir anahtarın
// süresiz kabul edilmesi anlamına gelirdi — kabul edilemez. 30 saniye,
// performans ile güvenlik arasında bilinçli bir dengedir.
//
// Neden Redis değil de yerel bellek? Anahtar doğrulaması saniyede on binlerce
// kez çalışır; Redis gidiş-dönüşü (~0.3 ms) bile burada baskındır. Yerel
// önbellek sıfır ağ maliyetlidir ve her instance kendi kopyasını tutar.
type KeyCache struct {
	store  *Store
	sealer *cryptox.Sealer
	ttl    time.Duration

	mu      sync.RWMutex
	entries map[string]cacheEntry

	// singleflight benzeri: aynı prefix için eşzamanlı DB sorgularını teke indirir
	loading sync.Map
}

func NewKeyCache(store *Store, sealer *cryptox.Sealer, ttl time.Duration) *KeyCache {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &KeyCache{
		store:   store,
		sealer:  sealer,
		ttl:     ttl,
		entries: make(map[string]cacheEntry),
	}
}

// Authenticate, ham API key'i doğrular ve tool kimliğini döner.
func (c *KeyCache) Authenticate(ctx context.Context, plaintextKey string) (*AuthedTool, error) {
	prefix, err := auth.ExtractPrefix(plaintextKey)
	if err != nil {
		return nil, ErrKeyUnknown
	}

	entry, err := c.lookup(ctx, prefix)
	if err != nil {
		return nil, err
	}

	rec := entry.record

	// 1) Anahtar hash'i sabit sürede doğrulanır.
	if err := auth.VerifyAPIKey(plaintextKey, rec.KeyHash); err != nil {
		return nil, ErrKeyUnknown
	}

	// 2) Anahtar yaşam döngüsü kontrolleri.
	now := time.Now()
	switch rec.Status {
	case "revoked":
		return nil, ErrKeyRevoked
	case "rotating":
		// Grace period: eski anahtar geçici olarak hâlâ geçerli (FR-1.3).
		if rec.GraceUntil == nil || now.After(*rec.GraceUntil) {
			return nil, fmt.Errorf("%w: grace period sona erdi", ErrKeyExpired)
		}
	}
	if rec.ExpiresAt != nil && now.After(*rec.ExpiresAt) {
		return nil, ErrKeyExpired
	}

	// 3) Tool durumu.
	if rec.ToolStatus != "active" {
		return nil, ErrToolBlocked
	}
	if rec.ToolKind != "publisher" && rec.ToolKind != "both" {
		return nil, ErrNotPublisher
	}

	return &AuthedTool{
		KeyID:      rec.KeyID,
		ToolID:     rec.ToolID,
		ToolSlug:   rec.ToolSlug,
		HMACSecret: entry.secret,
	}, nil
}

func (c *KeyCache) lookup(ctx context.Context, prefix string) (*cacheEntry, error) {
	c.mu.RLock()
	e, ok := c.entries[prefix]
	c.mu.RUnlock()

	if ok && time.Now().Before(e.expiresAt) {
		return &e, nil
	}

	// Aynı prefix için eşzamanlı yüklemeleri serileştir: soğuk önbellekte
	// yüzlerce istek gelirse DB'ye yüzlerce sorgu gitmesin.
	mu, _ := c.loading.LoadOrStore(prefix, &sync.Mutex{})
	lock, ok := mu.(*sync.Mutex)
	if !ok {
		// Teorik olarak imkânsız (yalnızca *sync.Mutex saklanıyor); yine de
		// panik yerine serileştirmeden devam et.
		lock = &sync.Mutex{}
	}
	lock.Lock()
	defer lock.Unlock()

	// Kilidi beklerken başkası doldurmuş olabilir.
	c.mu.RLock()
	e, ok = c.entries[prefix]
	c.mu.RUnlock()
	if ok && time.Now().Before(e.expiresAt) {
		return &e, nil
	}

	rec, err := c.store.LookupAPIKey(ctx, prefix)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrKeyUnknown
		}
		return nil, err
	}

	secret, err := c.sealer.Open(rec.HMACSecretEnc)
	if err != nil {
		return nil, fmt.Errorf("hmac secret çözülemedi (SECRET_ENCRYPTION_KEY değişmiş olabilir): %w", err)
	}

	entry := cacheEntry{
		record:    rec,
		secret:    secret,
		expiresAt: time.Now().Add(c.ttl),
	}

	c.mu.Lock()
	c.entries[prefix] = entry
	c.mu.Unlock()

	return &entry, nil
}

// Invalidate, bir prefix'i önbellekten düşürür.
func (c *KeyCache) Invalidate(prefix string) {
	c.mu.Lock()
	delete(c.entries, prefix)
	c.mu.Unlock()
}

// Sweep, süresi dolmuş kayıtları temizler (bellek sızıntısını önler).
func (c *KeyCache) Sweep() int {
	now := time.Now()
	removed := 0

	c.mu.Lock()
	for k, v := range c.entries {
		if now.After(v.expiresAt) {
			delete(c.entries, k)
			removed++
		}
	}
	c.mu.Unlock()
	return removed
}

// Size, önbellekteki kayıt sayısı (metrik).
func (c *KeyCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}
