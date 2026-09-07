package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mindmons/integration-hub/internal/config"
	"github.com/mindmons/integration-hub/internal/metrics"
)

// ReadyCheck, bir bağımlılığın (Postgres, Redis, Kafka) hazır olup olmadığını
// döner. Nil hata = hazır.
type ReadyCheck func(ctx context.Context) error

// Health, liveness ve readiness ayrımını yönetir.
//
// Neden ikisi ayrı? /healthz "proses ayakta mı" sorusunu yanıtlar ve asla
// bağımlılık kontrol etmez — Postgres düştüğünde container'ın yeniden
// başlatılması sorunu çözmez, sadece restart döngüsü yaratır. /readyz ise
// "trafik alabilir miyim" sorusunu yanıtlar ve bağımlılıkları yoklar.
type Health struct {
	mu     sync.RWMutex
	checks map[string]ReadyCheck
}

func NewHealth() *Health {
	return &Health{checks: make(map[string]ReadyCheck)}
}

func (h *Health) Register(name string, c ReadyCheck) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks[name] = c
}

func (h *Health) run(ctx context.Context) map[string]string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	results := make(map[string]string, len(h.checks))
	for name, check := range h.checks {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if err := check(cctx); err != nil {
			results[name] = "error: " + err.Error()
		} else {
			results[name] = "ok"
		}
		cancel()
	}
	return results
}

// MountObservability, her serviste ortak olan /healthz, /readyz ve /metrics
// uçlarını bağlar.
func MountObservability(r chi.Router, reg *metrics.Registry, health *Health) {
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		JSON(w, http.StatusOK, map[string]string{
			"status":  "ok",
			"service": reg.Service(),
		})
	})

	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		results := health.run(r.Context())

		status := http.StatusOK
		for _, v := range results {
			if v != "ok" {
				status = http.StatusServiceUnavailable
				break
			}
		}

		JSON(w, status, map[string]any{
			"status":     map[bool]string{true: "ready", false: "not_ready"}[status == http.StatusOK],
			"service":    reg.Service(),
			"components": results,
		})
	})

	r.Handle("/metrics", reg.Handler())
}

// NewRouter, tüm servislerde ortak middleware zinciriyle bir chi router üretir.
// Sıralama önemlidir: trace → recover → güvenlik → log → metrik.
func NewRouter(logger *slog.Logger, reg *metrics.Registry, maxBody int64) *chi.Mux {
	r := chi.NewRouter()
	r.Use(TraceID)
	r.Use(Recover(logger))
	r.Use(SecurityHeaders)
	r.Use(RequestLogger(logger))
	r.Use(Metrics(reg))
	if maxBody > 0 {
		r.Use(MaxBody(maxBody))
	}
	return r
}

// Run, HTTP sunucusunu başlatır ve SIGINT/SIGTERM geldiğinde zarifçe kapatır.
// Graceful shutdown olmadan, kapanış anındaki webhook teslimatları yarıda
// kalır ve gereksiz retry üretir.
func Run(ctx context.Context, logger *slog.Logger, base config.Base, handler http.Handler) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:              base.Addr(),
		Handler:           handler,
		ReadTimeout:       base.HTTPReadTimeout,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      base.HTTPWriteTimeout,
		IdleTimeout:       base.HTTPIdleTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("http sunucusu dinliyor", slog.String("addr", base.Addr()))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("kapatma sinyali alındı, zarifçe kapatılıyor",
			slog.Duration("timeout", base.HTTPShutdownTimeout))
	}

	// Kapatma context'i BİLİNÇLİ olarak yeni bir kökten türetilir.
	// Parent context zaten iptal edilmiş durumdadır (kapatma sinyali onu
	// iptal etti); ondan türetseydik Shutdown anında iptal olur ve
	// in-flight istekler yarıda kesilirdi — graceful shutdown'ın tam tersi.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), base.HTTPShutdownTimeout) //nolint:contextcheck
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	logger.Info("http sunucusu kapatıldı")
	return nil
}

// RunWithWorkers, HTTP sunucusunun yanında arka plan işçileri de çalıştıran
// servisler (routing-engine, delivery-worker, dlq-handler) için kullanılır.
// Kapatma sinyali geldiğinde önce işçilere iptal verilir, sonra HTTP kapatılır.
func RunWithWorkers(
	ctx context.Context,
	logger *slog.Logger,
	base config.Base,
	handler http.Handler,
	workers ...func(context.Context) error,
) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	workerCtx, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()

	var wg sync.WaitGroup
	workerErr := make(chan error, len(workers))

	for i, w := range workers {
		wg.Add(1)
		go func(idx int, fn func(context.Context) error) {
			defer wg.Done()
			if err := fn(workerCtx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("arka plan işçisi hata verdi",
					slog.Int("worker", idx), slog.String("err", err.Error()))
				workerErr <- err
			}
		}(i, w)
	}

	srv := &http.Server{
		Addr:              base.Addr(),
		Handler:           handler,
		ReadTimeout:       base.HTTPReadTimeout,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      base.HTTPWriteTimeout,
		IdleTimeout:       base.HTTPIdleTimeout,
	}

	httpErr := make(chan error, 1)
	go func() {
		logger.Info("http sunucusu dinliyor", slog.String("addr", base.Addr()))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			httpErr <- err
		}
	}()

	var runErr error
	select {
	case err := <-httpErr:
		runErr = err
	case err := <-workerErr:
		runErr = err
	case <-ctx.Done():
		logger.Info("kapatma sinyali alındı")
	}

	cancelWorkers()

	// Kapatma context'i BİLİNÇLİ olarak yeni bir kökten türetilir.
	// Parent context zaten iptal edilmiş durumdadır (kapatma sinyali onu
	// iptal etti); ondan türetseydik Shutdown anında iptal olur ve
	// in-flight istekler yarıda kesilirdi — graceful shutdown'ın tam tersi.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), base.HTTPShutdownTimeout) //nolint:contextcheck
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && runErr == nil {
		runErr = err
	}

	// İşçilerin temiz kapanmasını bekle (offset commit, in-flight webhook).
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
		logger.Info("tüm işçiler kapandı")
	case <-time.After(base.HTTPShutdownTimeout):
		logger.Warn("işçiler zaman aşımında kapanmadı, zorla çıkılıyor")
	}

	return runErr
}
