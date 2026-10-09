package metrics

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
)

// Проверяет начальные нули, абсолютные значения и признак доступности статистики.
func TestOutboxGauges(t *testing.T) {
	m := NewMetrics()
	check := func(pending, age, up float64) {
		t.Helper()
		families := gatherMetrics(t, m)
		for name, want := range map[string]float64{"food_outbox_pending_events": pending, "food_outbox_oldest_pending_age_seconds": age, "food_outbox_stats_up": up} {
			family := families[name]
			if family == nil || family.GetType() != dto.MetricType_GAUGE || len(family.GetMetric()) != 1 {
				t.Fatalf("family=%v", family)
			}
			metric := family.GetMetric()[0]
			if metric.GetGauge().GetValue() != want || !reflect.DeepEqual(metricLabels(metric), map[string]string{"service": "orders-service"}) {
				t.Fatalf("%s=%v want=%v", name, metric, want)
			}
		}
	}
	check(0, 0, 0)
	m.SetOutboxStats(7, 12.5)
	m.SetOutboxStatsAvailable(true)
	check(7, 12.5, 1)
	m.SetOutboxStatsAvailable(false)
	check(7, 12.5, 0)
	m.SetOutboxStats(2, 1.5)
	m.SetOutboxStatsAvailable(true)
	check(2, 1.5, 1)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept", "text/plain; version=0.0.4")
	m.Handler().ServeHTTP(w, req)
	for _, want := range []string{`food_outbox_pending_events{service="orders-service"} 2`, `food_outbox_oldest_pending_age_seconds{service="orders-service"} 1.5`, `food_outbox_stats_up{service="orders-service"} 1`} {
		if w.Code != 200 || !strings.Contains(w.Body.String(), want) {
			t.Fatalf("missing %s in export: %s", want, w.Body.String())
		}
	}
	m.SetOutboxStats(0, 0)
	check(0, 0, 1)
	other := NewMetrics()
	if gatherMetrics(t, other)["food_outbox_pending_events"].GetMetric()[0].GetGauge().GetValue() != 0 {
		t.Fatal("registry state leaked")
	}
}
