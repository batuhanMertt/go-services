// Package config, tüm servislerin ortak ortam değişkeni yükleme katmanıdır.
//
// Tasarım kararı: kod içinde hiçbir varsayılan bağlantı adresi, port veya
// secret bulunmaz. Zorunlu değerler Require* ile okunur ve eksikse servis
// AÇILIŞTA hata verip durur ("fail fast"). Yanlış yapılandırılmış bir servisin
// yarım çalışması, hiç açılmamasından daha tehlikelidir.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Loader, okuma sırasında biriken hataları toplar; böylece kullanıcı eksik
// değişkenleri tek tek değil, tek seferde görür.
type Loader struct {
	problems []string
}

func New() *Loader { return &Loader{} }

// Err, toplanan tüm yapılandırma sorunlarını tek bir hata olarak döndürür.
func (l *Loader) Err() error {
	if len(l.problems) == 0 {
		return nil
	}
	return fmt.Errorf("yapılandırma hatası:\n  - %s", strings.Join(l.problems, "\n  - "))
}

func (l *Loader) fail(format string, args ...any) {
	l.problems = append(l.problems, fmt.Sprintf(format, args...))
}

func lookup(key string) (string, bool) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return "", false
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	return v, true
}

// String, opsiyonel bir metin değeri okur.
func (l *Loader) String(key, def string) string {
	if v, ok := lookup(key); ok {
		return v
	}
	return def
}

// Require, zorunlu bir metin değeri okur; yoksa hata biriktirir.
func (l *Loader) Require(key string) string {
	v, ok := lookup(key)
	if !ok {
		l.fail("%s tanımlı değil (zorunlu)", key)
		return ""
	}
	return v
}

// RequireMinLen, secret'lar için minimum uzunluk da doğrular.
func (l *Loader) RequireMinLen(key string, minLen int) string {
	v := l.Require(key)
	if v != "" && len(v) < minLen {
		l.fail("%s en az %d karakter olmalı (mevcut: %d)", key, minLen, len(v))
	}
	return v
}

// Int, tam sayı okur.
func (l *Loader) Int(key string, def int) int {
	v, ok := lookup(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.fail("%s tam sayı olmalı, alınan: %q", key, v)
		return def
	}
	return n
}

// Float, ondalık sayı okur.
func (l *Loader) Float(key string, def float64) float64 {
	v, ok := lookup(key)
	if !ok {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		l.fail("%s ondalık sayı olmalı, alınan: %q", key, v)
		return def
	}
	return f
}

// Bool, "true/false/1/0/yes/no" değerlerini okur.
func (l *Loader) Bool(key string, def bool) bool {
	v, ok := lookup(key)
	if !ok {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		l.fail("%s boolean olmalı, alınan: %q", key, v)
		return def
	}
}

// Duration, "5s", "1h30m" gibi süreleri okur.
func (l *Loader) Duration(key string, def time.Duration) time.Duration {
	v, ok := lookup(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.fail("%s süre olmalı (örn. 10s, 2m), alınan: %q", key, v)
		return def
	}
	return d
}

// CSV, virgülle ayrılmış listeyi okur (CORS origin'leri, Kafka broker'ları).
func (l *Loader) CSV(key string, def []string) []string {
	v, ok := lookup(key)
	if !ok {
		return def
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}

// Base, her servisin paylaştığı temel yapılandırma.
type Base struct {
	ServiceName string
	Env         string
	LogLevel    string
	LogJSON     bool

	HTTPPort            int
	HTTPReadTimeout     time.Duration
	HTTPWriteTimeout    time.Duration
	HTTPIdleTimeout     time.Duration
	HTTPShutdownTimeout time.Duration
}

// LoadBase, tüm servislerde ortak olan değerleri yükler.
// portKey her serviste farklıdır (INGEST_HTTP_PORT, ADMIN_API_PORT …).
func (l *Loader) LoadBase(serviceName, portKey string, defaultPort int) Base {
	return Base{
		ServiceName:         serviceName,
		Env:                 l.String("APP_ENV", "dev"),
		LogLevel:            l.String("LOG_LEVEL", "info"),
		LogJSON:             l.Bool("LOG_JSON", true),
		HTTPPort:            l.Int(portKey, defaultPort),
		HTTPReadTimeout:     l.Duration("HTTP_READ_TIMEOUT", 10*time.Second),
		HTTPWriteTimeout:    l.Duration("HTTP_WRITE_TIMEOUT", 15*time.Second),
		HTTPIdleTimeout:     l.Duration("HTTP_IDLE_TIMEOUT", 60*time.Second),
		HTTPShutdownTimeout: l.Duration("HTTP_SHUTDOWN_TIMEOUT", 20*time.Second),
	}
}

// Addr, HTTP sunucusunun dinleyeceği adresi verir.
func (b Base) Addr() string { return fmt.Sprintf(":%d", b.HTTPPort) }

// Topics, Kafka topic isimlerini tutar. İsimler koda gömülmez.
type Topics struct {
	Ingested string
	Delivery string
	DLQ      string
}

func (l *Loader) LoadTopics() Topics {
	return Topics{
		Ingested: l.String("KAFKA_TOPIC_INGESTED", "events.ingested"),
		Delivery: l.String("KAFKA_TOPIC_DELIVERY", "events.delivery"),
		DLQ:      l.String("KAFKA_TOPIC_DLQ", "events.dlq"),
	}
}
