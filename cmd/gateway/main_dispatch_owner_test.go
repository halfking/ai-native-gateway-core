package main

import (
	"strings"
	"testing"
)

func TestJournalSnapshotReceiptOwnerIncludesProcessNonce(t *testing.T) {
	first := journalSnapshotReceiptOwner("gateway-a")
	second := journalSnapshotReceiptOwner("gateway-a")
	if !strings.HasPrefix(first, "gateway-a:") || !strings.HasPrefix(second, "gateway-a:") {
		t.Fatalf("owner must preserve instance prefix: %q %q", first, second)
	}
	if first == second {
		t.Fatalf("receipt owners must differ across process starts: %q", first)
	}
}
