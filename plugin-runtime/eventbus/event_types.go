package eventbus

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"time"
)

// Plugin lifecycle event types
const (
	EventTypePluginStarted  = "plugin.started"
	EventTypePluginStopped  = "plugin.stopped"
	EventTypePluginCrashed  = "plugin.crashed"
	EventTypePluginUpgraded = "plugin.upgraded"
	EventTypePluginEnabled  = "plugin.enabled"
	EventTypePluginDisabled = "plugin.disabled"
)

// MaaS inventory event types
const (
	EventTypeMaaSInventorySellableChanged      = "maas.inventory.sellable_changed"
	EventTypeMaaSInventoryReservationPrepared  = "maas.inventory.reservation_prepared"
	EventTypeMaaSInventoryReservationCommitted = "maas.inventory.reservation_committed"
	EventTypeMaaSInventoryReservationReleased  = "maas.inventory.reservation_released"
)

// MaaS commercial event types
const (
	EventTypeMaaSCommercialQuoteIssued   = "maas.commercial.quote_issued"
	EventTypeMaaSCommercialQuoteAccepted = "maas.commercial.quote_accepted"
	EventTypeMaaSCommercialQuoteRejected = "maas.commercial.quote_rejected"
)

// MaaS trading event types
const (
	EventTypeMaaSTradingContractConfirmed = "maas.trading.contract_confirmed"
	EventTypeMaaSTradingContractCancelled = "maas.trading.contract_cancelled"
	EventTypeMaaSTradingOrderPlaced       = "maas.trading.order_placed"
	EventTypeMaaSTradingOrderFulfilled    = "maas.trading.order_fulfilled"
)

// MaaS fulfillment event types
const (
	EventTypeMaaSFulfillmentPlanPublished = "maas.fulfillment.plan_published"
	EventTypeMaaSFulfillmentTaskAssigned  = "maas.fulfillment.task_assigned"
	EventTypeMaaSFulfillmentTaskCompleted = "maas.fulfillment.task_completed"
)

// MaaS ledger event types
const (
	EventTypeMaaSLedgerUsageRecorded = "maas.ledger.usage_recorded"
	EventTypeMaaSLedgerUsageSettled  = "maas.ledger.usage_settled"
	EventTypeMaaSLedgerInvoiceIssued = "maas.ledger.invoice_issued"
)

// Helper functions for creating events

// NewPluginEvent creates a plugin lifecycle event.
func NewPluginEvent(eventType, pluginID string, payload interface{}) (*Event, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	return &Event{
		ID:        generateEventID(),
		Type:      eventType,
		Source:    "plugin-runtime",
		Timestamp: time.Now(),
		Payload:   payloadJSON,
		Metadata: map[string]string{
			"plugin_id": pluginID,
		},
	}, nil
}

// NewMaaSEvent creates a MaaS business event.
func NewMaaSEvent(eventType, source string, payload interface{}) (*Event, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	return &Event{
		ID:        generateEventID(),
		Type:      eventType,
		Source:    source,
		Timestamp: time.Now(),
		Payload:   payloadJSON,
		Metadata:  make(map[string]string),
	}, nil
}

// NewEvent creates a generic event.
func NewEvent(eventType, source string, payload interface{}, metadata map[string]string) (*Event, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	if metadata == nil {
		metadata = make(map[string]string)
	}

	return &Event{
		ID:        generateEventID(),
		Type:      eventType,
		Source:    source,
		Timestamp: time.Now(),
		Payload:   payloadJSON,
		Metadata:  metadata,
	}, nil
}

// generateEventID generates a unique event ID.
func generateEventID() string {
	return fmt.Sprintf("evt-%d-%s", time.Now().UnixNano(), randomString(8))
}

// randomString generates a random alphanumeric string of length n.
func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}
