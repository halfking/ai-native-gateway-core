package licensing

import (
	"errors"
	"net/http"
)

const (
	CodeActivationSuccess      = "activation_success"
	CodeLicenseNotFound        = "license_not_found"
	CodeLicenseExpired         = "license_expired"
	CodeLicenseRevoked         = "license_revoked"
	CodeDeviceLimitExceeded    = "device_limit_exceeded"
	CodeDeviceAlreadyActivated = "device_already_activated"
)

func activationHTTPStatus(code string) int {
	switch code {
	case CodeLicenseNotFound:
		return http.StatusNotFound
	case CodeLicenseExpired:
		return http.StatusGone
	case CodeLicenseRevoked:
		return http.StatusForbidden
	case CodeDeviceLimitExceeded, CodeDeviceAlreadyActivated:
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

func mapValidatorError(err error) (code, message string) {
	switch {
	case errors.Is(err, ErrLicenseExpired):
		return CodeLicenseExpired, "license has expired"
	case errors.Is(err, ErrLicenseRevoked):
		return CodeLicenseRevoked, "license has been revoked"
	default:
		if err != nil && err.Error() == "license not found" {
			return CodeLicenseNotFound, "license not found"
		}
		return "", err.Error()
	}
}
