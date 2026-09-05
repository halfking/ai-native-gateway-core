package requestfact

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

var ErrInvalidContentArtifact = errors.New("requestfact: invalid content artifact")

// BuildClientContentDocument records an inbound body and the IR object that an
// existing protocol parser already produced. It never parses the raw body.
func BuildClientContentDocument(rawBody []byte, protocol string, parsedIR *ir.InternalRequest, extensions json.RawMessage) (ContentDocument, error) {
	return buildContentDocument("client", rawBody, protocol, parsedIR, extensions)
}

// BuildUpstreamContentDocument records final outbound wire bytes and the
// already-mutated semantic IR object passed to a serializer. The two are kept
// distinct because post-serializer wire transforms may change the sent bytes.
func BuildUpstreamContentDocument(finalWireBody []byte, protocol string, outboundIR *ir.InternalRequest, extensions json.RawMessage) (ContentDocument, error) {
	return buildContentDocument("upstream", finalWireBody, protocol, outboundIR, extensions)
}

func buildContentDocument(role string, rawBody []byte, protocol string, requestIR *ir.InternalRequest, extensions json.RawMessage) (ContentDocument, error) {
	if protocol == "" {
		return ContentDocument{}, fmt.Errorf("%w: %s protocol required", ErrInvalidContentArtifact, role)
	}
	if requestIR == nil {
		return ContentDocument{}, fmt.Errorf("%w: %s IR required", ErrInvalidContentArtifact, role)
	}
	body, err := cloneDocument(rawBody, role+" raw_body")
	if err != nil {
		return ContentDocument{}, err
	}
	clonedExtensions, err := cloneOptionalDocument(extensions, role+" extensions")
	if err != nil {
		return ContentDocument{}, err
	}
	encodedIR, err := ir.EncodeRequestDocument(requestIR)
	if err != nil {
		return ContentDocument{}, fmt.Errorf("requestfact: encode %s IR: %w", role, err)
	}
	return ContentDocument{
		RawBody:     body,
		CanonicalIR: append(json.RawMessage(nil), encodedIR...),
		Protocol:    protocol,
		Extensions:  clonedExtensions,
	}, nil
}

func cloneDocument(body []byte, field string) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || !json.Valid(trimmed) {
		return nil, fmt.Errorf("%w: %s must be non-null valid JSON", ErrInvalidContentArtifact, field)
	}
	return append(json.RawMessage(nil), body...), nil
}

func cloneOptionalDocument(body json.RawMessage, field string) (json.RawMessage, error) {
	if len(body) == 0 {
		return nil, nil
	}
	return cloneDocument(body, field)
}
