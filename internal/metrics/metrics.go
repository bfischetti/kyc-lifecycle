// Package metrics exposes the service's operational signals in Prometheus
// format: screening outcomes, Provider Errors, status transitions, the
// notification backlog, and HTTP latency.
//
// Labels are restricted to small, fixed sets (check types, results,
// statuses, route patterns), never ids or raw paths, so the number of
// series stays bounded.
package metrics

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
)

// Backlog reports undelivered Status Change Notifications.
type Backlog func(ctx context.Context) (pending, dead int64, err error)

// Metrics holds every collector, registered on its own registry.
type Metrics struct {
	reg            *prometheus.Registry
	checks         *prometheus.CounterVec
	providerErrors *prometheus.CounterVec
	transitions    *prometheus.CounterVec
	httpDuration   *prometheus.HistogramVec
}

// New registers the service's metrics plus the standard Go and process
// collectors. backlog is queried on every scrape.
func New(backlog Backlog, log *slog.Logger) *Metrics {
	m := &Metrics{
		reg: prometheus.NewRegistry(),
		checks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kyc_checks_total",
			Help: "Checks recorded, by check type and result.",
		}, []string{"check_type", "result"}),
		providerErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kyc_provider_errors_total",
			Help: "Screening Provider calls that returned no answer (timeouts, outages). Not Checks.",
		}, []string{"check_type"}),
		transitions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kyc_status_transitions_total",
			Help: "Committed Verification Status changes, by from and to status.",
		}, []string{"from", "to"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "kyc_http_request_duration_seconds",
			Help:    "HTTP request latency, by method, route pattern and status code.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route", "status"}),
	}
	m.reg.MustRegister(
		m.checks, m.providerErrors, m.transitions, m.httpDuration,
		&backlogCollector{backlog: backlog, log: log},
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	// Start every known check series at zero, so rates are defined from the
	// first scrape instead of appearing only after the first occurrence.
	for _, ct := range kyc.CheckTypes {
		m.providerErrors.WithLabelValues(string(ct))
		for _, r := range kyc.CheckResults {
			m.checks.WithLabelValues(string(ct), string(r))
		}
	}
	return m
}

// Handler serves the registry in Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{Registry: m.reg})
}

func (m *Metrics) CheckRecorded(t kyc.CheckType, r kyc.CheckResult) {
	m.checks.WithLabelValues(string(t), string(r)).Inc()
}

func (m *Metrics) ProviderError(t kyc.CheckType) {
	m.providerErrors.WithLabelValues(string(t)).Inc()
}

func (m *Metrics) StatusChanged(from, to kyc.Status) {
	m.transitions.WithLabelValues(string(from), string(to)).Inc()
}

// ObserveHTTP records one request. route must be a pattern, never a raw path.
func (m *Metrics) ObserveHTTP(method, route string, status int, d time.Duration) {
	m.httpDuration.WithLabelValues(method, route, strconv.Itoa(status)).Observe(d.Seconds())
}

// backlogCollector reads the outbox at scrape time, so the gauges are the
// database's truth rather than an in-process estimate, and agree across
// replicas.
type backlogCollector struct {
	backlog Backlog
	log     *slog.Logger
}

var backlogDesc = prometheus.NewDesc(
	"kyc_notifications",
	"Status Change Notifications not yet delivered: pending (still being retried) or dead (gave up).",
	[]string{"state"}, nil,
)

func (c *backlogCollector) Describe(ch chan<- *prometheus.Desc) { ch <- backlogDesc }

func (c *backlogCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pending, dead, err := c.backlog(ctx)
	if err != nil {
		// Omit the series rather than report a wrong number; an absent
		// gauge is visible to alerting, a stale zero is not.
		c.log.Warn("metrics: read notification backlog", slog.String("error", err.Error()))
		return
	}
	ch <- prometheus.MustNewConstMetric(backlogDesc, prometheus.GaugeValue, float64(pending), "pending")
	ch <- prometheus.MustNewConstMetric(backlogDesc, prometheus.GaugeValue, float64(dead), "dead")
}
