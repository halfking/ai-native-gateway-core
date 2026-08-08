package v2

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBodiesRecord_HasAttachmentFields(t *testing.T) {
	rec := BodiesRecord{
		SessionID: "s1", TurnNo: 1, TenantID: "default", RequestID: "r1",
		RequestAttachments: []AttachmentRef{
			{Name: "spec.md", ObjectKey: "k1", MIMEType: "text/markdown", SizeBytes: 100, SHA256: "abc"},
		},
	}
	if len(rec.RequestAttachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(rec.RequestAttachments))
	}
	if !strings.Contains(rec.RequestAttachments[0].Name, "spec") {
		t.Fatalf("attachment name mismatch: %s", rec.RequestAttachments[0].Name)
	}
	if rec.RequestAttachments[0].ObjectKey != "k1" {
		t.Fatalf("attachment object_key mismatch: %s", rec.RequestAttachments[0].ObjectKey)
	}
}

func TestAttachmentRef_JSONRoundtrip(t *testing.T) {
	original := []AttachmentRef{
		{Name: "image.png", ObjectKey: "obj/123", MIMEType: "image/png", SizeBytes: 2048, SHA256: "deadbeef", Replayable: true},
		{Name: "doc.pdf", ObjectKey: "obj/456", MIMEType: "application/pdf", SizeBytes: 8192, SHA256: "cafebabe"},
	}

	// Encode
	encoded, err := safeJSONMarshal(original)
	if err != nil {
		t.Fatalf("safeJSONMarshal: %v", err)
	}

	// Decode
	var decoded []AttachmentRef
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(decoded) != 2 {
		t.Fatalf("expected 2 attachments after roundtrip, got %d", len(decoded))
	}
	if decoded[0].Name != "image.png" || decoded[0].Replayable != true {
		t.Fatalf("attachment[0] mismatch: %+v", decoded[0])
	}
	if decoded[1].SizeBytes != 8192 {
		t.Fatalf("attachment[1].size_bytes mismatch: %d", decoded[1].SizeBytes)
	}
}
