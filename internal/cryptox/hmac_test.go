package cryptox

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSignIsDeterministic(t *testing.T) {
	secret := []byte("test-secret")
	body := []byte(`{"event_id":"abc","value":42}`)
	ts := "1735689600"

	first := Sign(secret, ts, body)
	second := Sign(secret, ts, body)

	if first != second {
		t.Fatalf("aynı girdi farklı imza üretti: %s != %s", first, second)
	}
	if len(first) != 64 {
		t.Fatalf("SHA-256 hex imzası 64 karakter olmalı, alınan: %d", len(first))
	}
}

func TestSignChangesWithEveryInput(t *testing.T) {
	base := Sign([]byte("secret"), "1000", []byte(`{"a":1}`))

	cases := map[string]string{
		"farklı secret":    Sign([]byte("other"), "1000", []byte(`{"a":1}`)),
		"farklı timestamp": Sign([]byte("secret"), "1001", []byte(`{"a":1}`)),
		"farklı gövde":     Sign([]byte("secret"), "1000", []byte(`{"a":2}`)),
	}

	for name, got := range cases {
		if got == base {
			t.Errorf("%s imzayı değiştirmedi — girdi imzaya dahil edilmiyor", name)
		}
	}
}

// Zaman damgasının imzaya dahil edilmesi replay saldırısını engellemenin
// temelidir; bu testi geçmek zorunda.
func TestSigningBaseIncludesTimestamp(t *testing.T) {
	got := string(SigningBase("1234567890", []byte(`{"x":1}`)))
	want := `1234567890.{"x":1}`

	if got != want {
		t.Fatalf("kanonik metin hatalı\n aldı: %q\nistedi: %q", got, want)
	}
}

func TestVerifyAcceptsValidSignature(t *testing.T) {
	secret := []byte("s3cr3t")
	body := []byte(`{"hello":"world"}`)
	ts := "1735689600"

	sig := Sign(secret, ts, body)

	if err := Verify(secret, ts, body, sig); err != nil {
		t.Fatalf("geçerli imza reddedildi: %v", err)
	}
}

func TestVerifyAcceptsPrefixedForm(t *testing.T) {
	secret := []byte("s3cr3t")
	body := []byte(`{"hello":"world"}`)
	ts := "1735689600"

	// Outbound webhook'lar "sha256=<hex>" biçimini kullanır; doğrulayıcı
	// her iki biçimi de kabul etmelidir.
	prefixed := SignPrefixed(secret, ts, body)
	if !strings.HasPrefix(prefixed, "sha256=") {
		t.Fatalf("SignPrefixed 'sha256=' öneki üretmedi: %s", prefixed)
	}

	if err := Verify(secret, ts, body, prefixed); err != nil {
		t.Fatalf("önekli imza reddedildi: %v", err)
	}
}

func TestVerifyRejectsTamperedInput(t *testing.T) {
	secret := []byte("s3cr3t")
	ts := "1735689600"
	sig := Sign(secret, ts, []byte(`{"amount":100}`))

	tests := []struct {
		name   string
		secret []byte
		ts     string
		body   []byte
		sig    string
	}{
		{"değiştirilmiş gövde", secret, ts, []byte(`{"amount":999}`), sig},
		{"yanlış secret", []byte("wrong"), ts, []byte(`{"amount":100}`), sig},
		{"değiştirilmiş timestamp", secret, "1735689601", []byte(`{"amount":100}`), sig},
		{"bozuk imza", secret, ts, []byte(`{"amount":100}`), "deadbeef"},
		{"boş imza", secret, ts, []byte(`{"amount":100}`), ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := Verify(tc.secret, tc.ts, tc.body, tc.sig); !errors.Is(err, ErrSignatureMismatch) {
				t.Fatalf("beklenen ErrSignatureMismatch, alınan: %v", err)
			}
		})
	}
}

func TestCheckTimestamp(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	tolerance := 5 * time.Minute

	tests := []struct {
		name    string
		ts      string
		wantErr error
	}{
		{"tam şimdi (unix)", strconv.FormatInt(now.Unix(), 10), nil},
		{"4 dk önce", strconv.FormatInt(now.Add(-4*time.Minute).Unix(), 10), nil},
		{"4 dk sonra (saat kayması)", strconv.FormatInt(now.Add(4*time.Minute).Unix(), 10), nil},
		{"RFC3339 biçimi", now.Format(time.RFC3339), nil},
		{"6 dk önce — replay", strconv.FormatInt(now.Add(-6*time.Minute).Unix(), 10), ErrTimestampExpired},
		{"1 saat sonra", strconv.FormatInt(now.Add(time.Hour).Unix(), 10), ErrTimestampExpired},
		{"boş", "", ErrTimestampInvalid},
		{"anlamsız", "not-a-timestamp", ErrTimestampInvalid},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckTimestamp(tc.ts, tolerance, now)

			if tc.wantErr == nil && err != nil {
				t.Fatalf("hata beklenmiyordu, alınan: %v", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("beklenen %v, alınan: %v", tc.wantErr, err)
			}
		})
	}
}

func TestVerifyRequestRejectsReplay(t *testing.T) {
	secret := []byte("s3cr3t")
	body := []byte(`{"replay":"test"}`)

	// 10 dakika önce imzalanmış, hâlâ geçerli imzalı bir istek.
	oldTS := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	sig := Sign(secret, oldTS, body)

	// İmza doğru — ama zaman damgası penceresi dışında.
	if err := Verify(secret, oldTS, body, sig); err != nil {
		t.Fatalf("imza kendisi geçerli olmalıydı: %v", err)
	}

	err := VerifyRequest(secret, oldTS, body, sig, 5*time.Minute)
	if !errors.Is(err, ErrTimestampExpired) {
		t.Fatalf("replay reddedilmeliydi, alınan: %v", err)
	}
}

func TestNowTimestampIsParseable(t *testing.T) {
	if err := CheckTimestamp(NowTimestamp(), time.Second, time.Now()); err != nil {
		t.Fatalf("NowTimestamp kendi kontrolünü geçemedi: %v", err)
	}
}

func BenchmarkSign(b *testing.B) {
	secret := []byte("benchmark-secret")
	body := []byte(`{"event_id":"3f9a1c72-5b0e-4a1d-9f6c-7c2e8d5b1a34","payload":{"x":1}}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Sign(secret, "1735689600", body)
	}
}
