package bg

import "time"

var activeRecoveryProbeDelays = []time.Duration{
	10 * time.Second,
	30 * time.Second,
	60 * time.Second,
	5 * time.Minute,
	1000 * time.Second,
	time.Hour,
}

// RecoveryProbeDelay returns the next real-probe delay for a recoverable
// credential failure. Recently successful credentials get quick verification;
// inactive credentials are checked conservatively to avoid spending quota.
func RecoveryProbeDelay(active bool, attempt int) time.Duration {
	if !active {
		return 6 * time.Hour
	}
	if attempt < 0 {
		attempt = 0
	}
	if attempt >= len(activeRecoveryProbeDelays) {
		return activeRecoveryProbeDelays[len(activeRecoveryProbeDelays)-1]
	}
	return activeRecoveryProbeDelays[attempt]
}
