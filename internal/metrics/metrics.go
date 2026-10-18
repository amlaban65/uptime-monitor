// Package metrics exposes Prometheus metrics.
//
// Two kinds of metric live here. Counters and histograms (checks run,
// latency, alerts sent) describe the work this replica did; Prometheus sums
// them across replicas. Gauges about monitor health are read from Postgres
// at scrape time instead, so every replica reports the same, correct value
// no matter which replica ran the last check.
package metrics

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/amlaban65/uptime-monitor/internal/monitor"
	"github.com/amlaban65/uptime-monitor/internal/store"
)

// GaugeSource reads current state from the database.
type GaugeSource interface {
	Gauges(ctx context.Context) (store.Gauges, error)
}

type Metrics struct {
	registry      *prometheus.Registry
	checks        *prometheus.CounterVec
	checkDuration *prometheus.HistogramVec
	alerts        *prometheus.CounterVec
}

// New registers all metrics. inFlight reports the scheduler's running checks.
func New(src GaugeSource, inFlight func() int, log *slog.Logger) *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		registry: reg,
		checks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "uptime_checks_total",
			Help: "Checks run by this replica, by monitor and result.",
		}, []string{"monitor", "result"}),
		checkDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "uptime_check_duration_seconds",
			Help:    "Check latency as measured by this replica.",
			Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
		}, []string{"monitor"}),
		alerts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "uptime_alert_attempts_total",
			Help: "Alert delivery attempts by this replica, by channel and result.",
		}, []string{"channel", "result"}),
	}
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.checks, m.checkDuration, m.alerts,
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "uptime_checks_in_flight",
			Help: "Checks currently running on this replica.",
		}, func() float64 { return float64(inFlight()) }),
		&dbCollector{src: src, log: log},
	)
	return m
}

// ObserveCheck is the scheduler's OnResult hook.
func (m *Metrics) ObserveCheck(mon monitor.Monitor, r monitor.Result) {
	result := "success"
	if !r.OK {
		result = "failure"
	}
	m.checks.WithLabelValues(mon.Name, result).Inc()
	m.checkDuration.WithLabelValues(mon.Name).Observe(r.Latency.Seconds())
}

// ObserveAlert is the dispatcher's OnAttempt hook.
func (m *Metrics) ObserveAlert(channel string, err error) {
	result := "success"
	if err != nil {
		result = "failure"
	}
	m.alerts.WithLabelValues(channel, result).Inc()
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

var (
	monitorUpDesc = prometheus.NewDesc("uptime_monitor_up",
		"1 if the monitor is up, 0 if it is down. Absent until the first check.", []string{"monitor"}, nil)
	openIncidentsDesc = prometheus.NewDesc("uptime_open_incidents",
		"Incidents that are currently open.", nil, nil)
	pendingAlertsDesc = prometheus.NewDesc("uptime_alerts_pending",
		"Alerts waiting to be delivered (including retries).", nil, nil)
	failedAlertsDesc = prometheus.NewDesc("uptime_alerts_failed",
		"Alerts that were abandoned after exhausting retries.", nil, nil)
	scrapeOKDesc = prometheus.NewDesc("uptime_db_scrape_success",
		"1 if the database-backed metrics were read successfully.", nil, nil)
)

type dbCollector struct {
	src GaugeSource
	log *slog.Logger
}

func (c *dbCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- monitorUpDesc
	ch <- openIncidentsDesc
	ch <- pendingAlertsDesc
	ch <- failedAlertsDesc
	ch <- scrapeOKDesc
}

func (c *dbCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	g, err := c.src.Gauges(ctx)
	if err != nil {
		c.log.Error("read metrics from database", "err", err)
		ch <- prometheus.MustNewConstMetric(scrapeOKDesc, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(scrapeOKDesc, prometheus.GaugeValue, 1)
	for name, status := range g.MonitorStatus {
		switch status {
		case "up":
			ch <- prometheus.MustNewConstMetric(monitorUpDesc, prometheus.GaugeValue, 1, name)
		case "down":
			ch <- prometheus.MustNewConstMetric(monitorUpDesc, prometheus.GaugeValue, 0, name)
		}
	}
	ch <- prometheus.MustNewConstMetric(openIncidentsDesc, prometheus.GaugeValue, float64(g.OpenIncidents))
	ch <- prometheus.MustNewConstMetric(pendingAlertsDesc, prometheus.GaugeValue, float64(g.PendingAlerts))
	ch <- prometheus.MustNewConstMetric(failedAlertsDesc, prometheus.GaugeValue, float64(g.FailedAlerts))
}
