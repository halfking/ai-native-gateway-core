//go:build !integration

package sessionv2mirror

// final_success_turn_gate_test.go — 2026-10-05（审计 §9.203）接线门。
//
// 这道门只证明**接线**存在，不证明标记真的落库（那是
// final_success_turn_realdb_test.go 的事）。之所以仍要有它：落点链跨三个文件
// 四个函数（telemetry 认领 → entry 标志 → runShadowWrite / replayOne → 标记），
// 每一处都可能被人重构时静默摘掉，而摘掉之后**编译通过、单测通过、真库门也会
// 因为「没写」而看不出来**——真库门量的是标记函数本身，不是有没有人调用它。
//
// 与 abandoned_turn_gate_test.go 同款：断言代码形状，不跑数据库。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// TestFinalSuccessClaimIsSetOnTheEntry replaces the former
// TestFinalSuccessFlagIsSetOnTheCallersEntry — **2026-10-21**.
//
// # 为什么旧门退役了（而不是「实现坏了」）
//
// 旧门钉的是 v1 终态成功认领的**旧形态**：认领发生在
// `updateRequestLog` 的 upsert-race 分支里，该分支会
// `fallback := *entry` 再 `insertRequestLog(&fallback)` ⇒
// 认领记在 **copy** 上 ⇒ 必须把 flag 从 copy **拷回** caller's entry
// （`entry.FinalSuccessClaimed = fallback.FinalSuccessClaimed`）。
//
// ★ 迁移 890（v1 族退役）把 v1 的终态成功认领**整段删除**，改成：
//
//	if shouldClaimFinalSuccess(entry) {
//	    entry.FinalSuccessClaimed = true
//	}
//
// ⇒ **不再有 copy，也不再需要拷回**；会话族自己仲裁
//
//	（session_turns 的 partial 唯一索引 + markTurnFinalSuccess）。
//
// ⇒ 旧门的断言对象（旧形态）**已不存在**，它的红是「门在守一个已经不存在的形态」。
//
//	★ 注意区分：这条门退役**不代表** `is_final_success` 坏了 ——
//	  新形态把它**设在 caller's entry 上**，反而比「拷回」更不容易丢。
//
// ⇒ 新门钉**新形态**，且仍然**有牙**：把设置点挪回 copy 形态就会红。
func TestFinalSuccessClaimIsSetOnTheEntry(t *testing.T) {
	code := stripGoComments(repoFile(t, "domains/hooks/observability/telemetry/client.go"))

	if !strings.Contains(code, "entry.FinalSuccessClaimed = true") {
		t.Fatalf("client.go 里没有 `entry.FinalSuccessClaimed = true` —— 终态成功认领必须" +
			"落在 caller's entry 上（消费方 firePersistedHooks(entry) 读的是 caller 的指针），" +
			"否则会话族的 is_final_success 永远不会被置位")
	}

	// ★ copy 形态已随 v1 认领整段删除而退役：若它回来了，说明认领又绕回了
	//   `fallback := *entry` 那条路 ⇒ 必须拷回，否则该分支上的认领会丢。
	if strings.Contains(code, "fallback.FinalSuccessClaimed") {
		t.Fatalf("client.go 里又出现了 `fallback.FinalSuccessClaimed` —— copy 形态已随迁移 890 " +
			"（v1 族退役）删除：认领现在直接在 entry 上设置。若你重新引入了 copy 形态，" +
			"就必须把 flag 拷回 caller 的 entry，否则走该分支的请求会丢掉认领")
	}

	// 设置点必须**由认领判定驱动**，不是无条件置位。
	if !strings.Contains(code, "shouldClaimFinalSuccess(entry)") {
		t.Fatalf("client.go 里没有 `shouldClaimFinalSuccess(entry)` —— 认领的判定入口不见了，" +
			"请先读懂现状再改：这条门现在钉的是「认领资格由 shouldClaimFinalSuccess 决定」")
	}
}

// TestFinalSuccessPadIsNotAnElseOfTheAbandonedPad guards the two pads against
// being collapsed into one branch.
//
// They are independent facts: a request can both have had no t0 and have won
// its session's final success. Writing `else if` (or nesting the second mark
// inside the T0Missing block) would make one mask the other, and the symptom
// would be a plausible-looking column that is populated for only a fraction of
// the rows it should cover — exactly the failure that made this whole column
// invisible in the first place.
func TestFinalSuccessPadIsNotAnElseOfTheAbandonedPad(t *testing.T) {
	code := repoFile(t, "internal/sessionv2mirror/hook.go")

	mark := strings.Index(code, "markTurnFinalSuccess(")
	if mark < 0 {
		t.Fatalf("runShadowWrite no longer calls markTurnFinalSuccess — the pad is " +
			"dead code and every new final-success claim is silently dropped on the " +
			"session side")
	}
	write := strings.Index(code, "err := w.Write(ctx, req)")
	if write < 0 {
		t.Fatalf("cannot locate `err := w.Write(ctx, req)` in hook.go; the ordering " +
			"assertion below would be vacuous")
	}
	if mark < write {
		t.Fatalf("markTurnFinalSuccess is called BEFORE w.Write — the turn row does " +
			"not exist yet, so the UPDATE matches 0 rows on essentially every " +
			"attempt (this is why the pad is placed where it is; see " +
			"final_success_turn.go)")
	}
	if strings.Contains(code, "} else if entry.FinalSuccessClaimed") {
		t.Fatalf("the final-success pad is an else-if of the abandoned pad — the two " +
			"are independent facts and one would mask the other")
	}
	// Both guards must be on err == nil; a mark attempted after a failed write
	// is a guaranteed mark_no_row.
	guard := code[mark-200 : mark]
	if !strings.Contains(guard, "entry.FinalSuccessClaimed") {
		t.Fatalf("the markTurnFinalSuccess call is not guarded on entry.FinalSuccessClaimed " +
			"between w.Write and the call — every turn would be marked, not just the " +
			"claimed one")
	}
}

// TestFinalSuccessPadRunsOnTheReplayPath pins the compensation path.
//
// registerFinalSuccessClaimOutbox registers the outbox row in the SAME
// transaction as a granted claim, and that outbox is precisely the repair path
// for "the claim happened but the mirror write never landed". If the replay
// path does not apply the mark, the one mechanism designed to close that hole
// reopens it for this column.
func TestFinalSuccessPadRunsOnTheReplayPath(t *testing.T) {
	code := repoFile(t, "internal/sessionv2mirror/replay.go")

	if !strings.Contains(code, "markTurnFinalSuccess(") {
		t.Fatalf("replayOne no longer calls markTurnFinalSuccess — replayed turns of a " +
			"granted claim would be written without the mark, which is the exact " +
			"G2 gap registerFinalSuccessClaimOutbox exists to close")
	}
	if !strings.Contains(code, "if entry.FinalSuccessClaimed {") {
		t.Fatalf("replayOne does not guard the pad on entry.FinalSuccessClaimed — every " +
			"replayed turn would be marked")
	}
	// The flag has to survive the outbox round-trip, i.e. it must be serialised.
	client := repoFile(t, "domains/hooks/observability/telemetry/client.go")
	if !strings.Contains(client, `json:"final_success_claimed,omitempty"`) {
		t.Fatalf("RequestLogEntry.FinalSuccessClaimed is not serialised — the outbox " +
			"payload would drop it and the replay path could never see that a " +
			"replayed turn is supposed to be the final success")
	}
	// The pool field is what makes the replay call well-typed: replayDB is an
	// interface, and a nil *pgxpool.Pool boxed into one compares != nil.
	if !strings.Contains(code, "pool      *pgxpool.Pool") {
		t.Fatalf("MirrorOutboxReaper no longer keeps the *pgxpool.Pool — replayOne " +
			"cannot pass a non-nil-checkable handle to markTurnFinalSuccess")
	}
}

// TestFinalSuccessSecondClaimIsRejectedByThePredicate —— §143.105（2026-10-24）。
//
// # 这道门钉的是什么
//
// 252 生产日志（10-22/10-23）实测 5,961 次 `uq_session_turns_hot_final_success`
// 23505 —— 890 之后每个成功请求都带认领资格，忙会话每天几千次「第二条认领
// 撞部分唯一索引」，每次 = PG 日志 ERROR + slog.Warn + 事务回滚，且 hot 侧
// 被放行的跨面第二条 final 行是 promote 侧 23505（897 场景⑤）的生成源。
//
// 修复形态：UPDATE 谓词带**两臂跨面半连接**（hot 臂 + 母表臂），落选走
// **确定性 0 行** + 三分分类（no_row / noop / superseded），superseded 降为
// Debug。任何一处被退化都必须红：
//
//  1. UPDATE 里必须有两臂 NOT EXISTS（退回裸 UPDATE ⇒ 23505 风暴回归）；
//  2. 0 行分支必须是三分（rowsWithID/alreadyMarked ⇒ 把落选混回 no_row 或
//     noop 都会让真异常告警说假话 —— 本文件头注记录过同款教训）；
//  3. mark_superseded 的落选路径必须是 Debug（Warn 会让忙会话每天几千条假警）。
//
// 有牙：变异腿删掉母表半连接臂 ⇒ 缺口列表必然非空
// （TestFinalSuccessPredicateGateHasTeeth）。
func TestFinalSuccessSecondClaimIsRejectedByThePredicate(t *testing.T) {
	code := stripGoComments(repoFile(t, "internal/sessionv2mirror/final_success_turn.go"))
	if gaps := finalSuccessPredicateGaps(code); len(gaps) > 0 {
		t.Fatalf("final_success_turn.go 的落选防御形状缺失：\n  - %s",
			strings.Join(gaps, "\n  - "))
	}
}

// finalSuccessPredicateGaps 返回「§143.104 落选防御」形状缺口列表（空 = 形状完整）。
// 单独提函数是为了让变异腿能对同一判据跑红（门与判据同一个实现，零漂移）。
func finalSuccessPredicateGaps(code string) []string {
	var gaps []string
	if !strings.Contains(code, "AND NOT EXISTS (") {
		gaps = append(gaps, "UPDATE 里没有跨面半连接 —— 第二条认领会重新走 23505 异常路径"+
			"（PG 日志 ERROR + WARN + 回滚风暴，252 实测 2 天 5,961 条）")
	}
	// 两臂必须分别指向两张脸（只查本表的半连接挡不住跨面持标 —— 那正是
	// promote 冲突的生成源）。
	if !strings.Contains(code, "public.session_turns_hot f") {
		gaps = append(gaps, "跨面半连接缺 hot 臂")
	}
	if !strings.Contains(code, "public.session_turns g") {
		gaps = append(gaps, "跨面半连接缺母表臂 —— hot/母表互相看不见持标行，"+
			"promote 侧 23505（897 场景⑤）的生成源回来了")
	}
	// 0 行分支必须三分。
	for _, needle := range []string{"rowsWithID", "alreadyMarked", "case rowsWithID == 0", "case alreadyMarked > 0"} {
		if !strings.Contains(code, needle) {
			gaps = append(gaps, "0 行分支缺三分分类（"+needle+"）—— 落选/幂等/行不存在"+
				"混为一谈会让告警说假话")
		}
	}
	// 落选必须 Debug（Warn = 忙会话每天几千条假警）。
	if strings.Contains(code, `slog.Warn("sessionv2mirror: final_success pad superseded`) {
		gaps = append(gaps, "落选路径仍在用 slog.Warn —— 多轮会话每天几千条落选"+
			"全按异常报警等于没有报警")
	}
	return gaps
}

// TestFinalSuccessPredicateGateHasTeeth —— 变异腿：删掉母表半连接臂必须红。
// 在**内存副本**上做（R98 模式），仓内真文件零改动。
func TestFinalSuccessPredicateGateHasTeeth(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal/sessionv2mirror/final_success_turn.go"))
	if err != nil {
		t.Fatalf("read final_success_turn.go: %v", err)
	}
	needle := `AND NOT EXISTS (
			       SELECT 1 FROM public.session_turns g`
	if !strings.Contains(string(raw), needle) {
		t.Fatalf("变异锚点（母表半连接臂）在源码里找不到 —— 先核对锚点再谈变异")
	}
	mutated := strings.Replace(string(raw), needle, "AND TRUE /* MUTATED: cross-face arm removed */", 1)
	if mutated == string(raw) {
		t.Fatalf("变异没有生效")
	}
	if gaps := finalSuccessPredicateGaps(stripGoComments(mutated)); len(gaps) == 0 {
		t.Fatalf("变异体（母表半连接臂被删）没有产生任何形状缺口 —— 门对这族缺陷没有牙")
	}
}
