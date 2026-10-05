package bg

// 「自动对未曾标注核实过的模型定时进行核实」的**写回**路径的真库判据。
//
// # 为什么需要这一批
//
// 2026-10-05 盘点发现：`bg/modality_verification_test.go` 的 11 条判据**全部是纯
// 单元测试**（`grep -c TEST_DATABASE_URL` = 0），而且都走 `scan` / `probe` /
// `persist` / `rollup` 这四个**可注入接缝**。
//
// 最关键的一处：`TestVerifyOnce_UpgradesTextModelAfterTwoSemanticPasses` 里那个
// 假 `persist` **自己重新实现了一遍 applyStreak**，注释原话是「复刻真实的连击
// 口径」。⇒ 它验的是**接缝的调用与次数**，而：
//
//   · `persistRow` 的 SQL（INSERT … ON CONFLICT … WHERE）从未执行过；
//   · `rollupVerdict` 的两段 SQL（读 v_model_modality_verdict、写回
//     models_canonical）从未执行过。
//
// 而这两段 SQL 就是目标第一半「能**标注**各个模型的多模态能力」的**落点**。
// 单元测试全绿、目标的核心动作却一次都没跑过 —— 与本包
// health_check_scan_guard_test.go 钉的那条属同一族「看起来做了」。
//
// # 这批验什么
//
// 端到端跑真实 SQL：两次语义通过 ⇒ models_canonical.modality 从 text 升到
// vision、modality_source 变 'semantic'、modality_verified_at 落时间。
//
// 承重的是**两条不可让步的护栏**：
//   1. **一次通过不升级**（streakGoal=2）。单次判据假阳性率 1/1680 ≈ 0.06%，
//      一次蒙对就把模型永久标成多模态是不可逆的路由级后果。
//   2. **modality_source='manual' 绝不覆盖**。Layer 3 手工覆盖是运维的显式决定，
//      探测结论无权推翻它。
//
// 依赖真库：没有 TEST_DATABASE_URL 时跳过；夹具表已存在时也跳过（它要建表）。

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/internal/schemaobj"
)

func TestRollupLabelsCanonicalAfterTwoSemanticPasses(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — this needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	tables := []string{"model_modality_verification", "models_canonical"}
	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name = ANY($1)`, tables).Scan(&existing); err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	if existing > 0 {
		t.Skipf("%d of the fixture tables already exist — this test drops them", existing)
	}

	// ★ 清理注册在**建表之前**：建到一半失败会留残桩，下一轮安全闸看到
	// 「表已存在」直接 SKIP，人看到的是「通过」，实际一次都没跑。
	defer func() {
		_, _ = pool.Exec(ctx, `
			DROP VIEW IF EXISTS public.v_model_modality_verdict;
			DROP TABLE IF EXISTS public.model_modality_verification;
			DROP TABLE IF EXISTS public.models_canonical;
			DROP SEQUENCE IF EXISTS public.models_canonical_id_seq;`)
	}()

	// models_canonical 从仓的逐对象 SSOT 推导（真表 id 上无主键/无唯一约束，
	// 手抄会写成 PRIMARY KEY 而比真表更宽松）。825 会用
	// `ADD CONSTRAINT models_canonical_pkey PRIMARY KEY (id)` 补上。
	if _, err := pool.Exec(ctx, "CREATE SEQUENCE IF NOT EXISTS public.models_canonical_id_seq;\n"+
		schemaobj.Table(t,
			"../sql/objects/tables/models_canonical.sql",
			"../sql/objects/sequences/models_canonical_id.sql",
			"../sql/objects/constraints/models_canonical_models_canonical_canonical_name_key.sql",
		)); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	mig, err := os.ReadFile("../sql/migrations/startup/825_modality_graded_verification.sql")
	if err != nil {
		t.Fatalf("read 825: %v", err)
	}
	if _, err := pool.Exec(ctx, string(mig)); err != nil {
		t.Fatalf("apply 825: %v", err)
	}

	// 两个模型：一个该被自动标注（text → vision），一个是运维手工钉死的
	// （modality_source='manual'），用来验护栏 2。
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.models_canonical (canonical_name, modality, modality_source)
		VALUES ('m-auto','text','inferred'), ('m-manual','text','manual');`); err != nil {
		t.Fatalf("seed canonical: %v", err)
	}
	// **量具自证**：825 建的列与视图必须真的在。
	var cols, view int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_name='models_canonical' AND column_name IN
		('modality_source','modality_verified_at','modality_evidence')`).Scan(&cols); err != nil {
		t.Fatalf("probe columns: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_views
		WHERE viewname='v_model_modality_verdict'`).Scan(&view); err != nil {
		t.Fatalf("probe view: %v", err)
	}
	if cols != 3 || view != 1 {
		t.Fatalf("after 825: %d/3 labelled columns and %d/1 verdict view — the assertions below "+
			"would be measuring something other than this migration", cols, view)
	}

	v := &ModalityVerification{db: pool}

	newTarget := func(name string) modalityVerifyTarget {
		var id int64
		if err := pool.QueryRow(ctx, `SELECT id FROM public.models_canonical
			WHERE canonical_name=$1`, name).Scan(&id); err != nil {
			t.Fatalf("read id of %s: %v", name, err)
		}
		return modalityVerifyTarget{
			CredentialID: 1, CanonicalID: id, CanonicalName: name,
			RawModel: name, OutboundModel: name, Modality: "vision",
			StoredModality: "text",
		}
	}
	pass := SemanticProbeResult{
		Carry: ModalityLevelAccepted, Read: ModalityLevelConfirmed,
		Expected: []string{"red", "green"}, Mentioned: []string{"red", "green"},
		Score: 2, Answer: "red, green",
	}

	readModality := func(name string) (modality, source string, verifiedAt *time.Time) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT COALESCE(modality,''), COALESCE(modality_source,''),
			modality_verified_at FROM public.models_canonical WHERE canonical_name=$1`, name).
			Scan(&modality, &source, &verifiedAt); err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return
	}

	// ---- 护栏 1：第一次通过**不得**升级 ----
	auto := newTarget("m-auto")
	if err := v.persistRow(ctx, auto, pass); err != nil {
		t.Fatalf("persist #1: %v", err)
	}
	// 判词此时应是 unknown（read_level 还没到 confirmed）⇒ 写回不发生。
	if err := v.rollupVerdict(ctx, auto); err != nil {
		t.Fatalf("rollup #1: %v", err)
	}
	if mod, src, _ := readModality("m-auto"); mod != "text" || src != "inferred" {
		t.Errorf("after ONE semantic pass m-auto is modality=%q source=%q, want text/inferred — "+
			"streakGoal is 2: a single probe can be a 1/1680 false positive, and a permanent "+
			"mislabel is an irreversible routing-level consequence", mod, src)
	}

	// 证据行必须真的落库了（否则上面那条「没升级」可能只是「什么都没写」）。
	var level string
	var pos, neg int
	if err := pool.QueryRow(ctx, `SELECT read_level, read_pos_streak, read_neg_streak
		FROM public.model_modality_verification
		WHERE canonical_name='m-auto' AND modality='vision'`).Scan(&level, &pos, &neg); err != nil {
		t.Fatalf("evidence row missing after persist #1: %v", err)
	}
	if level != "unknown" || pos != 1 {
		t.Errorf("evidence after pass #1 = level=%q pos=%d, want unknown/1 — the one-sided "+
			"streak is what makes the two-win rule work", level, pos)
	}

	// ---- 第二次通过 ⇒ 升级 ----
	auto.ExistingRead, auto.PosStreak, auto.NegStreak = level, pos, neg
	if err := v.persistRow(ctx, auto, pass); err != nil {
		t.Fatalf("persist #2: %v", err)
	}
	if err := v.rollupVerdict(ctx, auto); err != nil {
		t.Fatalf("rollup #2: %v", err)
	}
	mod, src, verifiedAt := readModality("m-auto")
	if mod != "vision" {
		t.Errorf("after TWO semantic passes m-auto modality=%q, want vision — this is the "+
			"actual deliverable of the target's first half (auto-labelling a model's "+
			"multimodal capability) and it is exercised here for the first time", mod)
	}
	if src != "semantic" {
		t.Errorf("m-auto modality_source=%q, want semantic — the source must record that this "+
			"came from a probe verdict, not from the rule table", src)
	}
	if verifiedAt == nil {
		t.Error("m-auto modality_verified_at is NULL after a confirmed verdict — the timestamp " +
			"is what the 30-day re-verification window keys on")
	}

	// ---- 护栏 2：modality_source='manual' 绝不覆盖 ----
	manual := newTarget("m-manual")
	if err := v.persistRow(ctx, manual, pass); err != nil {
		t.Fatalf("persist manual #1: %v", err)
	}
	if err := v.rollupVerdict(ctx, manual); err != nil {
		t.Fatalf("rollup manual #1: %v", err)
	}
	manual.ExistingRead, manual.PosStreak, manual.NegStreak = level, pos, neg
	if err := v.persistRow(ctx, manual, pass); err != nil {
		t.Fatalf("persist manual #2: %v", err)
	}
	if err := v.rollupVerdict(ctx, manual); err != nil {
		t.Fatalf("rollup manual #2: %v", err)
	}
	if mod, src, _ := readModality("m-manual"); mod != "text" || src != "manual" {
		t.Errorf("m-manual is modality=%q source=%q after two confirmed passes, want text/manual — "+
			"a manual override is the operator's explicit decision and a probe verdict has no "+
			"authority to overturn it", mod, src)
	}
}

// TestRollupLogDoesNotClaimUnmadeChanges 钉「日志不许说谎」。
//
// 2026-10-05 真库实测撞出来的缺陷：`rollupVerdict` 的 UPDATE 带三个可否决它的
// WHERE 条件（手工覆盖守卫 + IS DISTINCT FROM），而那条
// `slog.Info("canonical modality changed by semantic verdict")` 是**无条件**打印
// 的 ⇒ 一行都没改动也照样喊「changed … from=text to=vision」。
//
// 为什么这不是「日志不好看」：**日志是运维判读「标注到底生效没有」的主要证据**。
// 对一个 modality_source='manual' 的模型喊「changed」，会让人以为运维的手工决定
// 被探测推翻了；对一个本来就等于目标值的组合喊「changed」，会让人以为标注生效
// 而其实没有。两种误读都会导向错误处置。
//
// 这条判据抓的是**日志文本**，所以它与上面那条「抓库里的值」的判据互补：库对了
// 但日志说错了，同样是缺陷（而且更容易骗过人）。
func TestRollupLogDoesNotClaimUnmadeChanges(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — this needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	tables := []string{"model_modality_verification", "models_canonical"}
	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name = ANY($1)`, tables).Scan(&existing); err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	if existing > 0 {
		t.Skipf("%d of the fixture tables already exist — this test drops them", existing)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `
			DROP VIEW IF EXISTS public.v_model_modality_verdict;
			DROP TABLE IF EXISTS public.model_modality_verification;
			DROP TABLE IF EXISTS public.models_canonical;
			DROP SEQUENCE IF EXISTS public.models_canonical_id_seq;`)
	}()

	if _, err := pool.Exec(ctx, "CREATE SEQUENCE IF NOT EXISTS public.models_canonical_id_seq;\n"+
		schemaobj.Table(t,
			"../sql/objects/tables/models_canonical.sql",
			"../sql/objects/sequences/models_canonical_id.sql",
			"../sql/objects/constraints/models_canonical_models_canonical_canonical_name_key.sql",
		)); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	mig, err := os.ReadFile("../sql/migrations/startup/825_modality_graded_verification.sql")
	if err != nil {
		t.Fatalf("read 825: %v", err)
	}
	if _, err := pool.Exec(ctx, string(mig)); err != nil {
		t.Fatalf("apply 825: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.models_canonical
		(canonical_name, modality, modality_source) VALUES ('m-manual','text','manual');`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM public.models_canonical
		WHERE canonical_name='m-manual'`).Scan(&id); err != nil {
		t.Fatalf("read id: %v", err)
	}

	// 抓 slog 的输出。
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	v := &ModalityVerification{db: pool}
	tgt := modalityVerifyTarget{
		CredentialID: 1, CanonicalID: id, CanonicalName: "m-manual",
		RawModel: "m-manual", OutboundModel: "m-manual", Modality: "vision",
		StoredModality: "text",
	}
	pass := SemanticProbeResult{
		Carry: ModalityLevelAccepted, Read: ModalityLevelConfirmed,
		Expected: []string{"red"}, Mentioned: []string{"red"}, Score: 1, Answer: "red",
	}
	//
	// ⚠ 只调**一次** rollupVerdict，而且是在证据已经 confirmed 之后。
	//
	// 第一版我写成「循环两次 persist + rollup」，结果抓到的是**空日志**。查下来
	// 不是被测代码的问题，是我这条判据走不到那行：
	//   · rollupVerdict 在 `newModality == stored` 时**提前 return，不打日志**
	//     （那是正确行为：值没变就没有变化可报）；
	//   · 而 StoredModality 是 target 上的字段，循环里我从不刷新它，所以第二轮
	//     拿到的还是 "text"，直接撞上提前 return。
	// ⇒ 证据要落两次（streak 到 confirmed），rollup 只在**第二轮之后**调一次。
	if err := v.persistRow(ctx, tgt, pass); err != nil {
		t.Fatalf("persist #1: %v", err)
	}
	// ★ 连击计数必须**从库里读回来**再用，不能沿用 target 上的零值。
	//
	// 这不是测试脚手架：真实 worker 的 dueTargets 正是从 SQL 里取
	// COALESCE(v.read_level,'unknown') / read_pos_streak / read_neg_streak 填进
	// target 的，而 persistRow 用这三个值调 applyStreak。第一版这条判据忘了刷新，
	// 于是第二次 persistRow 从 pos=0 重新算 ⇒ 证据永远停在 unknown ⇒ rollup
	// 一直提前 return ⇒ 抓到空日志。**症状是「测不到」，根因是量具没按真实
	// 数据流喂输入。**
	var lvl string
	var pos, neg int
	if err := pool.QueryRow(ctx, `SELECT read_level, read_pos_streak, read_neg_streak
		FROM public.model_modality_verification
		WHERE canonical_name='m-manual' AND modality='vision'`).Scan(&lvl, &pos, &neg); err != nil {
		t.Fatalf("read streak after pass #1: %v", err)
	}
	if lvl != "unknown" || pos != 1 {
		t.Fatalf("after pass #1: level=%q pos=%d, want unknown/1 — the fixture did not land as "+
			"intended", lvl, pos)
	}
	tgt.ExistingRead, tgt.PosStreak, tgt.NegStreak = lvl, pos, neg

	if err := v.rollupVerdict(ctx, tgt); err != nil {
		t.Fatalf("rollup after pass #1: %v", err)
	}
	// 第一轮判词仍是 unknown ⇒ 提前 return ⇒ 此刻还没有任何日志。
	// 这本身是要记下来的事实：判据必须知道**第一轮不打日志是对的**。
	if buf.Len() != 0 {
		t.Fatalf("the first rollup logged something, but its verdict is still unknown so the "+
			"function is supposed to return before logging. Captured so far:\n%s", buf.String())
	}
	if err := v.persistRow(ctx, tgt, pass); err != nil {
		t.Fatalf("persist #2: %v", err)
	}
	// 第二轮：判词 confirmed、newModality='vision' ≠ stored='text' ⇒ 真正走到
	// UPDATE；手工覆盖守卫让它命中 0 行。日志必须如实说「没改」。
	if err := v.rollupVerdict(ctx, tgt); err != nil {
		t.Fatalf("rollup after pass #2: %v", err)
	}

	out := buf.String()
	if out == "" {
		t.Fatal("no log output captured — the rollup logged nothing, so this test cannot tell " +
			"a truthful log from a missing one")
	}
	if strings.Contains(out, "canonical modality changed by semantic verdict") {
		t.Errorf("the log claims a label change for a modality_source='manual' model, but the "+
			"UPDATE's guard rejects the row (0 rows affected). The operator reading this log would "+
			"conclude their manual override had been overturned by a probe. Captured log:\n%s", out)
	}
	if !strings.Contains(out, "no label change") {
		t.Errorf("the rollup did not explain that the verdict was reached but nothing was "+
			"written. Captured log:\n%s", out)
	}
}

// TestRollupDoesNotDowngradeMultimodalOnVisionOnlyNegative 钉 2026-10-05 查实的
// 第二个潜伏缺陷：**降级用的是局部证据**。
//
// 路径：stored='multimodal' 的模型，worker 只会探 vision（SQL 的 probe_modality
// CASE 对非 audio/video 一律给 vision；audio 目标现在被准入闸挡掉，见
// modalityVerifyAdmit）。vision 被拒 ⇒ 判词 negative ⇒ rollupVerdict 走
// `stored == "multimodal"` 分支 ⇒ 查 anyConfirmed ⇒ 只有 vision 被探过，
// confirmed 不可能存在 ⇒ 降级成 text。
//
// ⇒ 结论建立在「这个模型除 vision 外没有别的腿」上，而那件事**从未被测过**：
// 一个「能收 audio 不能看图」的模型会被降级成 text。而 modality='multimodal'
// 在路由侧的含义正是「收 audio 或图片」（820 文件头引的候选过滤
// `COALESCE(mc.modality,'text') IN ('audio','multimodal')`），降级会让它
// 从候选里消失。
//
// 真库规模：106 个 multimodal 模型 / 370 个绑定会走到这条路径。
func TestRollupDoesNotDowngradeMultimodalOnVisionOnlyNegative(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — this needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	tables := []string{"model_modality_verification", "models_canonical"}
	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name = ANY($1)`, tables).Scan(&existing); err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	if existing > 0 {
		t.Skipf("%d of the fixture tables already exist — this test drops them", existing)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `
			DROP VIEW IF EXISTS public.v_model_modality_verdict;
			DROP TABLE IF EXISTS public.model_modality_verification;
			DROP TABLE IF EXISTS public.models_canonical;
			DROP SEQUENCE IF EXISTS public.models_canonical_id_seq;`)
	}()

	if _, err := pool.Exec(ctx, "CREATE SEQUENCE IF NOT EXISTS public.models_canonical_id_seq;\n"+
		schemaobj.Table(t,
			"../sql/objects/tables/models_canonical.sql",
			"../sql/objects/sequences/models_canonical_id.sql",
			"../sql/objects/constraints/models_canonical_models_canonical_canonical_name_key.sql",
		)); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	mig, err := os.ReadFile("../sql/migrations/startup/825_modality_graded_verification.sql")
	if err != nil {
		t.Fatalf("read 825: %v", err)
	}
	if _, err := pool.Exec(ctx, string(mig)); err != nil {
		t.Fatalf("apply 825: %v", err)
	}
	// m-mm   ：只有 vision 一条证据（判负）—— 钉「单一模态不得降级」
	// m-mm2  ：vision 判负 + audio 只探了一次（unknown）—— 钉「已探 2 个模态
	//          但负证据没覆盖全部时同样不得降级」
	//
	// ⚠ m-mm2 是被一次变异实测逼出来的：把 `negativeModalities < probedModalities`
	// 那半段条件删掉，m-mm 这条**照样绿**（它只有 1 个模态，那半段恒为假）。
	// ⇒ 夹具必须含一个能分开两半的样本，否则后半段就是没人看守的分支。
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.models_canonical (canonical_name, modality, modality_source)
		VALUES ('m-mm','multimodal','semantic'), ('m-mm2','multimodal','semantic');`); err != nil {
		t.Fatalf("seed canonical: %v", err)
	}

	v := &ModalityVerification{db: pool}
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM public.models_canonical
		WHERE canonical_name='m-mm'`).Scan(&id); err != nil {
		t.Fatalf("read id: %v", err)
	}
	target := modalityVerifyTarget{
		CredentialID: 1, CanonicalID: id, CanonicalName: "m-mm",
		RawModel: "m-mm", OutboundModel: "m-mm", Modality: "vision",
		StoredModality: "multimodal",
	}
	// 「能收图但读不出来」= 真正的语义负例：carry accepted、read negative。
	//
	// ⚠ 判据不能写成 carry=rejected：ProbeVisionSemantics 在 carry 被拒时
	// **提前返回**，read 留在 unknown ⇒ 判词是 unknown ⇒ 走的是 no-op 分支，
	// 断言会「通过」而什么也没测。夹具自证那一关就是为拦住这个写的。
	neg := SemanticProbeResult{
		Carry: ModalityLevelAccepted, Read: ModalityLevelNegative,
		HTTPStatus: 200, Score: 0, Answer: "",
	}
	// ⚠ 必须写**连续 modalityVerifyStreakGoal 次**：persistRow 存进
	// read_level 的是 applyStreak 的结果，不是 res.Read —— 一次负读只把
	// neg_streak 加一，level 留在上一档（unknown）。这是刻意的防抖设计
	// （别被一次抖动带偏），所以夹具必须自己把连续次数攒够，否则判词是
	// unknown，下面断言会「通过」而测的是 no-op 分支。
	//
	// 还要**每写一次就回读一次** streak：applyStreak 读的是 target 里的
	// ExistingRead/PosStreak/NegStreak，而 persistRow 不会回填它。生产里
	// 是 dueTargets 每轮重扫才把新 streak 带进下一轮；夹具若一直用同一个
	// 零值 target，写两次算出来完全一样，neg_streak 永远停在 1。
	for i := 0; i < modalityVerifyStreakGoal; i++ {
		if err := v.persistRow(ctx, target, neg); err != nil {
			t.Fatalf("persist negative #%d: %v", i+1, err)
		}
		if err := pool.QueryRow(ctx, `SELECT COALESCE(read_level,''), read_pos_streak, read_neg_streak
			FROM public.model_modality_verification
			WHERE canonical_name='m-mm' AND modality='vision'`).
			Scan(&target.ExistingRead, &target.PosStreak, &target.NegStreak); err != nil {
			t.Fatalf("re-read streak after #%d: %v", i+1, err)
		}
	}
	// 量具自证：判词必须真的是 negative，否则下面测的是 unknown 分支。
	var verdict string
	if err := pool.QueryRow(ctx, `SELECT verdict FROM public.v_model_modality_verdict
		WHERE canonical_name='m-mm' AND modality='vision'`).Scan(&verdict); err != nil {
		t.Fatalf("read verdict: %v", err)
	}
	if verdict != "negative" {
		t.Fatalf("verdict=%q, want negative — the fixture did not produce a clean negative, so "+
			"the assertion below would be measuring the unknown path instead", verdict)
	}
	if err := v.rollupVerdict(ctx, target); err != nil {
		t.Fatalf("rollup: %v", err)
	}
	var mod, src string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(modality,''), COALESCE(modality_source,'')
		FROM public.models_canonical WHERE canonical_name='m-mm'`).Scan(&mod, &src); err != nil {
		t.Fatalf("read m-mm: %v", err)
	}
	if mod != "multimodal" {
		t.Errorf("a model stored as multimodal was rewritten to modality=%q (source=%q) on the "+
			"strength of a vision-only negative. Its audio leg was never probed — the worker cannot "+
			"probe audio at all — so \"no confirmed modality\" does not mean \"text only\", it means "+
			"\"nothing but vision was tested\". Downgrading here makes a working audio model "+
			"disappear from the audio/multimodal candidate filter.", mod, src)
	}

	// ---- 第二个样本：已探 2 个模态，但负证据只覆盖 1 个 ----
	//
	// 这条单独钉 `negativeModalities < probedModalities` 那半段：audio 只写了一次
	// （未到 streak 门槛）⇒ 判词 unknown ⇒ 「有一条腿还没判」就不该降级。
	// 没有它，删掉那半段条件这个测试依然全绿。
	var id2 int64
	if err := pool.QueryRow(ctx, `SELECT id FROM public.models_canonical
		WHERE canonical_name='m-mm2'`).Scan(&id2); err != nil {
		t.Fatalf("read id of m-mm2: %v", err)
	}
	visionT := modalityVerifyTarget{
		CredentialID: 1, CanonicalID: id2, CanonicalName: "m-mm2",
		RawModel: "m-mm2", OutboundModel: "m-mm2", Modality: "vision",
		StoredModality: "multimodal",
	}
	audioT := visionT
	audioT.Modality = "audio"

	writeStreak := func(tgt modalityVerifyTarget, res SemanticProbeResult) {
		t.Helper()
		for i := 0; i < modalityVerifyStreakGoal; i++ {
			if err := v.persistRow(ctx, tgt, res); err != nil {
				t.Fatalf("persist %s negative #%d: %v", tgt.Modality, i+1, err)
			}
			if err := pool.QueryRow(ctx, `SELECT COALESCE(read_level,''), read_pos_streak, read_neg_streak
				FROM public.model_modality_verification
				WHERE canonical_name=$1 AND modality=$2`, tgt.CanonicalName, tgt.Modality).
				Scan(&tgt.ExistingRead, &tgt.PosStreak, &tgt.NegStreak); err != nil {
				t.Fatalf("re-read streak: %v", err)
			}
		}
	}
	writeStreak(visionT, neg)
	// audio 只写一次 ⇒ 判词停在 unknown（streak 未满）
	if err := v.persistRow(ctx, audioT, neg); err != nil {
		t.Fatalf("persist audio once: %v", err)
	}

	var probed, negMods int
	if err := pool.QueryRow(ctx, `SELECT count(DISTINCT modality),
		count(DISTINCT modality) FILTER (WHERE verdict='negative')
		FROM public.v_model_modality_verdict WHERE canonical_name='m-mm2'`).
		Scan(&probed, &negMods); err != nil {
		t.Fatalf("read coverage: %v", err)
	}
	if probed != 2 || negMods != 1 {
		t.Fatalf("m-mm2 coverage is probed=%d negative=%d, want 2/1 — the fixture is supposed to be "+
			"the only sample that separates \"how many modalities were probed\" from \"how many are "+
			"negatively judged\"; without it the second half of the guard is untested", probed, negMods)
	}
	if err := v.rollupVerdict(ctx, visionT); err != nil {
		t.Fatalf("rollup m-mm2: %v", err)
	}
	var mod2, src2 string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(modality,''), COALESCE(modality_source,'')
		FROM public.models_canonical WHERE canonical_name='m-mm2'`).Scan(&mod2, &src2); err != nil {
		t.Fatalf("read m-mm2: %v", err)
	}
	if mod2 != "multimodal" {
		t.Errorf("m-mm2 was rewritten to modality=%q (source=%q) although its audio leg is still "+
			"UNKNOWN, not negative — \"probed 2 modalities\" is not \"judged both legs\". A model "+
			"whose audio verdict has not settled yet must keep its label.", mod2, src2)
	}
}
