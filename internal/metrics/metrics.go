package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "zester"

// Registry holds all Zester Prometheus metrics and the underlying registry.
type Registry struct {
	reg *prometheus.Registry

	// -- Master metrics --

	ConnectedPeels prometheus.Gauge
	JobsTotal      *prometheus.CounterVec
	JobDuration    *prometheus.HistogramVec
	JobActive      prometheus.Gauge
	JobReclaims    prometheus.Counter

	FactsSyncTotal  prometheus.Counter
	FactsSyncErrors prometheus.Counter

	SettingsRenderDuration prometheus.Histogram

	StateApplyTotal    *prometheus.CounterVec
	StateApplyDuration *prometheus.HistogramVec

	TargetResolutionDuration prometheus.Histogram

	// -- Peel metrics --

	PeelConnected          prometheus.Gauge
	PeelFactsCollectDur    prometheus.Histogram
	PeelStateApplyTotal    *prometheus.CounterVec
	PeelStateApplyDuration *prometheus.HistogramVec
	PeelBeaconEventsTotal  *prometheus.CounterVec
	PeelUptime             prometheus.Gauge

	// -- NATS transport metrics --

	NATSMsgsPublished  prometheus.Counter
	NATSMsgsReceived   prometheus.Counter
	NATSBytesPublished prometheus.Counter
	NATSBytesReceived  prometheus.Counter
	NATSReconnects     prometheus.Counter
	NATSDisconnects    prometheus.Counter
	NATSSlowConsumers  prometheus.Counter
}

// NewMasterRegistry creates a Registry pre-populated with master-side metrics.
func NewMasterRegistry() *Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	reg.MustRegister(collectors.NewBuildInfoCollector())

	m := &Registry{reg: reg}

	m.ConnectedPeels = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "connected_peels",
		Help:      "Number of currently connected peels.",
	})

	m.JobsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "jobs_total",
		Help:      "Total jobs dispatched, partitioned by status.",
	}, []string{"status"})

	m.JobDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "job_duration_seconds",
		Help:      "End-to-end job duration from dispatch to last return.",
		Buckets:   []float64{.1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300},
	}, []string{"function"})

	m.JobActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "job_active",
		Help:      "Number of currently running jobs.",
	})

	m.JobReclaims = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "job_reclaims_total",
		Help:      "Total jobs reclaimed from dead masters by the orphan scanner.",
	})

	m.FactsSyncTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "facts_sync_total",
		Help:      "Total fact sync operations received from peels.",
	})

	m.FactsSyncErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "facts_sync_errors_total",
		Help:      "Total fact sync operations that failed.",
	})

	m.SettingsRenderDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "settings_render_duration_seconds",
		Help:      "Time to render settings templates for a single peel.",
		Buckets:   []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1},
	})

	m.StateApplyTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "state_apply_total",
		Help:      "Total state apply operations across all peels.",
	}, []string{"state", "result"})

	m.StateApplyDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "state_apply_duration_seconds",
		Help:      "State apply duration as reported by peels.",
		Buckets:   []float64{.1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120},
	}, []string{"state"})

	m.TargetResolutionDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "targeting_resolution_duration_seconds",
		Help:      "Time to resolve a targeting expression to peel IDs.",
		Buckets:   []float64{.0001, .0005, .001, .005, .01, .05, .1},
	})

	m.NATSMsgsPublished = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "nats_msgs_published_total",
		Help:      "Total NATS messages published.",
	})

	m.NATSMsgsReceived = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "nats_msgs_received_total",
		Help:      "Total NATS messages received.",
	})

	m.NATSBytesPublished = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "nats_bytes_published_total",
		Help:      "Total bytes published to NATS.",
	})

	m.NATSBytesReceived = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "nats_bytes_received_total",
		Help:      "Total bytes received from NATS.",
	})

	m.NATSReconnects = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "nats_reconnects_total",
		Help:      "Total NATS reconnection events.",
	})

	m.NATSDisconnects = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "nats_disconnects_total",
		Help:      "Total NATS disconnection events.",
	})

	m.NATSSlowConsumers = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "nats_slow_consumers_total",
		Help:      "Total NATS slow consumer events.",
	})

	reg.MustRegister(
		m.ConnectedPeels,
		m.JobsTotal,
		m.JobDuration,
		m.JobActive,
		m.JobReclaims,
		m.FactsSyncTotal,
		m.FactsSyncErrors,
		m.SettingsRenderDuration,
		m.StateApplyTotal,
		m.StateApplyDuration,
		m.TargetResolutionDuration,
		m.NATSMsgsPublished,
		m.NATSMsgsReceived,
		m.NATSBytesPublished,
		m.NATSBytesReceived,
		m.NATSReconnects,
		m.NATSDisconnects,
		m.NATSSlowConsumers,
	)

	return m
}

// NewPeelRegistry creates a Registry pre-populated with peel-side metrics.
func NewPeelRegistry() *Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	reg.MustRegister(collectors.NewBuildInfoCollector())

	m := &Registry{reg: reg}

	m.PeelConnected = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "peel_connected",
		Help:      "1 if the peel is connected to a master, 0 otherwise.",
	})

	m.PeelFactsCollectDur = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "peel_facts_collect_duration_seconds",
		Help:      "Time to collect all facts.",
		Buckets:   []float64{.01, .05, .1, .25, .5, 1, 2.5, 5},
	})

	m.PeelStateApplyTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "peel_state_apply_total",
		Help:      "Total state apply operations on this peel.",
	}, []string{"state", "result"})

	m.PeelStateApplyDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "peel_state_apply_duration_seconds",
		Help:      "Duration of state apply operations on this peel.",
		Buckets:   []float64{.1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120},
	}, []string{"state"})

	m.PeelBeaconEventsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "peel_beacon_events_total",
		Help:      "Total beacon events emitted by this peel.",
	}, []string{"beacon"})

	m.PeelUptime = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "peel_uptime_seconds",
		Help:      "Peel process uptime in seconds.",
	})

	m.NATSReconnects = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "nats_reconnects_total",
		Help:      "Total NATS reconnection events.",
	})

	m.NATSSlowConsumers = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "nats_slow_consumers_total",
		Help:      "Total NATS slow consumer events.",
	})

	reg.MustRegister(
		m.PeelConnected,
		m.PeelFactsCollectDur,
		m.PeelStateApplyTotal,
		m.PeelStateApplyDuration,
		m.PeelBeaconEventsTotal,
		m.PeelUptime,
		m.NATSReconnects,
		m.NATSSlowConsumers,
	)

	return m
}

// Handler returns an http.Handler that serves the /metrics endpoint for
// this registry.
func (m *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{
		Registry:            m.reg,
		EnableOpenMetrics:   true,
		MaxRequestsInFlight: 5,
	})
}
