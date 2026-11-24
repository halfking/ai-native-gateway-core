package modelattr

import (
	"strings"
	"testing"
)

// The write-side sentinel and the read-side bucket must stay distinct and
// correctly spelled: the write side marks "we looked and found nothing", the
// read side is the single bucket that mark collapses into.
func TestSentinels(t *testing.T) {
	if UnknownPlaceholder != "<unknown>" {
		t.Fatalf("UnknownPlaceholder = %q, want <unknown>", UnknownPlaceholder)
	}
	if UnknownBucket != "unknown" {
		t.Fatalf("UnknownBucket = %q, want unknown", UnknownBucket)
	}
	if UnknownPlaceholder == UnknownBucket {
		t.Fatal("write-side sentinel and read-side bucket must not be the same string")
	}
}

// The bucket must be a plain identifier with no surrounding whitespace: a
// stray space would create yet another phantom bucket in every GROUP BY.
func TestUnknownBucketIsClean(t *testing.T) {
	if strings.TrimSpace(UnknownBucket) != UnknownBucket {
		t.Fatalf("UnknownBucket has surrounding whitespace: %q", UnknownBucket)
	}
	if strings.TrimSpace(UnknownPlaceholder) != UnknownPlaceholder {
		t.Fatalf("UnknownPlaceholder has surrounding whitespace: %q", UnknownPlaceholder)
	}
}
