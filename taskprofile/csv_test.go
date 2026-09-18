package taskprofile

import (
	"strings"
	"testing"
)

// csv_test.go — pure parse/validate logic of the corrections CSV format.

func validCSVRow(requestID, auto, human, agrees string) string {
	return strings.Join([]string{
		requestID, auto, human, agrees, "0.5", "cli", "unit", "quality",
		"2026-09-18T00:00:00Z",
	}, ",")
}

func TestParseCorrectionsCSV_HappyPath(t *testing.T) {
	payload := CorrectionCSVHeader + "\n" +
		validCSVRow("req-1", "coding", "coding", "true") + "\n" +
		validCSVRow("req-2", "chat", "testing", "false") + "\n"
	rows, errs, err := ParseCorrectionsCSV(strings.NewReader(payload), 100)
	if err != nil {
		t.Fatalf("ParseCorrectionsCSV: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected row errors: %+v", errs)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].RequestID != "req-1" || !rows[0].Agrees || rows[0].Confidence == nil || *rows[0].Confidence != 0.5 {
		t.Fatalf("row0 = %+v", rows[0])
	}
	if rows[1].Agrees {
		t.Fatal("row1 must be disagrees (auto=chat human=testing)")
	}
	// agrees column is re-derived, so a smuggled wrong agrees fails per-row
	// (the rest of the file can still import).
	badPayload := CorrectionCSVHeader + "\n" + validCSVRow("req-3", "coding", "coding", "false") + "\n"
	rows, errs, err = ParseCorrectionsCSV(strings.NewReader(badPayload), 100)
	if err != nil || len(rows) != 0 || len(errs) != 1 || !strings.Contains(errs[0].Message, "contradicts") {
		t.Fatalf("contradicting agrees must be a row error: rows=%d errs=%+v err=%v", len(rows), errs, err)
	}
}

func TestParseCorrectionsCSV_Rejections(t *testing.T) {
	cases := map[string]string{
		"wrong header":       "a,b,c\n1,2,3\n",
		"no data rows":       CorrectionCSVHeader + "\n",
		"short row":          CorrectionCSVHeader + "\nreq-1,coding,coding,true\n",
		"unknown human type": CorrectionCSVHeader + "\n" + validCSVRow("req-1", "coding", "mystery", "false") + "\n",
		"missing annotator":  strings.ReplaceAll(CorrectionCSVHeader+"\n"+validCSVRow("req-1", "coding", "coding", "true"), ",unit,", ",,"),
		"bad reason":         CorrectionCSVHeader + "\n" + strings.Replace(validCSVRow("req-1", "coding", "coding", "true"), ",quality,", ",nonsense,", 1),
		"bad confidence":     CorrectionCSVHeader + "\n" + strings.Replace(validCSVRow("req-1", "coding", "coding", "true"), ",0.5,", ",1.5,", 1),
		"bad created_at":     CorrectionCSVHeader + "\n" + strings.Replace(validCSVRow("req-1", "coding", "coding", "true"), "2026-09-18T00:00:00Z", "yesterday", 1),
		"duplicate in file":  CorrectionCSVHeader + "\n" + validCSVRow("req-1", "coding", "coding", "true") + "\n" + validCSVRow("req-1", "chat", "chat", "true") + "\n",
		"empty request_id":   CorrectionCSVHeader + "\n" + validCSVRow("", "coding", "coding", "true") + "\n",
		"empty auto_task":    CorrectionCSVHeader + "\n" + validCSVRow("req-1", "", "coding", "false") + "\n",
	}
	for name, payload := range cases {
		rows, errs, err := ParseCorrectionsCSV(strings.NewReader(payload), 100)
		if err == nil && len(errs) == 0 {
			t.Errorf("%s: expected rejection, got rows=%d errs=%d", name, len(rows), len(errs))
		}
	}
}

func TestParseCorrectionsCSV_PartialImport(t *testing.T) {
	payload := CorrectionCSVHeader + "\n" +
		validCSVRow("req-ok", "coding", "coding", "true") + "\n" +
		validCSVRow("req-bad", "coding", "mystery", "false") + "\n"
	rows, errs, err := ParseCorrectionsCSV(strings.NewReader(payload), 100)
	if err != nil {
		t.Fatalf("partial import must be structural-OK: %v", err)
	}
	if len(rows) != 1 || rows[0].RequestID != "req-ok" {
		t.Fatalf("valid row must survive: %+v (errs=%+v)", rows, errs)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Message, "not in registry") {
		t.Fatalf("bad row must land in errs: %+v", errs)
	}
}

func TestExportImportRoundTrip_Format(t *testing.T) {
	// Format-level round trip without a DB: build a record the way
	// ExportCorrectionsCSV writes it, and confirm ParseCorrectionsCSV
	// accepts it back with identical semantics.
	exported := strings.Join([]string{
		"req-x", "chat", "testing", "false", "0.75", "web", "alice", "quality",
		"2026-09-18T12:00:00Z",
	}, ",")
	rows, errs, err := ParseCorrectionsCSV(strings.NewReader(CorrectionCSVHeader+"\n"+exported+"\n"), 100)
	if err != nil || len(errs) != 0 || len(rows) != 1 {
		t.Fatalf("round trip failed: rows=%d errs=%+v err=%v", len(rows), errs, err)
	}
	r := rows[0]
	if r.AutoTaskType != "chat" || r.HumanTaskType != "testing" || r.Agrees ||
		r.Confidence == nil || *r.Confidence != 0.75 || r.Profile == nil || *r.Profile != "web" ||
		r.Annotator != "alice" || r.Reason != "quality" {
		t.Fatalf("round trip mismatch: %+v", r)
	}
}
