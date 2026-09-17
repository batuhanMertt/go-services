package ddos

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/mindmons/integration-hub/internal/httpx"
)

// TestLoadSimulation_AttackVsNormalTraffic, gerçek bir DDoS trafiği yerine
// TÜM zinciri (ConnLimiter → Observe → BanGate → handler) eşzamanlı
// goroutine'lerle bombardımana tutarak simüle eder:
//
//   - Bir "saldırgan" IP hızla çok sayıda geçersiz istek gönderir (brute
//     force / credential stuffing benzeri) → Guard onu banlamalı.
//   - Yüzlerce farklı "normal" IP aynı anda az sayıda istek gönderir →
//     saldırıdan etkilenmeden büyük çoğunlukla kabul edilmeli.
//   - Genel istek hacmi kısa sürede sıçrar → Detector attack moduna
//     geçmeli.
//   - Fırtına bittiğinde ConnLimiter'da sızan slot kalmamalı.
//
// Bu, gerçek ağ trafiği göndermeden (200 Gbit/s'lik hacimsel saldırıyı
// simüle etmek anlamsız/gereksiz) uygulama katmanı savunmalarının davranışını
// doğrulayan bir "white-box + concurrency" testidir.
func TestLoadSimulation_AttackVsNormalTraffic(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	guard := NewGuard(rdb, GuardConfig{StrikeWindow: 60, StrikeThreshold: 50, BanTTL: 300})
	detector := NewDetector(DetectorConfig{Window: 10 * time.Millisecond, Threshold: 50, Cooldown: time.Second})
	conns := NewConnLimiter(1000) // gerçekçi bir tavan; testin amacı sızıntı/deadlock olmadığını doğrulamak

	const attackerIP = "203.0.113.66"

	var (
		attackerRejectedBanned int64 // BanGate tarafından 403
		attackerRejectedAuth   int64 // ban'dan önce handler tarafından 401
		normalOK               int64
		normalRejected         int64
	)

	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := httpx.ClientIP(r)
		if ip == attackerIP {
			guard.Strike(r.Context(), ip)
			atomic.AddInt64(&attackerRejectedAuth, 1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		atomic.AddInt64(&normalOK, 1)
		w.WriteHeader(http.StatusOK)
	})

	chain := conns.Middleware(Observe(detector)(BanGate(guard)(final)))

	doRequest := func(ip string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/events", nil)
		req.Header.Set("X-Forwarded-For", ip)
		rec := httptest.NewRecorder()
		chain.ServeHTTP(rec, req)
		return rec.Code
	}

	// Attack modunu yakalamak için arka planda örnekle: mod, fırtına bittikten
	// sonra cooldown yüzünden hâlâ attack'ta olabilir ya da olmayabilir, bu
	// yüzden ANLIK durumu değil, fırtına SIRASINDA görülüp görülmediğini
	// kontrol ediyoruz.
	var attackModeSeen atomic.Bool
	stopMonitor := make(chan struct{})
	var monitorWG sync.WaitGroup
	monitorWG.Add(1)
	go func() {
		defer monitorWG.Done()
		for {
			select {
			case <-stopMonitor:
				return
			default:
				if detector.Mode() == ModeAttack {
					attackModeSeen.Store(true)
				}
				time.Sleep(time.Millisecond)
			}
		}
	}()

	var wg sync.WaitGroup

	// Saldırgan: 30 goroutine, her biri 10 istek → 300 hızlı geçersiz istek.
	for g := 0; g < 30; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				code := doRequest(attackerIP)
				if code == http.StatusForbidden {
					atomic.AddInt64(&attackerRejectedBanned, 1)
				}
			}
		}()
	}

	// Normal trafik: 300 farklı IP, her biri 1 istek.
	for i := 0; i < 300; i++ {
		ip := "198.51.100." + strconv.Itoa(i%254+1)
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			code := doRequest(ip)
			if code != http.StatusOK {
				atomic.AddInt64(&normalRejected, 1)
			}
		}(ip)
	}

	wg.Wait()
	close(stopMonitor)
	monitorWG.Wait()

	if !guard.Banned(context.Background(), attackerIP) {
		t.Fatal("saldırgan IP fırtına sonunda banlı olmalıydı")
	}
	if atomic.LoadInt64(&attackerRejectedBanned) == 0 {
		t.Fatal("BanGate hiç isteği 403 ile reddetmedi — ban etkisiz kalmış olabilir")
	}
	if !attackModeSeen.Load() {
		t.Fatal("Detector fırtına sırasında ATTACK moduna hiç geçmedi")
	}

	total := 300
	rejected := atomic.LoadInt64(&normalRejected)
	if rejected > int64(total)/20 { // %5'ten fazla kayıp kabul edilemez
		t.Fatalf("normal trafiğin %%%.1f'i reddedildi (saldırıdan sızma): %d/%d",
			100*float64(rejected)/float64(total), rejected, total)
	}

	if got := conns.InFlight(); got != 0 {
		t.Fatalf("fırtına sonrası ConnLimiter'da %d slot sızmış durumda", got)
	}
}
