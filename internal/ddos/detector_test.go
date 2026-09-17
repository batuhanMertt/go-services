package ddos

import (
	"testing"
	"time"
)

// fakeClock, testte zamanı elle ilerletmek için kullanılır.
type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time          { return f.t }
func (f *fakeClock) advance(d time.Duration) { f.t = f.t.Add(d) }

func newTestDetector(cfg DetectorConfig) (*Detector, *fakeClock) {
	d := NewDetector(cfg)
	fc := &fakeClock{t: time.Now()}
	d.now = fc.now
	d.windowStart = fc.t
	return d, fc
}

func TestDetectorStaysNormalUnderThreshold(t *testing.T) {
	d, fc := newTestDetector(DetectorConfig{Window: time.Second, Threshold: 100, Cooldown: 5 * time.Second})

	for i := 0; i < 50; i++ {
		d.Observe()
	}
	fc.advance(time.Second)
	d.Observe() // pencereyi kapatır ve değerlendirir

	if d.Mode() != ModeNormal {
		t.Fatalf("mode = %v, beklenen normal (50 req/s < 100 eşik)", d.Mode())
	}
	if d.Multiplier() != 1.0 {
		t.Fatalf("multiplier = %v, beklenen 1.0", d.Multiplier())
	}
}

func TestDetectorTripsAttackModeOverThreshold(t *testing.T) {
	d, fc := newTestDetector(DetectorConfig{Window: time.Second, Threshold: 100, Cooldown: 5 * time.Second})

	for i := 0; i < 500; i++ {
		d.Observe()
	}
	fc.advance(time.Second)
	d.Observe()

	if d.Mode() != ModeAttack {
		t.Fatalf("mode = %v, beklenen attack (500 req/s > 100 eşik)", d.Mode())
	}
	if d.Multiplier() >= 1.0 {
		t.Fatalf("attack modunda multiplier daraltılmadı: %v", d.Multiplier())
	}
	if d.Since().IsZero() {
		t.Fatal("attack başlangıç zamanı kaydedilmedi")
	}
}

func TestDetectorRecoversAfterCooldown(t *testing.T) {
	d, fc := newTestDetector(DetectorConfig{Window: time.Second, Threshold: 100, Cooldown: 3 * time.Second})

	// Saldırı penceresi.
	for i := 0; i < 500; i++ {
		d.Observe()
	}
	fc.advance(time.Second)
	d.Observe()
	if d.Mode() != ModeAttack {
		t.Fatal("saldırı tespit edilmedi")
	}

	// Cooldown dolmadan sakin bir pencere gelirse mod HÂLÂ attack kalmalı
	// (pulse saldırıya karşı koruma).
	fc.advance(500 * time.Millisecond)
	d.Observe()
	fc.advance(time.Second)
	d.Observe()
	if d.Mode() != ModeAttack {
		t.Fatal("cooldown dolmadan mod normale döndü")
	}

	// Cooldown dolduktan sonra sakin pencere: normale dönmeli.
	fc.advance(3 * time.Second)
	d.Observe()
	fc.advance(time.Second)
	d.Observe()
	if d.Mode() != ModeNormal {
		t.Fatalf("cooldown dolduktan sonra mode = %v, beklenen normal", d.Mode())
	}
}

func TestDetectorDefaultsAreSane(t *testing.T) {
	d := NewDetector(DetectorConfig{})
	if d.window <= 0 || d.threshold <= 0 || d.cooldown <= 0 {
		t.Fatalf("varsayılanlar geçersiz: window=%v threshold=%v cooldown=%v",
			d.window, d.threshold, d.cooldown)
	}
	if d.Mode() != ModeNormal {
		t.Fatal("yeni detector normal modda başlamalı")
	}
}
