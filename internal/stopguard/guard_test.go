package stopguard

import (
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot resolves the repository root from this package's directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// TestNoStopMethodWaitsOnAChannelItMayNeverReceive is the gate.
//
// 2026-10-21 (R71), measured on llm-gateway-go: the gateway's graceful
// shutdown hung for 97 minutes on two zombie processes. Evidence chain
// (SIGQUIT stack dumps):
//
//	goroutine 1 [sync.WaitGroup.Wait, 97 minutes]:
//	    db.(*DB).Close()   → pgxpool.Close() → puddle WaitGroup.Wait
//	    main.main.func3()  → the deferred db.Close()
//
//	goroutine 5631 [chan receive, 97 minutes]:
//	    bg.(*PassiveProbeListener).Stop()   → <-l.done
//	    main.main.func72()
//
// The shutdown sequence is ONE goroutine, so that single blocking Stop()
// prevented every later Stop() from running — including
// `autoRouteListener.Stop()`, which owns the long-lived LISTEN connection.
// The connection was therefore never returned to the pool, the deferred
// db.Close() blocked forever, and the process stayed alive as a zombie
// holding every Redis connection it had opened.
//
// The trigger was the DEFAULT probe mode: cmd/gateway/main.go constructs
// the legacy passive-probe listener and skips Start() when
// LLM_GATEWAY_USE_NEW_PROBE_MODE is true (the default since 2026-07-14).
//
// ⇒ The rule: a terminal channel wait inside a Stop() method must be
// unreachable unless the worker actually started.
func TestNoStopMethodWaitsOnAChannelItMayNeverReceive(t *testing.T) {
	root := repoRoot(t)
	findings, err := ScanDir(root)
	if err != nil {
		t.Fatalf("ScanDir(%s): %v", root, err)
	}
	if len(findings) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString("\n")
	for _, f := range findings {
		b.WriteString("  " + f.String() + "\n")
	}
	t.Fatalf("%d Stop() method(s) can block forever on a channel that is never closed:%s",
		len(findings), b.String())
}
