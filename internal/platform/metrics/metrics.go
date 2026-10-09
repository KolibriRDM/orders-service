package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Хранит реестр и метрики сервиса.
type Metrics struct {
	registry        *prometheus.Registry
	httpRequests    *prometheus.CounterVec
	httpDuration    *prometheus.HistogramVec
	outboxPending   prometheus.Gauge
	outboxOldestAge prometheus.Gauge
	outboxStatsUp   prometheus.Gauge
}

// Создаёт отдельный реестр и регистрирует метрики сервиса.
func NewMetrics() *Metrics {
	registry := prometheus.NewRegistry()
	httpRequests := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace:   "food",
			Subsystem:   "http",
			Name:        "requests_total",
			Help:        "Total number of completed HTTP requests.",
			ConstLabels: prometheus.Labels{"service": "orders-service"},
		},
		[]string{"method", "route", "status"},
	)
	httpDuration := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace:   "food",
			Subsystem:   "http",
			Name:        "request_duration_seconds",
			Help:        "HTTP request duration in seconds.",
			ConstLabels: prometheus.Labels{"service": "orders-service"},
			// Границы длительности запросов заданы в секундах.
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{"method", "route"},
	)
	outboxPending := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "food",
		Subsystem:   "outbox",
		Name:        "pending_events",
		Help:        "Number of unpublished outbox events.",
		ConstLabels: prometheus.Labels{"service": "orders-service"},
	})

	outboxOldestAge := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "food",
		Subsystem:   "outbox",
		Name:        "oldest_pending_age_seconds",
		Help:        "Age of the oldest unpublished outbox event in seconds.",
		ConstLabels: prometheus.Labels{"service": "orders-service"},
	})

	outboxStatsUp := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "food",
		Subsystem:   "outbox",
		Name:        "stats_up",
		Help:        "Whether the latest outbox statistics query succeeded.",
		ConstLabels: prometheus.Labels{"service": "orders-service"},
	})
	registry.MustRegister(httpRequests, httpDuration, outboxPending, outboxOldestAge, outboxStatsUp, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return &Metrics{
		registry:        registry,
		httpRequests:    httpRequests,
		httpDuration:    httpDuration,
		outboxPending:   outboxPending,
		outboxOldestAge: outboxOldestAge,
		outboxStatsUp:   outboxStatsUp,
	}
}
