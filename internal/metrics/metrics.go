// Package metrics, Prometheus metriklerinin merkezî tanım noktasıdır.
//
// Pull modeli seçildi: her servis /metrics endpoint'ini expose eder, Prometheus
// toplar. Push gateway'e göre avantajı, servisin izleme altyapısını hiç
// tanımaması (gevşek bağlılık) ve `up` metriğinin bedava gelmesidir.
package metrics

import (
	"net/http"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry, servise özel bir Prometheus registry'sidir. Global default
// registry yerine açık bir registry kullanmak, testlerde metrik sızıntısını
// ve "duplicate metrics" panic'lerini önler.
type Registry struct {
	reg     *prometheus.Registry
	service string

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec
	httpInFlight prometheus.Gauge
}

// New, Go runtime ve process collector'ları önceden kayıtlı bir registry döner.
func New(serviceName string) *Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	labels := prometheus.Labels{"service": serviceName}

	r := &Registry{
		reg:     reg,
		service: serviceName,
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "http_requests_total",
			Help:        "Toplam HTTP isteği sayısı.",
			ConstLabels: labels,
		}, []string{"method", "route", "code"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "http_request_duration_seconds",
			Help: "HTTP istek süresi (saniye).",
			// NFR-2.1 ingest p95 < 50ms hedefini ölçebilmek için 50ms
			// civarında yoğunlaştırılmış bucket'lar.
			Buckets:     []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
			ConstLabels: labels,
		}, []string{"method", "route"}),
		httpInFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name:        "http_requests_in_flight",
			Help:        "Şu anda işlenmekte olan HTTP istek sayısı.",
			ConstLabels: labels,
		}),
	}

	reg.MustRegister(r.httpRequests, r.httpDuration, r.httpInFlight)
	return r
}

// Prometheus, alt seviye registry'ye erişim verir (servise özel metrik kaydı).
func (r *Registry) Prometheus() *prometheus.Registry { return r.reg }

// Service, registry'nin ait olduğu servis adını döner.
func (r *Registry) Service() string { return r.service }

// MustRegister, servise özel collector'ları kaydeder.
func (r *Registry) MustRegister(cs ...prometheus.Collector) { r.reg.MustRegister(cs...) }

// ConstLabels, servise özel metriklerde tutarlı `service` etiketi sağlar.
func (r *Registry) ConstLabels() prometheus.Labels {
	return prometheus.Labels{"service": r.service}
}

// ObserveHTTP, httpx middleware'i tarafından her istek sonunda çağrılır.
func (r *Registry) ObserveHTTP(method, route string, code int, seconds float64) {
	r.httpRequests.WithLabelValues(method, route, strconv.Itoa(code)).Inc()
	r.httpDuration.WithLabelValues(method, route).Observe(seconds)
}

// IncInFlight / DecInFlight, eşzamanlı istek sayacını yönetir.
func (r *Registry) IncInFlight() { r.httpInFlight.Inc() }
func (r *Registry) DecInFlight() { r.httpInFlight.Dec() }

// Handler, /metrics endpoint'i için HTTP handler döner.
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
		Registry:          r.reg,
	})
}

// NewCounterVec ve arkadaşları: servise özel metrikleri `service` etiketiyle
// ve doğru registry'de oluşturmak için kısayollar.
func (r *Registry) NewCounterVec(name, help string, labels ...string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: name, Help: help, ConstLabels: r.ConstLabels(),
	}, labels)
	r.reg.MustRegister(c)
	return c
}

func (r *Registry) NewGauge(name, help string) prometheus.Gauge {
	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: name, Help: help, ConstLabels: r.ConstLabels(),
	})
	r.reg.MustRegister(g)
	return g
}

func (r *Registry) NewGaugeVec(name, help string, labels ...string) *prometheus.GaugeVec {
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: name, Help: help, ConstLabels: r.ConstLabels(),
	}, labels)
	r.reg.MustRegister(g)
	return g
}

func (r *Registry) NewHistogramVec(name, help string, buckets []float64, labels ...string) *prometheus.HistogramVec {
	if buckets == nil {
		buckets = prometheus.DefBuckets
	}
	h := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: name, Help: help, Buckets: buckets, ConstLabels: r.ConstLabels(),
	}, labels)
	r.reg.MustRegister(h)
	return h
}
