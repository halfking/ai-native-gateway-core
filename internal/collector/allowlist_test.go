//go:build !integration

package collector

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidatePayload_AllowsSchema(t *testing.T) {
	payload, err := json.Marshal(RuntimeMetrics{
		InstanceID:     "inst-1",
		Version:        "v1.0.0",
		LicenseKeyHash: "abcd1234abcd1234",
		Timestamp:      time.Now().UTC(),
		UptimeSecs:     60,
		ModelUsage:     map[string]int64{"gpt-4": 12},
		TenantCount:    3,
	})
	require.NoError(t, err)
	assert.NoError(t, ValidatePayload(payload))
}

func TestValidatePayload_RejectsForbiddenField(t *testing.T) {
	raw := []byte(`{"instance_id":"i","version":"v1","prompt":"secret"}`)
	err := ValidatePayload(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forbidden field")
}

func TestValidatePayload_RejectsUnknownField(t *testing.T) {
	raw := []byte(`{"instance_id":"i","version":"v1","unexpected_field":"x"}`)
	err := ValidatePayload(raw)
	require.Error(t, err)
}

func TestHashLicenseKey(t *testing.T) {
	assert.Len(t, HashLicenseKey("test-key"), 16)
	assert.Empty(t, HashLicenseKey(""))
}
