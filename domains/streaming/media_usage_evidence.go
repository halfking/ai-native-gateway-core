package streaming

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/reconciliation"
	"github.com/shopspring/decimal"
)

func exactMicroseconds(seconds json.Number) *int64 {
	raw := seconds.String()
	if raw == "" || len(raw) > 32 {
		return nil
	}
	if i := strings.IndexAny(raw, "eE"); i >= 0 {
		exponent, err := strconv.Atoi(raw[i+1:])
		if err != nil || exponent < -18 || exponent > 18 {
			return nil
		}
	}
	value, err := decimal.NewFromString(raw)
	if err != nil || value.IsNegative() {
		return nil
	}
	value = value.Shift(6)
	if !value.IsInteger() || value.GreaterThan(decimal.NewFromInt(1<<63-1)) {
		return nil
	}
	micros := value.IntPart()
	return &micros
}

func transcriptionMediaUsage(body []byte, source string, untypedUsageSeconds bool) reconciliation.MediaUsageEvidence {
	var response struct {
		Duration json.Number     `json:"duration"`
		Error    json.RawMessage `json:"error"`
		BaseResp struct {
			Status int `json:"status_code"`
		} `json:"base_resp"`
		Usage struct {
			Type    string      `json:"type"`
			Seconds json.Number `json:"seconds"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &response) != nil || response.BaseResp.Status != 0 || hasMediaJSONError(response.Error) {
		return reconciliation.MediaUsageEvidence{}
	}
	evidence := reconciliation.MediaUsageEvidence{AudioInputDurationUS: exactMicroseconds(response.Duration)}
	if response.Usage.Type == "duration" || untypedUsageSeconds && response.Usage.Type == "" {
		evidence.AudioBilledDurationUS = exactMicroseconds(response.Usage.Seconds)
	}
	if evidence.AudioInputDurationUS != nil || evidence.AudioBilledDurationUS != nil {
		evidence.Source = source
	}
	return evidence
}

func hasMediaJSONError(raw json.RawMessage) bool {
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null"))
}

// Only check for a returned asset field. Do not reallocate the encoded image
// or fetch the returned URL to measure this response.
type imageAssetField bool

func (p *imageAssetField) UnmarshalJSON(data []byte) error {
	*p = len(data) > 2 && data[0] == '"' && len(bytes.TrimSpace(data[1:len(data)-1])) > 0
	return nil
}

type boundedUsageJSON []byte

func (payload *boundedUsageJSON) UnmarshalJSON(data []byte) error {
	*payload = (*payload)[:0]
	if len(data) <= maxModalityEvidenceBody-len(`{"usage":}`) {
		*payload = append(*payload, data...)
	}
	return nil
}

func (payload boundedUsageJSON) envelope() []byte {
	if len(payload) == 0 {
		return nil
	}
	return append(append([]byte(`{"usage":`), payload...), '}')
}

func generatedImageEvidence(body []byte) (reconciliation.MediaUsageEvidence, []byte) {
	var response struct {
		Data []struct {
			URL    imageAssetField `json:"url"`
			Base64 imageAssetField `json:"b64_json"`
		} `json:"data"`
		Error  json.RawMessage  `json:"error"`
		Status string           `json:"status"`
		Usage  boundedUsageJSON `json:"usage"`
	}
	if json.Unmarshal(body, &response) != nil {
		return reconciliation.MediaUsageEvidence{}, nil
	}
	usage := response.Usage.envelope()
	if response.Data == nil || hasMediaJSONError(response.Error) {
		return reconciliation.MediaUsageEvidence{}, usage
	}
	switch response.Status {
	case "", "completed", "succeeded", "success":
	default:
		return reconciliation.MediaUsageEvidence{}, usage
	}
	for _, image := range response.Data {
		if !image.URL && !image.Base64 {
			return reconciliation.MediaUsageEvidence{}, usage
		}
	}
	count := int64(len(response.Data))
	return reconciliation.MediaUsageEvidence{GeneratedImages: &count, Source: "provider_image_assets"}, usage
}

func generatedVideoUsage(body []byte) (reconciliation.MediaUsageEvidence, string) {
	var response struct {
		ID          string          `json:"id"`
		Object      string          `json:"object"`
		Status      string          `json:"status"`
		Seconds     json.Number     `json:"seconds"`
		RemixedFrom string          `json:"remixed_from_video_id"`
		Error       json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &response) != nil || response.Object != "video" || response.ID == "" {
		return reconciliation.MediaUsageEvidence{}, "unknown"
	}
	if response.Status == "failed" || hasMediaJSONError(response.Error) {
		return reconciliation.MediaUsageEvidence{}, "failure"
	}
	if response.Status != "completed" {
		return reconciliation.MediaUsageEvidence{}, "unknown"
	}
	// A remix duration may include media from the source clip.
	if response.RemixedFrom == "" {
		if duration := exactMicroseconds(response.Seconds); duration != nil {
			return reconciliation.MediaUsageEvidence{VideoOutputDurationUS: duration, Source: "provider_completed_video"}, "success"
		}
	}
	return reconciliation.MediaUsageEvidence{}, "success"
}

// pcmWAVOutputUsage measures complete PCM RIFF output. An unverifiable header,
// compressed audio or a duration finer than a microsecond remains unmeasured.
func pcmWAVOutputUsage(body []byte) reconciliation.MediaUsageEvidence {
	missing := reconciliation.MediaUsageEvidence{}
	le := binary.LittleEndian
	if len(body) < 12 || string(body[:4]) != "RIFF" || string(body[8:12]) != "WAVE" ||
		uint64(le.Uint32(body[4:8]))+8 != uint64(len(body)) {
		return missing
	}
	var sampleRate, frameBytes, dataBytes uint64
	var haveData bool
	for chunks := body[12:]; len(chunks) > 0; {
		if len(chunks) < 8 {
			return missing
		}
		size := uint64(le.Uint32(chunks[4:8]))
		next := 8 + size + size%2
		if next > uint64(len(chunks)) {
			return missing
		}
		switch string(chunks[:4]) {
		case "fmt ":
			format := chunks[8 : 8+size]
			if sampleRate != 0 || len(format) < 16 || le.Uint16(format[:2]) != 1 {
				return missing
			}
			channels, bits := uint64(le.Uint16(format[2:4])), uint64(le.Uint16(format[14:16]))
			sampleRate, frameBytes = uint64(le.Uint32(format[4:8])), uint64(le.Uint16(format[12:14]))
			if sampleRate == 0 || frameBytes == 0 || bits%8 != 0 || frameBytes != channels*(bits/8) ||
				uint64(le.Uint32(format[8:12])) != sampleRate*frameBytes {
				return missing
			}
		case "data":
			if haveData {
				return missing
			}
			dataBytes, haveData = size, true
		}
		chunks = chunks[next:]
	}
	if !haveData || frameBytes == 0 || dataBytes%frameBytes != 0 {
		return missing
	}
	scaled := dataBytes / frameBytes * 1_000_000
	if scaled%sampleRate != 0 {
		return missing
	}
	duration := int64(scaled / sampleRate)
	return reconciliation.MediaUsageEvidence{AudioOutputDurationUS: &duration, Source: "provider_pcm_wav_samples"}
}
