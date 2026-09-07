package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

var (
	ErrTokenInvalid = errors.New("token geçersiz")
	ErrTokenExpired = errors.New("token süresi dolmuş")
)

// Claims, access token içeriği.
type Claims struct {
	UserID string `json:"uid"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}

// TokenManager, JWT üretimi ve doğrulamasını yönetir.
//
// Tasarım kararı — access token STATELESS, refresh token STATEFUL:
// Access token kısa ömürlüdür (15 dk) ve DB'ye bakılmadan doğrulanır; bu,
// her istekte veritabanı sorgusu yapmayı engeller. Ancak bu tokenlar iptal
// edilemez — "logout" anında geçersizleşme sağlamaz. Bu boşluğu refresh
// token kapatır: uzun ömürlüdür, DB'de hash'i saklanır ve iptal edilebilir.
// Böylece bir oturumu sonlandırmak en fazla access TTL kadar (15 dk) gecikir.
// Alternatif olan "her istekte token blacklist sorgusu", stateless olmanın
// tüm faydasını yok ederdi.
type TokenManager struct {
	secret     []byte
	issuer     string
	accessTTL  time.Duration
	refreshTTL time.Duration
}

func NewTokenManager(secret string, issuer string, accessTTL, refreshTTL time.Duration) (*TokenManager, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("JWT_SECRET en az 32 karakter olmalı (mevcut: %d)", len(secret))
	}
	return &TokenManager{
		secret:     []byte(secret),
		issuer:     issuer,
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
	}, nil
}

func (m *TokenManager) AccessTTL() time.Duration  { return m.accessTTL }
func (m *TokenManager) RefreshTTL() time.Duration { return m.refreshTTL }

// GenerateAccess, imzalı access token üretir.
func (m *TokenManager) GenerateAccess(userID, email, role string) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(m.accessTTL)

	claims := Claims{
		UserID: userID,
		Email:  email,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   userID,
			ID:        uuid.NewString(),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("token imzalanamadı: %w", err)
	}
	return signed, exp, nil
}

// ParseAccess, token'ı doğrular ve claim'leri döner.
func (m *TokenManager) ParseAccess(tokenString string) (*Claims, error) {
	claims := &Claims{}

	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		// Algoritma sabitlenir: `alg: none` ve HS/RS karıştırma saldırılarına
		// karşı zorunlu kontrol.
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("beklenmeyen imza algoritması: %v", t.Header["alg"])
		}
		return m.secret, nil
	},
		jwt.WithIssuer(m.issuer),
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
	)

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, fmt.Errorf("%w: %w", ErrTokenInvalid, err)
	}
	if !token.Valid {
		return nil, ErrTokenInvalid
	}
	return claims, nil
}

// RefreshToken, üretilen opak refresh token'ı ve DB'ye yazılacak hash'ini tutar.
type RefreshToken struct {
	Raw       string // yalnızca istemciye döner, saklanmaz
	Hash      []byte // DB'ye yazılır
	ExpiresAt time.Time
}

// GenerateRefresh, 32 baytlık rastgele opak token üretir.
//
// Neden JWT değil? Refresh token'ın içeriğine ihtiyacımız yok; sadece
// "bu token geçerli mi" sorusunu DB'den yanıtlıyoruz. Opak rastgele değer
// hem daha kısa hem de JWT'nin ayrıştırma yüzeyini ortadan kaldırıyor.
func (m *TokenManager) GenerateRefresh() (RefreshToken, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return RefreshToken{}, fmt.Errorf("refresh token üretilemedi: %w", err)
	}

	raw := base64.RawURLEncoding.EncodeToString(buf)
	return RefreshToken{
		Raw:       raw,
		Hash:      HashRefreshToken(raw),
		ExpiresAt: time.Now().Add(m.refreshTTL),
	}, nil
}

// HashRefreshToken, DB'de saklanacak SHA-256 hash'ini üretir.
// Refresh token yüksek entropili rastgele bir değerdir; sözlük saldırısına
// açık olmadığı için argon2 gibi yavaş bir fonksiyon gereksizdir.
func HashRefreshToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}
