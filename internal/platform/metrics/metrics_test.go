package metrics

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
)

// Собирает метрики из отдельного реестра без глобального состояния.
func gatherMetrics(t *testing.T, m *Metrics) map[string]*dto.MetricFamily {
	t.Helper()
	families, err := m.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]*dto.MetricFamily, len(families))
	for _, family := range families {
		result[family.GetName()] = family
	}
	return result
}

// Преобразует метки в карту для проверки их состава и значений.
func metricLabels(metric *dto.Metric) map[string]string {
	labels := make(map[string]string, len(metric.GetLabel()))
	for _, label := range metric.GetLabel() {
		labels[label.GetName()] = label.GetValue()
	}
	return labels
}

// Проверяет счётчики по статусам и перевод длительности в секунды.
func TestObserveHTTPRecordsCounterAndHistogram(t *testing.T) {
	m := NewMetrics()
	const route = "POST /v1/orders"
	m.ObserveHTTP(http.MethodPost, route, 201, 100*time.Millisecond)
	m.ObserveHTTP(http.MethodPost, route, 201, 250*time.Millisecond)
	m.ObserveHTTP(http.MethodPost, route, 429, 1500*time.Millisecond)
	families := gatherMetrics(t, m)
	requests := families["food_http_requests_total"]
	if requests == nil || requests.GetType() != dto.MetricType_COUNTER || len(requests.GetMetric()) != 2 {
		t.Fatalf("requests=%v", requests)
	}
	counts := make(map[string]float64)
	for _, metric := range requests.GetMetric() {
		labels := metricLabels(metric)
		if !reflect.DeepEqual(labels, map[string]string{"service": "orders-service", "method": http.MethodPost, "route": route, "status": labels["status"]}) {
			t.Fatalf("labels=%v", labels)
		}
		counts[labels["status"]] = metric.GetCounter().GetValue()
	}
	if !reflect.DeepEqual(counts, map[string]float64{"201": 2, "429": 1}) {
		t.Fatalf("counts=%v", counts)
	}
	durations := families["food_http_request_duration_seconds"]
	if durations == nil || durations.GetType() != dto.MetricType_HISTOGRAM || len(durations.GetMetric()) != 1 {
		t.Fatalf("duration=%v", durations)
	}
	metric := durations.GetMetric()[0]
	if !reflect.DeepEqual(metricLabels(metric), map[string]string{"service": "orders-service", "method": http.MethodPost, "route": route}) {
		t.Fatalf("labels=%v", metricLabels(metric))
	}
	histogram := metric.GetHistogram()
	if histogram.GetSampleCount() != 3 || math.Abs(histogram.GetSampleSum()-1.85) > 1e-9 {
		t.Fatalf("histogram=%v", histogram)
	}
	buckets := make(map[float64]uint64)
	for _, bucket := range histogram.GetBucket() {
		buckets[bucket.GetUpperBound()] = bucket.GetCumulativeCount()
	}
	// Проверяем накопительные счётчики по обе стороны измеренных длительностей.
	for bound, want := range map[float64]uint64{0.05: 0, 0.1: 1, 0.25: 2, 1: 2, 2.5: 3, 10: 3} {
		got, exists := buckets[bound]
		if !exists || got != want {
			t.Fatalf("bucket=%g count=%d exists=%v want=%d", bound, got, exists, want)
		}
	}
}

// Объединяет произвольные методы и пустые маршруты в ограниченные метки.
func TestObserveHTTPNormalizesLabels(t *testing.T) {
	m := NewMetrics()
	for i := range 50 {
		m.ObserveHTTP(fmt.Sprintf("CUSTOM-%d", i), "", 404, time.Millisecond)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace} {
		m.ObserveHTTP(method, "GET /v1/orders/{id}", 405, time.Millisecond)
	}
	families := gatherMetrics(t, m)
	requests := families["food_http_requests_total"].GetMetric()
	if len(requests) != 10 {
		t.Fatalf("series=%d want=10", len(requests))
	}
	methods := make(map[string]bool)
	for _, metric := range requests {
		labels := metricLabels(metric)
		methods[labels["method"]] = true
		if labels["method"] == "OTHER" {
			if labels["route"] != "unmatched" || metric.GetCounter().GetValue() != 50 {
				t.Fatalf("fallback=%v", metric)
			}
		} else if labels["route"] != "GET /v1/orders/{id}" || metric.GetCounter().GetValue() != 1 {
			t.Fatalf("known method=%v", metric)
		}
	}
	if len(methods) != 10 || !methods["OTHER"] {
		t.Fatalf("methods=%v", methods)
	}
	if len(families["food_http_request_duration_seconds"].GetMetric()) != 10 {
		t.Fatal("histogram labels were not normalized")
	}
}

// Проверяет изоляцию экземпляров и наличие стандартных метрик Go.
func TestMetricsRegistriesAreIndependent(t *testing.T) {
	first, second := NewMetrics(), NewMetrics()
	first.ObserveHTTP(http.MethodGet, "GET /v1/orders/{id}", 200, time.Second)
	firstFamilies, secondFamilies := gatherMetrics(t, first), gatherMetrics(t, second)
	if firstFamilies["food_http_requests_total"] == nil || secondFamilies["food_http_requests_total"] != nil {
		t.Fatal("custom metrics leaked across registries")
	}
	for _, families := range []map[string]*dto.MetricFamily{firstFamilies, secondFamilies} {
		if families["go_goroutines"] == nil {
			t.Fatal("Go collector is missing")
		}
	}
	second.ObserveHTTP(http.MethodPost, "POST /v1/orders", 201, time.Second)
	firstMetrics := gatherMetrics(t, first)["food_http_requests_total"].GetMetric()
	if len(firstMetrics) != 1 || metricLabels(firstMetrics[0])["method"] != http.MethodGet {
		t.Fatal("second registry changed the first")
	}
}

// Проверяет HTTP-экспорт собственного реестра в текстовом формате Prometheus.
func TestMetricsHandlerExportsRegistry(t *testing.T) {
	m := NewMetrics()
	m.ObserveHTTP(http.MethodPost, "POST /v1/orders", 429, 250*time.Millisecond)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept", "text/plain; version=0.0.4")
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, req)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("status=%d content-type=%q body=%s", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	for _, want := range []string{
		"# TYPE food_http_requests_total counter",
		`food_http_requests_total{method="POST",route="POST /v1/orders",service="orders-service",status="429"} 1`,
		"# TYPE food_http_request_duration_seconds histogram",
		`food_http_request_duration_seconds_count{method="POST",route="POST /v1/orders",service="orders-service"} 1`,
		`food_http_request_duration_seconds_sum{method="POST",route="POST /v1/orders",service="orders-service"} 0.25`,
		"go_goroutines ",
	} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("missing %q in export", want)
		}
	}
}

// Проверяет конкурентное обновление во время сбора метрик без потери запросов.
func TestObserveHTTPConcurrent(t *testing.T) {
	m := NewMetrics()
	const workers, attempts = 8, 100
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range attempts {
				m.ObserveHTTP(http.MethodPost, "POST /v1/orders", 201, 10*time.Millisecond)
			}
		}()
	}
	close(start)
	for range 5 {
		gatherMetrics(t, m)
	}
	wg.Wait()
	families := gatherMetrics(t, m)
	requests := families["food_http_requests_total"].GetMetric()
	if len(requests) != 1 || requests[0].GetCounter().GetValue() != workers*attempts {
		t.Fatalf("requests=%v", requests)
	}
	histogram := families["food_http_request_duration_seconds"].GetMetric()[0].GetHistogram()
	if histogram.GetSampleCount() != workers*attempts || math.Abs(histogram.GetSampleSum()-8) > 1e-9 {
		t.Fatalf("histogram=%v", histogram)
	}
}
