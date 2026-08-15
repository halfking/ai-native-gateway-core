package outbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	HeaderSignature     = "X-Gateway-Event-Signature"
	HeaderTimestamp     = "X-Gateway-Event-Timestamp"
	HeaderNonce         = "X-Gateway-Event-Nonce"
	HeaderCorrelationID = "X-Correlation-ID"
)

// DeliveryError describes a non-2xx response from the session manager.
type DeliveryError struct {
	StatusCode int
	Body       string
}

func (e *DeliveryError) Error() string {
	return fmt.Sprintf("ai-session-manager returned %d: %s", e.StatusCode, e.Body)
}

// Retryable reports whether a delivery failure should be retried automatically.
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	var deliveryErr *DeliveryError
	if AsDeliveryError(err, &deliveryErr) {
		return deliveryErr.StatusCode >= 500 && deliveryErr.StatusCode <= 599
	}
	return true
}

// AsDeliveryError unwraps an error without exposing errors.As at call sites.
func AsDeliveryError(err error, target **DeliveryError) bool {
	for err != nil {
		if typed, ok := err.(*DeliveryError); ok {
			*target = typed
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

type deliveryRequest struct {
	Events []json.RawMessage `json:"events"`
}

// HTTPDelivererConfig configures contract-compliant webhook delivery.
type HTTPDelivererConfig struct {
	Endpoint        string
	Secret          string
	AcceptDuplicate bool
	HTTPClient      *http.Client
	Now             func() time.Time
	Nonce           func() (string, error)
}

// HTTPDeliverer posts outbox events to ai-session-manager.
type HTTPDeliverer struct {
	endpoint        string
	secret          string
	acceptDuplicate bool
	httpClient      *http.Client
	now             func() time.Time
	nonce           func() (string, error)
}

func NewHTTPDeliverer(cfg HTTPDelivererConfig) *HTTPDeliverer {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Nonce == nil {
		cfg.Nonce = randomNonce
	}
	return &HTTPDeliverer{
		endpoint: cfg.Endpoint, secret: cfg.Secret, acceptDuplicate: cfg.AcceptDuplicate,
		httpClient: cfg.HTTPClient,
		now:        cfg.Now,
		nonce:      cfg.Nonce,
	}
}

// Deliver posts one event in the contract's batch wrapper.
func (d *HTTPDeliverer) Deliver(ctx context.Context, env EventEnvelope) error {
	eventJSON, err := RenderWireEnvelope(env)
	if err != nil {
		return fmt.Errorf("render event: %w", err)
	}
	body, err := json.Marshal(deliveryRequest{Events: []json.RawMessage{eventJSON}})
	if err != nil {
		return fmt.Errorf("marshal delivery request: %w", err)
	}
	nonce, err := d.nonce()
	if err != nil {
		return fmt.Errorf("generate delivery nonce: %w", err)
	}
	timestamp := d.now().UTC().Format(time.RFC3339Nano)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create delivery request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderNonce, nonce)
	req.Header.Set(HeaderSignature, SignPayload(d.secret, timestamp, nonce, body))
	if env.CorrelationID != "" {
		req.Header.Set(HeaderCorrelationID, env.CorrelationID)
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("post delivery request: %w", err)
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &DeliveryError{StatusCode: resp.StatusCode, Body: string(responseBody)}
	}
	var ack struct {
		Data struct {
			Accepted   int `json:"accepted"`
			Duplicates int `json:"duplicates"`
			Failed     []struct {
				EventID string `json:"event_id"`
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"failed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(responseBody, &ack); err != nil {
		return fmt.Errorf("decode delivery acknowledgment: %w", err)
	}
	if len(ack.Data.Failed) > 0 {
		failed := ack.Data.Failed[0]
		return &DeliveryError{StatusCode: http.StatusUnprocessableEntity, Body: failed.Code + ": " + failed.Message}
	}
	if ack.Data.Accepted == 1 && ack.Data.Duplicates == 0 {
		return nil
	}
	if d.acceptDuplicate && ack.Data.Accepted == 0 && ack.Data.Duplicates == 1 {
		return nil
	}
	return fmt.Errorf("inconsistent delivery acknowledgment: accepted=%d duplicates=%d", ack.Data.Accepted, ack.Data.Duplicates)
}

func randomNonce() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
