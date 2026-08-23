package startup

import (
	"os"
	"strings"
	"testing"
)

func TestMigration571UsesUnboundedNumericForTokenRatio(t *testing.T) {
	for _, path := range []string{
		"571_session_summary_large_token_ratio.sql",
		"../../objects/functions/update_session_summary.sql",
	} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if !strings.Contains(text, "v_prompt_tokens::numeric / v_total_tokens::numeric") {
			t.Errorf("%s must use unbounded numeric token ratio", path)
		}
		if strings.Contains(text, "v_prompt_tokens::DECIMAL(10,6) / v_total_tokens::DECIMAL(10,6)") {
			t.Errorf("%s retains overflow-prone DECIMAL(10,6) token casts", path)
		}
	}
}

func TestMigration571DownRefusesUnsafeRollback(t *testing.T) {
	body, err := os.ReadFile("571_session_summary_large_token_ratio.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "cannot be rolled back safely") {
		t.Fatal("migration 571 down must refuse restoring overflow-prone token casts")
	}
}
