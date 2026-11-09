// Package metrics exposes bounded-cardinality Prometheus instrumentation.
package metrics

import (
	"bufio"
	"context"
	"encoding/json"
	"maps"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/brendatamaar/arcana/internal/audit"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/store"
)

// Metrics owns an isolated registry; no global collectors are registered.
type Metrics struct {
	registry                                   *prometheus.Registry
	templates                                  map[string]bool
	creates                                    *prometheus.HistogramVec
	destroys, execs, egress, requests, orphans *prometheus.CounterVec
	durations, latencies                       *prometheus.HistogramVec
}

// New registers the PRD collectors and lazy snapshots from explicit sources.
func New(database *store.Store, cfg config.Config, pool func() map[string]int, stats func() audit.Stats) *Metrics {
	m := &Metrics{registry: prometheus.NewRegistry(), templates: map[string]bool{}}
	m.creates = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "arcana_sandbox_create_seconds", Help: "Successful sandbox acquisition latency."}, []string{"source"})
	m.destroys = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "arcana_sandboxes_destroyed_total", Help: "Terminated sandboxes by reason."}, []string{"reason"})
	m.execs = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "arcana_execs_total", Help: "Completed execs by result."}, []string{"result"})
	m.egress = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "arcana_egress_requests_total", Help: "Proxy decisions."}, []string{"decision"})
	m.requests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "arcana_api_requests_total", Help: "API responses by canonical route and status."}, []string{"route", "code"})
	m.orphans = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "arcana_reconcile_orphans_total", Help: "Managed orphan resources removed."}, []string{"kind"})
	m.durations = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "arcana_exec_duration_seconds", Help: "Exec latency by template."}, []string{"template"})
	m.latencies = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "arcana_api_request_seconds", Help: "API latency by canonical route."}, []string{"route"})
	m.registry.MustRegister(m.creates, m.destroys, m.execs, m.egress, m.requests, m.orphans, m.durations, m.latencies)
	for _, name := range []string{"pool", "cold", "snapshot"} {
		m.creates.WithLabelValues(name)
	}
	for _, name := range []string{"ok", "nonzero", "timeout", "cancelled", "error"} {
		m.execs.WithLabelValues(name)
	}
	for _, name := range []string{"allow", "deny"} {
		m.egress.WithLabelValues(name)
	}
	for _, name := range []string{"container", "volume", "network"} {
		m.orphans.WithLabelValues(name)
	}
	for _, name := range []string{"ttl", "idle", "exec_deadline", "disk_quota", "api", "shutdown", "lost", "other"} {
		m.destroys.WithLabelValues(name)
	}
	for _, template := range cfg.Templates {
		m.templates[template.Name] = true
		m.durations.WithLabelValues(template.Name)

	}
	names := make([]string, 0, len(cfg.Templates))
	for _, template := range cfg.Templates {
		names = append(names, template.Name)
	}
	m.registry.MustRegister(&poolCollector{names: names, source: pool, descriptor: prometheus.NewDesc("arcana_pool_available", "Fresh pool members.", []string{"template"}, nil)})
	defaults := map[[2]string]float64{}
	for _, template := range cfg.Templates {
		defaults[[2]string{template.Name, cfg.Runtime.Docker.OCIRuntime}] = 0
	}
	m.registry.MustRegister(&activeCollector{defaults: defaults, database: database, descriptor: prometheus.NewDesc("arcana_sandboxes_active", "Active sandbox reservations.", []string{"template", "runtime"}, nil)})
	for _, item := range []struct {
		name, help string
		read       func(audit.Stats) float64
	}{
		{"arcana_audit_events_dropped_total", "Audit admission and upstream queue losses.", func(s audit.Stats) float64 { return float64(s.Dropped) }},
		{"arcana_audit_write_errors_total", "Audit persistence and pruning failures.", func(s audit.Stats) float64 { return float64(s.Errors) }},
	} {
		read := item.read
		m.registry.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{Name: item.name, Help: item.help}, func() float64 {
			if stats == nil {
				return 0
			}
			return read(stats())
		}))
	}
	m.registry.MustRegister(prometheus.NewGauge(prometheus.GaugeOpts{Name: "arcana_ui_sessions_active", Help: "Dashboard sessions; enabled in v0.3."}))
	subscribers := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "arcana_event_subscribers", Help: "Live subscriptions; enabled in v0.3."}, []string{"stream"})
	subscribers.WithLabelValues("events")
	subscribers.WithLabelValues("exec_output")
	m.registry.MustRegister(subscribers)
	return m
}

type activeCollector struct {
	defaults   map[[2]string]float64
	database   *store.Store
	descriptor *prometheus.Desc
}

func (c *activeCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.descriptor }
func (c *activeCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rows, err := c.database.SQL.QueryContext(ctx, "SELECT template,runtime,COUNT(*) FROM sandboxes WHERE state IN ('creating','running','destroying') GROUP BY template,runtime LIMIT 1000")
	if err != nil {
		ch <- prometheus.NewInvalidMetric(c.descriptor, err)
		return
	}
	defer func() { _ = rows.Close() }()
	values := maps.Clone(c.defaults)
	for rows.Next() {
		var template, runtime string
		var count float64
		if err := rows.Scan(&template, &runtime, &count); err != nil {
			ch <- prometheus.NewInvalidMetric(c.descriptor, err)
			return
		}
		values[[2]string{template, runtime}] = count
	}
	for labels, count := range values {
		ch <- prometheus.MustNewConstMetric(c.descriptor, prometheus.GaugeValue, count, labels[0], labels[1])
	}
	if err := rows.Err(); err != nil {
		ch <- prometheus.NewInvalidMetric(c.descriptor, err)
	}
}

// ObserveAudit records operational outcomes independently of audit queue admission.
func (m *Metrics) ObserveAudit(event audit.Event) {
	var details struct {
		Template, Source, Reason, Result, Kind string
		Allowed                                bool
		Duration                               float64 `json:"duration_seconds"`
	}
	if json.Unmarshal(event.Details, &details) != nil {
		return
	}
	template := details.Template
	if !m.templates[template] {
		template = "other"
	}
	switch event.Action {
	case "sandbox.created":
		source := details.Source
		if source != "pool" && source != "cold" && source != "snapshot" {
			source = "cold"
		}
		m.creates.WithLabelValues(source).Observe(details.Duration)
	case "sandbox.destroyed":
		reason := details.Reason
		switch reason {
		case "ttl", "idle", "exec_deadline", "disk_quota", "api", "shutdown", "lost":
		default:
			reason = "other"
		}
		m.destroys.WithLabelValues(reason).Inc()
	case "exec.ended":
		result := details.Result
		switch result {
		case "ok", "nonzero", "timeout", "cancelled", "error":
		default:
			result = "error"
		}
		m.execs.WithLabelValues(result).Inc()
		m.durations.WithLabelValues(template).Observe(details.Duration)
	case "egress.decision":
		decision := "deny"
		if details.Allowed {
			decision = "allow"
		}
		m.egress.WithLabelValues(decision).Inc()
	case "reconcile.orphan":
		m.Orphan(details.Kind)
	}
}

// Orphan records only the fixed resource-kind vocabulary.
func (m *Metrics) Orphan(kind string) {
	switch kind {
	case "container", "volume", "network":
		m.orphans.WithLabelValues(kind).Inc()
	}
}

// Handler serves only the metrics endpoint with bounded concurrent gathers.
func (m *Metrics) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{MaxRequestsInFlight: 4, Timeout: 5 * time.Second}))
	return mux
}

type response struct {
	http.ResponseWriter
	status int
}

func (w *response) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *response) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
		w.ResponseWriter.WriteHeader(code)
	}
}

func (w *response) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}
func (w *response) Flush() { _ = http.NewResponseController(w.ResponseWriter).Flush() }

// HTTP records canonical route labels, including authentication failures.
func (m *Metrics) HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		wrapped := &response{ResponseWriter: w}
		next.ServeHTTP(wrapped, r)
		code := wrapped.status
		if code == 0 {
			code = http.StatusOK
		}
		route := routeLabel(r.URL.Path)
		m.requests.WithLabelValues(route, strconv.Itoa(code)).Inc()
		m.latencies.WithLabelValues(route).Observe(time.Since(started).Seconds())
	})
}

func routeLabel(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 3 && (parts[1] == "sandboxes" || parts[1] == "keys") {
		parts[2] = "{id}"
	}
	route := "/" + strings.Join(parts, "/")
	switch route {
	case "/v1/health", "/v1/templates", "/v1/audit", "/v1/keys", "/v1/keys/{id}", "/v1/sandboxes", "/v1/sandboxes/{id}", "/v1/sandboxes/{id}/keepalive", "/v1/sandboxes/{id}/exec", "/v1/sandboxes/{id}/exec/stream", "/v1/sandboxes/{id}/files", "/v1/sandboxes/{id}/files/list", "/v1/sandboxes/{id}/files/archive":
		return route
	default:
		return "unmatched"
	}
}

func (w *response) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buffer, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.status = http.StatusSwitchingProtocols
	}
	return conn, buffer, err
}

type poolCollector struct {
	names      []string
	source     func() map[string]int
	descriptor *prometheus.Desc
}

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.descriptor }
func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	values := map[string]int{}
	if c.source != nil {
		values = c.source()
	}
	for _, name := range c.names {
		ch <- prometheus.MustNewConstMetric(c.descriptor, prometheus.GaugeValue, float64(values[name]), name)
	}
}
