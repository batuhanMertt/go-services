// Package lock, Redis tabanlı dağıtık kilit sağlar.
//
// Problem: `events.delivery` topic'inde at-least-once teslimat vardır. Bir
// consumer mesajı işlerken çökerse (offset commit edilmeden), mesaj başka bir
// worker'a yeniden dağıtılır. Rebalance sırasında da aynı mesaj iki worker'a
// düşebilir. Kilit olmadan subscriber AYNI webhook'u iki kez alır.
//
// Çözüm: teslimat görevinin id'si üzerinde kısa ömürlü bir kilit. Kilit
// yalnızca "aynı anda iki worker" durumunu engeller; kalıcı idempotency
// garantisi delivery_tasks tablosundaki durum makinesiyle sağlanır. Yani
// kilit bir optimizasyondur, tek savunma hattı değildir — Redis düşse bile
// veri bütünlüğü DB seviyesinde korunur.
package lock

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/mindmons/integration-hub/internal/cryptox"
	"github.com/mindmons/integration-hub/internal/redisx"
)

// ErrNotAcquired, kilidin başkasında olduğunu belirtir.
var ErrNotAcquired = errors.New("kilit alınamadı")

// releaseScript, kilidi YALNIZCA sahibi serbest bırakabilsin diye token
// karşılaştırmasını atomik yapar.
//
// Neden gerekli? Düz `DEL key` kullanılsaydı şu senaryo oluşurdu: A kilidi
// alır, işi TTL'den uzun sürer, kilit kendiliğinden düşer, B kilidi alır,
// sonra A işini bitirip DEL çağırır ve B'nin kilidini silmiş olur.
const releaseScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`

// extendScript, uzun süren işlerde kilit süresini sahibiyken uzatır.
const extendScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`

// Locker, kilit üretir.
type Locker struct {
	rdb     *redis.Client
	release *redis.Script
	extend  *redis.Script
}

func New(rdb *redis.Client) *Locker {
	return &Locker{
		rdb:     rdb,
		release: redis.NewScript(releaseScript),
		extend:  redis.NewScript(extendScript),
	}
}

// Handle, alınmış bir kilidi temsil eder.
type Handle struct {
	locker *Locker
	key    string
	token  string
	// Degraded true ise Redis erişilemedi; kilit alınmış SAYILDI (fail-open).
	Degraded bool
}

// Acquire, verilen isim için kilit almaya çalışır.
// Alınamazsa ErrNotAcquired döner (bloklamaz — Kafka mesajı ertelenir).
func (l *Locker) Acquire(ctx context.Context, name string, ttl time.Duration) (*Handle, error) {
	if l == nil || l.rdb == nil {
		return &Handle{locker: l, Degraded: true}, nil
	}

	token, err := cryptox.RandomHex(16)
	if err != nil {
		return nil, err
	}

	key := redisx.Key(redisx.NSLock, name)

	ok, err := l.rdb.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		// Redis erişilemiyor: teslimatı tamamen durdurmak yerine devam edilir.
		// Mükerrer gönderim riski, teslimatın hiç yapılmamasından yeğdir
		// (at-least-once garantisi zaten mükerrerliği kabul eder).
		return &Handle{locker: l, key: key, token: token, Degraded: true},
			fmt.Errorf("lock: redis hatası, fail-open: %w", err)
	}
	if !ok {
		return nil, ErrNotAcquired
	}

	return &Handle{locker: l, key: key, token: token}, nil
}

// Release, kilidi sahibiyse serbest bırakır.
func (h *Handle) Release(ctx context.Context) error {
	if h == nil || h.locker == nil || h.locker.rdb == nil || h.key == "" || h.Degraded {
		return nil
	}
	return h.locker.release.Run(ctx, h.locker.rdb, []string{h.key}, h.token).Err()
}

// Extend, kilit süresini uzatır (uzun webhook timeout'ları için).
func (h *Handle) Extend(ctx context.Context, ttl time.Duration) error {
	if h == nil || h.locker == nil || h.locker.rdb == nil || h.key == "" || h.Degraded {
		return nil
	}
	return h.locker.extend.Run(ctx, h.locker.rdb, []string{h.key}, h.token, ttl.Milliseconds()).Err()
}
