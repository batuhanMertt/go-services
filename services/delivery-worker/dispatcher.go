package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/mindmons/integration-hub/internal/backoff"
	"github.com/mindmons/integration-hub/internal/breaker"
	"github.com/mindmons/integration-hub/internal/cryptox"
	"github.com/mindmons/integration-hub/internal/kafkax"
	"github.com/mindmons/integration-hub/internal/lock"
	"github.com/mindmons/integration-hub/internal/logging"
	"github.com/mindmons/integration-hub/internal/model"
	"github.com/mindmons/integration-hub/internal/redisx"
)

// Outbound webhook başlıkları — kontrat docs/api-contracts/webhook-delivery.md.
const (
	HeaderHubSignature = "X-Hub-Signature-256"
	HeaderHubTimestamp = "X-Hub-Timestamp"
	HeaderEventID      = "X-Event-Id"
	HeaderEventType    = "X-Event-Type"
	HeaderDeliveryID   = "X-Delivery-Id"
	HeaderAttempt      = "X-Delivery-Attempt"
	HeaderMaxAttempts  = "X-Delivery-Max-Attempts"
	HeaderTraceID      = "X-Trace-Id"
)

// runConsumer, bir consumer goroutine'i çalıştırır.
// Birden fazlası aynı consumer group'ta çalışır; Kafka partition'ları paylaştırır.
func (s *Server) runConsumer(index int) func(context.Context) error {
	return func(ctx context.Context) error {
		reader := kafkax.NewReader(kafkax.ReaderConfig{
			Brokers: s.cfg.Infra.KafkaBrokers,
			Topic:   s.cfg.Infra.Topics.Delivery,
			GroupID: s.cfg.ConsumerGroup,
			Logger:  s.log,
		})
		defer func() { _ = reader.Close() }()

		s.log.Info("delivery consumer başladı", slog.Int("index", index))

		for {
			msg, err := reader.FetchMessage(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) || ctx.Err() != nil {
					s.log.Info("delivery consumer kapanıyor", slog.Int("index", index))
					return nil
				}
				s.mConsumeErrors.WithLabelValues(s.cfg.Infra.Topics.Delivery).Inc()
				s.log.ErrorContext(ctx, "kafka okuma hatası", slog.String("err", err.Error()))

				select {
				case <-ctx.Done():
					return nil
				case <-time.After(time.Second):
				}
				continue
			}

			s.handleMessage(ctx, msg)

			if err := reader.CommitMessages(ctx, msg); err != nil && ctx.Err() == nil {
				s.log.WarnContext(ctx, "offset commit edilemedi", slog.String("err", err.Error()))
			}
		}
	}
}

// handleMessage, tek bir teslimat görevini işler.
//
// Offset DAİMA commit edilir (hata olsa bile). Gerekçe: görevin gerçek durumu
// Postgres'te tutulur ve retry zamanlayıcısı onu yeniden yayınlar. Kafka
// mesajını yeniden denemek, DB'deki durumla çakışan ikinci bir retry kaynağı
// yaratırdı. Tek retry otoritesi vardır: delivery_tasks tablosu.
func (s *Server) handleMessage(ctx context.Context, msg kafka.Message) {
	var task model.DeliveryTask
	if err := json.Unmarshal(msg.Value, &task); err != nil {
		s.log.ErrorContext(ctx, "ayrıştırılamayan teslimat görevi atlandı",
			slog.String("err", err.Error()), slog.Int64("offset", msg.Offset))
		return
	}

	ctx = logging.WithTraceID(ctx, task.Envelope.TraceID)

	// 1) Dağıtık kilit — aynı görevin iki worker tarafından işlenmesini engeller.
	handle, err := s.locker.Acquire(ctx, "delivery:"+task.TaskID, s.cfg.LockTTL)
	if errors.Is(err, lock.ErrNotAcquired) {
		s.mSkipped.WithLabelValues("locked").Inc()
		s.log.DebugContext(ctx, "görev başka bir worker'da, atlanıyor",
			slog.String("task_id", task.TaskID))
		return
	}
	if err != nil {
		// Redis erişilemiyor: fail-open ile devam (teslimatı durdurmaktansa
		// mükerrer gönderim riskini kabul ediyoruz — at-least-once).
		s.log.WarnContext(ctx, "kilit alınamadı, fail-open ile devam ediliyor",
			slog.String("err", err.Error()))
	}
	defer func() {
		if handle != nil {
			_ = handle.Release(context.WithoutCancel(ctx))
		}
	}()

	// 2) Bulkhead — yavaş komşu diğerlerini yavaşlatmasın.
	release, ok := s.bulkhead.TryAcquire(task.SubscriptionID)
	if !ok {
		s.mSkipped.WithLabelValues("bulkhead_full").Inc()
		next := time.Now().Add(s.cfg.DeferDelay)
		if err := s.store.Defer(ctx, task.TaskID, next, "bulkhead dolu, ertelendi"); err != nil {
			s.log.WarnContext(ctx, "görev ertelenemedi", slog.String("err", err.Error()))
		}
		return
	}
	defer release()

	// 3) Circuit breaker — sürekli hata veren hedefe ağa çıkmadan "hayır" de.
	if allowed, probe := s.breakers.Allow(task.SubscriptionID); !allowed {
		s.mSkipped.WithLabelValues("circuit_open").Inc()
		next := time.Now().Add(s.cfg.Breaker.OpenDuration)
		if err := s.store.Defer(ctx, task.TaskID, next,
			"circuit breaker açık: hedef sürekli hata veriyor"); err != nil {
			s.log.WarnContext(ctx, "görev ertelenemedi", slog.String("err", err.Error()))
		}
		s.log.InfoContext(ctx, "circuit breaker açık, teslimat ertelendi",
			slog.String("subscriber", task.SubscriberSlug))
		return
	} else if probe {
		s.log.InfoContext(ctx, "circuit breaker yarı açık, deneme gönderiliyor",
			slog.String("subscriber", task.SubscriberSlug))
	}

	// 4) Görev durumunu DB'de kilitle ve deneme numarasını al.
	attempt, err := s.store.BeginAttempt(ctx, task.TaskID)
	if errors.Is(err, ErrTaskNotActionable) {
		s.mSkipped.WithLabelValues("already_settled").Inc()
		return
	}
	if err != nil {
		s.log.ErrorContext(ctx, "deneme başlatılamadı", slog.String("err", err.Error()))
		return
	}

	s.dispatch(ctx, task, attempt)
}

// dispatch, webhook'u gönderir ve sonucu değerlendirir.
func (s *Server) dispatch(ctx context.Context, task model.DeliveryTask, attempt *AttemptContext) {
	sentAt := time.Now().UTC()

	payload := task.ToWebhookPayload(attempt.AttemptNo, sentAt)
	body, err := json.Marshal(payload)
	if err != nil {
		s.failAttempt(ctx, task, attempt, nil, "", 0, "payload serileştirilemedi: "+err.Error())
		return
	}

	timestamp := strconv.FormatInt(sentAt.Unix(), 10)

	// Platform kendi imzasını atar: subscriber "bu gerçekten hub'dan mı geldi"
	// sorusunu bu imzayla yanıtlar (FR-4.2).
	signature := cryptox.SignPrefixed([]byte(s.cfg.WebhookSigningKey), timestamp, body)

	headers := map[string]string{
		"Content-Type":     "application/json",
		"User-Agent":       "MindMons-IntegrationHub/1.0",
		HeaderHubSignature: signature,
		HeaderHubTimestamp: timestamp,
		HeaderEventID:      task.Envelope.EventID,
		HeaderEventType:    task.Envelope.EventType,
		HeaderDeliveryID:   task.TaskID,
		HeaderAttempt:      strconv.Itoa(attempt.AttemptNo),
		HeaderMaxAttempts:  strconv.Itoa(attempt.MaxRetries),
		HeaderTraceID:      task.Envelope.TraceID,
	}
	// Aboneliğe özel ek başlıklar; platform başlıklarının üzerine YAZAMAZ.
	for k, v := range task.ExtraHeaders {
		if _, reserved := headers[k]; !reserved {
			headers[k] = v
		}
	}

	timeout := time.Duration(task.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = s.cfg.HTTPTimeout
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, task.TargetURL, bytes.NewReader(body))
	if err != nil {
		s.failAttempt(ctx, task, attempt, nil, "", 0, "istek oluşturulamadı: "+err.Error())
		return
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := s.client.Do(req)
	latency := time.Since(start)

	s.mDeliveryLatency.WithLabelValues(task.SubscriberSlug).Observe(latency.Seconds())

	if err != nil {
		// Ağ hatası / timeout: geçici kabul edilir, retry edilir.
		s.failAttempt(ctx, task, attempt, nil, "", int(latency.Milliseconds()),
			"http isteği başarısız: "+err.Error())
		return
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}()

	snippet := readSnippet(resp.Body, s.cfg.ResponseSnippetLen)
	status := resp.StatusCode

	if status >= 200 && status < 300 {
		s.succeedAttempt(ctx, task, attempt, headers, status, snippet, int(latency.Milliseconds()))
		return
	}

	s.failAttempt(ctx, task, attempt, &status, snippet, int(latency.Milliseconds()),
		fmt.Sprintf("hedef %d döndü", status))
}

func (s *Server) succeedAttempt(ctx context.Context, task model.DeliveryTask,
	attempt *AttemptContext, headers map[string]string, status int, snippet string, latencyMS int) {

	if err := s.store.RecordAttempt(ctx, task.TaskID, attempt.AttemptNo,
		redactHeaders(headers), &status, snippet, latencyMS, ""); err != nil {
		s.log.WarnContext(ctx, "deneme kaydı yazılamadı", slog.String("err", err.Error()))
	}

	if err := s.store.MarkSucceeded(ctx, task.TaskID); err != nil {
		s.log.ErrorContext(ctx, "görev başarılı işaretlenemedi", slog.String("err", err.Error()))
	}

	s.breakers.Success(task.SubscriptionID)

	s.mAttempts.WithLabelValues("success", backoff.StatusClass(status), task.SubscriberSlug).Inc()

	// Uçtan uca gecikme: publisher'ın event'i gönderdiği andan subscriber'ın
	// 200 döndüğü ana kadar. Boru hattının gerçek SLI'ı budur.
	if !task.ReceivedAt.IsZero() {
		s.mE2ELatency.Observe(time.Since(task.ReceivedAt).Seconds())
	}

	s.log.InfoContext(ctx, "webhook teslim edildi",
		slog.String("subscriber", task.SubscriberSlug),
		slog.String("event_id", task.Envelope.EventID),
		slog.Int("attempt", attempt.AttemptNo),
		slog.Int("status", status),
		slog.Int("latency_ms", latencyMS))
}

// failAttempt, başarısız denemeyi kaydeder ve retry / DLQ kararını verir.
func (s *Server) failAttempt(ctx context.Context, task model.DeliveryTask,
	attempt *AttemptContext, status *int, snippet string, latencyMS int, errMsg string) {

	statusCode := 0
	if status != nil {
		statusCode = *status
	}

	if err := s.store.RecordAttempt(ctx, task.TaskID, attempt.AttemptNo,
		nil, status, snippet, latencyMS, errMsg); err != nil {
		s.log.WarnContext(ctx, "deneme kaydı yazılamadı", slog.String("err", err.Error()))
	}

	s.breakers.Failure(task.SubscriptionID)
	s.mAttempts.WithLabelValues("failure", backoff.StatusClass(statusCode), task.SubscriberSlug).Inc()

	// Karar 1: bu hata yeniden denenebilir mi?
	// Ağ hatası/timeout (statusCode == 0) daima yeniden denenir.
	retryable := statusCode == 0 || backoff.IsRetryable(statusCode)

	// Karar 2: deneme hakkı kaldı mı?
	maxRetries := attempt.MaxRetries
	if maxRetries <= 0 {
		maxRetries = s.cfg.MaxRetries
	}
	hasBudget := attempt.AttemptNo < maxRetries+1

	if retryable && hasBudget {
		delay := s.cfg.Backoff.Delay(attempt.AttemptNo)
		next := time.Now().Add(delay)

		if err := s.store.ScheduleRetry(ctx, task.TaskID, next, errMsg); err != nil {
			s.log.ErrorContext(ctx, "retry planlanamadı", slog.String("err", err.Error()))
			return
		}

		s.mRetries.WithLabelValues(task.SubscriberSlug).Inc()
		s.log.WarnContext(ctx, "teslimat başarısız, yeniden denenecek",
			slog.String("subscriber", task.SubscriberSlug),
			slog.String("event_id", task.Envelope.EventID),
			slog.Int("attempt", attempt.AttemptNo),
			slog.Int("max_retries", maxRetries),
			slog.Duration("retry_in", delay),
			slog.String("error", errMsg))
		return
	}

	// Yeniden denenemez veya bütçe bitti → DLQ.
	var reason string
	if retryable {
		reason = fmt.Sprintf("maksimum deneme sayısı aşıldı (%d): %s", maxRetries, errMsg)
	} else {
		reason = fmt.Sprintf("kalıcı hata (%d): %s", statusCode, errMsg)
	}

	if err := s.store.MarkDead(ctx, task.TaskID, reason); err != nil {
		s.log.ErrorContext(ctx, "görev ölü işaretlenemedi", slog.String("err", err.Error()))
	}

	if err := s.publishDLQ(ctx, task, attempt.AttemptNo, reason); err != nil {
		s.log.ErrorContext(ctx, "DLQ'ya yayınlanamadı", slog.String("err", err.Error()))
		s.mPublishErrors.WithLabelValues(s.cfg.Infra.Topics.DLQ).Inc()
		return
	}

	s.mDLQ.WithLabelValues(task.SubscriberSlug).Inc()
	s.log.ErrorContext(ctx, "teslimat DLQ'ya alındı",
		slog.String("subscriber", task.SubscriberSlug),
		slog.String("event_id", task.Envelope.EventID),
		slog.Int("attempts", attempt.AttemptNo),
		slog.String("reason", reason))
}

func (s *Server) publishDLQ(ctx context.Context, task model.DeliveryTask, attempts int, reason string) error {
	dlq := model.DLQMessage{
		TaskID:          task.TaskID,
		EventInstanceID: task.EventInstanceID,
		SubscriptionID:  task.SubscriptionID,
		TargetURL:       task.TargetURL,
		Reason:          reason,
		Attempts:        attempts,
		FailedAt:        time.Now().UTC(),
		Envelope:        task.Envelope,
	}

	payload, err := json.Marshal(dlq)
	if err != nil {
		return err
	}

	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()

	return s.dlqWriter.WriteMessages(writeCtx, kafka.Message{
		Key:   []byte(task.TaskID),
		Value: payload,
		Headers: []kafka.Header{
			kafkax.Header("event_id", task.Envelope.EventID),
			kafkax.Header("trace_id", task.Envelope.TraceID),
			kafkax.Header("subscription_id", task.SubscriptionID),
		},
	})
}

func readSnippet(body io.Reader, limit int) string {
	if limit <= 0 {
		limit = 512
	}
	buf := make([]byte, limit)
	n, _ := io.ReadFull(io.LimitReader(body, int64(limit)), buf)
	return string(buf[:n])
}

// redactHeaders, denetim kaydına yazılmadan önce imza gibi hassas başlıkları
// maskeler. Teslimat logları admin panelinde görünür; imza sızmamalıdır.
func redactHeaders(h map[string]string) map[string]string {
	if h == nil {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		switch k {
		case HeaderHubSignature, "Authorization":
			out[k] = "***redacted***"
		default:
			out[k] = v
		}
	}
	return out
}

// breakerStateValue, Prometheus gauge için sayısal karşılık.
func breakerStateValue(st breaker.State) float64 {
	switch st {
	case breaker.StateOpen:
		return 1
	case breaker.StateHalfOpen:
		return 2
	default:
		return 0
	}
}

// redisKeyForBreaker, ileride durumu paylaşmak istenirse kullanılacak anahtar.
var _ = redisx.NSBreaker
