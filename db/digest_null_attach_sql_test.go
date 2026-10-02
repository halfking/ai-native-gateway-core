package db

import "strings"
import "testing"

func TestAttachDigestNullSkipsWhenPartitionAlreadyHasChild(t *testing.T) {
	sql := attachDigestNullChildrenSQL
	if !strings.Contains(sql, "tbl.relname = part") {
		t.Fatal("attach SQL must skip when any child index already covers the partition table")
	}
	if !strings.Contains(sql, "idx_session_turns_digest_null") {
		t.Fatal("attach SQL lost the parent index name")
	}
	// The old guard only asked whether the canonical name was already a
	// child. A differently named child on the same partition then made
	// ATTACH raise 55000 and database startup abort.
	oldOnly := "ci.inhrelid = to_regclass(format('public.%I', part || '_digest_null_idx'))"
	if strings.Contains(sql, oldOnly) {
		t.Fatal("attach SQL still treats a name mismatch as missing and will ATTACH a second child")
	}
}
