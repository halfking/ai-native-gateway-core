package maascontracts_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
)

func TestEstimateQuoteRequest_JSONRoundtrip(t *testing.T) {
	tenantID := "ten_test_001"
	priceVersionID := "prv_001"
	window := "monthly"

	original := maascontracts.EstimateQuoteRequest{
		TenantID:           &tenantID,
		ModelSKU:           "model_gpt4",
		Quantity:           maascontracts.Quantity{Amount: "1000", Unit: "token", Window: &window},
		ResourceSnapshotID: "snap_001",
		PriceVersionID:     &priceVersionID,
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded maascontracts.EstimateQuoteRequest
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, *original.TenantID, *decoded.TenantID)
	assert.Equal(t, original.ModelSKU, decoded.ModelSKU)
	assert.Equal(t, original.Quantity.Amount, decoded.Quantity.Amount)
	assert.Equal(t, original.ResourceSnapshotID, decoded.ResourceSnapshotID)
	assert.Equal(t, *original.PriceVersionID, *decoded.PriceVersionID)
}

func TestEstimateQuoteResponse_JSONRoundtrip(t *testing.T) {
	expiresAt := time.Now().UTC().Add(1 * time.Hour).Truncate(time.Second)

	original := maascontracts.EstimateQuoteResponse{
		QuoteID:            "quo_test_001",
		QuoteSnapshotID:    "qsnap_001",
		PriceVersionID:     "prv_001",
		ResourceSnapshotID: "snap_001",
		Status:             maascontracts.QuoteStatusReference,
		Money:              maascontracts.Money{AmountMinor: "10000", Currency: "USD"},
		ExpiresAt:          &expiresAt,
		HoldCreated:        false,
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded maascontracts.EstimateQuoteResponse
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original.QuoteID, decoded.QuoteID)
	assert.Equal(t, original.Status, decoded.Status)
	assert.Equal(t, original.Money.AmountMinor, decoded.Money.AmountMinor)
	assert.Equal(t, original.HoldCreated, decoded.HoldCreated)
	assert.False(t, decoded.HoldCreated, "EstimateQuote must not create holds")
}

func TestActivateEntitlementRequest_JSONRoundtrip(t *testing.T) {
	tenantID := "ten_test_001"
	legalRef := "legal_ref_001"
	contractID := "ctr_001"

	original := maascontracts.ActivateEntitlementRequest{
		TenantID:        &tenantID,
		HoldID:          "hld_test_001",
		LegalSignoffRef: &legalRef,
		ContractID:      &contractID,
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded maascontracts.ActivateEntitlementRequest
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, *original.TenantID, *decoded.TenantID)
	assert.Equal(t, original.HoldID, decoded.HoldID)
	assert.Equal(t, *original.LegalSignoffRef, *decoded.LegalSignoffRef)
	assert.Equal(t, *original.ContractID, *decoded.ContractID)
}

func TestActivateEntitlementResponse_JSONRoundtrip(t *testing.T) {
	activatedAt := time.Now().UTC().Truncate(time.Second)

	original := maascontracts.ActivateEntitlementResponse{
		EntitlementID: "ent_test_001",
		Status:        maascontracts.EntitlementStatusActive,
		HoldID:        "hld_test_001",
		ActivatedAt:   &activatedAt,
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded maascontracts.ActivateEntitlementResponse
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original.EntitlementID, decoded.EntitlementID)
	assert.Equal(t, original.Status, decoded.Status)
	assert.Equal(t, original.HoldID, decoded.HoldID)
	require.NotNil(t, decoded.ActivatedAt)
	assert.Equal(t, original.ActivatedAt.Unix(), decoded.ActivatedAt.Unix())
}

func TestUsageHoldRequest_JSONRoundtrip(t *testing.T) {
	tenantID := "ten_test_001"
	modelSKU := "model_gpt4"

	original := maascontracts.UsageHoldRequest{
		TenantID:      &tenantID,
		EntitlementID: "ent_test_001",
		RequestID:     "req_test_001",
		Quantity:      maascontracts.Quantity{Amount: "100", Unit: "token"},
		ModelSKU:      &modelSKU,
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded maascontracts.UsageHoldRequest
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, *original.TenantID, *decoded.TenantID)
	assert.Equal(t, original.EntitlementID, decoded.EntitlementID)
	assert.Equal(t, original.RequestID, decoded.RequestID)
	assert.Equal(t, original.Quantity.Amount, decoded.Quantity.Amount)
	assert.Equal(t, *original.ModelSKU, *decoded.ModelSKU)
}

func TestUsageSettleRequest_JSONRoundtrip(t *testing.T) {
	tenantID := "ten_test_001"

	original := maascontracts.UsageSettleRequest{
		TenantID:          &tenantID,
		Actual:            maascontracts.Quantity{Amount: "85", Unit: "token"},
		StreamInterrupted: true,
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded maascontracts.UsageSettleRequest
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, *original.TenantID, *decoded.TenantID)
	assert.Equal(t, original.Actual.Amount, decoded.Actual.Amount)
	assert.Equal(t, original.StreamInterrupted, decoded.StreamInterrupted)
}

func TestUsageSettleResponse_JSONRoundtrip(t *testing.T) {
	entryID := "entry_001"

	original := maascontracts.UsageSettleResponse{
		UsageHoldID: "usg_test_001",
		Status:      maascontracts.UsageStatusSettled,
		Settled:     maascontracts.Quantity{Amount: "85", Unit: "token"},
		Released:    maascontracts.Quantity{Amount: "15", Unit: "token"},
		EntryID:     &entryID,
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded maascontracts.UsageSettleResponse
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original.UsageHoldID, decoded.UsageHoldID)
	assert.Equal(t, original.Status, decoded.Status)
	assert.Equal(t, original.Settled.Amount, decoded.Settled.Amount)
	assert.Equal(t, original.Released.Amount, decoded.Released.Amount)
	assert.Equal(t, *original.EntryID, *decoded.EntryID)
}

func TestUsageCorrectRequest_JSONRoundtrip(t *testing.T) {
	tenantID := "ten_test_001"
	reason := "billing_adjustment"

	original := maascontracts.UsageCorrectRequest{
		TenantID: &tenantID,
		EntryID:  "entry_001",
		Reversal: maascontracts.Quantity{Amount: "-10", Unit: "token"},
		Reason:   &reason,
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded maascontracts.UsageCorrectRequest
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, *original.TenantID, *decoded.TenantID)
	assert.Equal(t, original.EntryID, decoded.EntryID)
	assert.Equal(t, original.Reversal.Amount, decoded.Reversal.Amount)
	assert.Equal(t, *original.Reason, *decoded.Reason)
}
