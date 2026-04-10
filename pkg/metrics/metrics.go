package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
)

var (
	// ScoreBucketCurrent tracks the current score in the bucket per node.
	ScoreBucketCurrent = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "npd_node_replace_score_bucket_current",
			Help: "Current score in the tolerance bucket for a node",
		},
		[]string{"node", "nodegroup"},
	)

	// ActionsTotal counts the total number of actions performed.
	ActionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "npd_node_replace_actions_total",
			Help: "Total number of actions performed on nodes",
		},
		[]string{"node", "action", "escalated"},
	)

	// EventsTotal counts the total number of NPD events recorded.
	EventsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "npd_node_replace_events_total",
			Help: "Total number of NPD events recorded per node",
		},
		[]string{"node", "event_type"},
	)

	// NIRActive tracks the number of active NodeIssueReport resources.
	NIRActive = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "npd_node_replace_nir_active",
			Help: "Number of active NodeIssueReport resources",
		},
	)
)

func init() {
	prometheus.MustRegister(ScoreBucketCurrent)
	prometheus.MustRegister(ActionsTotal)
	prometheus.MustRegister(EventsTotal)
	prometheus.MustRegister(NIRActive)
}

// StartMetricsServer starts an HTTP server exposing /metrics on the given address.
func StartMetricsServer(addr string) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	log.Infof("starting metrics server on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Errorf("metrics server failed: %v", err)
	}
}
