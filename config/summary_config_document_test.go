package config

import "testing"

func TestDocumentSummaryDimensionIsValidButNotInDefaultBatch(t *testing.T) {
	if !SummaryDimensionDocumentSummary.Valid() {
		t.Fatal("document_summary must be a valid on-demand dimension")
	}
	if got := SummaryDimensionDocumentSummary.SummaryModelKey(); got != "summary_models.document_summary" {
		t.Fatalf("SummaryModelKey() = %q", got)
	}
	for _, dim := range AllSummaryDimensions() {
		if dim == SummaryDimensionDocumentSummary {
			t.Fatal("document_summary must not be included in SummarizeAll's default six dimensions")
		}
	}
}
