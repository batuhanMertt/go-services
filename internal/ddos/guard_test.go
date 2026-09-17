package ddos

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestGuard(t *testing.T, cfg GuardConfig) (*Guard, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	return NewGuard(client, cfg), mr
}

func TestGuardBansAfterThreshold(t *testing.T) {
	g, _ := newTestGuard(t, GuardConfig{StrikeWindow: 60, StrikeThreshold: 3, BanTTL: 300})
	ctx := context.Background()
	ip := "203.0.113.5"

	if g.Banned(ctx, ip) {
		t.Fatal("hiç strike almamış IP banlı görünüyor")
	}

	g.Strike(ctx, ip)
	g.Strike(ctx, ip)
	if g.Banned(ctx, ip) {
		t.Fatal("eşik aşılmadan IP banlandı")
	}

	g.Strike(ctx, ip) // 3. strike eşiği aşar
	if !g.Banned(ctx, ip) {
		t.Fatal("eşik aşıldığı hâlde IP banlanmadı")
	}
}

func TestGuardIsolatesIPs(t *testing.T) {
	g, _ := newTestGuard(t, GuardConfig{StrikeWindow: 60, StrikeThreshold: 2, BanTTL: 300})
	ctx := context.Background()

	g.Strike(ctx, "1.1.1.1")
	g.Strike(ctx, "1.1.1.1")
	if !g.Banned(ctx, "1.1.1.1") {
		t.Fatal("1.1.1.1 banlanmalıydı")
	}
	if g.Banned(ctx, "2.2.2.2") {
		t.Fatal("farklı IP etkilendi")
	}
}

func TestGuardStrikesExpireWithWindow(t *testing.T) {
	g, mr := newTestGuard(t, GuardConfig{StrikeWindow: 60, StrikeThreshold: 3, BanTTL: 300})
	ctx := context.Background()
	ip := "198.51.100.9"

	g.Strike(ctx, ip)
	g.Strike(ctx, ip)
	if got := g.Strikes(ctx, ip); got != 2 {
		t.Fatalf("Strikes() = %d, beklenen 2", got)
	}

	mr.FastForward(61 * time.Second)
	if got := g.Strikes(ctx, ip); got != 0 {
		t.Fatalf("pencere dolduktan sonra Strikes() = %d, beklenen 0", got)
	}
	if g.Banned(ctx, ip) {
		t.Fatal("pencere dolduktan sonra IP banlı kalmamalı")
	}
}

func TestGuardUnban(t *testing.T) {
	g, _ := newTestGuard(t, GuardConfig{StrikeWindow: 60, StrikeThreshold: 1, BanTTL: 300})
	ctx := context.Background()
	ip := "192.0.2.77"

	g.Strike(ctx, ip)
	if !g.Banned(ctx, ip) {
		t.Fatal("IP banlanmadı")
	}

	if err := g.Unban(ctx, ip); err != nil {
		t.Fatal(err)
	}
	if g.Banned(ctx, ip) {
		t.Fatal("Unban sonrası IP hâlâ banlı")
	}
	if got := g.Strikes(ctx, ip); got != 0 {
		t.Fatalf("Unban sonrası Strikes() = %d, beklenen 0", got)
	}
}

func TestGuardFailOpenWhenRedisDown(t *testing.T) {
	g, mr := newTestGuard(t, GuardConfig{})
	mr.Close()

	ctx := context.Background()
	// Redis erişilemezken ne Strike ne Banned patlamalı; fail-open.
	g.Strike(ctx, "10.0.0.1")
	if g.Banned(ctx, "10.0.0.1") {
		t.Fatal("Redis kesintisinde fail-open olmalıydı")
	}
}

func TestGuardNilSafe(t *testing.T) {
	g := NewGuard(nil, GuardConfig{})
	ctx := context.Background()

	g.Strike(ctx, "10.0.0.1") // panic etmemeli
	if g.Banned(ctx, "10.0.0.1") {
		t.Fatal("nil client ile ban olmamalı")
	}
	if err := g.Unban(ctx, "10.0.0.1"); err != nil {
		t.Fatalf("nil client ile Unban hata verdi: %v", err)
	}
}
