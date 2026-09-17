package ddos

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestConnLimiterAllowsUpToCapacity(t *testing.T) {
	c := NewConnLimiter(2)

	if !c.TryAcquire() {
		t.Fatal("1. slot reddedildi")
	}
	if !c.TryAcquire() {
		t.Fatal("2. slot reddedildi")
	}
	if c.TryAcquire() {
		t.Fatal("kapasite dolu olduğu hâlde 3. slot kabul edildi")
	}

	// White-box: iç sayaç kapasiteyle eşleşmeli.
	if got := c.InFlight(); got != 2 {
		t.Fatalf("InFlight() = %d, beklenen 2", got)
	}

	c.Release()
	if got := c.InFlight(); got != 1 {
		t.Fatalf("Release sonrası InFlight() = %d, beklenen 1", got)
	}
	if !c.TryAcquire() {
		t.Fatal("slot boşaldıktan sonra acquire reddedildi")
	}
}

func TestConnLimiterZeroMeansUnlimited(t *testing.T) {
	c := NewConnLimiter(0)
	for i := 0; i < 1000; i++ {
		if !c.TryAcquire() {
			t.Fatalf("sınırsız limiter %d. istekte reddetti", i)
		}
	}
	if got := c.Capacity(); got != 0 {
		t.Fatalf("Capacity() = %d, beklenen 0", got)
	}
}

func TestConnLimiterMiddlewareRejectsOverCapacity(t *testing.T) {
	c := NewConnLimiter(1)

	release := make(chan struct{})
	handler := c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // ilk isteği kasıtlı olarak "yavaş" tut
		w.WriteHeader(http.StatusOK)
	}))

	var wg sync.WaitGroup
	wg.Add(1)
	firstDone := make(chan struct{})
	go func() {
		defer wg.Done()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		close(firstDone)
	}()

	// İlk istek handler içinde bloke olana kadar bekle (sınırlı deneme, sonsuz spin yok).
	deadline := time.Now().Add(2 * time.Second)
	for c.InFlight() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("ilk istek beklenen sürede acquire etmedi")
		}
		time.Sleep(time.Millisecond)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("kapasite dolu isteğin kodu = %d, beklenen %d", rec.Code, http.StatusTooManyRequests)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After başlığı eksik")
	}

	close(release)
	<-firstDone
	wg.Wait()
}
