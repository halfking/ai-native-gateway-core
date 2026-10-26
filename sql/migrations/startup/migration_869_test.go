package startup_test

import (
	"os"
	"strings"
	"testing"
)

// 869 regression gate: the session_turns_hot → monthly-partition promotion
// path silently dropped 52 of the 106 columns (trace_events, is_final_success,
// request_delta, identity_hash, ...) because the promote function carried a
// hardcoded 54-column list.
//
// Two distinct failure classes are pinned here:
//
//  1. the function body must derive its column list from pg_attribute instead of
//     enumerating columns, so symmetric ADD COLUMN flows through automatically;
//  2. the deployment channel must leave this migration LAST in the function's
//     intentional chain. 707 already shipped the catalog-derived body, but 640
//     is registered after it (for 713's client_protocol prerequisite) and its
//     older hardcoded body overwrote the fix on every replay — which is exactly
//     how the live body ended up dropping columns while the column-shape guard
//     (which compares hot vs parent table shapes) stayed green, because both
//     tables did have all 106 columns.
func TestMigration869PromotesAllColumnsAndKeepsTerminalChainPosition(t *testing.T) {
	body, err := os.ReadFile("869_promote_session_turns_trace_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	raw := string(body)
	sql := strings.ToLower(raw)

	// Assert against the promote FUNCTION BODY only. The post-condition block
	// re-derives the same column list from pg_attribute, so a whole-file
	// substring check stays green even when the function body has been reverted
	// to a hardcoded list — measured: that mutation left this gate passing.
	fnStart := strings.Index(sql, "create or replace function public.promote_session_turns_hot_to_partition(")
	if fnStart < 0 {
		t.Fatal("migration 869 does not define promote_session_turns_hot_to_partition")
	}
	fnEnd := strings.Index(sql[fnStart:], "\n$$;")
	if fnEnd < 0 {
		t.Fatal("cannot locate end of promote function body")
	}
	fn := sql[fnStart : fnStart+fnEnd]

	for _, want := range []string{
		// catalog-derived column list, mapped by name on INSERT
		"string_agg(quote_ident(attname), ',' order by attnum)",
		"insert into public.session_turns (%s)",
		"select %s from moved",
		"returning h.*",
		// hot/parent column shape guard (name:type:notnull, both sides)
		"v_parent_shape is distinct from v_hot_shape",
		"session_turns hot/parent column contract has drifted",
	} {
		if !strings.Contains(fn, want) {
			t.Fatalf("promote function body missing %q", want)
		}
	}

	// post-conditions that make a silent re-clobber fail loudly
	for _, want := range []string{
		"derived column list is filtered by column name",
		"installed body is not the catalog-derived form",
		"derived column list lacks trace_events",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("migration 869 missing post-condition %q", want)
		}
	}

	// A hardcoded column enumeration is the defect itself: if any assignment of
	// v_cols stops coming from pg_attribute, the silent-drop class is back.
	if strings.Contains(fn, "v_cols :=") {
		t.Fatal("promote function body assigns v_cols literally; column list must be " +
			"derived from pg_attribute or symmetric ADD COLUMN is dropped again")
	}

	// A hardcoded column enumeration is the defect itself: the live body listed
	// these names explicitly and dropped every column absent from the list.
	for _, banned := range []string{
		"h.client_protocol, h.upstream_protocol, h.ir_metadata",
		"t9_response_end_at, client_protocol",
	} {
		if strings.Contains(fn, banned) {
			t.Fatalf("migration 869 reintroduced a hardcoded column list (%q)", banned)
		}
	}

	// Default retention must stay at the deployed value. 707/688 use '8 hours'
	// (matching bg/partition_manager.go DefaultRetentionWindow); the live body
	// carried '7 days' because of the 640 clobber. Narrowing the hot window 21x
	// is a behaviour change, not a data-loss fix, so 869 deliberately keeps the
	// deployed value and leaves that divergence for the owner to decide.
	if !strings.Contains(raw, "p_retention INTERVAL DEFAULT '7 days'") {
		t.Fatal("migration 869 must preserve the deployed default retention ('7 days')")
	}
}

// TestMigration869IsTerminalInEveryChannel guards the ordering that actually
// caused the loss: in the deployment channel 640 is sequenced after 707, so a
// catalog-derived body is only durable while it stays last.
func TestMigration869IsTerminalInEveryChannel(t *testing.T) {
	script, err := os.ReadFile("../../../scripts/apply-db-revision-sequence.sh")
	if err != nil {
		t.Fatal(err)
	}
	chain := "'promote_session_turns_hot_to_partition|707_session_turns_s1a.sql|640_session_turns_protocol_fields.sql|869_promote_session_turns_trace_events.sql|897_session_turns_promote_final_success_self_heal.sql|'"
	if !strings.Contains(string(script), chain) {
		t.Fatal("deployment channel chain must end with the terminal promote-function version " +
			"(897 since the 2026-10-23 hot-drain round; previously 869); without it the clobber " +
			"guard aborts, and with 640 last the hardcoded body wins again")
	}
	if strings.Contains(string(script), "|640_session_turns_protocol_fields.sql|'") {
		t.Fatal("stale chain entry still ends at 640, which re-arms the silent column drop")
	}
}
