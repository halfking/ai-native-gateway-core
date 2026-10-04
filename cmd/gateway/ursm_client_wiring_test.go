package main

import (
	"regexp"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Every URSM-namespace component must be wired to ursmV2Redis.
//
// The db14 incident was not one bug. bootstrap.Apply was the instance that
// failed loudly; the same class of wiring existed at five more call sites, and
// one of them was persist.New — the very component whose full-db SCAN cost
// motivated URSM_V2_REDIS_DB in the first place. Migrating the namespace while
// leaving persist on the gateway client would have delivered none of the
// intended relief, while still splitting sticky / intent / stickyload state
// across two databases.
//
// The set of URSM packages is derived from main.go's own import block rather
// than hand-listed, because a hand-maintained roster rots silently: a new
// subsystem lands, the gate keeps passing, and the drift ships.
// ---------------------------------------------------------------------------

var (
	ursmImportRe = regexp.MustCompile(`(?m)^[ \t]*(?:(\w+)[ \t]+)?"github\.com/kaixuan/llm-gateway-go/domains/ursm(?:/[^"]*)?"`)
	// Must capture the package qualifier. Matching only the identifier before
	// "(" yields "NewStickyStore" for "ursmcache.NewStickyStore(", which has
	// no dot and is therefore never recognised as a URSM call — the gate would
	// look armed while checking nothing. Mutations M8-M10 exist because of that.
	callHeadRe = regexp.MustCompile(`\b(\w+(?:\.\w+)?)\(`)
)

// ursmImportAliases returns the identifier each domains/ursm/... package is
// bound to in main.go: the declared alias, or the last path segment.
func ursmImportAliases(src string) map[string]bool {
	aliases := map[string]bool{}
	for _, line := range strings.Split(src, "\n") {
		if !strings.Contains(line, "llm-gateway-go/domains/ursm") {
			continue
		}
		m := ursmImportRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if m[1] != "" {
			aliases[m[1]] = true
			continue
		}
		// No alias: the identifier Go binds is the final path segment.
		q := strings.Index(line, `"`)
		if q < 0 {
			continue
		}
		path := strings.Trim(line[q+1:], `",`)
		if i := strings.LastIndex(path, "/"); i >= 0 {
			path = path[i+1:]
		}
		if path != "" {
			aliases[path] = true
		}
	}
	return aliases
}

// ursmCallsOnGatewayClient returns every "<ursmPkg>.<fn>(" call in src whose
// argument list mentions redisClientForCache.
func ursmCallsOnGatewayClient(src string) []string {
	aliases := ursmImportAliases(src)
	var bad []string
	seen := map[string]bool{}
	for _, loc := range callHeadRe.FindAllStringSubmatchIndex(src, -1) {
		name := src[loc[2]:loc[3]]
		dot := strings.LastIndex(name, ".")
		if dot < 0 {
			continue
		}
		if !aliases[name[:dot]] {
			continue
		}
		args := topLevelArgs(src[loc[0]:], name)
		if args == nil {
			continue
		}
		for _, a := range args {
			if strings.Contains(a, "redisClientForCache") {
				key := name + " @ " + a
				if !seen[key] {
					seen[key] = true
					bad = append(bad, key)
				}
				break
			}
		}
	}
	return bad
}

func TestNoUrsmComponentIsWiredToTheGatewayClient(t *testing.T) {
	src := mainGoSource(t)
	aliases := ursmImportAliases(src)
	if len(aliases) == 0 {
		t.Fatal("no domains/ursm imports found in main.go; the gate would pass vacuously")
	}
	if bad := ursmCallsOnGatewayClient(src); len(bad) > 0 {
		t.Fatalf("URSM components wired to the gateway client (must be ursmV2Redis): %v\nknown URSM aliases: %v",
			bad, keysOf(aliases))
	}
}

// The two load-bearing sites are named explicitly, because their miswiring is
// invisible at runtime: persist still snapshots successfully — it is just slow;
// the rebuild still "succeeds" — it just writes the manifest where nothing
// reads it. Neither shows up as an error.
func TestPersistWriterAndRebuildUseTheManagersRedisClient(t *testing.T) {
	src := mainGoSource(t)

	persistArgs := topLevelArgs(src, "persist.New")
	if persistArgs == nil {
		t.Fatal("persist.New call not found in main.go")
	}
	if len(persistArgs) == 0 || persistArgs[0] != "ursmV2Redis" {
		t.Fatalf("persist.New must receive ursmV2Redis as its first argument, got %v", persistArgs)
	}

	block := blockLiteral(src, "rebuildOptions{")
	if block == "" {
		t.Fatal("rebuildOptions literal not found in main.go")
	}
	ok := false
	for _, line := range strings.Split(block, "\n") {
		if regexp.MustCompile(`^\s*rdb:\s*ursmV2Redis\s*,`).MatchString(line) {
			ok = true
			break
		}
	}
	if !ok {
		t.Fatalf("the runtime coverage rebuild must use rdb: ursmV2Redis, got block:\n%s", block)
	}
}

// The gate is only as good as the alias derivation feeding it. If the import
// regex ever stops matching, the alias set shrinks silently and every call site
// stops being checked — a gate that passes because it stopped looking is worse
// than no gate. This asserts the derivation is complete against main.go's own
// import block, using no hand-counted number that could rot.
func TestUrsmAliasDerivationCoversEveryUrsmImport(t *testing.T) {
	src := mainGoSource(t)
	importLines := 0
	for _, line := range strings.Split(src, "\n") {
		if strings.Contains(line, "llm-gateway-go/domains/ursm") && strings.Contains(line, `"`) {
			importLines++
		}
	}
	if importLines == 0 {
		t.Fatal("no domains/ursm imports found; the wiring gate would pass vacuously")
	}
	aliases := ursmImportAliases(src)
	if len(aliases) != importLines {
		t.Fatalf("alias derivation produced %d identifiers for %d URSM import lines (%v) — the gate would check only part of the namespace",
			len(aliases), importLines, keysOf(aliases))
	}
}

// The detector itself needs a test, or a regression in it reads as a clean
// board. M8-M10 happened because the call-head regex stopped matching
// package-qualified calls: every mutation was "fixed" code that the gate
// happily ignored. A detector that can only ever return an empty list is
// indistinguishable from a clean codebase, so it must be shown a violation.
//
// Known limitation, stated rather than papered over: the detector matches the
// gateway client by its main.go identifier, so a *renamed or aliased* gateway
// client would evade it. That is why the load-bearing sites (persist.New,
// withRebuild, bootstrap) also carry their own explicit gates rather than
// relying on this sweep alone.
func TestUrsmWiringDetectorFlagsAKnownViolation(t *testing.T) {
	const bad = `package main

import (
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/cache"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/persist"
)

func wiring() {
	sticky = cache.NewStickyStore(redisClientForCache.Client(), 100000, time.Hour)
	writer = persist.New(redisClientForCache.Client(), "ursm:v2:", nil)
}
`
	badList := ursmCallsOnGatewayClient(bad)
	if len(badList) != 2 {
		t.Fatalf("detector must flag both known violations, got %d: %v", len(badList), badList)
	}
	joined := strings.Join(badList, " | ")
	for _, want := range []string{"cache.NewStickyStore", "persist.New"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("detector missed %s: %v", want, badList)
		}
	}

	// And it must stay quiet on a correctly wired snippet, or it is useless
	// as a gate (a detector that flags everything trains people to ignore it).
	const good = `package main

import (
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/cache"
)

func wiring() {
	sticky = cache.NewStickyStore(ursmV2Redis, 100000, time.Hour)
}
`
	if got := ursmCallsOnGatewayClient(good); len(got) != 0 {
		t.Fatalf("detector must stay silent on correct wiring, got %v", got)
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
