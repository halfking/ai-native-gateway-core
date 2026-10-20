package sessionv2mirror

// Gates for the migration-821 abandoned landing pad (audit §9.92/§9.93).
//
// These moved here from domains/hooks/observability/telemetry when the pad was
// relocated, and they were rewritten rather than re-pointed: the relocation
// changed *what the invariants are*, not just which file they live in.
//
// The invariants, in descending order of how quietly they break:
//
//  1. The flag must land on `entry`, not on a copy of it. The consumer is
//     firePersistedHooks(entry) → PersistHook → runShadowWrite, and it reads the
//     caller's pointer. Setting the flag on the `fallback := *entry` copy the
//     insert path uses is a silent no-op: everything compiles, every test
//     passes, and the column stays NULL forever. This is the defect the first
//     round of the relocation actually had (audit §9.93).
//
//  2. The predicate's DIRECTION — the flag is set only when the row is
//     genuinely absent. Inverted, it flags ~100% of normal traffic as abandoned
//     and no threshold alert can catch that.
//
//  3. The mark must run AFTER w.Write and only when it succeeded. Issued before,
//     it matches 0 rows (the row does not exist yet); issued unconditionally, it
//     marks rows that were never written.
//
//  4. RLS GUCs must be established in the pad's own transaction. Both faces are
//     RLS-protected and a pooled connection carries none of the GUCs, so
//     without them the UPDATE silently matches 0 rows on every call. Measured,
//     not assumed — see the probe recorded in audit §9.93 and the header of
//     abandoned_turn.go.
//
//  5. tenant_id must be the raw value the row was written with. A conventional
//     "default" stand-in makes the UPDATE search for a tenant the row does not
//     have, which is a permanent silent miss for exactly the traffic the pad
//     exists to catch.
//
//  6. BOTH session_turns faces get the flag. A single-face write is "green but
//     broken": the other face matches 0 rows, and 0 rows is indistinguishable
//     from "nothing was abandoned".
//
//  7. No t0 placeholder row. session_turns is idempotent on
//     (tenant_id, request_id, partition_date) with ON CONFLICT DO NOTHING, so
//     inserting a placeholder would permanently pin success=false.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot is the repository root relative to this package's directory.
//
// Verified by probing a file that only exists at the true root, not by counting
// directories. The previous version of this file lived in the telemetry package
// and claimed the depth was 4 because that package has its own go.mod — it does
// not, and the claim was wrong. A count and a stale justification can agree with
// each other and still send a future reader to the wrong place, which is why
// every read below goes through this one constant and the depth is asserted by
// TestRepoRootResolvesToTheRealRepositoryRoot.
const repoRoot = "../../"

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// TestRepoRootResolvesToTheRealRepositoryRoot keeps repoRoot honest.
//
// A wrong depth does not read like a wrong depth: every path error reads like
// "that file does not exist", so the failure invites someone to "fix" it by
// editing the filename. Asserting against a file that exists only at the true
// root turns that into an explicit, located failure.
func TestRepoRootResolvesToTheRealRepositoryRoot(t *testing.T) {
	if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err != nil {
		t.Fatalf("repoRoot=%q does not resolve to the repository root: %v", repoRoot, err)
	}
}

// clientGo returns the telemetry source, where the predicate lives.
func clientGo(t *testing.T) string {
	t.Helper()
	return readRepoFile(t, "domains/hooks/observability/telemetry/client.go")
}

// padGo returns the pad implementation.
func padGo(t *testing.T) string {
	t.Helper()
	return readRepoFile(t, "internal/sessionv2mirror/abandoned_turn.go")
}

// TestAbandonedTurnTriggerSourceIsRetired replaces the two former gates
// (invariant 1 "flag on entry, not on a copy" and invariant 2 "predicate
// direction") — **2026-10-21**.
//
// # 为什么这两条门退役了（而不是「实现坏了」）
//
// 这两条门钉的是「telemetry 侧在 `updateRequestLog` 的 upsert-race 分支里
// 查 v1 表、判断 t0 行是否存在，然后设置 `entry.T0Missing = true`」这个**形态**。
//
// ★ 迁移 890 把 v1 族整族 DROP，**并把那段认领逻辑一并删除** ⇒
//
//	现在**没有任何代码**会设置 `entry.T0Missing = true` ⇒
//	⇒ 落地垫**恒不触发**，`session_turns.is_abandoned` 恒为 NULL/false。
//
// ⇒ 这两条门的前提**随被观测对象一起消失**了。它们的红不是缺陷，
//
//	是「门在守一个已经不存在的对象」。
//
// ★★ 处理方式（与 `internal/trace.FlushToPG` 的 R52「停写即丢弃」同口径）：
//
//	① **不**把实现改回去（v1 族退役是既定决策）；
//	② **不**把门删了（删掉就等于把「复活所需的登记」一起删了）；
//	③ 改成钉**退役这件事本身**：触发源必须**仍然不存在**，
//	   且**必须留下显式登记**（abandoned_turn.go 的头部）。
//
// ⇒ 这条门仍然是**有牙**的：有人把 `T0Missing = true` 写回去（不管对错），
//
//	它会红并指出「触发源回来了，请同步检查落地垫与本登记」。
//
// # 登记债
//
// 「有没有 t0」在 v1 退役后**没有判据源**了 ⇒ `is_abandoned` 恒为 NULL。
// 要还它需要一次语义设计（会话族的「开始了却没到终态」怎么判）——
// 不在本轮范围。登记见 abandoned_turn.go 头部。
func TestAbandonedTurnTriggerSourceIsRetired(t *testing.T) {
	client := stripGoComments(clientGo(t))
	if strings.Contains(client, "T0Missing = true") {
		t.Fatalf("client.go 里又出现了 `T0Missing = true` 的设置点 —— v1 族已被迁移 890 退役，" +
			"这个设置点依赖 `SELECT EXISTS(...)` 查 v1 表 ⇒ 若你重新加了它，说明触发源回来了，" +
			"请同步检查：① 落地垫是否随之复活 ② 本文件的退役登记是否要撤掉")
	}

	// ★ 退役必须**机器可读地**留痕：只查注释里的关键词不够 ——
	//   实测：把登记段首句整句抹掉，只查「退役」二字的门**仍然绿**
	//   （因为文件别处也有这两个字）⇒ 门恒绿 = 没有牙。
	//   ⇒ 这里钉一枚**常量**的值。
	pad := padGo(t)
	if !strings.Contains(pad, `abandonedTurnTriggerRetired = "v1-family-retired@2026-10-20"`) {
		t.Fatalf("internal/sessionv2mirror/abandoned_turn.go 里的机器可读退役标记缺失或被改写 —— " +
			"「触发源已退役」这件事必须**机器可读**（常量 abandonedTurnTriggerRetired）," +
			"否则登记只能靠注释里的关键词⇒ 门会恒绿、变成没有牙的门")
	}

	// 退役必须**留痕**（给人读的那一份）：没有登记的退役 = 静默的能力消失。
	if !strings.Contains(pad, "退役") {
		t.Fatalf("internal/sessionv2mirror/abandoned_turn.go 里没有「退役」登记 —— " +
			"触发源消失这件事没有被记录 ⇒ 后来的人只会看到「is_abandoned 永远是 NULL」，" +
			"却查不到它是什么时候、因为什么消失的")
	}
	if !strings.Contains(pad, "T0Missing") {
		t.Fatalf("abandoned_turn.go 的退役登记里没有提到 T0Missing —— 登记必须指明" +
			"「哪个触发源退役了」，否则它只是一句无关的说明")
	}
	if !strings.Contains(pad, "is_abandoned") {
		t.Fatalf("abandoned_turn.go 的退役登记里没有提到 is_abandoned —— 登记必须指明" +
			"「哪一列从此恒为 NULL」，否则读的人无法判断影响面")
	}
}

// TestAbandonedTurnMarkRunsAfterWriteAndOnlyOnSuccess pins invariant 3.
//
// This is the invariant the whole relocation exists for. Marking from the
// synchronous telemetry path raced the async turn insert and lost every time;
// marking from before the write, or without checking that the write succeeded,
// reintroduces the same race by a different arrangement.
func TestAbandonedTurnMarkRunsAfterWriteAndOnlyOnSuccess(t *testing.T) {
	code := stripGoComments(readRepoFile(t, "internal/sessionv2mirror/hook.go"))

	writeIdx := strings.Index(code, "w.Write(ctx, req)")
	if writeIdx < 0 {
		t.Fatalf("hook.go no longer calls w.Write(ctx, req) — the pad's anchor moved; " +
			"re-read runShadowWrite before trusting this gate")
	}
	markIdx := strings.Index(code, "markAbandonedTurnIfT0Missing(")
	if markIdx < 0 {
		t.Fatalf("runShadowWrite no longer calls markAbandonedTurnIfT0Missing — the " +
			"landing pad is not wired at all (audit §9.93 invariant 3)")
	}
	if markIdx < writeIdx {
		t.Fatalf("the pad is marked BEFORE w.Write runs — the turn row does not exist "+
			"yet, so the UPDATE matches 0 rows on every call. That is the first "+
			"implementation's defect, reintroduced (write=%d, mark=%d)", writeIdx, markIdx)
	}

	// The success guard must sit between the two, and must actually consult err.
	guard := code[writeIdx:markIdx]
	if !strings.Contains(guard, "err == nil") {
		t.Fatalf("the pad is not guarded on `err == nil` between w.Write and the mark — "+
			"it would flag rows whose turn write failed.\nsegment was:\n%s", guard)
	}
	if !strings.Contains(guard, "entry.T0Missing") {
		t.Fatalf("the pad is not guarded on entry.T0Missing between w.Write and the "+
			"mark — it would run a pointless UPDATE for every terminal turn.\n"+
			"segment was:\n%s", guard)
	}
}

// TestAbandonedTurnMarkEstablishesRLSGUCs pins invariant 4.
//
// This is the gate that would have caught the defect the first round of the
// relocation shipped with. Measured on the live database as a non-superuser
// role (audit §9.93): with no GUCs the UPDATE matches 0 rows; with only
// app.current_tenant it STILL matches 0 rows, because a RESTRICTIVE owner_filter
// policy also applies; with setBypassGUCs + tenant it matches 1 row.
//
// ⚠ A unit test cannot substitute for that measurement, and neither can a test
// that connects as `llm_gateway` — that role is rolsuper=t AND rolbypassrls=t,
// so RLS never applies to it. This gate pins the *shape*; the measurement is
// recorded in the audit.
func TestAbandonedTurnMarkEstablishesRLSGUCs(t *testing.T) {
	code := stripGoComments(padGo(t))

	if !strings.Contains(code, "setBypassGUCs(ctx, tx)") {
		t.Fatalf("the pad does not call setBypassGUCs — both session_turns faces are " +
			"RLS-protected and a pooled connection carries none of the GUCs, so the " +
			"UPDATE silently matches 0 rows on every call (audit §9.93 invariant 4)")
	}
	if !strings.Contains(code, "set_config('app.current_tenant'") {
		t.Fatalf("the pad does not set app.current_tenant — the statement no longer " +
			"says which tenant's row it targets, and the WITH CHECK arm of the tenant " +
			"policy has nothing to compare against")
	}

	// The GUCs must be established inside the pad's own transaction. A
	// session-scoped set_config would leak into the next borrower of the pooled
	// connection — replay.go:136 records exactly that hazard.
	if strings.Contains(code, `set_config('app.bypass_rls'`) &&
		!strings.Contains(code, "pool.Begin(ctx)") {
		t.Fatalf("bypass GUCs are set without a transaction in the pad — a " +
			"session-scoped set_config would leak across pool borrowers " +
			"(see execBypass's comment in replay.go)")
	}
}

// TestAbandonedTurnMarkUsesTheRawTenantID pins invariant 5.
//
// turn_writer writes rec.TenantID verbatim as $3 with no defaulting, and
// entryToProcessedRequest copies entry.TenantID unchanged. So the call site must
// pass req.TenantID. Substituting a conventional default (as the removed
// telemetry-side version did) makes the UPDATE search for tenant_id='default'
// while the row says ” — a permanent silent miss.
func TestAbandonedTurnMarkUsesTheRawTenantID(t *testing.T) {
	code := stripGoComments(readRepoFile(t, "internal/sessionv2mirror/hook.go"))

	i := strings.Index(code, "markAbandonedTurnIfT0Missing(")
	if i < 0 {
		t.Fatalf("call site not found — see TestAbandonedTurnMarkRunsAfterWriteAndOnlyOnSuccess")
	}
	// The argument list runs to the paren that BALANCES the call's opening one.
	//
	// ⚠ Two mistakes here, both caught by running the gate rather than reading
	// it. Cutting at the first `)` ends the slice inside `mirrorOutbox.Load()`.
	// And passing the offset of the IDENTIFIER makes balancedCall see `m`
	// instead of `(` and bail out — which reads as "unterminated call" on code
	// that is perfectly fine. Both are measurement bugs, not product failures,
	// and both produced confident false reds.
	open := i + strings.Index(code[i:], "(")
	if open < 0 {
		t.Fatalf("no `(` after the call name at offset %d", i)
	}
	call := balancedCall(code, open)
	if call == "" {
		lo, hi := i-120, i+200
		if lo < 0 {
			lo = 0
		}
		if hi > len(code) {
			hi = len(code)
		}
		t.Fatalf("could not balance the call at offset %d — the paren scan never "+
			"returned to depth 0, which means the stripped source around it is not "+
			"what it appears to be. Context:\n%s", i, code[lo:hi])
	}

	if !strings.Contains(call, "req.TenantID") {
		t.Fatalf("the pad is not keyed on req.TenantID, which is the value the turn "+
			"row was actually written with.\ncall was:\n%s", call)
	}
	if strings.Contains(call, "nonEmpty") || strings.Contains(call, `"default"`) {
		t.Fatalf("the pad substitutes a default tenant, but turn_writer writes "+
			"rec.TenantID verbatim ($3, no defaulting). The UPDATE would then search "+
			"for a tenant the row does not have — a permanent silent miss for exactly "+
			"the traffic the pad exists to catch (audit §9.93 invariant 5).\n"+
			"call was:\n%s", call)
	}

	// ⚠ The two substring checks above are NECESSARY BUT NOT SUFFICIENT, and a
	// mutation proved it: wrapping the value in a helper —
	// `tenantOr(req.TenantID)` — keeps "req.TenantID" as a substring while
	// moving the "default" literal outside the call, so BOTH checks pass and
	// the gate goes green on code that is permanently wrong for empty tenants.
	// ⇒ Assert on the ARGUMENT structure: exactly one argument must be the bare
	// identifier `req.TenantID`, and no argument may carry a default.
	args := callArgs(call)
	isBare := false
	for _, a := range args {
		if a == "req.TenantID" {
			isBare = true
		}
		if strings.Contains(a, `"default"`) || strings.Contains(a, "nonEmpty") {
			t.Fatalf("argument %q carries a default tenant. The row was written with "+
				"req.TenantID verbatim, so the UPDATE would search for a tenant the row "+
				"does not have (audit §9.93 invariant 5).\nargs were:\n%s", a, args)
		}
	}
	if !isBare {
		t.Fatalf("no argument is the bare identifier `req.TenantID`; the pad is keyed "+
			"on something derived from it, which means the value that identifies the "+
			"row is no longer provably the one the writer used. A helper such as "+
			"tenantOr(req.TenantID) is exactly the shape that slipped past the "+
			"substring checks above.\nargs were:\n%s", args)
	}
}

// callArgs splits a call's argument list on top-level commas, honouring nested
// parens/braces and quoted strings so that `f(a, g(b, c))` yields two args.
//
// Comment-stripped source is assumed, so string literals are the only quoted
// thing left to skip over.
func callArgs(call string) []string {
	open := strings.Index(call, "(")
	closeIdx := strings.LastIndex(call, ")")
	if open < 0 || closeIdx <= open {
		return nil
	}
	inner := call[open+1 : closeIdx]

	var (
		args  []string
		cur   strings.Builder
		depth int
		inStr bool
		esc   bool
	)
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		switch {
		case inStr:
			cur.WriteByte(c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		case c == '"':
			inStr = true
			cur.WriteByte(c)
			continue
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
		case c == ',' && depth == 0:
			args = append(args, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		args = append(args, s)
	}
	return args
}

// TestAbandonedTurnMarkTouchesBothTurnsFaces pins invariant 6.
//
// ⚠ Read the comment-stripped source. The file header names both tables many
// times in prose, so a raw substring check passes on a file that writes one
// face. That is the "a comment mentioning a key is not the key being written"
// trap, and it is exactly what made the first draft of this gate pass a
// one-face mutation.
func TestAbandonedTurnMarkTouchesBothTurnsFaces(t *testing.T) {
	code := stripGoComments(padGo(t))

	if !strings.Contains(code, "for _, tbl := range") {
		t.Fatalf("abandoned_turn.go does not iterate over the turn faces — the pad must " +
			"write both, because a single-face write silently matches 0 rows on the " +
			"other, and 0 rows is indistinguishable from \"nothing was abandoned\"")
	}
	// Both names must appear as SEPARATE quoted literals in one []string.
	// Checking for two substrings is weaker than it looks: "public.session_turns"
	// is a prefix of "public.session_turns_hot", so a file that writes only hot
	// still passes them. Ordered both ways; a fixed order would make this gate
	// fire on a harmless reordering.
	twoFaces := strings.Contains(code, `[]string{"public.session_turns_hot", "public.session_turns"}`) ||
		strings.Contains(code, `[]string{"public.session_turns", "public.session_turns_hot"}`)
	if !twoFaces {
		t.Fatalf("the iterated face list does not contain BOTH session_turns faces as "+
			"separate literals — writing only one face is the half-fixed bug this "+
			"project has hit repeatedly (§9.92 invariant 2).\ngot: %s", faceListOf(code))
	}
}

// faceListOf extracts the []string{…} literal used for the face iteration, for
// error messages.
func faceListOf(code string) string {
	i := strings.Index(code, "for _, tbl := range ")
	if i < 0 {
		return "<no range over tbl>"
	}
	rest := code[i:]
	if j := strings.Index(rest, "\n"); j > 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

// TestAbandonedTurnNeverInsertsAPlaceholderRow pins invariant 7.
//
// The failure this prevents is quiet: a t0 placeholder would insert with
// success=false, and the later terminal write would be swallowed by ON CONFLICT
// DO NOTHING — leaving a permanently wrong row that still counts as a real turn
// in every aggregate.
func TestAbandonedTurnNeverInsertsAPlaceholderRow(t *testing.T) {
	code := stripGoComments(padGo(t))

	// ⚠ The INSERT can only appear as a concatenated literal here (`+tbl+`),
	// because the table name is looped over. A check for the literal
	// "INSERT INTO public.session_turns" therefore passes on a file that inserts
	// a placeholder row — which is precisely the mutation this assertion exists
	// to catch, and it is why the first draft let that mutation through.
	if regexpInsertIntoTurns.MatchString(code) {
		t.Fatalf("abandoned_turn.go contains an INSERT into session_turns.\n" +
			"session_turns is idempotent on (tenant_id, request_id, partition_date) " +
			"with ON CONFLICT DO NOTHING, so a t0 placeholder row would be pinned at " +
			"success=false forever and swallow the terminal enrichment. The pad must " +
			"be a flag on the terminal row, not a row of its own.")
	}

	// The write must be a real UPDATE (idempotent, cannot create rows) and must
	// not touch the terminal row's outcome columns.
	if !strings.Contains(code, "SET is_abandoned = TRUE") {
		t.Fatalf("abandoned_turn.go does not set is_abandoned = TRUE — the pad's only " +
			"write should be that one assignment")
	}
	for _, forbidden := range []string{"success =", "status_code =", "error_kind ="} {
		if strings.Contains(code, forbidden) {
			t.Fatalf("abandoned_turn.go writes %q — the pad must not touch the terminal "+
				"row's outcome columns; it only marks the row", forbidden)
		}
	}
}

// TestMigration821ExistsOnBothFacesAndIsRegistered guards the sync points.
//
// §9.64.10 recorded the shape this prevents: the migration file sits in the
// canonical tree, every package test is green, and no machine ever runs it —
// because a running gateway does not apply startup migrations; the installer is
// the only executor.
func TestMigration821ExistsOnBothFacesAndIsRegistered(t *testing.T) {
	const rel = "sql/migrations/startup/821_session_turns_abandoned_marker.sql"
	mig := readRepoFile(t, rel)

	// Both faces must be altered in the migration itself, not just mentioned.
	for _, face := range []string{"public.session_turns", "public.session_turns_hot"} {
		if !strings.Contains(mig, "ALTER TABLE "+face) {
			t.Fatalf("migration 821 does not ALTER %s — one face would be left without "+
				"the column and every UPDATE against it would fail 42703", face)
		}
	}

	// The embeddata copy must be byte-identical: the installer would otherwise
	// apply a different migration than the one under review.
	embedRel := filepath.Join("installer/cmd/llm-gw-installer/embeddata/startup",
		"821_session_turns_abandoned_marker.sql")
	if a, b := readRepoFile(t, rel), readRepoFile(t, embedRel); a != b {
		t.Fatalf("embeddata copy differs from canonical source — the installer would " +
			"apply a different migration than the one under review (sync point 1)")
	}

	mainGo := readRepoFile(t, "installer/cmd/llm-gw-installer/main.go")
	if !strings.Contains(mainGo, "//go:embed embeddata/startup/821_session_turns_abandoned_marker.sql") {
		t.Fatalf("main.go has no //go:embed for migration 821 (sync point 2)")
	}
	if !strings.Contains(mainGo, `"startup/821_session_turns_abandoned_marker.sql"`) {
		t.Fatalf("main.go has no embeddedSQLFiles entry for migration 821 (sync point 3)")
	}

	runner := readRepoFile(t, "installer/internal/dbinit/runner.go")
	if !strings.Contains(runner, `"821_session_turns_abandoned_marker.sql"`) {
		t.Fatalf("runner.go StartupFiles has no entry for migration 821 (sync point 4) — " +
			"the installer would never apply it, and a running gateway does not apply " +
			"startup migrations either (§9.64.10)")
	}

	tsv := readRepoFile(t, "sql/schema/installed_startup_migrations.tsv")
	if !strings.Contains(tsv, "821_session_turns_abandoned_marker.sql") {
		t.Fatalf("installed_startup_migrations.tsv has no entry for migration 821 (sync point 5)")
	}

	// sync point 6: the deployment channel script. §9.92.7f recorded that this leg
	// still pointed at the deleted 819 while the rest of the repo moved on, and
	// apply-db-revision-sequence.sh exits 4 on a missing file — i.e. it would
	// take down every 252/154 upgrade.
	sh := readRepoFile(t, "scripts/apply-db-revision-sequence.sh")
	if !strings.Contains(sh, "821_session_turns_abandoned_marker.sql") {
		t.Fatalf("apply-db-revision-sequence.sh does not reference migration 821 " +
			"(sync point 6) — the sequence script exits 4 on a missing file")
	}
	if strings.Contains(sh, "819_request_abandoned") {
		t.Fatalf("apply-db-revision-sequence.sh still references the removed 819 " +
			"migration — the script exits 4 on a missing file, which would break " +
			"every 252/154 upgrade (§9.92.7f)")
	}
}

// balancedCall returns the call expression ending at the paren that balances
// the one at i. i must point at the OPENING paren, not at the identifier.
//
// Operates on comment-stripped source, so parens inside strings and comments
// cannot skew the count.
func balancedCall(code string, i int) string {
	if i < 0 || i >= len(code) || code[i] != '(' {
		return ""
	}
	depth := 0
	for j := i; j < len(code); j++ {
		switch code[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return code[i-1 : j+1]
			}
		}
	}
	return ""
}

// braceDepth returns the net nesting depth of `{` minus `}` in src.
//
// Computed on a comment- and string-stripped view: a brace inside a comment or
// a string literal must not move the depth, or the measurement answers a
// question about the prose rather than about the code.
func braceDepth(src string) int {
	depth := 0
	inLineComment, inBlockComment, inStr, inRune := false, false, false, false
	escaped := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		n := byte(0)
		if i+1 < len(src) {
			n = src[i+1]
		}
		switch {
		case inLineComment:
			if c == '\n' {
				inLineComment = false
			}
		case inBlockComment:
			if c == '*' && n == '/' {
				inBlockComment = false
				i++
			}
		case inStr:
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inStr = false
			}
		case inRune:
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '\'' {
				inRune = false
			}
		default:
			switch {
			case c == '/' && n == '/':
				inLineComment = true
				i++
			case c == '/' && n == '*':
				inBlockComment = true
				i++
			case c == '"':
				inStr = true
			case c == '\'':
				inRune = true
			case c == '{':
				depth++
			case c == '}':
				depth--
			}
		}
	}
	return depth
}

// stripGoComments removes // and /* */ comments while preserving every other
// byte, so indexes and string literals still line up with the original.
//
// Kept deliberately naive: this is a *gate input*, not a parser. It exists so
// that "the identifier is mentioned in prose" cannot be mistaken for "the
// identifier is used in code".
//
// ⚠ Backtick raw strings ARE handled, and not for tidiness. hook.go carries
// struct tags like `json:"messages"`, whose embedded double quotes used to
// desynchronise the double-quote scanner; every paren-balance computed after
// such a tag was then off, and a gate reported "unterminated call" against code
// that is fine. A measurement that silently corrupts its input is worse than no
// measurement: it produces confident false reds. (A raw string cannot contain a
// backtick, so a plain scan to the next one is exact.)
func stripGoComments(src string) string {
	var out []byte
	inStr, inRune, inRaw, esc := false, false, false, false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inRaw {
			out = append(out, c)
			if c == '`' {
				inRaw = false
			}
			continue
		}
		if inStr {
			out = append(out, c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		if inRune {
			out = append(out, c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '\'' {
				inRune = false
			}
			continue
		}
		if c == '`' {
			inRaw = true
			out = append(out, c)
			continue
		}
		if c == '"' {
			inStr = true
			out = append(out, c)
			continue
		}
		if c == '\'' {
			inRune = true
			out = append(out, c)
			continue
		}
		if c == '/' && i+1 < len(src) && src[i+1] == '/' {
			for i < len(src) && src[i] != '\n' {
				i++
			}
			out = append(out, '\n')
			continue
		}
		if c == '/' && i+1 < len(src) && src[i+1] == '*' {
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i++
			continue
		}
		out = append(out, c)
	}
	return string(out)
}

// regexpInsertIntoTurns matches an INSERT aimed at a session_turns face, in
// either the literal or the concatenated form, so it survives the table name
// being looped over:
//
//	INSERT INTO public.session_turns_hot
//	INSERT INTO ` + tbl + `
var regexpInsertIntoTurns = regexp.MustCompile(
	"INSERT\\s+INTO\\s+" + "`?\\s*(\\+tbl\\+|public\\.session_turns)")
