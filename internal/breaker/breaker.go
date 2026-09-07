// Package breaker, subscriber başına circuit breaker uygular.
//
// Problem ("slow / bad neighbor"): Bir subscriber kalıcı olarak çökmüşse, her
// teslimat denemesi timeout süresi kadar (10 sn) bir worker slotunu bloke eder.
// 100 bekleyen mesaj varsa tüm worker havuzu tek bozuk subscriber tarafından
// tüketilir ve SAĞLIKLI subscriber'lar da teslimat alamaz.
//
// Çözüm: ardışık hata eşiği aşıldığında devreyi AÇ ve o subscriber'a giden
// istekleri anında (ağa çıkmadan) reddet. Belirli bir süre sonra YARI AÇIK
// duruma geç, tek bir deneme yolla; başarılıysa devreyi kapat.
//
// Kapsam kararı: durum her worker instance'ının belleğindedir, Redis'te
// paylaşılmaz. Gerekçe — breaker bir yerel kaynak koruma mekanizmasıdır;
// her instance kendi kaynağını korur. Paylaşımlı durum her karar için ağ
// gidiş-dönüşü ekler ve breaker'ın amacı olan "hızlı başarısızlık"ı zayıflatır.
package breaker

import (
	"sync"
	"time"
)

// State, devrenin durumu.
type State int

const (
	StateClosed   State = iota // normal akış
	StateOpen                  // istekler anında reddediliyor
	StateHalfOpen              // tek deneme yollanıyor
)

func (s State) String() string {
	switch s {
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half_open"
	default:
		return "closed"
	}
}

// Config, breaker parametreleri.
type Config struct {
	// FailureThreshold: devreyi açmak için gereken ardışık hata sayısı.
	FailureThreshold int
	// OpenDuration: devrenin açık kalacağı süre; sonrasında yarı açık olur.
	OpenDuration time.Duration
	// SuccessThreshold: yarı açık durumda devreyi kapatmak için gereken
	// ardışık başarı sayısı.
	SuccessThreshold int
}

func DefaultConfig() Config {
	return Config{FailureThreshold: 5, OpenDuration: 30 * time.Second, SuccessThreshold: 1}
}

type circuit struct {
	state        State
	failures     int
	successes    int
	openedAt     time.Time
	halfOpenBusy bool
}

// Group, anahtar (subscription id) başına bir circuit tutar.
type Group struct {
	cfg Config
	mu  sync.Mutex
	m   map[string]*circuit
	now func() time.Time
}

func NewGroup(cfg Config) *Group {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 5
	}
	if cfg.OpenDuration <= 0 {
		cfg.OpenDuration = 30 * time.Second
	}
	if cfg.SuccessThreshold <= 0 {
		cfg.SuccessThreshold = 1
	}
	return &Group{cfg: cfg, m: make(map[string]*circuit), now: time.Now}
}

// SetClock, testlerde zamanı kontrol etmek için.
func (g *Group) SetClock(fn func() time.Time) { g.now = fn }

func (g *Group) get(key string) *circuit {
	c, ok := g.m[key]
	if !ok {
		c = &circuit{state: StateClosed}
		g.m[key] = c
	}
	return c
}

// Allow, isteğin gönderilip gönderilemeyeceğini söyler.
// İkinci dönüş değeri, yarı açık durumda verilen "deneme hakkı" olup olmadığı.
func (g *Group) Allow(key string) (allowed bool, probe bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	c := g.get(key)

	switch c.state {
	case StateClosed:
		return true, false

	case StateOpen:
		if g.now().Sub(c.openedAt) >= g.cfg.OpenDuration {
			c.state = StateHalfOpen
			c.successes = 0
			c.halfOpenBusy = true
			return true, true
		}
		return false, false

	case StateHalfOpen:
		// Yarı açık durumda aynı anda yalnızca TEK deneme geçer; aksi halde
		// çökük bir servise yine yığın halinde istek gider.
		if c.halfOpenBusy {
			return false, false
		}
		c.halfOpenBusy = true
		return true, true

	default:
		return true, false
	}
}

// Success, başarılı bir teslimatı bildirir.
func (g *Group) Success(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	c := g.get(key)
	c.halfOpenBusy = false

	switch c.state {
	case StateHalfOpen:
		c.successes++
		if c.successes >= g.cfg.SuccessThreshold {
			c.state = StateClosed
			c.failures = 0
			c.successes = 0
		}
	default:
		c.failures = 0
		c.state = StateClosed
	}
}

// Failure, başarısız bir teslimatı bildirir.
func (g *Group) Failure(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	c := g.get(key)
	c.halfOpenBusy = false

	switch c.state {
	case StateHalfOpen:
		// Yarı açıkta tek hata bile yeterli: hedef hâlâ bozuk.
		c.state = StateOpen
		c.openedAt = g.now()
		c.successes = 0

	default:
		c.failures++
		if c.failures >= g.cfg.FailureThreshold {
			c.state = StateOpen
			c.openedAt = g.now()
		}
	}
}

// State, bir anahtarın güncel durumunu döner.
func (g *Group) State(key string) State {
	g.mu.Lock()
	defer g.mu.Unlock()

	c := g.get(key)
	// Açık süresi dolduysa raporlamada da yarı açık görünsün.
	if c.state == StateOpen && g.now().Sub(c.openedAt) >= g.cfg.OpenDuration {
		return StateHalfOpen
	}
	return c.state
}

// Snapshot, tüm devrelerin durumunu döner (metrik export'u için).
func (g *Group) Snapshot() map[string]State {
	g.mu.Lock()
	defer g.mu.Unlock()

	out := make(map[string]State, len(g.m))
	for k, c := range g.m {
		s := c.state
		if s == StateOpen && g.now().Sub(c.openedAt) >= g.cfg.OpenDuration {
			s = StateHalfOpen
		}
		out[k] = s
	}
	return out
}
