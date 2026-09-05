package credentialquota

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	metricAcquire = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "credential_client_quota_acquire_total",
		Help: "Acquire decisions by outcome.",
	}, []string{"outcome"})

	metricRelease = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "credential_client_quota_release_total",
		Help: "Release decisions by outcome.",
	}, []string{"outcome"})

	metricRenew = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "credential_client_quota_renew_total",
		Help: "Renew decisions by outcome.",
	}, []string{"outcome"})

	metricActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "credential_client_quota_active_leases",
		Help: "Current number of in-flight leases.",
	})
)

// Register attaches metrics to the supplied registerer. Safe to call once.
func Register(r prometheus.Registerer) error {
	for _, c := range []prometheus.Collector{metricAcquire, metricRelease, metricRenew, metricActive} {
		if err := r.Register(c); err != nil {
			return err
		}
	}
	return nil
}

// MustRegister is the test convenience wrapper.
func MustRegister() {
	if err := Register(prometheus.DefaultRegisterer); err != nil {
		if _, dup := err.(prometheus.AlreadyRegisteredError); dup {
			return
		}
		panic(err)
	}
}
