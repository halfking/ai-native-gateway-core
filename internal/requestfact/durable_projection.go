package requestfact

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

const DurableProjectionInputVersionV1 = 1

var ErrInvalidDurableProjectionInput = errors.New("requestfact: invalid durable projection input")

// DurableProjectionInput is the pre-terminal input for durable execution
// snapshots. It carries only handler-resolved immutable facts available at the
// durable cut point: after authentication, session resolution, tool-ID
// expansion, and body normalization, but before candidate resolution.
//
// It is intentionally not derived from CanonicalRequestFact: that fact is a
// terminal archive document, while a durable task may still be running.
//
// Excluded by design: scheduler/store-owned state (task ID, status, leases,
// fencing, attempts, deadlines, results, ciphertext), credentials and routing
// material (authorization headers, cookies, API key values, credential IDs,
// selected providers, candidates, live routing state). Recovery re-verifies by
// APIKeyID and rebuilds routing dynamically. NormalizedBody is opaque user
// content destined for durable encryption; it is never keyword-filtered.
type DurableProjectionInput struct {
	Protocol           string
	Endpoint           string
	TenantID           string
	ApplicationID      int
	APIKeyID           int
	SessionID          string
	SessionSource      string
	ClientModel        string
	NormalizedBody     []byte
	ClientIdentityHash string
	ToolsRequested     bool
	RequestID          string
	ParentRequestID    string
	PolicyVersion      string
}

// DurableProjection is the neutral, credential-free projection of a
// DurableProjectionInput. A future domains/streaming bridge maps it onto the
// existing durable snapshot DTO; durable remains the encryption/persistence
// boundary.
type DurableProjection struct {
	Version              int
	Endpoint             string
	ClientProtocol       string
	ClientModel          string
	NormalizedBody       []byte
	RequestHash          string
	APIKeyID             int
	TenantID             string
	ApplicationID        int
	SessionID            string
	SessionSource        string
	ClientIdentityHash   string
	ToolsRequested       bool
	ResponseFormat       string
	HasMultimodalContent bool
	PolicyVersion        string
	RequestID            string
	TaskCorrelationID    string
	ParentRequestID      string
	Warnings             []ConversionWarning
}

// ProjectDurable validates the input and returns the neutral projection. It
// performs no protocol parsing, serialization, crypto, clock, store, or
// network work.
//
// RequestHash is SHA-256 over the exact NormalizedBody bytes. It feeds the
// durable AAD binding, so formatting differences must change it. This is
// deliberately different from BodySHA256, which canonicalizes JSON.
func ProjectDurable(input DurableProjectionInput) (*DurableProjection, error) {
	switch {
	case input.Protocol == "":
		return nil, fmt.Errorf("%w: protocol required", ErrInvalidDurableProjectionInput)
	case input.Endpoint == "":
		return nil, fmt.Errorf("%w: endpoint required", ErrInvalidDurableProjectionInput)
	case input.TenantID == "":
		return nil, fmt.Errorf("%w: tenant required", ErrInvalidDurableProjectionInput)
	case input.APIKeyID <= 0:
		return nil, fmt.Errorf("%w: API key ID required", ErrInvalidDurableProjectionInput)
	case input.SessionID == "":
		return nil, fmt.Errorf("%w: session required", ErrInvalidDurableProjectionInput)
	case input.ClientModel == "":
		return nil, fmt.Errorf("%w: client model required", ErrInvalidDurableProjectionInput)
	case input.ClientIdentityHash == "":
		return nil, fmt.Errorf("%w: client identity hash required", ErrInvalidDurableProjectionInput)
	case input.PolicyVersion == "":
		return nil, fmt.Errorf("%w: policy version required", ErrInvalidDurableProjectionInput)
	case input.RequestID == "":
		return nil, fmt.Errorf("%w: request ID required", ErrInvalidDurableProjectionInput)
	}
	trimmed := bytes.TrimSpace(input.NormalizedBody)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || !json.Valid(trimmed) {
		return nil, fmt.Errorf("%w: normalized body must be non-null valid JSON", ErrInvalidDurableProjectionInput)
	}

	body := append([]byte(nil), input.NormalizedBody...)
	sum := sha256.Sum256(body)
	projection := &DurableProjection{
		Version:            DurableProjectionInputVersionV1,
		Endpoint:           input.Endpoint,
		ClientProtocol:     input.Protocol,
		ClientModel:        input.ClientModel,
		NormalizedBody:     body,
		RequestHash:        hex.EncodeToString(sum[:]),
		APIKeyID:           input.APIKeyID,
		TenantID:           input.TenantID,
		ApplicationID:      input.ApplicationID,
		SessionID:          input.SessionID,
		SessionSource:      input.SessionSource,
		ClientIdentityHash: input.ClientIdentityHash,
		ToolsRequested:     input.ToolsRequested,
		PolicyVersion:      input.PolicyVersion,
		RequestID:          input.RequestID,
		TaskCorrelationID:  input.RequestID,
		ParentRequestID:    input.ParentRequestID,
	}
	projection.ResponseFormat, projection.HasMultimodalContent = probeNormalizedBody(body)
	if input.ParentRequestID == "" {
		projection.Warnings = append(projection.Warnings, ConversionWarning{
			Code:  WarningOptionalFieldOmitted,
			Field: "parent_request_id",
		})
	}
	if input.SessionSource == "" {
		projection.Warnings = append(projection.Warnings, ConversionWarning{
			Code:  WarningOptionalFieldOmitted,
			Field: "session_source",
		})
	}
	return projection, nil
}

// probeNormalizedBody derives shallow response_format and multimodal markers
// from the JSON shape. It is a conservative probe, not a protocol parser: it
// recognizes the common typed-block arrays (OpenAI/Anthropic messages and
// Gemini contents.parts). When it cannot prove a non-text block exists it
// leaves HasMultimodalContent false; the eventual streaming bridge remains
// free to apply its full modality detection.
func probeNormalizedBody(body []byte) (responseFormat string, hasMultimodal bool) {
	var probe struct {
		ResponseFormat struct {
			Type string `json:"type"`
		} `json:"response_format"`
		Messages []json.RawMessage `json:"messages"`
		Contents []struct {
			Parts []json.RawMessage `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return "", false
	}
	for _, raw := range probe.Messages {
		var message struct {
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &message) == nil && contentDeclaresMedia(message.Content) {
			hasMultimodal = true
			break
		}
	}
	if !hasMultimodal {
		for _, entry := range probe.Contents {
			media := false
			for _, part := range entry.Parts {
				if blockDeclaresMedia(part) {
					media = true
					break
				}
			}
			if media {
				hasMultimodal = true
				break
			}
		}
	}
	return probe.ResponseFormat.Type, hasMultimodal
}

func contentDeclaresMedia(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] == '"' {
		return false
	}
	if trimmed[0] == '[' {
		var blocks []json.RawMessage
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return false
		}
		for _, block := range blocks {
			if blockDeclaresMedia(block) {
				return true
			}
		}
		return false
	}
	return blockDeclaresMedia(raw)
}

func blockDeclaresMedia(raw json.RawMessage) bool {
	var block struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &block); err != nil {
		return false
	}
	switch block.Type {
	case "image", "image_url", "input_image", "audio", "input_audio", "video", "video_url":
		return true
	}
	return false
}
