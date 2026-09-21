package requestfact

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

func TestBuildClientContentDocumentUsesVersionedIRDocument(t *testing.T) {
	request := &ir.InternalRequest{
		Model:          "gpt-4o-mini",
		SourceProtocol: "openai",
		Messages: []ir.Message{{
			Role:    "user",
			Content: []ir.ContentBlock{{Type: "text", Text: "hello"}},
		}},
		Extensions: map[string]json.RawMessage{"source_extension": json.RawMessage(`true`)},
	}
	rawBody := []byte(" \n{\"model\":\"gpt-4o-mini\",\"messages\":[]}\n")
	extensions := json.RawMessage(`{"archive_extension":true}`)

	document, err := BuildClientContentDocument(rawBody, "openai", request, extensions)
	if err != nil {
		t.Fatalf("BuildClientContentDocument() error = %v", err)
	}

	if got, want := string(document.RawBody), string(rawBody); got != want {
		t.Errorf("RawBody = %q, want %q", got, want)
	}
	if got, want := document.Protocol, "openai"; got != want {
		t.Errorf("Protocol = %q, want %q", got, want)
	}
	if got, want := string(document.Extensions), string(extensions); got != want {
		t.Errorf("Extensions = %s, want %s", got, want)
	}

	decoded, err := ir.DecodeRequestDocument(document.CanonicalIR)
	if err != nil {
		t.Fatalf("DecodeRequestDocument() error = %v", err)
	}
	if !reflect.DeepEqual(decoded, request) {
		t.Errorf("decoded IR = %#v, want %#v", decoded, request)
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(document.CanonicalIR, &envelope); err != nil {
		t.Fatalf("unmarshal IR document: %v", err)
	}
	if got := string(envelope["version"]); got != "1" {
		t.Errorf("version = %s, want 1", got)
	}
	if _, exists := envelope["payload"]; !exists {
		t.Error("IR document is missing payload")
	}
}

func TestContentBuildersCopyCallerOwnedBytes(t *testing.T) {
	request := &ir.InternalRequest{Model: "model", SourceProtocol: "openai"}
	rawBody := []byte(`{"model":"model"}`)
	extensions := json.RawMessage(`{"trace":"one"}`)

	document, err := BuildClientContentDocument(rawBody, "openai", request, extensions)
	if err != nil {
		t.Fatalf("BuildClientContentDocument() error = %v", err)
	}
	rawBody[2] = 'X'
	extensions[2] = 'X'
	request.Model = "mutated-after-build"

	if got, want := string(document.RawBody), `{"model":"model"}`; got != want {
		t.Errorf("RawBody after mutation = %q, want %q", got, want)
	}
	if got, want := string(document.Extensions), `{"trace":"one"}`; got != want {
		t.Errorf("Extensions after mutation = %q, want %q", got, want)
	}
	decoded, err := ir.DecodeRequestDocument(document.CanonicalIR)
	if err != nil {
		t.Fatalf("DecodeRequestDocument() error = %v", err)
	}
	if got, want := decoded.Model, "model"; got != want {
		t.Errorf("encoded Model = %q, want %q", got, want)
	}
}

func TestBuildUpstreamContentDocumentPreservesFinalWireBody(t *testing.T) {
	request := &ir.InternalRequest{
		Model:          "claude-sonnet-4-20250514",
		SourceProtocol: "anthropic",
		Messages:       []ir.Message{{Role: "user", Content: []ir.ContentBlock{{Type: "text", Text: "hello"}}}},
	}
	finalWireBody := []byte("{\"model\":\"claude-sonnet-4-20250514\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}],\"stream\":false}")

	document, err := BuildUpstreamContentDocument(finalWireBody, "anthropic", request, nil)
	if err != nil {
		t.Fatalf("BuildUpstreamContentDocument() error = %v", err)
	}
	if !bytes.Equal(document.RawBody, finalWireBody) {
		t.Errorf("RawBody = %s, want final wire body %s", document.RawBody, finalWireBody)
	}
	decoded, err := ir.DecodeRequestDocument(document.CanonicalIR)
	if err != nil {
		t.Fatalf("DecodeRequestDocument() error = %v", err)
	}
	if !reflect.DeepEqual(decoded, request) {
		t.Errorf("decoded IR = %#v, want %#v", decoded, request)
	}
}

func TestContentBuildersRejectInvalidInputs(t *testing.T) {
	validRequest := &ir.InternalRequest{Model: "model", SourceProtocol: "openai"}
	testCases := []struct {
		name       string
		build      func() (ContentDocument, error)
		wantDetail string
	}{
		{
			name: "missing protocol",
			build: func() (ContentDocument, error) {
				return BuildClientContentDocument([]byte(`{}`), "", validRequest, nil)
			},
			wantDetail: "protocol required",
		},
		{
			name: "missing IR",
			build: func() (ContentDocument, error) {
				return BuildClientContentDocument([]byte(`{}`), "openai", nil, nil)
			},
			wantDetail: "IR required",
		},
		{
			name: "empty raw body",
			build: func() (ContentDocument, error) {
				return BuildClientContentDocument(nil, "openai", validRequest, nil)
			},
			wantDetail: "raw_body",
		},
		{
			name: "null raw body",
			build: func() (ContentDocument, error) {
				return BuildClientContentDocument([]byte(" null "), "openai", validRequest, nil)
			},
			wantDetail: "raw_body",
		},
		{
			name: "invalid raw body",
			build: func() (ContentDocument, error) {
				return BuildClientContentDocument([]byte(`{"model":`), "openai", validRequest, nil)
			},
			wantDetail: "raw_body",
		},
		{
			name: "invalid extensions",
			build: func() (ContentDocument, error) {
				return BuildClientContentDocument([]byte(`{}`), "openai", validRequest, json.RawMessage(`{`))
			},
			wantDetail: "extensions",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := testCase.build()
			if !errors.Is(err, ErrInvalidContentArtifact) {
				t.Fatalf("error = %v, want ErrInvalidContentArtifact", err)
			}
			if !bytes.Contains([]byte(err.Error()), []byte(testCase.wantDetail)) {
				t.Errorf("error = %q, want detail %q", err, testCase.wantDetail)
			}
		})
	}
}
