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
	"github.com/mindmons/integration-hub/internal/routing"
)

// runConsumer, `events.ingested` topic'ini tüketip teslimat görevleri üretir.
//
// Offset yönetimi: mesaj TAM OLARAK işlendikten sonra commit edilir. İşleme
// sırasında çökme olursa offset commit edilmediği için mesaj yeniden gelir
// (at-least-once). Mükerrer işlemenin zararsız olmasını delivery_tasks
// üzerindeki UNIQUE kısıtı sağlar.
//
// Yatay ölçekleme: aynı consumer group'ta birden fazla instance çalıştığında
// Kafka partition'ları aralarında paylaştırır. 6 partition → en fazla 6 etkin
// instance. Bu, `docker compose up --scale routing-engine=2` ile doğrulanabilir.
func (s *Server) runConsumer(ctx context.Context) error {
	reader := kafkax.NewReader(kafkax.ReaderConfig{
		Brokers:  s.cfg.Infra.KafkaBrokers,
		Topic:    s.cfg.Infra.Topics.Ingested,
		GroupID:  s.cfg.ConsumerGroup,
		MaxBytes: s.cfg.KafkaFetchMaxBytes,
		Logger:   s.log,
	})
	defer func() { _ = reader.Close() }()

	writer := kafkax.NewWriter(s.cfg.Infra.KafkaBrokers, s.cfg.Infra.Topics.Delivery)
	defer func() { _ = writer.Close() }()

	s.log.Info("routing engine tüketmeye başladı",
		slog.String("topic", s.cfg.Infra.Topics.Ingested),
		slog.String("group", s.cfg.ConsumerGroup))

	for {
		msg, err := reader.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				s.log.Info("routing engine kapanıyor")
				return nil
			}
			s.mConsumeErrors.WithLabelValues(s.cfg.Infra.Topics.Ingested).Inc()
			s.log.ErrorContext(ctx, "kafka okuma hatası", slog.String("err", err.Error()))

			// Broker geçici olarak erişilemez olabilir; sıkı döngüye girme.
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}

		if err := s.processMessage(ctx, writer, msg); err != nil {
			// İşlenemedi: offset COMMIT EDİLMEZ, mesaj yeniden gelecek.
			s.log.ErrorContext(ctx, "event işlenemedi, offset commit edilmedi",
				slog.String("err", err.Error()),
				slog.Int64("offset", msg.Offset),
				slog.Int("partition", msg.Partition))

			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}

		if err := reader.CommitMessages(ctx, msg); err != nil {
			// Commit başarısız: mesaj yeniden işlenecek (idempotent olduğu için sorun değil).
			s.log.WarnContext(ctx, "offset commit edilemedi", slog.String("err", err.Error()))
		}
	}
}

func (s *Server) processMessage(ctx context.Context, writer *kafka.Writer, msg kafka.Message) error {
	start := time.Now()

	var ingested model.IngestedEvent
	if err := json.Unmarshal(msg.Value, &ingested); err != nil {
		// Zehirli mesaj (poison message): asla ayrıştırılamayacağı için
		// sonsuza dek yeniden denemenin anlamı yok. Loglayıp atlıyoruz;
		// mesaj Kafka'da retention süresince incelenebilir durumda kalır.
		s.mRouted.WithLabelValues("malformed").Inc()
		s.log.ErrorContext(ctx, "ayrıştırılamayan mesaj atlandı",
			slog.String("err", err.Error()), slog.Int64("offset", msg.Offset))
		return nil
	}

	env := ingested.Envelope
	ctx = logging.WithTraceID(ctx, env.TraceID)

	// Kanonik event_instance id'sini DB'den çöz (outbox'taki değer yer tutucudur).
	instanceID, err := s.store.ResolveEventInstanceID(ctx, env.EventID)
	if err != nil {
		return err
	}

	targets, err := s.cache.Targets(ctx, env.EventType, env.EventVersion)
	if err != nil {
		return err
	}

	doc := routing.BuildDocument(env.EventType, env.EventVersion, env.SourceTool,
		env.TraceID, env.Payload, env.Metadata)

	matched := 0
	var produced []kafka.Message

	for i := range targets {
		t := &targets[i]

		if !t.Matches(doc) {
			s.mRuleEvaluations.WithLabelValues("no_match").Inc()
			continue
		}
		s.mRuleEvaluations.WithLabelValues("match").Inc()
		matched++

		taskID, created, err := s.store.CreateDeliveryTask(ctx,
			instanceID, t.SubscriptionID, t.EndpointURL, t.MaxRetries, s.cfg.RecoveryDelay)
		if err != nil {
			return err
		}
		if !created {
			// Bu event bu abonelik için zaten işlenmiş: mükerrer tüketim.
			s.mTasks.WithLabelValues("duplicate").Inc()
			continue
		}

		task := model.DeliveryTask{
			TaskID:          taskID,
			EventInstanceID: instanceID,
			SubscriptionID:  t.SubscriptionID,
			SubscriberSlug:  t.SubscriberSlug,
			TargetURL:       t.EndpointURL,
			ExtraHeaders:    t.Headers,
			MaxRetries:      t.MaxRetries,
			TimeoutMS:       t.TimeoutMS,
			Attempt:         1,
			ReceivedAt:      ingested.ReceivedAt,
			Envelope:        env,
		}

		payload, err := json.Marshal(task)
		if err != nil {
			return err
		}

		produced = append(produced, kafka.Message{
			// Topic writer'da sabitlenmiştir; mesajda tekrar verilmez
			// (kafka-go ikisini birden kabul etmez).
			// Partition key = task id: aynı görevin retry'leri aynı partition'a
			// düşer, sıralama korunur ve worker'lar arasında dengeli dağılır.
			Key:   []byte(taskID),
			Value: payload,
			Headers: []kafka.Header{
				kafkax.Header("event_id", env.EventID),
				kafkax.Header("event_type", env.EventType),
				kafkax.Header("trace_id", env.TraceID),
				kafkax.Header("subscription_id", t.SubscriptionID),
			},
		})
		s.mTasks.WithLabelValues("created").Inc()
	}

	if len(produced) > 0 {
		writeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := writer.WriteMessages(writeCtx, produced...)
		cancel()

		if err != nil {
			s.mPublishErrors.WithLabelValues(s.cfg.Infra.Topics.Delivery).Inc()
			// Görevler DB'de "pending" olarak duruyor; delivery-worker'ın retry
			// zamanlayıcısı RecoveryDelay sonunda onları kurtaracak. Yine de
			// offset commit etmiyoruz ki mesaj yeniden denensin.
			return err
		}
	}

	if err := s.store.MarkEventRouted(ctx, instanceID, matched); err != nil {
		s.log.WarnContext(ctx, "event durumu güncellenemedi", slog.String("err", err.Error()))
	}

	if matched == 0 {
		s.mRouted.WithLabelValues("no_match").Inc()
		s.log.DebugContext(ctx, "event için eşleşen abonelik yok",
			slog.String("event_type", env.EventType))
	} else {
		s.mRouted.WithLabelValues("routed").Inc()
	}

	// Yönlendirme gecikmesi: ingest'ten teslimat görevi üretimine kadar.
	s.mRoutingLatency.Observe(time.Since(ingested.ReceivedAt).Seconds())
	s.mProcessDuration.Observe(time.Since(start).Seconds())

	return nil
}
