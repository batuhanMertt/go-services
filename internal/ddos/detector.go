package ddos

import (
	"sync"
	"sync/atomic"
	"time"
)

// Mode, sistemin o anki trafik değerlendirmesi.
type Mode int

const (
	// ModeNormal: trafik beklenen sınırlar içinde, standart kurallar geçerli.
	ModeNormal Mode = iota
	// ModeAttack: eşik aşıldı, kurallar sıkılaştırılmalı (Multiplier < 1).
	ModeAttack
)

func (m Mode) String() string {
	if m == ModeAttack {
		return "attack"
	}
	return "normal"
}

// Detector, saniyelik istek hacmini gözlemleyip global saldırı modunu
// belirler. Amaç tekil bir IP'yi değerlendirmek değil (bunu ratelimit +
// Guard yapar); "şu anda platform genelinde anormal bir yük var mı" sorusuna
// cevap vermektir. Cevap "evet" olduğunda çağıran taraf (handler) normal
// rate limit kurallarını Multiplier ile sıkılaştırır — böylece saldırı
// sırasında gerçek kullanıcılar tamamen kapatılmaz, sadece daha sıkı bir
// kotaya tabi olurlar.
//
// Tamamen bellek içi ve tek instance kapsamlıdır: her servis instance'ı
// kendi yükünü değerlendirir. Bu bilinçli bir tercihtir — instance'lar arası
// koordinasyon (örn. Redis pub/sub ile) ekstra bir bağımlılık ve gecikme
// getirirdi; oysa saldırı tespiti saniyeler içinde, YEREL olarak devreye
// girmelidir.
type Detector struct {
	mu sync.Mutex

	windowStart time.Time
	windowCount int64

	window    time.Duration
	threshold float64 // bu eşiği aşan req/s "attack" sayılır
	cooldown  time.Duration

	mode        atomic.Int32 // Mode olarak okunur
	attackUntil time.Time
	attackSince time.Time

	now func() time.Time
}

// DetectorConfig, Detector'ün davranışını belirler.
type DetectorConfig struct {
	// Window, istek hızının ölçüldüğü pencere (örn. 1s).
	Window time.Duration
	// Threshold, aşıldığında ATTACK moduna geçilen req/s eşiği.
	Threshold float64
	// Cooldown, son anormal pencereden sonra NORMAL'e dönmeden önce
	// beklenecek süre. Saldırgan trafiği aralıklı gönderirse (örn.
	// pulse/burst saldırısı) modun her pencerede aç/kapa yapmasını önler.
	Cooldown time.Duration
}

// NewDetector, verilen ayarlarla bir Detector oluşturur. Eksik/geçersiz
// alanlar makul varsayılanlarla doldurulur.
func NewDetector(cfg DetectorConfig) *Detector {
	if cfg.Window <= 0 {
		cfg.Window = time.Second
	}
	if cfg.Threshold <= 0 {
		cfg.Threshold = 1000 // varsayılan: 1000 req/s üstü şüpheli kabul edilir
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 10 * time.Second
	}

	d := &Detector{
		window:    cfg.Window,
		threshold: cfg.Threshold,
		cooldown:  cfg.Cooldown,
		now:       time.Now,
	}
	d.windowStart = d.now()
	return d
}

// Observe, bir isteğin geldiğini kaydeder. Handler zincirinde her istekte
// (rate limit'ten ÖNCE, çünkü reddedilen istekler de yük bindirir) çağrılmalıdır.
func (d *Detector) Observe() {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := d.now()
	elapsed := now.Sub(d.windowStart)

	if elapsed >= d.window {
		rps := float64(d.windowCount) / elapsed.Seconds()
		if rps >= d.threshold {
			if d.attackSince.IsZero() {
				d.attackSince = now
			}
			d.attackUntil = now.Add(d.cooldown)
			d.mode.Store(int32(ModeAttack))
		} else if !now.Before(d.attackUntil) {
			// Cooldown doldu ve bu pencere de sakin: normale dön.
			d.mode.Store(int32(ModeNormal))
			d.attackSince = time.Time{}
		}
		d.windowStart = now
		d.windowCount = 0
	}

	d.windowCount++
}

// Mode, o anki değerlendirmeyi atomik olarak döner (kilitsiz, sık çağrı için
// güvenli).
func (d *Detector) Mode() Mode {
	return Mode(d.mode.Load())
}

// Since, geçerli ATTACK modunun ne zaman başladığını döner. Attack modunda
// değilse sıfır zaman döner.
func (d *Detector) Since() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.attackSince
}

// Multiplier, o anki moda göre rate limit kurallarına uygulanacak çarpanı
// döner: NORMAL'de 1.0 (değişiklik yok), ATTACK'ta < 1.0 (daha sıkı).
func (d *Detector) Multiplier() float64 {
	if d.Mode() == ModeAttack {
		return 0.2 // ATTACK modunda kapasitenin %20'sine düş
	}
	return 1.0
}
