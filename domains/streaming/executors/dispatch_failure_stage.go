package executors

import "errors"

// dispatchFailureStage keeps pre-upstream admission failures distinguishable in
// the same candidate failure stream without expanding the persisted schema.
func dispatchFailureStage(err error) (stage, reason string) {
	switch {
	case errors.Is(err, errDispatchCircuitOpen):
		return "circuit", "circuit_open"
	case errors.Is(err, errDispatchKeysExhausted):
		return "key_rotation", "keys_exhausted"
	case err != nil:
		return "limiter", "admission_rejected"
	default:
		return "", ""
	}
}
