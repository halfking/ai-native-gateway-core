package bg

// 「定时核实要进自检任务」这条目标的判据。
//
// # 这条判据钉的是什么
//
// 目标原文：「能自动对未曾标注核实过的模型定时进行核实。**这个需要加入到自检
// 任务中**」。前半句一直满足（bg.ModalityVerification 是定时循环，
// cmd/gateway/main.go 两处 authoritative 块都有 .Run(ctx)），后半句在 835 之前
// **没有兑现**，四条独立证据见 835 迁移文件头。
//
// 本判据钉的是兑现的**那一半**：VerifyOnce 每做一次核实尝试，就在
// system_probe_runs 留一行。没有它，「多模态核实有没有在跑」这个问题在运维侧
// 只能翻日志，而日志活不过一次重启 —— 语义探针经 internal/upstreamurl 直连
// 上游，不产生 request_logs，进程内 m.attempts 也随重启清零。
//
// # 为什么观测的是 system_probe_runs 而不是返回值
//
// 「核实成功了」由 VerifyOnce 的返回值 claimed 与 persisted 表达。若只断言
// 返回值，这条判据无法区分「结论落了库」与「台账也落了行」—— 而后者正是本
// 次要钉的东西。所以下面一律**独立 SELECT 台账**来数行、验字段，不量
// recordProbeLedger 自己的返回。
//
// # 承重的三段
//
//	A **量具自证（加 835 之前）**：台账表按 sql/objects/ 的生产列定义建好后，
//	  直接插一行 task_type='modality_verify' 必须**失败**（CHECK 拒绝）。
//	  少了这一段，后面所有「写进去了」的断言都可能建立在一张**本来就没有那条
//	  CHECK** 的表上——那样 835 就成了可有可无的装饰，而判据照样全绿。
//	B **解耦**：在还没有 835 的表上跑一次完整核实，结论仍然要写成
//	  （written==1）而台账行数仍为 0。反向的失败（为了记账把核实循环弄停）
//	  是更危险的形态，账本是出口不是依赖。
//	C **四个臂的判词**：成功 / 准入拒绝 / 出错 / 日预算用尽，四种结局的
//	  status 与 skip_reason 都不同。只测「成功」一臂的话，把 status 恒写成
//	  'success' 也能过——那正是把「跳过」伪装成「跑过」。
//
// 依赖真库：没有 TEST_DATABASE_URL 时跳过；夹具表已存在时也跳过（它要建表）。

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/internal/schemaobj"
)

func TestVerifyOnceLeavesOneLedgerRowPerAttemptInTheSelfCheckLedger(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — this needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name='system_probe_runs'`).Scan(&existing); err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	if existing > 0 {
		t.Skip("the fixture table public.system_probe_runs already exists — this test drops it")
	}
	defer func() {
		_, _ = pool.Exec(ctx, `
			DROP TABLE IF EXISTS public.system_probe_runs CASCADE;
			DROP TABLE IF EXISTS public.system_probe_runs_default CASCADE;`)
	}()

	// 夹具表照抄生产列定义（sql/objects/tables/system_probe_runs.sql，pg_dump 原样）。
	// ⚠ 那份拷贝是**冻结快照**：它既不含 826 的 baseline_* 列也不含 833 的非负
	//   约束，所以它的 task_type 仍是迁移前的 6 值。下面正好用这一点做 A 段。
	if _, err := pool.Exec(ctx,
		schemaobj.Table(t, "../sql/objects/tables/system_probe_runs.sql")); err != nil {
		t.Fatalf("fixture table: %v", err)
	}
	// 生产里 system_probe_runs 是按 created_at RANGE 分区的，DEFAULT 叶子就是
	// 承接全部写入的那张表。列定义由父表继承，形状与生产一致。
	if _, err := pool.Exec(ctx,
		`CREATE TABLE public.system_probe_runs_default PARTITION OF public.system_probe_runs DEFAULT;`); err != nil {
		t.Fatalf("fixture default partition: %v", err)
	}
	// ⚠ sql/objects/ 那份拷贝的 id 是裸 `bigint NOT NULL`（无默认值），而**现网**
	//   是 `bigint GENERATED ALWAYS AS IDENTITY`（2026-10-06 真机 pg_dump 核对，
	//   主键为 (id, created_at)）。照抄快照会让任何不显式给 id 的 INSERT 撞
	//   23502，而生产不会 —— 那是夹具在测自己。这里补齐成生产形状。
	//   快照漂移本身不在本判据的修法范围内（那份拷贝是冻结快照，见上），只在此
	//   记录，避免下一个人以为 objects/ 与现网一致。
	if _, err := pool.Exec(ctx, `
		ALTER TABLE public.system_probe_runs
			ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY;`); err != nil {
		t.Fatalf("align the fixture id column with production: %v", err)
	}

	// probeModel 是本判据自己用来验「835 生效了」的那一行。它不是任何一次核实
	// 尝试，所以**刻意**不计入下面四臂的计数 —— 否则断言会把测试夹具当成
	// 被测行为（这正是「量具与被测量必须分开」在计数上的形态）。
	const probeModel = "zz-probe-835"

	countRows := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM public.system_probe_runs
			 WHERE task_type='modality_verify' AND raw_model <> 'zz-probe-835'`).Scan(&n); err != nil {
			t.Fatalf("count ledger rows: %v", err)
		}
		return n
	}

	// -----------------------------------------------------------------------
	// A —— 量具自证：835 之前，这条 CHECK 必须挡住 modality_verify。
	//
	// 这一段是整条判据的牙齿。没有它，一张「压根没有那条 CHECK」的表也能让
	// C 段全绿，835 就成了纯装饰。
	// -----------------------------------------------------------------------
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.system_probe_runs
			(task_id, task_type, automaticity, credential_id, raw_model,
			 source, worker_id, status, started_at, finished_at)
		VALUES (0, 'modality_verify', 'automatic', 1, 'zz-probe-835', 's', 'w', 'success', now(), now())`); err == nil {
		t.Fatal("task_type='modality_verify' was accepted BEFORE migration 835 — the fixture " +
			"does not carry the production CHECK, so every assertion below would be vacuous")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("expected a 23514 check-constraint violation, got %v", err)
		}
	}

	// -----------------------------------------------------------------------
	// B —— 解耦：台账写不进去，核实结论照样要写成。
	// -----------------------------------------------------------------------
	newVerifier := func(dailyBudget int, probes []time.Time) *ModalityVerification {
		return &ModalityVerification{
			db: pool, dailyBudget: dailyBudget, probes: probes,
			batchLimit: 10,
			attempts:   map[string]time.Time{},
			decryptFn:  func(modalityVerifyTarget) (string, error) { return "sk-test", nil },
			persist:    func(context.Context, modalityVerifyTarget, SemanticProbeResult) error { return nil },
			rollup:     func(context.Context, modalityVerifyTarget) error { return nil },
			probe: func(context.Context, modalityVerifyTarget, string) SemanticProbeResult {
				return SemanticProbeResult{Carry: ModalityLevelAccepted, Read: ModalityLevelConfirmed}
			},
		}
	}
	admitted := func(raw string) modalityVerifyTarget {
		return modalityVerifyTarget{
			CredentialID: 4242, CanonicalID: 1, CanonicalName: "m-ledger",
			RawModel: raw, Modality: "vision",
			CredentialStatus: "active", LifecycleStatus: "active",
			BindingAvailable: true, ProviderEnabled: true,
		}
	}

	v := newVerifier(0, nil)
	v.scan = func(context.Context) ([]modalityVerifyTarget, error) {
		return []modalityVerifyTarget{admitted("raw-decoupled")}, nil
	}
	written, err := v.VerifyOnce(ctx)
	if err != nil {
		t.Fatalf("VerifyOnce before 835: %v", err)
	}
	if written != 1 {
		t.Fatalf("written=%d, want 1 — the verdict must land even when the ledger write fails; "+
			"making the ledger a dependency of the verdict would let a bookkeeping problem "+
			"stop the verification loop", written)
	}
	if n := countRows(); n != 0 {
		t.Fatalf("%d ledger row(s) exist before migration 835 — the CHECK is not doing its job, "+
			"so C below would pass for the wrong reason", n)
	}

	// -----------------------------------------------------------------------
	// 应用 835。
	// -----------------------------------------------------------------------
	mig, err := os.ReadFile("../sql/migrations/startup/835_modality_verify_probe_ledger.sql")
	if err != nil {
		t.Fatalf("read 835: %v", err)
	}
	if _, err := pool.Exec(ctx, string(mig)); err != nil {
		t.Fatalf("apply 835: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.system_probe_runs
			(task_id, task_type, automaticity, credential_id, raw_model,
			 source, worker_id, status, started_at, finished_at)
		VALUES (0, 'modality_verify', 'automatic', 1, 'zz-probe-835', 's', 'w', 'success', now(), now())`); err != nil {
		t.Fatalf("task_type='modality_verify' still rejected after 835: %v", err)
	}

	// -----------------------------------------------------------------------
	// C —— 四个臂。
	// -----------------------------------------------------------------------

	// C1 成功。
	vOK := newVerifier(0, nil)
	vOK.scan = func(context.Context) ([]modalityVerifyTarget, error) {
		return []modalityVerifyTarget{admitted("raw-ok")}, nil
	}
	if _, err := vOK.VerifyOnce(ctx); err != nil {
		t.Fatalf("C1 VerifyOnce: %v", err)
	}

	// C2 准入闸门拒绝：不是错误，但必须在台账里说清**为什么**没探。
	//    措辞取自 modalityVerifyAdmit（纯函数），不靠本判据复述一遍。
	rejected := admitted("raw-rejected")
	rejected.CredentialStatus = "retired"
	_, wantSkipWhy := modalityVerifyAdmit(rejected)
	if wantSkipWhy == "" {
		t.Fatal("a retired credential is admitted by the gate — arm C2 is not measuring what it claims")
	}
	vSkip := newVerifier(0, nil)
	vSkip.scan = func(context.Context) ([]modalityVerifyTarget, error) {
		return []modalityVerifyTarget{rejected}, nil
	}
	if _, err := vSkip.VerifyOnce(ctx); err != nil {
		t.Fatalf("C2 VerifyOnce: %v", err)
	}

	// C3 出错（解密失败）。
	vErr := newVerifier(0, nil)
	vErr.decryptFn = func(modalityVerifyTarget) (string, error) { return "", errors.New("cannot decrypt fixture ciphertext") }
	vErr.scan = func(context.Context) ([]modalityVerifyTarget, error) {
		return []modalityVerifyTarget{admitted("raw-err")}, nil
	}
	if _, err := vErr.VerifyOnce(ctx); err != nil {
		t.Fatalf("C3 VerifyOnce: %v", err)
	}

	// C4 过了闸门却没出网 = 日预算用尽。这一臂若不测，recordProbeLedger 里的
	//    `case !didProbe` 分支就是恒真的死代码（基线态永远走不到）。
	vBudget := newVerifier(1, []time.Time{time.Now()}) // budget=1，已用掉 1 次
	vBudget.scan = func(context.Context) ([]modalityVerifyTarget, error) {
		return []modalityVerifyTarget{admitted("raw-budget")}, nil
	}
	if _, err := vBudget.VerifyOnce(ctx); err != nil {
		t.Fatalf("C4 VerifyOnce: %v", err)
	}

	// ---- 目的地侧断言：直接读台账，不量 recordProbeLedger 的返回 ----
	type row struct {
		status, skipReason, errDetail, source, workerID, taskType string
		taskID                                                    int64
		credentialID                                              int
		rawModel                                                  string
	}
	rows := map[string]row{}
	scan, err := pool.Query(ctx, `
		SELECT task_id, task_type, credential_id, raw_model, source, worker_id,
		       status, COALESCE(skip_reason,''), COALESCE(err_detail,'')
		  FROM public.system_probe_runs
		 WHERE task_type='modality_verify' AND raw_model <> 'zz-probe-835'`)
	if err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	for scan.Next() {
		var r row
		if err := scan.Scan(&r.taskID, &r.taskType, &r.credentialID, &r.rawModel,
			&r.source, &r.workerID, &r.status, &r.skipReason, &r.errDetail); err != nil {
			scan.Close()
			t.Fatalf("scan: %v", err)
		}
		rows[r.rawModel] = r
	}
	scan.Close()
	if err := scan.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	if len(rows) != 4 {
		keys := make([]string, 0, len(rows))
		for k := range rows {
			keys = append(keys, k)
		}
		t.Fatalf("the ledger holds %d modality_verify row(s) %v, want exactly 4 (one per attempt) — "+
			"the decoupled run in B must not have left a row, and each arm must leave exactly one",
			len(rows), keys)
	}

	for _, name := range []string{"raw-ok", "raw-rejected", "raw-err", "raw-budget"} {
		r, ok := rows[name]
		if !ok {
			t.Errorf("no ledger row for %s", name)
			continue
		}
		// task_id=0 是**刻意**的：多模态核实不是 credential_probe_queue 的任务，
		// 合成一个哈希任务号会在按 task_id 分组的看板上伪装成别的任务。
		if r.taskID != 0 {
			t.Errorf("%s: task_id=%d, want 0 — these rows are not queue tasks and must not "+
				"be grouped as if they were", name, r.taskID)
		}
		if r.source != "modality_verification" || r.workerID != "modality-verification-worker" {
			t.Errorf("%s: source=%q worker_id=%q, want modality_verification / "+
				"modality-verification-worker — the ledger must be able to tell whose run this was",
				name, r.source, r.workerID)
		}
		if r.credentialID != 4242 {
			t.Errorf("%s: credential_id=%d, want 4242 — the row must point at a concrete "+
				"(credential, model), not a sentinel", name, r.credentialID)
		}
	}

	if got := rows["raw-ok"]; got.status != "success" {
		t.Errorf("raw-ok: status=%q, want success", got.status)
	} else if got.skipReason != "" || got.errDetail != "" {
		t.Errorf("raw-ok: skip_reason=%q err_detail=%q, want both empty — a success row that "+
			"carries a reason reads as a skip on the dashboard", got.skipReason, got.errDetail)
	}

	if got := rows["raw-rejected"]; got.status != "skipped" {
		t.Errorf("raw-rejected: status=%q, want skipped", got.status)
	} else if !strings.Contains(got.skipReason, wantSkipWhy) {
		t.Errorf("raw-rejected: skip_reason=%q does not carry the gate's own reason %q — "+
			"the operator cannot act on \"it was skipped\" without knowing which of the nine gates",
			got.skipReason, wantSkipWhy)
	}

	if got := rows["raw-err"]; got.status != "failed" {
		t.Errorf("raw-err: status=%q, want failed", got.status)
	} else if got.errDetail == "" {
		t.Errorf("raw-err: err_detail is empty — the whole point of a failed row is what failed")
	} else if !strings.Contains(got.errDetail, "decrypt") {
		t.Errorf("raw-err: err_detail=%q does not mention the underlying error", got.errDetail)
	}

	if got := rows["raw-budget"]; got.status != "skipped" {
		t.Errorf("raw-budget: status=%q, want skipped", got.status)
	} else if !strings.Contains(got.skipReason, "budget") {
		t.Errorf("raw-budget: skip_reason=%q does not say the daily probe budget was the cause — "+
			"an admitted target that never egressed has exactly one explanation here", got.skipReason)
	}

	// -----------------------------------------------------------------------
	// 阴性对照：835 放宽了词表，但没有把它变成摆设。
	// -----------------------------------------------------------------------
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.system_probe_runs
			(task_id, task_type, automaticity, credential_id, raw_model,
			 source, worker_id, status, started_at, finished_at)
		VALUES (0, 'chat_tool', 'automatic', 1, 'm', 's', 'w', 'success', now(), now())`); err != nil {
		t.Errorf("a pre-existing task_type was rejected after 835: %v — the migration must widen, "+
			"not replace", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.system_probe_runs
			(task_id, task_type, automaticity, credential_id, raw_model,
			 source, worker_id, status, started_at, finished_at)
		VALUES (0, 'modality_verifi', 'automatic', 1, 'm', 's', 'w', 'success', now(), now())`); err == nil {
		t.Error("a misspelled task_type was accepted — 835 widened the vocabulary without " +
			"constraining it, so the ledger would silently accumulate typos as distinct kinds")
	}
}
