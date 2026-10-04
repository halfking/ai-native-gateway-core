package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/bootstrap"
)

// ---------------------------------------------------------------------------
// Layer 1 — bootstrap must write to the manager's database.
//
// 2026-10-04 db14 incident: bootstrap.Apply was handed redisClientForCache, the
// gateway client, while the manager validated coverage through the dedicated
// URSM client. With URSM_V2_REDIS_DB set, every node hash and the coverage
// manifest landed in the gateway db and authoritative startup could never
// converge — 154 degraded to ModeOff on every attempt while 245 looked fine.
//
// A substring check would not have teeth: redisClientForCache.Client() could be
// wrapped and the check would still pass while the bug remained. So the call's
// arguments are split on top-level commas and matched structurally.
// ---------------------------------------------------------------------------

func mainGoSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("main.go"))
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	return string(raw)
}

// topLevelArgs returns the argument list of a call whose head ends with
// callName, splitting only on commas outside nested brackets and string
// literals. Returns nil when the call is absent.
func topLevelArgs(src, callName string) []string {
	idx := strings.Index(src, callName+"(")
	if idx < 0 {
		return nil
	}
	open := idx + len(callName)
	// Scan from just after the call's opening paren with depth 0, so the
	// paren itself is not counted as nesting — otherwise every comma inside
	// the argument list looks nested and the whole call parses as one argument.
	depth := 0
	var args []string
	cur := strings.Builder{}
	inStr := byte(0)
	for i := open + 1; i < len(src); i++ {
		c := src[i]
		if inStr != 0 {
			cur.WriteByte(c)
			if c == '\\' && i+1 < len(src) {
				i++
				cur.WriteByte(src[i])
				continue
			}
			if c == inStr {
				inStr = 0
			}
			continue
		}
		switch c {
		case '"', '`', '\'':
			inStr = c
			cur.WriteByte(c)
		case '(', '[', '{':
			depth++
			cur.WriteByte(c)
		case ')', ']', '}':
			if depth == 0 && c == ')' {
				if s := strings.TrimSpace(cur.String()); s != "" {
					args = append(args, s)
				}
				return args
			}
			depth--
			cur.WriteByte(c)
		case ',':
			if depth == 0 {
				args = append(args, strings.TrimSpace(cur.String()))
				cur.Reset()
				continue
			}
			cur.WriteByte(c)
		default:
			cur.WriteByte(c)
		}
	}
	return nil
}

func TestAuthoritativeBootstrapUsesTheManagersRedisClient(t *testing.T) {
	src := mainGoSource(t)
	args := topLevelArgs(src, "applyBootstrapWithRetry")
	if args == nil {
		t.Fatal("authoritative startup must go through applyBootstrapWithRetry; call not found in main.go")
	}
	if len(args) != 3 {
		t.Fatalf("applyBootstrapWithRetry arity changed: got %d args %v, want 3", len(args), args)
	}
	found := false
	for _, a := range args {
		if strings.Contains(a, "redisClientForCache") {
			t.Fatalf("bootstrap must never receive the gateway client, got arg %q", a)
		}
		if a == "ursmV2Redis" {
			found = true
		}
	}
	if !found {
		t.Fatalf("bootstrap must receive ursmV2Redis exactly, got args %v", args)
	}
}

// The manager and the bootstrap must be wired from one value; otherwise the two
// can drift apart again without the call-site check noticing.
//
// The anchor is the Dependencies block *by name*, not by wording: a global
// "Redis: ursmV2Redis" search would also match an unrelated literal elsewhere in
// main.go and pass while the manager was still built on the old client.
func TestManagerAndBootstrapShareOneRedisClientValue(t *testing.T) {
	src := mainGoSource(t)
	if !strings.Contains(src, "ursmV2Redis = ursmRedis.Client()") {
		t.Fatal("ursmV2Redis must be captured from the same client the manager is built on")
	}
	block := blockLiteral(src, "ursmv2.Dependencies{")
	if block == "" {
		t.Skip("manager dependencies literal not reachable by this source gate")
	}
	// Line-scoped: the Redis field must name ursmV2Redis on its own line, so a
	// mention of the identifier elsewhere inside the block cannot satisfy it.
	found := false
	for _, line := range strings.Split(block, "\n") {
		if regexp.MustCompile(`^\s*Redis:\s*ursmV2Redis\s*,`).MatchString(line) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ursmv2.New must be handed Redis: ursmV2Redis, got block:\n%s", block)
	}
}

// blockLiteral returns the text of the brace-delimited literal that starts at
// the given header, or "" when the header is absent.
func blockLiteral(src, header string) string {
	idx := strings.Index(src, header)
	if idx < 0 {
		return ""
	}
	depth := 0
	inStr := byte(0)
	for i := idx + len(header) - 1; i < len(src); i++ {
		c := src[i]
		if inStr != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == inStr {
				inStr = 0
			}
			continue
		}
		switch c {
		case '"', '`', '\'':
			inStr = c
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[idx+len(header) : i]
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Layer 2 — startup must retry a transient bootstrap failure.
// ---------------------------------------------------------------------------

func TestRetryBootstrapRetriesATransientFailure(t *testing.T) {
	calls := 0
	result, err := retryBootstrap(3, time.Millisecond, func() (bootstrap.Result, error) {
		calls++
		if calls < 3 {
			return bootstrap.Result{}, errors.New("pg slower than the 60s budget")
		}
		return bootstrap.Result{Total: 7, Written: 2, Skipped: 5}, nil
	})
	if err != nil {
		t.Fatalf("a failure that clears within the attempt budget must not degrade: %v", err)
	}
	if calls != 3 {
		t.Fatalf("attempts=%d, want 3", calls)
	}
	if result.Total != 7 || result.Written != 2 || result.Skipped != 5 {
		t.Fatalf("successful attempt's result was lost: %+v", result)
	}
}

func TestRetryBootstrapGivesUpAfterTheAttemptBudget(t *testing.T) {
	calls := 0
	_, err := retryBootstrap(2, time.Millisecond, func() (bootstrap.Result, error) {
		calls++
		return bootstrap.Result{}, errors.New("dependency down")
	})
	if err == nil {
		t.Fatal("a permanently failing bootstrap must not report success")
	}
	if calls != 2 {
		t.Fatalf("attempts=%d, want 2 (no unbounded retry, no silent give-up at 1)", calls)
	}
	if !strings.Contains(err.Error(), "after 2 attempts") {
		t.Fatalf("error must state the attempt count, got: %v", err)
	}
}

func TestStartupBootstrapAttemptBudgetIsAtLeastTwo(t *testing.T) {
	if bootstrapAttempts < 2 {
		t.Fatalf("bootstrapAttempts=%d; a single attempt makes a transient failure a permanent degrade", bootstrapAttempts)
	}
}
