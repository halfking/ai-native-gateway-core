package sessionaudithook

import (
	"context"
	"testing"
)

func TestApprovedResumeContextMarker(t *testing.T) {
	if IsApprovedResume(context.Background()) {
		t.Fatal("background context must not be an approved resume")
	}
	if IsApprovedResume(WithApprovedResume(context.Background(), "")) {
		t.Fatal("empty approval id must not set resume marker")
	}
	if !IsApprovedResume(WithApprovedResume(context.Background(), "approval-1")) {
		t.Fatal("approved resume marker missing")
	}
}
