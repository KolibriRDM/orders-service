package metrics

// Обновляет размер очереди и возраст самого старого события.
func (m *Metrics) SetOutboxStats(pending int64, oldestAgeSeconds float64) {
	m.outboxPending.Set(float64(pending))
	m.outboxOldestAge.Set(oldestAgeSeconds)
}

// Показывает доступность последнего измерения очереди.
func (m *Metrics) SetOutboxStatsAvailable(available bool) {
	if available {
		m.outboxStatsUp.Set(1)
	} else {
		m.outboxStatsUp.Set(0)
	}
}
