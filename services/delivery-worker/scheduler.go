package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/mindmons/integration-hub/internal/kafkax"
)

// runRetryScheduler, "gecikmeli kuyruk" desenini uygular.
//
// Kafka'da yerleşik gecikmeli teslimat yoktur. Zamanı gelen görevler
// Postgres'ten okunup `events.delivery` topic'ine YENİDEN basılır; böylece
// normal teslimat akışı tek bir kod yolundan geçer (retry için ayrı bir
// dispatcher yazmaya gerek kalmaz).
//
// Bu zamanlayıcı aynı zamanda bir KURTARMA mekanizmasıdır: routing-engine
// görevi DB'ye yazıp Kafka'ya basamadan çökerse, görev burada bulunur ve
// yayınlanır. Yani teslimat boru hattının tek bir noktası bile kalıcı kayba
// yol açmaz.
func (s *Server) runRetryScheduler(ctx context.Context) error {
	writer := kafkax.NewWriter(s.cfg.Infra.KafkaBrokers, s.cfg.Infra.Topics.Delivery)
	defer func() { _ = writer.Close() }()

	interval := s.cfg.SchedulerInterval
	if interval <= 0 {
		interval = time.Second
	}

	// Kiralama süresi: yeniden yayınlanan bir görev bu süre içinde tekrar
	// alınmaz. Webhook timeout'undan uzun olmalı ki iş bitmeden yeniden
	// planlanmasın.
	lease := s.cfg.HTTPTimeout + 20*time.Second

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	s.log.Info("retry zamanlayıcısı başladı",
		slog.Duration("interval", interval),
		slog.Duration("lease", lease))

	for {
		select {
		case <-ctx.Done():
			s.log.Info("retry zamanlayıcısı kapanıyor")
			return nil

		case <-ticker.C:
			n, err := s.republishDue(ctx, writer, lease)
			if err != nil {
				s.log.ErrorContext(ctx, "zamanlanmış görevler yayınlanamadı",
					slog.String("err", err.Error()))
				continue
			}
			if n > 0 {
				s.log.Debug("zamanı gelen görevler yeniden yayınlandı", slog.Int("count", n))
			}
		}
	}
}

func (s *Server) republishDue(ctx context.Context, writer *kafka.Writer, lease time.Duration) (int, error) {
	tasks, err := s.store.ClaimDueTasks(ctx, s.cfg.SchedulerBatch, lease)
	if err != nil {
		return 0, err
	}
	if len(tasks) == 0 {
		return 0, nil
	}

	messages := make([]kafka.Message, 0, len(tasks))
	for _, t := range tasks {
		payload, err := json.Marshal(t.Task)
		if err != nil {
			s.log.WarnContext(ctx, "görev serileştirilemedi",
				slog.String("task_id", t.Task.TaskID), slog.String("err", err.Error()))
			continue
		}

		messages = append(messages, kafka.Message{
			Key:   []byte(t.Task.TaskID),
			Value: payload,
			Headers: []kafka.Header{
				kafkax.Header("event_id", t.Task.Envelope.EventID),
				kafkax.Header("trace_id", t.Task.Envelope.TraceID),
				kafkax.Header("retry", "true"),
			},
		})
	}

	if len(messages) == 0 {
		return 0, nil
	}

	writeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := writer.WriteMessages(writeCtx, messages...); err != nil {
		s.mPublishErrors.WithLabelValues(s.cfg.Infra.Topics.Delivery).Inc()
		// Görevler kiralama süresi sonunda yeniden alınacak — kayıp yok.
		return 0, err
	}

	s.mRescheduled.Add(float64(len(messages)))
	return len(messages), nil
}

// runMetricsUpdater, periyodik gauge güncellemeleri.
func (s *Server) runMetricsUpdater(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case <-ticker.C:
			if n, err := s.store.PendingCount(ctx); err == nil {
				s.mPendingTasks.Set(float64(n))
			}
			s.mInFlight.Set(float64(s.bulkhead.InFlight()))

			// Circuit breaker durumlarını dışa aktar: Grafana'da hangi
			// subscriber'ın devre dışı olduğu anında görülür.
			for subID, state := range s.breakers.Snapshot() {
				s.mBreakerState.WithLabelValues(subID).Set(breakerStateValue(state))
			}
		}
	}
}
