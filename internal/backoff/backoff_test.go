package backoff

import (
	"net/http"
	"testing"
	"time"
)

// FR-4.3: "1s, 2s, 4s, 8s, 16s katlanarak artan aralıklar".
func TestScheduleMatchesRequirement(t *testing.T) {
	p := DefaultPolicy()
	got := p.Schedule(5)

	want := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
	}

	if len(got) != len(want) {
		t.Fatalf("uzunluk %d, beklenen %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("deneme %d: %v, beklenen %v", i+1, got[i], want[i])
		}
	}
}

func TestDelayIsCappedAtMax(t *testing.T) {
	p := Policy{Base: time.Second, Max: 16 * time.Second, Jitter: 0}

	// 10. denemede 2^9 = 512 sn olurdu; üst sınır devreye girmeli.
	if d := p.Delay(10); d != 16*time.Second {
		t.Fatalf("üst sınır uygulanmadı: %v", d)
	}
	// Aşırı büyük deneme numarası taşma yaratmamalı.
	if d := p.Delay(1000); d != 16*time.Second {
		t.Fatalf("büyük deneme numarasında taşma: %v", d)
	}
}

func TestDelayHandlesInvalidAttempt(t *testing.T) {
	p := DefaultPolicy()

	// 0 ve negatif değerler ilk denemeye eşitlenmeli, panik olmamalı.
	for _, attempt := range []int{0, -1, -100} {
		if d := p.Delay(attempt); d <= 0 {
			t.Fatalf("deneme %d için geçersiz gecikme: %v", attempt, d)
		}
	}
}

// Jitter olmadan aynı anda başarısız olan yüzlerce teslimat tam olarak aynı
// saniyede geri döner ve hedefi ikinci kez çökertir (thundering herd).
func TestJitterSpreadsDelays(t *testing.T) {
	p := Policy{Base: time.Second, Max: 16 * time.Second, Jitter: 0.2}

	seen := make(map[time.Duration]bool)
	for i := 0; i < 200; i++ {
		seen[p.Delay(3)] = true
	}

	if len(seen) < 20 {
		t.Fatalf("jitter yeterince dağıtmıyor: 200 çağrıda yalnızca %d farklı değer", len(seen))
	}

	// Yine de makul sınırlar içinde kalmalı: 4s ± %20
	for d := range seen {
		if d < 3*time.Second || d > 5*time.Second {
			t.Fatalf("jitter beklenen aralığın dışına çıktı: %v", d)
		}
	}
}

func TestZeroJitterIsDeterministic(t *testing.T) {
	p := Policy{Base: time.Second, Max: 16 * time.Second, Jitter: 0}

	first := p.Delay(3)
	for i := 0; i < 50; i++ {
		if p.Delay(3) != first {
			t.Fatal("jitter=0 olmasına rağmen sonuç değişti")
		}
	}
}

func TestPolicyDefaultsOnZeroValues(t *testing.T) {
	var p Policy // sıfır değerli politika

	if d := p.Delay(1); d <= 0 {
		t.Fatalf("sıfır değerli politika geçersiz gecikme üretti: %v", d)
	}
}

// FR-4.3: 5xx ve 429 yeniden denenir; diğer 4xx'ler kalıcı hatadır.
func TestIsRetryable(t *testing.T) {
	tests := []struct {
		status int
		want   bool
		why    string
	}{
		{http.StatusOK, false, "başarılı"},
		{http.StatusCreated, false, "başarılı"},
		{http.StatusMovedPermanently, false, "yönlendirme takip edilmiyor"},
		{http.StatusBadRequest, false, "isteğin kendisi hatalı — tekrar aynı sonucu verir"},
		{http.StatusUnauthorized, false, "kimlik sorunu — tekrar çözmez"},
		{http.StatusForbidden, false, "yetki sorunu"},
		{http.StatusNotFound, false, "yanlış URL"},
		{http.StatusRequestTimeout, true, "geçici"},
		{http.StatusTooEarly, true, "geçici"},
		{http.StatusTooManyRequests, true, "hız limiti — bekleyip dene"},
		{http.StatusInternalServerError, true, "sunucu geçici olarak bozuk"},
		{http.StatusBadGateway, true, "geçici"},
		{http.StatusServiceUnavailable, true, "geçici"},
		{http.StatusGatewayTimeout, true, "geçici"},
	}

	for _, tc := range tests {
		if got := IsRetryable(tc.status); got != tc.want {
			t.Errorf("IsRetryable(%d) = %v, beklenen %v (%s)", tc.status, got, tc.want, tc.why)
		}
	}
}

func TestStatusClass(t *testing.T) {
	tests := map[int]string{
		0: "none", 100: "1xx", 200: "2xx", 301: "3xx", 404: "4xx", 500: "5xx", 599: "5xx",
	}

	for status, want := range tests {
		if got := StatusClass(status); got != want {
			t.Errorf("StatusClass(%d) = %q, beklenen %q", status, got, want)
		}
	}
}
