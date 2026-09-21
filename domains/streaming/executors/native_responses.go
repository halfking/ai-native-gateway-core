package executors

import (
	"bytes"
	"encoding/json"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// validateNativeResponsesBody validates the non-stream Responses envelope before
// any bytes are committed to the client. Native bodies are otherwise kept
// byte-for-byte unchanged so provider-specific fields are not lost.
func validateNativeResponsesBody(body []byte) error {
	if len(body) == 0 || !json.Valid(body) {
		return nativeResponsesError(errorsx.KindConversion, "native Responses upstream returned invalid JSON", body)
	}

	var envelope struct {
		Object json.RawMessage `json:"object"`
		Output json.RawMessage `json:"output"`
		Status string          `json:"status"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nativeResponsesError(errorsx.KindConversion, "native Responses upstream returned invalid JSON", body)
	}
	var object string
	if err := json.Unmarshal(envelope.Object, &object); err != nil || object != "response" {
		return nativeResponsesError(errorsx.KindConversion, "native Responses upstream returned a non-Responses envelope", body)
	}
	if len(envelope.Output) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Output), []byte("null")) {
		return nativeResponsesError(errorsx.KindConversion, "native Responses upstream omitted output", body)
	}
	var output []json.RawMessage
	if err := json.Unmarshal(envelope.Output, &output); err != nil {
		return nativeResponsesError(errorsx.KindConversion, "native Responses upstream returned an invalid output array", body)
	}
	if len(output) == 0 {
		return nativeResponsesError(errorsx.KindEmptyResponse, "native Responses upstream returned empty output", body)
	}
	if envelope.Status == "failed" || (len(envelope.Error) > 0 && !bytes.Equal(bytes.TrimSpace(envelope.Error), []byte("null"))) {
		return nativeResponsesError(errorsx.KindTransient, "native Responses upstream reported a failed response", body)
	}
	return nil
}

func nativeResponsesError(kind errorsx.ErrorKind, message string, body []byte) error {
	return &upstreampkg.Error{
		Kind:       kind,
		Message:    message,
		Body:       append([]byte(nil), body...),
		StatusCode: 200,
	}
}

func nativeResponsesUsage(body []byte) (*int, *int) {
	var envelope struct {
		Usage struct {
			InputTokens  *int `json:"input_tokens"`
			OutputTokens *int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, nil
	}
	return envelope.Usage.InputTokens, envelope.Usage.OutputTokens
}
