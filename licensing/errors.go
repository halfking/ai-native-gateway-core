package licensing

import "errors"

// License verification errors (additional to those in validator.go)
var (
	ErrFingerprintMismatch  = errors.New("hardware fingerprint does not match")
	ErrClockRollback        = errors.New("system clock rollback detected")
	ErrLicenseFileNotFound  = errors.New("license file not found")
	ErrLicenseFileCorrupted = errors.New("license file is corrupted")
)
