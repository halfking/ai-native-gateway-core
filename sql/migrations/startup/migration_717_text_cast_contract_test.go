package startup

import (
	"os"
	"strings"
	"testing"
)

// TestMigration717UsingExpressionsAreTextCastGuarded pins the R37 hardening
// of 717 (flagged by e0f94a799's commit message): every USING expression that
// applies a text operator (~ regex, IN ('true',...), = ”) to an aligned
// column must read the column through an explicit ::text cast. After the R36
// baseline hand-alignment a fresh install loads request_logs_hot already in
// the mother types (customer_id bigint, protocol_conversion boolean, *_muta
// tions/ir_extensions jsonb) BEFORE 717 runs — a bare `col ~ regex` raises
// 42883 (operator does not exist: bigint ~ unknown) and aborts the fresh
// install's startup chain.
func TestMigration717UsingExpressionsAreTextCastGuarded(t *testing.T) {
	body, err := os.ReadFile("717_request_logs_hot_column_alignment.sql")
	if err != nil {
		t.Fatalf("read 717: %v", err)
	}
	sql := string(body)

	mustContain := []string{
		// customer_id: text-cast regex + bounded digit count (bigint overflow
		// debris maps to NULL instead of aborting the migration).
		"customer_id::text ~ '^[0-9]{1,18}$'",
		// protocol_conversion: bare `col IN ('true',...)` is boolean = text
		// on an aligned boolean column.
		"protocol_conversion::text IN ('true', 't', '1', 'yes')",
		// ir_extensions / sanitizer_mutations: bare `= ''` and `~` are
		// jsonb/text operator errors on aligned jsonb columns; the final
		// cast round-trips losslessly. The pre-guard must alternate the
		// JSON literals explicitly — a bare t/f/n char class admits words
		// like "not-json" and then fails the ::jsonb parse it guards.
		"ir_extensions::text = ''",
		"ir_extensions::text ~ '^[[:space:]]*([\\[\\{\"-]|-?[0-9]|true|false|null)'",
		"ir_extensions::text::jsonb",
		"sanitizer_mutations::text = ''",
		"sanitizer_mutations::text ~ '^[[:space:]]*([\\[\\{\"-]|-?[0-9]|true|false|null)'",
		"sanitizer_mutations::text::jsonb",
	}
	for _, want := range mustContain {
		if !strings.Contains(sql, want) {
			t.Errorf("717 lost its ::text guard: %q not found; a fresh install (aligned baseline) would abort at 717 with 42883", want)
		}
	}

	mustNotContain := []string{
		"WHEN customer_id ~",
		"WHEN protocol_conversion IN",
		"WHEN ir_extensions ~",
		"WHEN sanitizer_mutations ~",
		"ir_extensions = ''",
		"sanitizer_mutations = ''",
	}
	for _, banned := range mustNotContain {
		if strings.Contains(sql, banned) {
			t.Errorf("717 regressed to a bare column operator: %q — on an aligned (fresh-install) hot table this is 42883", banned)
		}
	}
	// R36's char-class pre-guard admitted words like "not-json" (n/t/f are in
	// the class) and then failed the ::jsonb parse it was guarding (22P02 on
	// drifted installs — caught live by the integration fixture).
	if strings.Contains(sql, "0-9tfn-") {
		t.Error(`717 regressed to the R36 char-class pre-guard "[\[\{"0-9tfn-]" — it admits "not-json"-shaped debris and aborts the migration with 22P02`)
	}
}
