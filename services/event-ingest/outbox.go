package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/mindmons/integration-hub/internal/kafkax"
)

// runOutboxPublisher, Transactional Outbox'ın "publisher" ayağıdır.
//
// Akış: kilitle (SKIP LOCKED) → Kafka'ya bas → published_at işaretle → commit.
// Kafka yazımı başarısız olursa transaction geri alınır; satırlar bir sonraki
// turda yeniden denenir. Bu, at-least-once garantisini sağlar: bir mesaj
// birden fazla kez yayınlanabilir (Kafka yazımı başarılı olup commit çökerse),
// ama ASLA kaybolmaz. Mükerrerlik downstream'de idempotency ile ele alınır
// (routing-engine'de delivery_tasks üzerindeki UNIQUE kısıtı).
//
// Neden Debezium/CDC değil? Debezium ayrı bir Kafka Connect kümesi, WAL
// yapılandırması ve replication slot yönetimi gerektirir — bu projenin
// ölçeğinde altyapı maliyeti faydasını aşar. Poller yaklaşımı ~500 ms
// gecikme ekler; teslimat zaten asenkron olduğu için bu kabul edilebilir.
// Ölçek büyüdüğünde geçiş yolu açıktır: outbox tablosu aynen kalır.
func (s *Server) runOutboxPublisher(ctx context.Context) error {
	// Topic mesaj başına belirlenir: outbox tablosu her satırda hedef topic'i
	// taşır, böylece aynı poller ileride başka topic'lere de yayın yapabilir.
	writer := kafkax.NewMultiTopicWriter(s.cfg.Infra.KafkaBrokers)
	defer func() { _ = writer.Close() }()

	ticker := time.NewTicker(s.cfg.OutboxPollInterval)
	defer ticker.Stop()

	s.log.Info("outbox publisher başladı",
		slog.Duration("interval", s.cfg.OutboxPollInterval),
		slog.Int("batch", s.cfg.OutboxBatchSize))

	for {
		select {
		case <-ctx.Done():
			s.log.Info("outbox publisher kapanıyor")
			return nil

		case <-ticker.C:
			published, err := s.publishOutboxBatch(ctx, writer)
			if err != nil {
				s.log.ErrorContext(ctx, "outbox turu başarısız", slog.String("err", err.Error()))
				continue
			}
			// Bekleyen iş varsa beklemeden devam et: birikmiş kuyruk hızla erisin.
			if published >= s.cfg.OutboxBatchSize {
				for published >= s.cfg.OutboxBatchSize {
					select {
					case <-ctx.Done():
						return nil
					default:
					}
					published, err = s.publishOutboxBatch(ctx, writer)
					if err != nil {
						s.log.ErrorContext(ctx, "outbox turu başarısız", slog.String("err", err.Error()))
						break
					}
				}
			}
		}
	}
}

func (s *Server) publishOutboxBatch(ctx context.Context, writer *kafka.Writer) (int, error) {
	tx, rows, err := s.store.ClaimOutboxBatch(ctx, s.cfg.OutboxBatchSize)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if len(rows) == 0 {
		return 0, tx.Commit(ctx)
	}

	messages := make([]kafka.Message, 0, len(rows))
	ids := make([]int64, 0, len(rows))

	for _, row := range rows {
		headers := []kafka.Header{kafkax.Header("source", "event-ingest")}
		for k, v := range row.Headers {
			headers = append(headers, kafkax.Header(k, v))
		}

		messages = append(messages, kafka.Message{
			Topic:   row.Topic,
			Key:     []byte(row.PartitionKey),
			Value:   row.Payload,
			Headers: headers,
		})
		ids = append(ids, row.ID)
	}

	writeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := writer.WriteMessages(writeCtx, messages...); err != nil {
		s.mKafkaPublishErrors.WithLabelValues(s.cfg.Infra.Topics.Ingested).Inc()

		// Deneme sayacını artır ve COMMIT et: sayaç kalıcı olmalı ki
		// sürekli başarısız olan bir mesaj operasyonel olarak görünsün.
		if markErr := MarkFailed(ctx, tx, ids, truncate(err.Error(), 500)); markErr != nil {
			return 0, markErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return 0, commitErr
		}
		return 0, err
	}

	if err := MarkPublished(ctx, tx, ids); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		// Commit çöktü ama Kafka'ya yazıldı: mesajlar bir sonraki turda
		// yeniden yayınlanır (at-least-once). Bilinçli kabul.
		return 0, err
	}

	s.mOutboxPublished.Add(float64(len(ids)))
	s.log.DebugContext(ctx, "outbox mesajları yayınlandı", slog.Int("count", len(ids)))
	return len(ids), nil
}

// runOutboxMaintenance, metrik güncelleme ve eski kayıt temizliği yapar.
func (s *Server) runOutboxMaintenance(ctx context.Context) error {
	metricTicker := time.NewTicker(15 * time.Second)
	defer metricTicker.Stop()

	purgeTicker := time.NewTicker(time.Hour)
	defer purgeTicker.Stop()

	sweepTicker := time.NewTicker(5 * time.Minute)
	defer sweepTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case <-metricTicker.C:
			if n, err := s.store.PendingOutboxCount(ctx); err == nil {
				s.mOutboxPending.Set(float64(n))
			}
			s.mKeyCacheSize.Set(float64(s.keys.Size()))

		case <-sweepTicker.C:
			if n := s.keys.Sweep(); n > 0 {
				s.log.Debug("api key önbelleğinden süresi dolan kayıtlar silindi", slog.Int("count", n))
			}

		case <-purgeTicker.C:
			// Yayınlanmış outbox kayıtları 24 saat sonra silinir: tablo
			// sonsuza dek büyümemeli, ancak yakın geçmiş hata ayıklama için kalsın.
			n, err := s.store.PurgePublishedOutbox(ctx, 24*time.Hour)
			if err != nil {
				s.log.WarnContext(ctx, "outbox temizliği başarısız", slog.String("err", err.Error()))
				continue
			}
			if n > 0 {
				s.log.Info("yayınlanmış outbox kayıtları temizlendi", slog.Int64("count", n))
			}
		}
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
