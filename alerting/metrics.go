package alerting

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// AlertsFired 告警触发次数
	AlertsFired = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_gateway_alerts_fired_total",
			Help: "Total number of alerts fired",
		},
		[]string{"severity", "name"},
	)

	// AlertsResolved 告警解决次数
	AlertsResolved = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_gateway_alerts_resolved_total",
			Help: "Total number of alerts resolved",
		},
		[]string{"severity", "name"},
	)

	// ActiveAlerts 当前活跃告警数
	ActiveAlerts = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "llm_gateway_active_alerts",
			Help: "Number of currently active alerts",
		},
		[]string{"severity"},
	)

	// NotificationsSent 通知发送次数
	NotificationsSent = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_gateway_notifications_sent_total",
			Help: "Total number of notifications sent",
		},
		[]string{"notifier", "status"}, // status: success|failure
	)
)

// RecordAlert 记录告警
func RecordAlert(severity Severity, status string) {
	if status == "firing" {
		AlertsFired.WithLabelValues(string(severity), "").Inc()
		ActiveAlerts.WithLabelValues(string(severity)).Inc()
	} else if status == "resolved" {
		AlertsResolved.WithLabelValues(string(severity), "").Inc()
		ActiveAlerts.WithLabelValues(string(severity)).Dec()
	}
}

// RecordNotification 记录通知
func RecordNotification(notifier, status string) {
	NotificationsSent.WithLabelValues(notifier, status).Inc()
}
