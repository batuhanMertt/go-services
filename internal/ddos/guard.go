package ddos

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/mindmons/integration-hub/internal/redisx"
)

// strikeScript, bir IP'nin "kötü" istek sayacını atomik olarak artırır ve
// eşik aşıldığında ayrı bir ban anahtarı yazar.
//
// Neden tek script? "INCR sayaç → oku → eşiği kontrol et → SET ban" ayrı
// komutlar olsaydı, eşzamanlı isteklerde sayaç doğru artsa da ban kararı
// yarış durumuna girebilir (iki istek de "henüz eşik aşılmadı" görüp ban
// yazmayabilir, ya da gecikmeli aşım nedeniyle bir isteklik gecikme olabilir
// — kritik değil ama script atomikliği bunu bedavaya çözüyor).
const strikeScript = `
local strikeKey = KEYS[1]
local banKey     = KEYS[2]
local window     = tonumber(ARGV[1])
local threshold  = tonumber(ARGV[2])
local banTTL     = tonumber(ARGV[3])

local count = redis.call('INCR', strikeKey)
if count == 1 then
  redis.call('EXPIRE', strikeKey, window)
end

local banned = 0
if count >= threshold then
  redis.call('SET', banKey, '1', 'EX', banTTL)
  banned = 1
end

return { count, banned }
`

// GuardConfig, IP itibar sisteminin eşikleri.
type GuardConfig struct {
	// StrikeWindow: bu süre içindeki "kötü" istekler toplanır (örn. 1dk).
	StrikeWindow int // saniye
	// StrikeThreshold: bu sayıya ulaşınca IP geçici olarak banlanır.
	StrikeThreshold int
	// BanTTL: ban süresi (saniye).
	BanTTL int
}

// Guard, tekrarlayan kötü niyetli istekler (geçersiz API key, bozuk imza,
// başarısız login vb.) gönderen IP'leri geçici olarak banlar.
//
// Bu, Detector'dan (global/anlık trafik hacmi) farklı bir savunma
// katmanıdır: Detector "şu an saldırı var mı" sorusuna, Guard ise "BU IP
// güvenilir mi" sorusuna cevap verir. Örn. tek bir IP'den 10.000 başarısız
// login denemesi, global trafik hacmini hiç etkilemeyebilir ama o IP'nin
// banlanması gerekir.
type Guard struct {
	rdb    *redis.Client
	script *redis.Script
	cfg    GuardConfig
}

func NewGuard(rdb *redis.Client, cfg GuardConfig) *Guard {
	if cfg.StrikeWindow <= 0 {
		cfg.StrikeWindow = 60
	}
	if cfg.StrikeThreshold <= 0 {
		cfg.StrikeThreshold = 20
	}
	if cfg.BanTTL <= 0 {
		cfg.BanTTL = 300
	}
	return &Guard{
		rdb:    rdb,
		script: redis.NewScript(strikeScript),
		cfg:    cfg,
	}
}

// Strike, verilen IP için bir "kötü istek" kaydeder. Eşik aşılırsa IP
// otomatik olarak banlanır. Redis erişilemezse hata sessizce yutulur ve
// istek engellenmez (fail-open — Guard bir güvenlik duvarı değil, itibar
// sistemidir; Redis kesintisinde tüm platformu durdurmaya değmez).
func (g *Guard) Strike(ctx context.Context, ip string) {
	if g == nil || g.rdb == nil || ip == "" {
		return
	}

	strikeKey := redisx.Key(redisx.NSGuard, "strikes", ip)
	banKey := redisx.Key(redisx.NSGuard, "ban", ip)

	_, _ = g.script.Run(ctx, g.rdb, []string{strikeKey, banKey},
		g.cfg.StrikeWindow, g.cfg.StrikeThreshold, g.cfg.BanTTL).Slice()
}

// Banned, IP'nin şu anda banlı olup olmadığını söyler. Redis erişilemezse
// false döner (fail-open, aynı gerekçe).
func (g *Guard) Banned(ctx context.Context, ip string) bool {
	if g == nil || g.rdb == nil || ip == "" {
		return false
	}

	banKey := redisx.Key(redisx.NSGuard, "ban", ip)
	n, err := g.rdb.Exists(ctx, banKey).Result()
	if err != nil {
		return false
	}
	return n > 0
}

// Unban, bir IP'nin banını manuel olarak kaldırır (admin aksiyonları ve
// testler için).
func (g *Guard) Unban(ctx context.Context, ip string) error {
	if g == nil || g.rdb == nil || ip == "" {
		return nil
	}
	banKey := redisx.Key(redisx.NSGuard, "ban", ip)
	strikeKey := redisx.Key(redisx.NSGuard, "strikes", ip)
	if err := g.rdb.Del(ctx, banKey, strikeKey).Err(); err != nil {
		return fmt.Errorf("ddos: ban kaldırılamadı: %w", err)
	}
	return nil
}

// Strikes, bir IP'nin geçerli pencere içindeki kötü istek sayısını döner
// (test ve gözlem için; hata durumunda 0).
func (g *Guard) Strikes(ctx context.Context, ip string) int {
	if g == nil || g.rdb == nil || ip == "" {
		return 0
	}
	strikeKey := redisx.Key(redisx.NSGuard, "strikes", ip)
	n, err := g.rdb.Get(ctx, strikeKey).Int()
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0
	}
	return n
}
