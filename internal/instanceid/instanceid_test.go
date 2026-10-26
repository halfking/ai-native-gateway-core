package instanceid

import (
	"os"
	"strings"
	"testing"
)

// TestResolvePrefersExplicitEnvOverHostname pins the precedence: an explicit
// operator-provided identity wins over the hostname, and the result is never
// empty.
//
// The "never empty" leg matters because the value lands in
// session_turns.host (migration 885): an empty host would be
// indistinguishable from "the writer forgot", and the write-path test
// asserts the column comes back non-empty.
func TestResolvePrefersExplicitEnvOverHostname(t *testing.T) {
	t.Setenv(EnvInstanceID, "  gw-prod-7  ")
	got := Resolve()
	if got != "gw-prod-7" {
		t.Fatalf("显式环境变量必须优先（且去空白），实得 %q", got)
	}

	t.Setenv(EnvInstanceID, "   ")
	got = Resolve()
	if strings.TrimSpace(got) == "" {
		t.Fatal("环境变量为空时不得返回空串（应回落到主机名或兜底常量）")
	}
	if got == "   " {
		t.Fatal("空白环境变量不得被当作身份")
	}
}

// TestResolveIsNeverEmpty pins the last-resort leg: whatever the
// environment says, the identity is a non-empty string.
func TestResolveIsNeverEmpty(t *testing.T) {
	t.Setenv(EnvInstanceID, "")
	if got := Resolve(); strings.TrimSpace(got) == "" {
		t.Fatal("Resolve() 不得返回空身份")
	}
}

// TestResolveIsStableAcrossCalls pins stability: the same process must not
// report two different identities (a timestamp-based fallback would break
// the "which machine wrote this row" question on every restart).
func TestResolveIsStableAcrossCalls(t *testing.T) {
	t.Setenv(EnvInstanceID, "gw-stable")
	first := Resolve()
	for i := 0; i < 5; i++ {
		if got := Resolve(); got != first {
			t.Fatalf("身份在同进程内漂移：%q → %q", first, got)
		}
	}
}

// TestEnvInstanceIDName pins the env var name: it is an operator-facing
// contract (deploy scripts set it), so a rename must be a deliberate change.
func TestEnvInstanceIDName(t *testing.T) {
	if EnvInstanceID != "LLM_GATEWAY_INSTANCE_ID" {
		t.Fatalf("环境变量名 = %q，必须是 LLM_GATEWAY_INSTANCE_ID", EnvInstanceID)
	}
	if _, ok := os.LookupEnv(EnvInstanceID); ok {
		t.Log("环境里已设置该变量（不影响断言）")
	}
}
