package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/mindmons/integration-hub/internal/redisx"
	"github.com/mindmons/integration-hub/internal/routing"
)

// CompiledTarget, çalışma zamanında kullanılan hedef: koşullar bir kez
// ayrıştırılmış ve doğrulanmıştır.
type CompiledTarget struct {
	Target
	Conditions []routing.Condition
}

// Matches, event belgesine göre bu hedefin eşleşip eşleşmediğini söyler.
//
// Anlambilim (semantik):
//   - Abonelik VAR ama kural YOK  → koşulsuz eşleşir. Abonelik zaten "bu event
//     tipini istiyorum" beyanıdır; kural olmadan filtre de yoktur.
//   - Birden fazla kural varsa    → OR mantığı. Kurallar birbirinin alternatifi
//     olarak düşünülür ("A durumunda VEYA B durumunda gönder"). AND davranışı
//     tek kural içinde {"op":"and", ...} ile ifade edilir.
func (c *CompiledTarget) Matches(doc map[string]any) bool {
	if len(c.Conditions) == 0 {
		return true
	}
	for _, cond := range c.Conditions {
		if cond.Evaluate(doc) {
			return true
		}
	}
	return false
}

// RuleCache, üç katmanlı önbellek yönetir:
//
//  1. yerel bellek  — sıfır ağ maliyeti, sıcak yol
//  2. Redis         — instance'lar arası paylaşım, soğuk başlangıcı hızlandırır
//  3. PostgreSQL    — tek doğruluk kaynağı (Redis çökse de sistem çalışır)
//
// NFR-1.2 (graceful degradation): Redis erişilemezse 2. katman sessizce atlanır
// ve doğrudan Postgres'ten okunur. Performans düşer, işlevsellik düşmez.
type RuleCache struct {
	store *Store
	rdb   *redis.Client
	log   *slog.Logger
	ttl   time.Duration

	genCheckInterval time.Duration

	mu          sync.RWMutex
	entries     map[string]cacheEntry
	generation  int64
	lastGenPoll time.Time
}

type cacheEntry struct {
	targets   []CompiledTarget
	expiresAt time.Time
}

func NewRuleCache(store *Store, rdb *redis.Client, log *slog.Logger, ttl, genCheck time.Duration) *RuleCache {
	if ttl <= 0 {
		ttl = time.Minute
	}
	if genCheck <= 0 {
		genCheck = 2 * time.Second
	}
	return &RuleCache{
		store:            store,
		rdb:              rdb,
		log:              log,
		ttl:              ttl,
		genCheckInterval: genCheck,
		entries:          make(map[string]cacheEntry),
		generation:       -1,
	}
}

// Targets, bir event tipi için eşleşme adaylarını döner.
func (c *RuleCache) Targets(ctx context.Context, eventType string, version int) ([]CompiledTarget, error) {
	c.checkGeneration(ctx)

	key := eventType + "@" + strconv.Itoa(version)

	// 1. katman: yerel bellek
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if ok && time.Now().Before(entry.expiresAt) {
		return entry.targets, nil
	}

	// 2. katman: Redis
	if targets, ok := c.readRedis(ctx, key); ok {
		c.storeLocal(key, targets)
		return targets, nil
	}

	// 3. katman: PostgreSQL (tek doğruluk kaynağı)
	raw, err := c.store.LoadTargets(ctx, eventType, version)
	if err != nil {
		return nil, err
	}

	compiled := compileTargets(raw, c.log)
	c.storeLocal(key, compiled)
	c.writeRedis(ctx, key, compiled)

	return compiled, nil
}

func compileTargets(raw []Target, log *slog.Logger) []CompiledTarget {
	out := make([]CompiledTarget, 0, len(raw))

	for _, t := range raw {
		ct := CompiledTarget{Target: t}
		for _, condRaw := range t.Conditions {
			cond, err := routing.Parse(condRaw)
			if err != nil {
				// Bozuk kural TÜM aboneliği düşürmez; yalnızca o kural atlanır
				// ve loglanır. Kurallar admin-api'de yazılırken zaten
				// doğrulanıyor; buraya düşen bir hata veri bozulmasına işaret eder.
				log.Warn("bozuk yönlendirme kuralı atlandı",
					slog.String("subscription_id", t.SubscriptionID),
					slog.String("err", err.Error()))
				continue
			}
			ct.Conditions = append(ct.Conditions, cond)
		}
		out = append(out, ct)
	}
	return out
}

func (c *RuleCache) storeLocal(key string, targets []CompiledTarget) {
	c.mu.Lock()
	c.entries[key] = cacheEntry{targets: targets, expiresAt: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}

// redisKey, generation'ı anahtara dahil eder: sayaç arttığında eski anahtarlar
// otomatik olarak "erişilemez" hale gelir ve TTL ile temizlenir. Böylece
// pahalı bir "pattern ile sil" işlemine gerek kalmaz.
func (c *RuleCache) redisKey(key string) string {
	c.mu.RLock()
	gen := c.generation
	c.mu.RUnlock()
	return redisx.Key(redisx.NSRules, strconv.FormatInt(gen, 10), key)
}

func (c *RuleCache) readRedis(ctx context.Context, key string) ([]CompiledTarget, bool) {
	if c.rdb == nil {
		return nil, false
	}

	data, err := c.rdb.Get(ctx, c.redisKey(key)).Bytes()
	if err != nil {
		return nil, false // cache miss veya Redis yok — sessizce Postgres'e düş
	}

	var raw []Target
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, false
	}
	return compileTargets(raw, c.log), true
}

func (c *RuleCache) writeRedis(ctx context.Context, key string, targets []CompiledTarget) {
	if c.rdb == nil {
		return
	}

	plain := make([]Target, 0, len(targets))
	for _, t := range targets {
		plain = append(plain, t.Target)
	}

	data, err := json.Marshal(plain)
	if err != nil {
		return
	}
	if err := c.rdb.Set(ctx, c.redisKey(key), data, c.ttl).Err(); err != nil {
		c.log.Debug("kural önbelleği Redis'e yazılamadı", slog.String("err", err.Error()))
	}
}

// checkGeneration, admin tarafında bir değişiklik olup olmadığını yoklar.
// Sayaç değiştiyse yerel önbellek tamamen boşaltılır.
func (c *RuleCache) checkGeneration(ctx context.Context) {
	if c.rdb == nil {
		return
	}

	c.mu.RLock()
	last := c.lastGenPoll
	c.mu.RUnlock()

	if time.Since(last) < c.genCheckInterval {
		return
	}

	c.mu.Lock()
	c.lastGenPoll = time.Now()
	current := c.generation
	c.mu.Unlock()

	val, err := c.rdb.Get(ctx, redisx.Key(redisx.NSRules, "gen")).Int64()
	if err != nil {
		// errors.Is kullanılıyor: go-redis bazı yollarda hatayı sarmalar,
		// düz `==` karşılaştırması sarmalanmış hatada sessizce başarısız olur.
		if errors.Is(err, redis.Nil) {
			val = 0 // henüz hiç değişiklik yapılmamış
		} else {
			return // Redis erişilemiyor: TTL yeterli güvence
		}
	}

	if val != current {
		c.mu.Lock()
		c.generation = val
		c.entries = make(map[string]cacheEntry)
		c.mu.Unlock()

		c.log.Info("kural önbelleği geçersiz kılındı",
			slog.Int64("generation", val))
	}
}

// Size, önbellekteki event tipi sayısı (metrik).
func (c *RuleCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}
