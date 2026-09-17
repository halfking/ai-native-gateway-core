package taskprofile

import "strings"

// reasons.go — annotation-reason enum for corrections. Mirrors the P2.1
// annotation reasons (annotation/types.go) so operators see one vocabulary;
// duplicated locally to keep taskprofile import-clean.

// correctionReasons is the closed set of accepted reason values.
var correctionReasons = []string{
	"performance",
	"cost",
	"availability",
	"quality",
	"other",
	"correct", // human confirms the auto task type
}

// IsValidReason reports whether reason is an accepted annotation reason.
func IsValidReason(reason string) bool {
	for _, r := range correctionReasons {
		if reason == r {
			return true
		}
	}
	return false
}

func reasonsCSV() string { return strings.Join(correctionReasons, ", ") }

func TaskTypesCSV() string { return strings.Join(TaskTypes(), ", ") }
