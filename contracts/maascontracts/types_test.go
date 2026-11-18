package maascontracts_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
)

func TestMoney_JSONRoundtrip(t *testing.T) {
	original := maascontracts.Money{
		AmountMinor: "1250",
		Currency:    "USD",
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded maascontracts.Money
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original.AmountMinor, decoded.AmountMinor)
	assert.Equal(t, original.Currency, decoded.Currency)
}

func TestQuantity_JSONRoundtrip(t *testing.T) {
	window := "monthly"
	original := maascontracts.Quantity{
		Amount: "1000.5",
		Unit:   "token",
		Window: &window,
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded maascontracts.Quantity
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original.Amount, decoded.Amount)
	assert.Equal(t, original.Unit, decoded.Unit)
	require.NotNil(t, decoded.Window)
	assert.Equal(t, *original.Window, *decoded.Window)
}

func TestQuerySellableRequest_JSONRoundtrip(t *testing.T) {
	tenantID := "ten_test_001"
	region := "us-west-1"
	commitment := "hard"

	original := maascontracts.QuerySellableRequest{
		TenantID: &tenantID,
		ModelSKU: "model_gpt4",
		Dimensions: &maascontracts.QuerySellableDimensions{
			Region:     &region,
			Commitment: &commitment,
		},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded maascontracts.QuerySellableRequest
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original.ModelSKU, decoded.ModelSKU)
	assert.Equal(t, *original.TenantID, *decoded.TenantID)
	assert.Equal(t, *original.Dimensions.Region, *decoded.Dimensions.Region)
	assert.Equal(t, *original.Dimensions.Commitment, *decoded.Dimensions.Commitment)
}

func TestQuerySellableResponse_JSONRoundtrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	window := "monthly"

	original := maascontracts.QuerySellableResponse{
		ResourceSnapshotID: "snap_001",
		InventoryRevision:  42,
		ObservedAt:         now,
		Stale:              false,
		Items: []maascontracts.SellableItem{
			{
				ModelSKU: "model_gpt4",
				Sellable: maascontracts.Quantity{
					Amount: "1000",
					Unit:   "token",
					Window: &window,
				},
				Unit: "token",
			},
		},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded maascontracts.QuerySellableResponse
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original.ResourceSnapshotID, decoded.ResourceSnapshotID)
	assert.Equal(t, original.InventoryRevision, decoded.InventoryRevision)
	assert.Equal(t, original.ObservedAt.Unix(), decoded.ObservedAt.Unix())
	assert.Equal(t, original.Stale, decoded.Stale)
	require.Len(t, decoded.Items, 1)
	assert.Equal(t, original.Items[0].ModelSKU, decoded.Items[0].ModelSKU)
	assert.Equal(t, original.Items[0].Sellable.Amount, decoded.Items[0].Sellable.Amount)
}

func TestHoldCreateRequest_JSONRoundtrip(t *testing.T) {
	tenantID := "ten_test_001"
	snapshotID := "snap_001"
	quoteSnapshotID := "qsnap_001"
	expectedVersion := int64(5)
	expiresAt := time.Now().UTC().Add(1 * time.Hour).Truncate(time.Second)
	window := "monthly"

	original := maascontracts.HoldCreateRequest{
		TenantID:           &tenantID,
		QuoteSnapshotID:    &quoteSnapshotID,
		ResourceSnapshotID: &snapshotID,
		ExpectedVersion:    &expectedVersion,
		Lines: []maascontracts.HoldLine{
			{
				ModelSKU: "model_gpt4",
				Quantity: maascontracts.Quantity{
					Amount: "1000",
					Unit:   "token",
					Window: &window,
				},
			},
		},
		ExpiresAt: expiresAt,
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded maascontracts.HoldCreateRequest
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, *original.TenantID, *decoded.TenantID)
	assert.Equal(t, *original.QuoteSnapshotID, *decoded.QuoteSnapshotID)
	assert.Equal(t, *original.ResourceSnapshotID, *decoded.ResourceSnapshotID)
	assert.Equal(t, *original.ExpectedVersion, *decoded.ExpectedVersion)
	assert.Equal(t, original.ExpiresAt.Unix(), decoded.ExpiresAt.Unix())
	require.Len(t, decoded.Lines, 1)
	assert.Equal(t, original.Lines[0].ModelSKU, decoded.Lines[0].ModelSKU)
	assert.Equal(t, original.Lines[0].Quantity.Amount, decoded.Lines[0].Quantity.Amount)
}

func TestHoldCreateResponse_JSONRoundtrip(t *testing.T) {
	expiresAt := time.Now().UTC().Add(1 * time.Hour).Truncate(time.Second)

	original := maascontracts.HoldCreateResponse{
		HoldID:            "hld_test_001",
		Status:            maascontracts.HoldStatusPending,
		FencingToken:      "fence_token_xyz",
		InventoryRevision: 43,
		ExpiresAt:         expiresAt,
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded maascontracts.HoldCreateResponse
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original.HoldID, decoded.HoldID)
	assert.Equal(t, original.Status, decoded.Status)
	assert.Equal(t, original.FencingToken, decoded.FencingToken)
	assert.Equal(t, original.InventoryRevision, decoded.InventoryRevision)
	assert.Equal(t, original.ExpiresAt.Unix(), decoded.ExpiresAt.Unix())
}

func TestHoldConfirmRequest_JSONRoundtrip(t *testing.T) {
	tenantID := "ten_test_001"
	expectedVersion := int64(43)
	quoteSnapshotID := "qsnap_001"

	original := maascontracts.HoldConfirmRequest{
		TenantID:        &tenantID,
		FencingToken:    "fence_token_xyz",
		ExpectedVersion: &expectedVersion,
		QuoteSnapshotID: &quoteSnapshotID,
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded maascontracts.HoldConfirmRequest
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, *original.TenantID, *decoded.TenantID)
	assert.Equal(t, original.FencingToken, decoded.FencingToken)
	assert.Equal(t, *original.ExpectedVersion, *decoded.ExpectedVersion)
	assert.Equal(t, *original.QuoteSnapshotID, *decoded.QuoteSnapshotID)
}

func TestHoldConfirmResponse_JSONRoundtrip(t *testing.T) {
	committedAt := time.Now().UTC().Truncate(time.Second)

	original := maascontracts.HoldConfirmResponse{
		HoldID:            "hld_test_001",
		Status:            maascontracts.HoldStatusCommitted,
		InventoryRevision: 44,
		CommittedAt:       &committedAt,
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded maascontracts.HoldConfirmResponse
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original.HoldID, decoded.HoldID)
	assert.Equal(t, original.Status, decoded.Status)
	assert.Equal(t, original.InventoryRevision, decoded.InventoryRevision)
	require.NotNil(t, decoded.CommittedAt)
	assert.Equal(t, original.CommittedAt.Unix(), decoded.CommittedAt.Unix())
}

func TestHoldReleaseRequest_JSONRoundtrip(t *testing.T) {
	tenantID := "ten_test_001"
	fencingToken := "fence_token_abc123"
	reason := "customer_cancelled"

	original := maascontracts.HoldReleaseRequest{
		TenantID:     &tenantID,
		FencingToken: &fencingToken,
		Reason:       &reason,
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded maascontracts.HoldReleaseRequest
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, *original.TenantID, *decoded.TenantID)
	assert.Equal(t, *original.FencingToken, *decoded.FencingToken)
	assert.Equal(t, *original.Reason, *decoded.Reason)
}

func TestError_JSONRoundtrip(t *testing.T) {
	traceID := "trace_123"
	original := maascontracts.Error{
		Code:      maascontracts.ErrorSellableInsufficient,
		Message:   "Insufficient inventory available",
		Retryable: false,
		TraceID:   &traceID,
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded maascontracts.Error
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original.Code, decoded.Code)
	assert.Equal(t, original.Message, decoded.Message)
	assert.Equal(t, original.Retryable, decoded.Retryable)
	require.NotNil(t, decoded.TraceID)
	assert.Equal(t, *original.TraceID, *decoded.TraceID)
}
