package breaker

import (
	"sync"
	"testing"
	"time"
)

func newTestGroup() (*Group, func(time.Duration)) {
	now := time.Now()
	g := NewGroup(Config{FailureThreshold: 3, OpenDuration: 30 * time.Second, SuccessThreshold: 1})
	g.SetClock(func() time.Time { return now })

	advance := func(d time.Duration) { now = now.Add(d) }
	return g, advance
}

func TestStartsClosed(t *testing.T) {
	g, _ := newTestGroup()

	if got := g.State("sub-1"); got != StateClosed {
		t.Fatalf("başlangıç durumu %v, beklenen closed", got)
	}
	if allowed, _ := g.Allow("sub-1"); !allowed {
		t.Fatal("kapalı devre isteği engellememeli")
	}
}

func TestOpensAfterThreshold(t *testing.T) {
	g, _ := newTestGroup()

	// Eşiğin bir altı: hâlâ kapalı olmalı.
	for i := 0; i < 2; i++ {
		g.Failure("sub-1")
	}
	if g.State("sub-1") != StateClosed {
		t.Fatal("eşik aşılmadan devre açıldı")
	}

	g.Failure("sub-1") // 3. hata → eşik
	if g.State("sub-1") != StateOpen {
		t.Fatal("eşik aşıldığında devre açılmadı")
	}

	if allowed, _ := g.Allow("sub-1"); allowed {
		t.Fatal("açık devre isteğe izin verdi")
	}
}

func TestSuccessResetsFailureCount(t *testing.T) {
	g, _ := newTestGroup()

	g.Failure("sub-1")
	g.Failure("sub-1")
	g.Success("sub-1") // sayaç sıfırlanmalı
	g.Failure("sub-1")
	g.Failure("sub-1")

	if g.State("sub-1") != StateClosed {
		t.Fatal("başarı sayacı sıfırlamadı — ardışık olmayan hatalar devreyi açtı")
	}
}

func TestHalfOpenAfterTimeout(t *testing.T) {
	g, advance := newTestGroup()

	for i := 0; i < 3; i++ {
		g.Failure("sub-1")
	}
	if allowed, _ := g.Allow("sub-1"); allowed {
		t.Fatal("devre hemen açılmalıydı")
	}

	advance(31 * time.Second)

	allowed, probe := g.Allow("sub-1")
	if !allowed {
		t.Fatal("süre dolduğunda deneme isteğine izin verilmedi")
	}
	if !probe {
		t.Fatal("bu bir yarı açık deneme olarak işaretlenmeliydi")
	}
}

func TestHalfOpenAllowsSingleProbe(t *testing.T) {
	g, advance := newTestGroup()

	for i := 0; i < 3; i++ {
		g.Failure("sub-1")
	}
	advance(31 * time.Second)

	if allowed, _ := g.Allow("sub-1"); !allowed {
		t.Fatal("ilk deneme geçmeliydi")
	}
	// İkinci istek beklemeli: çökük bir servise yığın hâlinde istek gitmemeli.
	if allowed, _ := g.Allow("sub-1"); allowed {
		t.Fatal("yarı açık durumda ikinci isteğe izin verildi")
	}
}

func TestHalfOpenSuccessClosesCircuit(t *testing.T) {
	g, advance := newTestGroup()

	for i := 0; i < 3; i++ {
		g.Failure("sub-1")
	}
	advance(31 * time.Second)

	g.Allow("sub-1")
	g.Success("sub-1")

	if g.State("sub-1") != StateClosed {
		t.Fatal("başarılı deneme devreyi kapatmadı")
	}
	if allowed, _ := g.Allow("sub-1"); !allowed {
		t.Fatal("kapanan devre isteği engelliyor")
	}
}

func TestHalfOpenFailureReopensImmediately(t *testing.T) {
	g, advance := newTestGroup()

	for i := 0; i < 3; i++ {
		g.Failure("sub-1")
	}
	advance(31 * time.Second)

	g.Allow("sub-1")
	g.Failure("sub-1") // hedef hâlâ bozuk

	if allowed, _ := g.Allow("sub-1"); allowed {
		t.Fatal("yarı açıkta başarısız deneme sonrası devre kapanmadı")
	}
}

// Bulkhead'in tamamlayıcısı: bir subscriber'ın devresi açılınca diğerleri
// etkilenmemeli.
func TestCircuitsAreIsolatedPerKey(t *testing.T) {
	g, _ := newTestGroup()

	for i := 0; i < 5; i++ {
		g.Failure("bozuk-subscriber")
	}

	if g.State("bozuk-subscriber") != StateOpen {
		t.Fatal("bozuk subscriber'ın devresi açılmadı")
	}
	if g.State("saglikli-subscriber") != StateClosed {
		t.Fatal("sağlıklı subscriber etkilendi — izolasyon yok")
	}
	if allowed, _ := g.Allow("saglikli-subscriber"); !allowed {
		t.Fatal("sağlıklı subscriber engellendi")
	}
}

func TestSnapshotReportsAllCircuits(t *testing.T) {
	g, _ := newTestGroup()

	for i := 0; i < 3; i++ {
		g.Failure("a")
	}
	g.Success("b")

	snap := g.Snapshot()

	if snap["a"] != StateOpen {
		t.Errorf("a durumu %v, beklenen open", snap["a"])
	}
	if snap["b"] != StateClosed {
		t.Errorf("b durumu %v, beklenen closed", snap["b"])
	}
}

func TestConcurrentAccessIsSafe(t *testing.T) {
	g := NewGroup(DefaultConfig())

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := "sub-" + string(rune('a'+n%5))

			g.Allow(key)
			if n%2 == 0 {
				g.Failure(key)
			} else {
				g.Success(key)
			}
			g.State(key)
			g.Snapshot()
		}(i)
	}
	wg.Wait() // -race ile çalıştığında veri yarışı tespit edilir
}

func TestStateString(t *testing.T) {
	tests := map[State]string{
		StateClosed:   "closed",
		StateOpen:     "open",
		StateHalfOpen: "half_open",
	}
	for s, want := range tests {
		if s.String() != want {
			t.Errorf("%d.String() = %q, beklenen %q", s, s.String(), want)
		}
	}
}
