package casdoor

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// WebhookEvent is a Casdoor approval decision callback event.
type WebhookEvent struct {
	EventID         string `json:"eventId"`
	EventVersion    int    `json:"eventVersion"`
	Org             string `json:"org"`
	Application     string `json:"application"`
	TaskID          int    `json:"taskId"`
	ExternalID      string `json:"externalId"`
	Status          string `json:"status"` // approved/rejected/cancelled/expired
	Action          string `json:"action"`
	Actor           string `json:"actor"` // decider casdoor user id
	ResourceType    string `json:"resourceType"`
	ResourceID      string `json:"resourceId"`
	DecisionVersion int    `json:"decisionVersion"`
	Comment         string `json:"comment,omitempty"`
}

// VerifyWebhookSignatureV2 verifies Casdoor webhook v2 signature.
//
// Signature scheme (spec §9):
//
//	HMAC-SHA256(callbackSecret, "v1\n{eventId}\n{timestamp}\n{sha256hex(body)}")
//
// Headers:
//
//	X-Casdoor-Signature: v1=<hex>
//	X-Casdoor-Event-Id: <uuid>
//	X-Casdoor-Timestamp: <RFC3339>
//	X-Casdoor-Kid: <key-identifier>
//
// Protection:
//   - Replay attack: timestamp window validation (default 5 minutes)
//   - Forgery: HMAC signature over event_id + timestamp + body_hash
//   - Key rotation: kid verification
func (c *Client) VerifyWebhookSignatureV2(
	body []byte,
	signature, eventID, timestamp, kid string,
) error {
	// 1. Verify kid
	if c.config.CallbackKid != "" && kid != c.config.CallbackKid {
		return fmt.Errorf("kid mismatch: expected %q, got %q", c.config.CallbackKid, kid)
	}

	// 2. Verify timestamp window (anti-replay)
	ts, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return fmt.Errorf("invalid timestamp: %w", err)
	}
	drift := time.Since(ts)
	const toleranceWindow = 5 * time.Minute
	if drift > toleranceWindow || drift < -toleranceWindow {
		return fmt.Errorf("timestamp out of window: drift=%v", drift)
	}

	// 3. Verify signature
	if !strings.HasPrefix(signature, "v1=") {
		return fmt.Errorf("invalid signature format: must start with v1=")
	}

	bodyHash := sha256.Sum256(body)
	message := fmt.Sprintf("v1\n%s\n%s\n%s",
		eventID, timestamp, hex.EncodeToString(bodyHash[:]))

	mac := hmac.New(sha256.New, []byte(c.config.CallbackSecret))
	mac.Write([]byte(message))
	expected := hex.EncodeToString(mac.Sum(nil))

	provided := signature[3:] // strip "v1="
	if !hmac.Equal([]byte(expected), []byte(provided)) {
		return fmt.Errorf("signature mismatch")
	}

	return nil
}

// ParseWebhookEvent parses webhook request body into WebhookEvent.
func ParseWebhookEvent(body []byte) (*WebhookEvent, error) {
	var event WebhookEvent
	if err := json.Unmarshal(body, &event); err != nil {
		return nil, fmt.Errorf("parse webhook event: %w", err)
	}
	return &event, nil
}

// HandleWebhook is a helper that verifies signature and parses the event.
// The caller is responsible for processing the event and responding.
func (c *Client) HandleWebhook(r *http.Request) (*WebhookEvent, error) {
	// Read body
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	// Extract headers
	signature := r.Header.Get("X-Casdoor-Signature")
	eventID := r.Header.Get("X-Casdoor-Event-Id")
	timestamp := r.Header.Get("X-Casdoor-Timestamp")
	kid := r.Header.Get("X-Casdoor-Kid")

	// Verify signature
	if err := c.VerifyWebhookSignatureV2(body, signature, eventID, timestamp, kid); err != nil {
		return nil, fmt.Errorf("signature verification failed: %w", err)
	}

	// Parse event
	event, err := ParseWebhookEvent(body)
	if err != nil {
		return nil, err
	}

	return event, nil
}
