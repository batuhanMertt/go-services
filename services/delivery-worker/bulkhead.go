package main

import "sync"

// Bulkhead, "slow neighbor" problemini çözer.
//
// Problem: 10 saniyelik timeout'u olan tek bir yavaş subscriber, tüm worker
// slotlarını işgal edebilir; sağlıklı subscriber'lar sırada bekler.
//
// Gemi güvertesi metaforu: gemi su geçirmez bölmelere (bulkhead) ayrılır;
// bir bölme su alsa da gemi batmaz. Burada her subscriber kendi bölmesindedir
// ve en fazla `perKey` kadar eşzamanlı slot kullanabilir.
//
// Kritik tasarım tercihi: slot dolduğunda BEKLEMEYİZ, görevi erteleriz.
// Beklemek, Kafka partition'ını bloke eder ve aynı partition'daki diğer
// subscriber'ların mesajlarını da geciktirirdi — çözmeye çalıştığımız
// problemin ta kendisi.
type Bulkhead struct {
	global chan struct{}

	mu     sync.Mutex
	perKey map[string]chan struct{}
	limit  int
}

func NewBulkhead(globalLimit, perKeyLimit int) *Bulkhead {
	if globalLimit <= 0 {
		globalLimit = 64
	}
	if perKeyLimit <= 0 {
		perKeyLimit = 8
	}
	return &Bulkhead{
		global: make(chan struct{}, globalLimit),
		perKey: make(map[string]chan struct{}),
		limit:  perKeyLimit,
	}
}

func (b *Bulkhead) slot(key string) chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch, ok := b.perKey[key]
	if !ok {
		ch = make(chan struct{}, b.limit)
		b.perKey[key] = ch
	}
	return ch
}

// TryAcquire, slot almayı dener; alamazsa false döner (bloke etmez).
func (b *Bulkhead) TryAcquire(key string) (release func(), ok bool) {
	select {
	case b.global <- struct{}{}:
	default:
		return nil, false
	}

	ch := b.slot(key)
	select {
	case ch <- struct{}{}:
	default:
		<-b.global // global slotu geri ver
		return nil, false
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			<-ch
			<-b.global
		})
	}, true
}

// InFlight, global olarak kullanımdaki slot sayısı (metrik).
func (b *Bulkhead) InFlight() int { return len(b.global) }
