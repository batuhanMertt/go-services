package main

import (
	"time"

	"github.com/mindmons/integration-hub/internal/config"
)

// Config, admin-api servisinin tüm ayarları. Hiçbiri koda gömülü değildir.
type Config struct {
	Base  config.Base
	Infra config.Infra

	// Güvenlik
	JWTSecret     string
	JWTAccessTTL  time.Duration
	JWTRefreshTTL time.Duration
	EncryptionKey string
	CORSOrigins   []string

	// İlk admin kullanıcısı (yoksa açılışta oluşturulur)
	BootstrapEmail    string
	BootstrapPassword string

	// Rate limit
	RateAdmin      config.RateRule
	RateFailClosed bool

	// Canlı event akışı (SSE) yenileme aralığı
	SSEInterval time.Duration

	// Maksimum istek gövdesi
	MaxBodyBytes int64
}

func loadConfig() (Config, error) {
	l := config.New()

	cfg := Config{
		Base:  l.LoadBase("admin-api", "ADMIN_API_PORT", 8080),
		Infra: l.LoadInfra(),

		JWTSecret:     l.RequireMinLen("JWT_SECRET", 32),
		JWTAccessTTL:  l.Duration("JWT_ACCESS_TTL", 15*time.Minute),
		JWTRefreshTTL: l.Duration("JWT_REFRESH_TTL", 168*time.Hour),
		EncryptionKey: l.RequireMinLen("SECRET_ENCRYPTION_KEY", 64),
		CORSOrigins:   l.CSV("CORS_ALLOWED_ORIGINS", []string{"http://localhost:3000"}),

		BootstrapEmail:    l.Require("ADMIN_EMAIL"),
		BootstrapPassword: l.RequireMinLen("ADMIN_PASSWORD", 8),

		RateAdmin:      l.LoadRateRule("RATE_LIMIT_ADMIN", 20, 40),
		RateFailClosed: l.Bool("RATE_LIMIT_FAIL_CLOSED", false),

		SSEInterval:  l.Duration("ADMIN_SSE_INTERVAL", 2*time.Second),
		MaxBodyBytes: int64(l.Int("ADMIN_MAX_BODY_BYTES", 1048576)),
	}

	return cfg, l.Err()
}
