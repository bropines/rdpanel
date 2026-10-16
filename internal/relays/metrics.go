package relays

import (
	"github.com/prometheus/client_golang/prometheus"
)

// metrics are labelled by relay name: the set is bounded by the admin (one
// series per configured relay); gauges are rebuilt on every pass so renamed
// or deleted relays disappear.
type metrics struct {
	up         *prometheus.GaugeVec
	health     *prometheus.GaugeVec
	probe      *prometheus.GaugeVec
	probes     *prometheus.CounterVec
	active     *prometheus.GaugeVec
	configLag  *prometheus.GaugeVec
	weight     *prometheus.GaugeVec
	connected  prometheus.Gauge
	rotation   prometheus.Gauge
	enrollFail prometheus.Counter
	authFail   prometheus.Counter
}

func newMetrics(reg prometheus.Registerer) (*metrics, error) {
	m := &metrics{
		up: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "rdpanel", Subsystem: "relay", Name: "up",
			Help: "1 if the relay's last end-to-end probe passed.",
		}, []string{"relay", "kind"}),
		health: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "rdpanel", Subsystem: "relay", Name: "health",
			Help: "Computed relay health; 1 for the current state (pending, active, degraded, offline).",
		}, []string{"relay", "state"}),
		probe: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "rdpanel", Subsystem: "relay", Name: "probe_seconds",
			Help: "Data round trip of the last successful end-to-end relay probe.",
		}, []string{"relay"}),
		probes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "rdpanel", Subsystem: "relay", Name: "probes_total",
			Help: "End-to-end relay probes by result (ok, error).",
		}, []string{"relay", "result"}),
		active: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "rdpanel", Subsystem: "relay", Name: "active_sessions",
			Help: "Active relayed sessions reported by the relay agent (hbbr usage).",
		}, []string{"relay"}),
		configLag: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "rdpanel", Subsystem: "relay", Name: "config_lag",
			Help: "Desired minus acknowledged config version of a relay agent.",
		}, []string{"relay"}),
		weight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "rdpanel", Subsystem: "relay", Name: "rotation_weight",
			Help: "Entries of the relay in hbbs' computed relay list (0 = not in rotation).",
		}, []string{"relay"}),
		connected: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "rdpanel", Subsystem: "relay", Name: "agents_connected",
			Help: "Relay agents with an open WebSocket session.",
		}),
		rotation: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "rdpanel", Subsystem: "relay", Name: "rotation_entries",
			Help: "Entries in hbbs' computed relay list (with weight duplicates).",
		}),
		enrollFail: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "rdpanel", Subsystem: "relay", Name: "enroll_failures_total",
			Help: "Rejected relay agent enrollments (bad or expired token).",
		}),
		authFail: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "rdpanel", Subsystem: "relay", Name: "agent_auth_failures_total",
			Help: "Rejected relay agent sessions (bad signature, clock skew, revoked).",
		}),
	}
	if reg != nil {
		for _, c := range []prometheus.Collector{m.up, m.health, m.probe, m.probes, m.active, m.configLag,
			m.weight, m.connected, m.rotation, m.enrollFail, m.authFail} {
			if err := reg.Register(c); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}
