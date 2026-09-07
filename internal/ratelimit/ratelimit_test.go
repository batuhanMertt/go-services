package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newLimiter(t *testing.T, failClosed bool) (*Limiter, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	return New(client, failClosed), mr
}

func TestAllowsUpToBurst(t *testing.T) {
	l, _ := newLimiter(t, false)
	ctx := context.Background()
	rule := Rule{Rate: 10, Burst: 5}

	for i := 1; i <= 5; i++ {
		res, err := l.Allow(ctx, "tool", "billing", rule)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Allowed {
			t.Fatalf("burst içindeki %d. istek reddedildi", i)
		}
	}

	res, err := l.Allow(ctx, "tool", "billing", rule)
	if err != nil {
		t.Fatal(err)
	}
	if res.Allowed {
		t.Fatal("burst aşıldığı hâlde istek kabul edildi")
	}
	if res.RetryAfter <= 0 {
		t.Fatal("reddedilen istekte Retry-After hesaplanmadı")
	}
}

func TestScopesAreIsolated(t *testing.T) {
	l, _ := newLimiter(t, false)
	ctx := context.Background()
	rule := Rule{Rate: 10, Burst: 2}

	// "billing" kotasını tüket.
	l.Allow(ctx, "tool", "billing", rule)
	l.Allow(ctx, "tool", "billing", rule)

	if res, _ := l.Allow(ctx, "tool", "billing", rule); res.Allowed {
		t.Fatal("billing kotası tükenmemiş görünüyor")
	}

	// Farklı tool etkilenmemeli.
	if res, _ := l.Allow(ctx, "tool", "crm", rule); !res.Allowed {
		t.Fatal("farklı tool'un kotası etkilendi")
	}
	// Farklı kapsam da etkilenmemeli.
	if res, _ := l.Allow(ctx, "ip", "billing", rule); !res.Allowed {
		t.Fatal("farklı kapsam etkilendi")
	}
}

func TestTokensRefillOverTime(t *testing.T) {
	l, mr := newLimiter(t, false)
	ctx := context.Background()
	rule := Rule{Rate: 2, Burst: 2} // saniyede 2 token

	// Script zamanı Redis'in TIME komutundan okur; miniredis'te bu saati
	// SetTime ile kontrol ediyoruz (FastForward yalnızca TTL'leri etkiler).
	now := time.Now()
	mr.SetTime(now)

	l.Allow(ctx, "tool", "x", rule)
	l.Allow(ctx, "tool", "x", rule)

	if res, _ := l.Allow(ctx, "tool", "x", rule); res.Allowed {
		t.Fatal("kova boşken istek kabul edildi")
	}

	mr.SetTime(now.Add(2 * time.Second))

	if res, _ := l.Allow(ctx, "tool", "x", rule); !res.Allowed {
		t.Fatal("token'lar zamanla yenilenmedi")
	}
}

func TestBurstIsCapped(t *testing.T) {
	l, mr := newLimiter(t, false)
	ctx := context.Background()
	rule := Rule{Rate: 10, Burst: 3}

	now := time.Now()
	mr.SetTime(now)

	l.Allow(ctx, "tool", "x", rule)

	// Uzun süre bekleyip kovanın taşmadığını doğrula.
	mr.SetTime(now.Add(time.Hour))

	allowed := 0
	for i := 0; i < 10; i++ {
		if res, _ := l.Allow(ctx, "tool", "x", rule); res.Allowed {
			allowed++
		}
	}

	if allowed != 3 {
		t.Fatalf("kova kapasitesi aşıldı: %d istek geçti, beklenen 3", allowed)
	}
}

func TestInvalidRuleDisablesLimiting(t *testing.T) {
	l, _ := newLimiter(t, false)
	ctx := context.Background()

	// Yanlış yapılandırma trafiği kesmemeli.
	for _, rule := range []Rule{{}, {Rate: 0, Burst: 10}, {Rate: 10, Burst: 0}, {Rate: -1, Burst: -1}} {
		res, err := l.Allow(ctx, "tool", "x", rule)
		if err != nil {
			t.Fatalf("geçersiz kural hata üretti: %v", err)
		}
		if !res.Allowed {
			t.Fatalf("geçersiz kural (%+v) isteği engelledi", rule)
		}
	}
}

func TestFailOpenWhenRedisDown(t *testing.T) {
	l, mr := newLimiter(t, false)
	mr.Close() // Redis'i düşür

	res, err := l.Allow(context.Background(), "tool", "x", Rule{Rate: 1, Burst: 1})

	if err == nil {
		t.Fatal("Redis erişilemezken hata raporlanmadı")
	}
	if !res.Allowed {
		t.Fatal("fail-open modunda istek reddedildi")
	}
	if !res.Degraded {
		t.Fatal("degraded bayrağı işaretlenmedi")
	}
}

func TestFailClosedWhenRedisDown(t *testing.T) {
	l, mr := newLimiter(t, true) // fail-closed
	mr.Close()

	res, _ := l.Allow(context.Background(), "tool", "x", Rule{Rate: 1, Burst: 1})

	if res.Allowed {
		t.Fatal("fail-closed modunda istek kabul edildi")
	}
	if !res.Degraded {
		t.Fatal("degraded bayrağı işaretlenmedi")
	}
}

func TestNilLimiterIsSafe(t *testing.T) {
	// Redis hiç kurulamadıysa limiter nil client ile oluşturulur.
	l := New(nil, false)

	res, err := l.Allow(context.Background(), "tool", "x", Rule{Rate: 1, Burst: 1})
	if err != nil {
		t.Fatalf("nil client hata üretti: %v", err)
	}
	if !res.Allowed {
		t.Fatal("nil client fail-open olmalıydı")
	}
}

func TestReset(t *testing.T) {
	l, _ := newLimiter(t, false)
	ctx := context.Background()
	rule := Rule{Rate: 1, Burst: 1}

	l.Allow(ctx, "tool", "x", rule)
	if res, _ := l.Allow(ctx, "tool", "x", rule); res.Allowed {
		t.Fatal("kova tükenmiş olmalıydı")
	}

	if err := l.Reset(ctx, "tool", "x"); err != nil {
		t.Fatal(err)
	}
	if res, _ := l.Allow(ctx, "tool", "x", rule); !res.Allowed {
		t.Fatal("Reset sonrası kova dolmadı")
	}
}

func TestAllowNConsumesMultipleTokens(t *testing.T) {
	l, _ := newLimiter(t, false)
	ctx := context.Background()
	rule := Rule{Rate: 10, Burst: 10}

	if res, _ := l.AllowN(ctx, "tool", "x", rule, 7); !res.Allowed {
		t.Fatal("7 token'lık istek reddedildi")
	}
	if res, _ := l.AllowN(ctx, "tool", "x", rule, 5); res.Allowed {
		t.Fatal("kalan 3 token ile 5 token'lık istek kabul edildi")
	}
}
