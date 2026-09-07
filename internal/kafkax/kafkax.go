// Package kafkax, segmentio/kafka-go üzerine ince bir sarmalayıcıdır.
//
// Kütüphane seçimi (docs/03-tech-stack.md):
//   - confluent-kafka-go: librdkafka'ya CGO ile bağlanır → statik binary ve
//     distroless image mümkün olmaz, cross-compile zorlaşır.
//   - franz-go: performansı en yüksek olan, ancak API yüzeyi geniş.
//   - segmentio/kafka-go: saf Go, sade API, consumer group desteği yerleşik.
//     Bu projenin hacminde performans farkı belirleyici değil; taşınabilirlik
//     ve okunabilirlik belirleyici. Bu yüzden kafka-go seçildi.
package kafkax

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"
)

// NewWriter, idempotent-benzeri güvenli üretici üretir.
//
// RequiredAcks = RequireAll: mesaj tüm in-sync replikalara yazılmadan başarılı
// sayılmaz. At-least-once teslimat garantisinin Kafka tarafındaki ayağı budur;
// tek broker'lı dev ortamında bile doğru varsayılan olarak bırakılır.
func NewWriter(brokers []string, topic string) *kafka.Writer {
	return &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        topic,
		Balancer:     &kafka.Hash{}, // partition = hash(key) → aynı event_id daima aynı partition
		RequiredAcks: kafka.RequireAll,
		MaxAttempts:  5,
		BatchTimeout: 20 * time.Millisecond,
		WriteTimeout: 10 * time.Second,
		ReadTimeout:  10 * time.Second,
		// Auto topic creation kapalı olduğu için writer da topic yaratmaya
		// çalışmamalı; aksi halde yanlış partition sayısıyla topic oluşur.
		AllowAutoTopicCreation: false,
		Async:                  false, // senkron: hata görülmeden "başarılı" denmez
		Compression:            kafka.Snappy,
	}
}

// NewMultiTopicWriter, topic'i MESAJ başına belirlenen bir üretici döner.
//
// Neden ayrı bir kurucu? kafka-go, writer'da ve mesajda topic'in AYNI ANDA
// verilmesini reddeder ("Topic must not be specified for both Writer and
// Message"). Outbox tablosu her satırda hedef topic'i taşıdığı için burada
// writer'ın topic'i boş bırakılır ve karar mesaja bırakılır. Böylece tek bir
// outbox poller birden fazla topic'e yayın yapabilir.
func NewMultiTopicWriter(brokers []string) *kafka.Writer {
	return &kafka.Writer{
		Addr:                   kafka.TCP(brokers...),
		Balancer:               &kafka.Hash{},
		RequiredAcks:           kafka.RequireAll,
		MaxAttempts:            5,
		BatchTimeout:           20 * time.Millisecond,
		WriteTimeout:           10 * time.Second,
		ReadTimeout:            10 * time.Second,
		AllowAutoTopicCreation: false,
		Async:                  false,
		Compression:            kafka.Snappy,
	}
}

// ReaderConfig, consumer group ayarları.
type ReaderConfig struct {
	Brokers  []string
	Topic    string
	GroupID  string
	MinBytes int
	MaxBytes int
	Logger   *slog.Logger
}

// NewReader, consumer group üyesi bir okuyucu üretir.
//
// Offset yönetimi ELLE yapılır (CommitInterval=0): mesaj başarıyla İŞLENDİKTEN
// sonra commit edilir. Otomatik commit, işleme sırasında çökme durumunda mesaj
// kaybına (at-most-once) yol açardı; bizim hedefimiz at-least-once.
func NewReader(cfg ReaderConfig) *kafka.Reader {
	minBytes := cfg.MinBytes
	if minBytes <= 0 {
		minBytes = 1
	}
	maxBytes := cfg.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 10 << 20 // 10 MiB
	}

	return kafka.NewReader(kafka.ReaderConfig{
		Brokers:  cfg.Brokers,
		Topic:    cfg.Topic,
		GroupID:  cfg.GroupID,
		MinBytes: minBytes,
		MaxBytes: maxBytes,
		// Yeni consumer group ilk kez bağlandığında en eskiden başla:
		// başlangıçtaki mesajlar kaybolmasın.
		StartOffset:    kafka.FirstOffset,
		MaxWait:        500 * time.Millisecond,
		CommitInterval: 0, // elle commit
		// Rebalance sırasında yavaş consumer'ın gruptan atılmaması için.
		SessionTimeout:    30 * time.Second,
		RebalanceTimeout:  30 * time.Second,
		HeartbeatInterval: 3 * time.Second,
		ReadBackoffMin:    100 * time.Millisecond,
		ReadBackoffMax:    2 * time.Second,
	})
}

// WaitForBrokers, broker erişilebilir olana kadar bekler.
func WaitForBrokers(ctx context.Context, brokers []string, timeout time.Duration) error {
	if len(brokers) == 0 {
		return fmt.Errorf("kafka: broker listesi boş")
	}

	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		conn, err := kafka.DialContext(ctx, "tcp", brokers[0])
		if err == nil {
			_, lastErr = conn.Brokers()
			_ = conn.Close()
			if lastErr == nil {
				return nil
			}
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}

	return fmt.Errorf("kafka: broker %s hazır olmadı: %w", brokers[0], lastErr)
}

// HealthCheck, /readyz için broker erişilebilirliğini kontrol eder.
func HealthCheck(brokers []string) func(context.Context) error {
	return func(ctx context.Context) error {
		if len(brokers) == 0 {
			return fmt.Errorf("kafka: broker listesi boş")
		}
		d := &kafka.Dialer{Timeout: 3 * time.Second, DualStack: true}
		conn, err := d.DialContext(ctx, "tcp", brokers[0])
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		_, err = conn.Brokers()
		return err
	}
}

// TLSConfig, üretimde TLS gerekirse kullanılacak yardımcı (dev'de nil).
func TLSConfig(enabled bool) *tls.Config {
	if !enabled {
		return nil
	}
	return &tls.Config{MinVersion: tls.VersionTLS12}
}

// Header, kafka mesaj başlığı üretmek için kısayol.
func Header(key, value string) kafka.Header {
	return kafka.Header{Key: key, Value: []byte(value)}
}

// HeaderValue, mesajdaki başlığı okur.
func HeaderValue(msg kafka.Message, key string) string {
	for _, h := range msg.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}
