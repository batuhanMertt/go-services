package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/mindmons/integration-hub/internal/kafkax"
	"github.com/mindmons/integration-hub/internal/logging"
	"github.com/mindmons/integration-hub/internal/model"
)

// runConsumer, `events.dlq` topic'ini tüketip ölü mesajları kalıcılaştırır.
//
// DLQ'nun operasyonel değeri şudur: teslim edilemeyen bir mesaj sessizce
// kaybolmaz; sorgulanabilir, incelenebilir ve hedef düzeldiğinde yeniden
// gönderilebilir bir kayda dönüşür. DLQ olmayan sistemlerde bu mesajlar ya
// sonsuza dek yeniden denenir (kaynak yakar) ya da atılır (veri kaybı).
func (s *Server) runConsumer(ctx context.Context) error {
	reader := kafkax.NewReader(kafkax.ReaderConfig{
		Brokers: s.cfg.Infra.KafkaBrokers,
		Topic:   s.cfg.Infra.Topics.DLQ,
		GroupID: s.cfg.ConsumerGroup,
		Logger:  s.log,
	})
	defer func() { _ = reader.Close() }()

	s.log.Info("dlq consumer başladı", slog.String("topic", s.cfg.Infra.Topics.DLQ))

	for {
		msg, err := reader.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				s.log.Info("dlq consumer kapanıyor")
				return nil
			}
			s.mConsumeErrors.WithLabelValues(s.cfg.Infra.Topics.DLQ).Inc()
			s.log.ErrorContext(ctx, "kafka okuma hatası", slog.String("err", err.Error()))

			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}

		if err := s.storeDLQMessage(ctx, msg); err != nil {
			s.log.ErrorContext(ctx, "dlq kaydı yazılamadı, offset commit edilmedi",
				slog.String("err", err.Error()))

			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}

		if err := reader.CommitMessages(ctx, msg); err != nil && ctx.Err() == nil {
			s.log.WarnContext(ctx, "offset commit edilemedi", slog.String("err", err.Error()))
		}
	}
}

func (s *Server) storeDLQMessage(ctx context.Context, msg kafka.Message) error {
	var dlq model.DLQMessage
	if err := json.Unmarshal(msg.Value, &dlq); err != nil {
		// Zehirli mesaj: sonsuza dek yeniden denemenin anlamı yok.
		s.log.ErrorContext(ctx, "ayrıştırılamayan dlq mesajı atlandı",
			slog.String("err", err.Error()), slog.Int64("offset", msg.Offset))
		return nil
	}

	ctx = logging.WithTraceID(ctx, dlq.Envelope.TraceID)

	inserted, err := s.store.SaveDLQEvent(ctx, dlq)
	if err != nil {
		return err
	}

	label := "updated"
	if inserted {
		label = "inserted"
	}
	s.mStored.WithLabelValues(label).Inc()

	s.log.WarnContext(ctx, "mesaj karantinaya alındı",
		slog.String("event_id", dlq.Envelope.EventID),
		slog.String("event_type", dlq.Envelope.EventType),
		slog.String("target", dlq.TargetURL),
		slog.Int("attempts", dlq.Attempts),
		slog.String("reason", dlq.Reason))

	return nil
}

// runReplayWorker, admin'in replay talebi verdiği kayıtları yeniden yayınlar.
//
// Mimari not: admin-api yalnızca `status = replay_requested` yazar; Kafka'ya
// basma işi burada yapılır (mimari diyagramdaki "REPLAY / PUBLISH" oku).
// Bu ayrımın faydası, kontrol düzleminin broker bağımlılığı taşımaması ve
// replay talebinin kalıcı (DB'de) olmasıdır — servis yeniden başlasa bile
// talep kaybolmaz.
func (s *Server) runReplayWorker(ctx context.Context) error {
	writer := kafkax.NewWriter(s.cfg.Infra.KafkaBrokers, s.cfg.Infra.Topics.Delivery)
	defer func() { _ = writer.Close() }()

	interval := s.cfg.ReplayInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	s.log.Info("replay worker başladı", slog.Duration("interval", interval))

	for {
		select {
		case <-ctx.Done():
			s.log.Info("replay worker kapanıyor")
			return nil

		case <-ticker.C:
			n, err := s.processReplayQueue(ctx, writer)
			if err != nil {
				s.log.ErrorContext(ctx, "replay kuyruğu işlenemedi", slog.String("err", err.Error()))
				continue
			}
			if n > 0 {
				s.log.Info("dlq mesajları yeniden yayınlandı", slog.Int("count", n))
			}
		}
	}
}

func (s *Server) processReplayQueue(ctx context.Context, writer *kafka.Writer) (int, error) {
	candidates, err := s.store.ClaimReplayQueue(ctx, s.cfg.ReplayBatch)
	if err != nil {
		return 0, err
	}
	if len(candidates) == 0 {
		return 0, nil
	}

	messages := make([]kafka.Message, 0, len(candidates))
	dlqIDs := make([]string, 0, len(candidates))

	for _, c := range candidates {
		payload, err := json.Marshal(c.Task)
		if err != nil {
			s.log.WarnContext(ctx, "replay görevi serileştirilemedi",
				slog.String("dlq_id", c.DLQID), slog.String("err", err.Error()))
			continue
		}

		messages = append(messages, kafka.Message{
			Key:   []byte(c.TaskID),
			Value: payload,
			Headers: []kafka.Header{
				kafkax.Header("event_id", c.Task.Envelope.EventID),
				kafkax.Header("trace_id", c.Task.Envelope.TraceID),
				kafkax.Header("replay", "true"),
			},
		})
		dlqIDs = append(dlqIDs, c.DLQID)
	}

	if len(messages) == 0 {
		return 0, nil
	}

	writeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := writer.WriteMessages(writeCtx, messages...); err != nil {
		s.mPublishErrors.WithLabelValues(s.cfg.Infra.Topics.Delivery).Inc()

		// Görevler DB'de zaten 'pending' + next_attempt_at=now() durumunda;
		// delivery-worker'ın retry zamanlayıcısı onları kurtaracak. Yine de
		// operatör görebilsin diye hata kaydedilir.
		if merr := s.store.MarkReplayFailed(ctx, dlqIDs, err.Error()); merr != nil {
			s.log.WarnContext(ctx, "replay hatası kaydedilemedi", slog.String("err", merr.Error()))
		}
		return 0, err
	}

	s.mReplayed.Add(float64(len(messages)))
	return len(messages), nil
}

// runRetentionJob, saklama politikasını uygular (Adım 11.2).
//
// Zaman bazlı tablolar (event_instances, delivery_attempts, dlq_events)
// sınırsız büyürse disk dolar ve sorgular yavaşlar. Silme işlemi batch'ler
// halinde yapılır: tek bir dev DELETE, tabloyu uzun süre kilitler ve
// replikasyon gecikmesi yaratır.
func (s *Server) runRetentionJob(ctx context.Context) error {
	interval := s.cfg.RetentionInterval
	if interval <= 0 {
		interval = time.Hour
	}

	// Açılışta hemen çalıştırma: servis başlangıcında ağır silme işi yapmak
	// soğuk başlangıcı yavaşlatır. İlk tur bir dakika sonra.
	select {
	case <-ctx.Done():
		return nil
	case <-time.After(time.Minute):
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	s.runRetentionOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.runRetentionOnce(ctx)
		}
	}
}

func (s *Server) runRetentionOnce(ctx context.Context) {
	summary := map[string]int64{}

	if n, err := s.store.PurgeDLQ(ctx, s.cfg.DLQRetentionDays, s.cfg.RetentionBatch); err != nil {
		s.log.WarnContext(ctx, "dlq temizliği başarısız", slog.String("err", err.Error()))
	} else {
		summary["dlq_events"] = n
		s.mPurged.WithLabelValues("dlq_events").Add(float64(n))
	}

	if n, err := s.store.PurgeAttempts(ctx, s.cfg.AttemptRetentionDays, s.cfg.RetentionBatch); err != nil {
		s.log.WarnContext(ctx, "deneme logu temizliği başarısız", slog.String("err", err.Error()))
	} else {
		summary["delivery_attempts"] = n
		s.mPurged.WithLabelValues("delivery_attempts").Add(float64(n))
	}

	if n, err := s.store.PurgeEvents(ctx, s.cfg.EventRetentionDays, s.cfg.RetentionBatch); err != nil {
		s.log.WarnContext(ctx, "event temizliği başarısız", slog.String("err", err.Error()))
	} else {
		summary["event_instances"] = n
		s.mPurged.WithLabelValues("event_instances").Add(float64(n))
	}

	total := int64(0)
	for _, v := range summary {
		total += v
	}
	if total == 0 {
		return
	}

	s.log.Info("retention temizliği tamamlandı", slog.Any("deleted", summary))

	// Veri silme denetlenebilir olmalı: ne zaman, ne kadar silindi?
	if err := s.store.WriteAudit(ctx, "retention.purge", summary); err != nil {
		s.log.WarnContext(ctx, "retention denetim kaydı yazılamadı", slog.String("err", err.Error()))
	}
}

// runMetricsUpdater, DLQ birikimi metriğini günceller (alert kaynağı).
func (s *Server) runMetricsUpdater(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case <-ticker.C:
			n, err := s.store.PendingCount(ctx)
			if err != nil {
				continue
			}
			s.mPending.Set(float64(n))

			if n > s.cfg.AlertThreshold {
				s.mAlertActive.Set(1)
			} else {
				s.mAlertActive.Set(0)
			}
		}
	}
}
