package auth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Testlerde hızlı olması için düşük maliyet; üretimde DefaultArgon2Params.
func fastParams() Argon2Params {
	return Argon2Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
}

func TestHashPasswordProducesPHCFormat(t *testing.T) {
	hash, err := HashPassword("correct-horse-battery", fastParams())
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(hash, "$argon2id$v=19$") {
		t.Fatalf("PHC formatı beklendi, alınan: %s", hash)
	}
	if parts := strings.Split(hash, "$"); len(parts) != 6 {
		t.Fatalf("PHC 6 bölümden oluşmalı, alınan: %d", len(parts))
	}
	if strings.Contains(hash, "correct-horse-battery") {
		t.Fatal("hash düz metin şifreyi içeriyor")
	}
}

func TestHashUsesRandomSalt(t *testing.T) {
	a, _ := HashPassword("same-password", fastParams())
	b, _ := HashPassword("same-password", fastParams())

	if a == b {
		t.Fatal("aynı şifre aynı hash'i üretti — salt kullanılmıyor")
	}
}

func TestVerifyPassword(t *testing.T) {
	hash, _ := HashPassword("s3cure-p4ssw0rd", fastParams())

	if err := VerifyPassword("s3cure-p4ssw0rd", hash); err != nil {
		t.Fatalf("doğru şifre reddedildi: %v", err)
	}
	if err := VerifyPassword("wrong-password", hash); !errors.Is(err, ErrPasswordNoMatch) {
		t.Fatalf("yanlış şifre kabul edildi veya beklenmeyen hata: %v", err)
	}
	if err := VerifyPassword("", hash); !errors.Is(err, ErrPasswordNoMatch) {
		t.Fatalf("boş şifre kabul edildi: %v", err)
	}
}

func TestHashRejectsShortPassword(t *testing.T) {
	if _, err := HashPassword("1234567", fastParams()); err == nil {
		t.Fatal("8 karakterden kısa şifre kabul edildi")
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	malformed := []string{
		"",
		"plaintext",
		"$argon2id$v=19$m=8192$onlyfour",
		"$bcrypt$v=19$m=8192,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=999$m=8192,t=1,p=1$c2FsdA$aGFzaA", // desteklenmeyen sürüm
	}

	for _, h := range malformed {
		if err := VerifyPassword("x", h); !errors.Is(err, ErrInvalidHash) {
			t.Errorf("bozuk hash %q için ErrInvalidHash bekleniyordu, alınan: %v", h, err)
		}
	}
}

// Parametreler hash'in içinde saklandığı için maliyet artırıldığında eski
// hash'ler doğrulanmaya devam etmeli (kademeli geçiş).
func TestVerifyWorksAcrossParameterChanges(t *testing.T) {
	weak := Argon2Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	hash, _ := HashPassword("legacy-password", weak)

	if err := VerifyPassword("legacy-password", hash); err != nil {
		t.Fatalf("eski parametrelerle üretilen hash doğrulanamadı: %v", err)
	}
}

// --- API anahtarları -------------------------------------------------------

func TestGenerateAPIKeyFormat(t *testing.T) {
	gen, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}

	parts := strings.Split(gen.Plaintext, "_")
	if len(parts) != 3 || parts[0] != "ih" {
		t.Fatalf("ih_<prefix>_<secret> bekleniyordu: %s", gen.Plaintext)
	}
	if len(parts[1]) != 12 {
		t.Fatalf("prefix 12 hex karakter olmalı, alınan: %d", len(parts[1]))
	}
	if len(parts[2]) != 48 {
		t.Fatalf("secret 48 hex karakter (192 bit) olmalı, alınan: %d", len(parts[2]))
	}
	if gen.Prefix != "ih_"+parts[1] {
		t.Fatalf("prefix alanı tutarsız: %s", gen.Prefix)
	}
	if len(gen.Hash) != 32 {
		t.Fatalf("SHA-256 hash 32 byte olmalı, alınan: %d", len(gen.Hash))
	}
	if len(gen.HMACSecret) != 64 {
		t.Fatalf("HMAC secret 64 hex karakter olmalı, alınan: %d", len(gen.HMACSecret))
	}
}

func TestGenerateAPIKeyIsUnique(t *testing.T) {
	seen := make(map[string]bool, 200)

	for i := 0; i < 200; i++ {
		gen, err := GenerateAPIKey()
		if err != nil {
			t.Fatal(err)
		}
		if seen[gen.Plaintext] {
			t.Fatal("mükerrer anahtar üretildi")
		}
		seen[gen.Plaintext] = true
	}
}

func TestExtractPrefix(t *testing.T) {
	gen, _ := GenerateAPIKey()

	prefix, err := ExtractPrefix(gen.Plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if prefix != gen.Prefix {
		t.Fatalf("prefix=%s, beklenen %s", prefix, gen.Prefix)
	}
}

func TestExtractPrefixRejectsMalformed(t *testing.T) {
	malformed := []string{
		"",
		"not-a-key",
		"ih_short_key",
		"xx_a1b2c3d4e5f6_" + strings.Repeat("a", 48), // yanlış önek
		"ih_a1b2c3d4e5f6", // secret yok
		"ih_zzzzzzzzzzzz_" + strings.Repeat("a", 48), // hex değil
	}

	for _, key := range malformed {
		if _, err := ExtractPrefix(key); !errors.Is(err, ErrAPIKeyMalformed) {
			t.Errorf("bozuk anahtar %q kabul edildi: %v", key, err)
		}
	}
}

func TestVerifyAPIKey(t *testing.T) {
	gen, _ := GenerateAPIKey()

	if err := VerifyAPIKey(gen.Plaintext, gen.Hash); err != nil {
		t.Fatalf("doğru anahtar reddedildi: %v", err)
	}

	other, _ := GenerateAPIKey()
	if err := VerifyAPIKey(other.Plaintext, gen.Hash); !errors.Is(err, ErrAPIKeyNoMatch) {
		t.Fatalf("yanlış anahtar kabul edildi: %v", err)
	}
}

// --- JWT --------------------------------------------------------------------

const testSecret = "test-jwt-secret-at-least-32-characters-long"

func TestNewTokenManagerRejectsWeakSecret(t *testing.T) {
	if _, err := NewTokenManager("short", "issuer", time.Minute, time.Hour); err == nil {
		t.Fatal("32 karakterden kısa secret kabul edildi")
	}
}

func TestAccessTokenRoundTrip(t *testing.T) {
	m, err := NewTokenManager(testSecret, "integration-hub", 15*time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	token, exp, err := m.GenerateAccess("user-1", "admin@example.com", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if !exp.After(time.Now()) {
		t.Fatal("son kullanma tarihi geçmişte")
	}

	claims, err := m.ParseAccess(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != "user-1" || claims.Email != "admin@example.com" || claims.Role != "admin" {
		t.Fatalf("claim'ler kaybolmuş: %+v", claims)
	}
}

func TestParseAccessRejectsExpired(t *testing.T) {
	m, _ := NewTokenManager(testSecret, "integration-hub", -time.Minute, time.Hour)

	token, _, _ := m.GenerateAccess("user-1", "a@b.c", "admin")

	if _, err := m.ParseAccess(token); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("süresi dolmuş token kabul edildi: %v", err)
	}
}

func TestParseAccessRejectsWrongSecret(t *testing.T) {
	issuer, _ := NewTokenManager(testSecret, "integration-hub", time.Minute, time.Hour)
	attacker, _ := NewTokenManager("a-completely-different-secret-value-32", "integration-hub", time.Minute, time.Hour)

	token, _, _ := issuer.GenerateAccess("user-1", "a@b.c", "admin")

	if _, err := attacker.ParseAccess(token); err == nil {
		t.Fatal("farklı secret ile imzalanmış token kabul edildi")
	}
}

func TestParseAccessRejectsWrongIssuer(t *testing.T) {
	a, _ := NewTokenManager(testSecret, "integration-hub", time.Minute, time.Hour)
	b, _ := NewTokenManager(testSecret, "other-system", time.Minute, time.Hour)

	token, _, _ := a.GenerateAccess("user-1", "a@b.c", "admin")

	if _, err := b.ParseAccess(token); err == nil {
		t.Fatal("farklı issuer'a ait token kabul edildi")
	}
}

// `alg: none` saldırısına karşı algoritma sabitlenmiş olmalı.
func TestParseAccessRejectsGarbage(t *testing.T) {
	m, _ := NewTokenManager(testSecret, "integration-hub", time.Minute, time.Hour)

	garbage := []string{
		"",
		"not.a.token",
		"eyJhbGciOiJub25lIn0.eyJ1aWQiOiJhZG1pbiJ9.", // alg: none
	}

	for _, tok := range garbage {
		if _, err := m.ParseAccess(tok); err == nil {
			t.Errorf("geçersiz token kabul edildi: %q", tok)
		}
	}
}

func TestRefreshTokenIsRandomAndHashed(t *testing.T) {
	m, _ := NewTokenManager(testSecret, "integration-hub", time.Minute, 24*time.Hour)

	a, err := m.GenerateRefresh()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := m.GenerateRefresh()

	if a.Raw == b.Raw {
		t.Fatal("iki refresh token aynı — rastgelelik yok")
	}
	if len(a.Hash) != 32 {
		t.Fatalf("hash 32 byte olmalı, alınan: %d", len(a.Hash))
	}
	if !a.ExpiresAt.After(time.Now()) {
		t.Fatal("refresh token süresi geçmişte")
	}

	// Hash deterministik olmalı: DB lookup'ı buna dayanıyor.
	again := HashRefreshToken(a.Raw)
	if string(again) != string(a.Hash) {
		t.Fatal("HashRefreshToken deterministik değil")
	}
}
