package hostedtask

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 矩阵 B/D/G/H 的库级验证：幂等创建/重放/异体冲突、CAS 终态抢占（0 行语义）、
// 事件 seq 唯一、回调认领、召回（742 recalled 事件 + EventID 幂等键回调重置）。
// TEST_PG_URL 未设置时跳过（不得记 PASS —— 验收矩阵 D 纪律）。
// 仅指一次性库：迁移 711+742 up 在测试前应用，down 在测试后回滚。
func TestStoreAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL not set; store integration requires a live PostgreSQL (convention: tests/integration gating)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TEST_PG_URL: %v", err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	upSQL, err := os.ReadFile("../../sql/migrations/startup/711_hosted_tasks.sql")
	if err != nil {
		t.Fatal(err)
	}
	up742SQL, err := os.ReadFile("../../sql/migrations/startup/742_hosted_task_recalled_event.sql")
	if err != nil {
		t.Fatal(err)
	}
	down742SQL, err := os.ReadFile("../../sql/migrations/startup/742_hosted_task_recalled_event.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downSQL, err := os.ReadFile("../../sql/migrations/startup/711_hosted_tasks.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, string(upSQL)); err != nil {
		t.Fatalf("apply 711: %v", err)
	}
	if _, err := admin.Exec(ctx, string(up742SQL)); err != nil {
		t.Fatalf("apply 742: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), string(down742SQL))
		_, _ = admin.Exec(context.Background(), string(downSQL))
		_ = admin.Close(context.Background())
	})

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	store := NewStore(pool)

	mkInput := func(idem string) CreateInput {
		return CreateInput{
			TenantID: "tenant-A", APIKeyID: 1, Goal: "goal-1", DoneWhen: "done-1",
			Context:        map[string]any{"summary": "s"},
			Environment:    map[string]any{"cwd": "/srv/ws1"},
			CallbackURLEnc: "v1:k:enc", CallbackSecEnc: "v1:k:enc2",
			IdempotencyKey: idem, RequestHash: "hash-" + idem,
			Deadline: time.Now().UTC().Add(time.Hour),
		}
	}

	// ── 幂等创建 / 同键同体重放 / 同键异体 409 ─────────────────────────
	task, created, err := store.CreateTask(ctx, mkInput("idem-0001"))
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	if task.Status != StatusDelegated || task.GwSessionID != "gw_"+task.ID {
		t.Errorf("task defaults wrong: %+v", task)
	}
	replay, created2, err := store.CreateTask(ctx, mkInput("idem-0001"))
	if err != nil || created2 {
		t.Fatalf("replay: created=%v err=%v", created2, err)
	}
	if replay.ID != task.ID {
		t.Errorf("replay returned different task: %s vs %s", replay.ID, task.ID)
	}
	bad := mkInput("idem-0001")
	bad.RequestHash = "different"
	if _, _, err := store.CreateTask(ctx, bad); !errors.Is(err, ErrIdempotencyConflict) {
		t.Errorf("different body = %v, want ErrIdempotencyConflict", err)
	}

	// accepted 事件已入时间线。
	events, err := store.ListEvents(ctx, "tenant-A", task.ID, 10)
	if err != nil || len(events) != 1 || events[0].Type != EventAccepted {
		t.Fatalf("events after create: %+v err=%v", events, err)
	}

	// ── dispatch 投影 ───────────────────────────────────────────────
	due, err := store.ListDispatchDue(ctx, time.Now().UTC(), 0, 10)
	if err != nil || len(due) == 0 {
		t.Fatalf("dispatch due scan: n=%d err=%v", len(due), err)
	}
	if ok, err := store.BeginDispatch(ctx, task.ID); err != nil || !ok {
		t.Fatalf("begin dispatch: ok=%v err=%v", ok, err)
	}
	if err := store.RecordDispatchSuccess(ctx, task.ID, "cmd-1", "run-1", "gw-hosted-"+task.ID+"-a1"); err != nil {
		t.Fatalf("record dispatch: %v", err)
	}
	got, err := store.GetTask(ctx, "tenant-A", task.ID)
	if err != nil || got.Status != StatusRunning || got.AccCommandID != "cmd-1" || got.AccRunID != "run-1" {
		t.Fatalf("after dispatch: %+v err=%v", got, err)
	}

	// 跨租户统一 ErrNotFound（404 语义）。
	if _, err := store.GetTask(ctx, "tenant-B", task.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-tenant get = %v, want ErrNotFound", err)
	}

	// ── 终态 CAS（矩阵 H：迟到回写 0 行）──────────────────────────────
	settled, seq, err := store.SettleTask(ctx, task.ID, SettleInput{
		To: StatusCompleted, EventType: EventCompleted,
		Result: map[string]any{"summary": "ok", "outcome": "completed"},
	})
	if err != nil || !settled || seq != 3 { // accepted + running + completed
		t.Fatalf("settle: settled=%v seq=%d err=%v", settled, seq, err)
	}
	// 迟到的第二个终态（如 cancelled）必须被 CAS 拒（0 行 → settled=false）。
	settled2, _, err := store.SettleTask(ctx, task.ID, SettleInput{
		To: StatusCancelled, EventType: EventCancelled,
	})
	if err != nil || settled2 {
		t.Errorf("late write on terminal: settled=%v err=%v (want false/nil)", settled2, err)
	}
	after, err := store.GetTask(ctx, "tenant-A", task.ID)
	if err != nil || after.Status != StatusCompleted || after.ResultVersion != 1 {
		t.Fatalf("after late write: status=%s rv=%d err=%v", after.Status, after.ResultVersion, err)
	}

	// 回调已随终态入队。
	jobs, err := store.ClaimDueCallbacks(ctx, time.Now().UTC(), 10)
	if err != nil || len(jobs) == 0 {
		t.Fatalf("claim callbacks: n=%d err=%v", len(jobs), err)
	}
	var job CallbackJob
	for _, j := range jobs {
		if j.TaskID == task.ID {
			job = j
		}
	}
	if job.EventID != EventID(task.ID, seq) {
		t.Errorf("callback event_id = %s, want %s", job.EventID, EventID(task.ID, seq))
	}
	// 4xx → 不重试直 DLQ。
	if err := store.RecordCallbackOutcome(ctx, job, CallbackOutcome{
		Delivered: false, Retryable: false, StatusCode: 410, Err: "gone",
	}); err != nil {
		t.Fatalf("record 4xx: %v", err)
	}
	jobs2, _ := store.ClaimDueCallbacks(ctx, time.Now().UTC(), 10)
	for _, j := range jobs2 {
		if j.TaskID == task.ID {
			t.Error("dlq'd job must not be re-claimed")
		}
	}

	// ── cancel 终态抢占 + 事件 + 回调入队（R66 矩阵 H/E：终态后补投递） ──
	task2, created, err := store.CreateTask(ctx, mkInput("idem-0002"))
	if err != nil || !created {
		t.Fatalf("create 2: %v", err)
	}
	if _, won, err := store.CancelTask(ctx, "tenant-A", task2.ID); err != nil || !won {
		t.Fatalf("first cancel: won=%v err=%v", won, err)
	}
	// 事件序列：accepted → cancel_requested(seq=2) → cancelled(seq=3)。
	evs2, err := store.ListEvents(ctx, "tenant-A", task2.ID, 10)
	if err != nil || len(evs2) != 3 {
		t.Fatalf("events after cancel: n=%d err=%v", len(evs2), err)
	}
	cancelledSeq := evs2[2].Seq
	if evs2[1].Type != EventCancelRequested || evs2[2].Type != EventCancelled {
		t.Fatalf("event order wrong: %+v", evs2)
	}
	// 回调台账已按 EventID(taskID, cancelledSeq) 幂等键置 pending
	// （§6.1 与 SettleTask 同款；hosted_task_callbacks.event_id 由 §6.1
	// 接收方按 event_id 幂等重投——cancel 终态后回调不再静默）。
	var cancelCbEventID, cancelCbStatus, cancelCbURL string
	if err := admin.QueryRow(ctx, `
		SELECT event_id, status, url_enc FROM hosted_task_callbacks WHERE task_id = $1
	`, task2.ID).Scan(&cancelCbEventID, &cancelCbStatus, &cancelCbURL); err != nil {
		t.Fatalf("load callback row: %v", err)
	}
	if want := EventID(task2.ID, cancelledSeq); cancelCbEventID != want || cancelCbStatus != "pending" || cancelCbURL == "" {
		t.Errorf("callback = %s/%s/url=%q, want %s/pending/non-empty", cancelCbEventID, cancelCbStatus, cancelCbURL, want)
	}
	// 第二次 CancelTask won=false → handler 409（终态 sticky）。
	gotTask, won2, err := store.CancelTask(ctx, "tenant-A", task2.ID)
	if err != nil || won2 || gotTask.Status != StatusCancelled {
		t.Errorf("second cancel: won=%v status=%s err=%v", won2, gotTask.Status, err)
	}
	// 二次取消不应再写台账（won=false 时 store 提前返回，未走 cancelRowInTx）；
	// event_id 仍等于首次入队键=EventID(taskID, cancelledSeq）。
	var cancelCbEventID2 string
	if err := admin.QueryRow(ctx, `
		SELECT event_id FROM hosted_task_callbacks WHERE task_id = $1
	`, task2.ID).Scan(&cancelCbEventID2); err != nil {
		t.Fatalf("reload callback row: %v", err)
	}
	if cancelCbEventID2 != EventID(task2.ID, cancelledSeq) {
		t.Errorf("callback event_id after 2nd cancel = %s, want %s", cancelCbEventID2, EventID(task2.ID, cancelledSeq))
	}

	// ── 召回（§3.3 ④ 轻量路径：742 recalled 事件 + EventID 幂等回调）──
	// 3a. 非终态召回：cancelled 抢占 + recalled 事件 + 回调重置。
	task3, created, err := store.CreateTask(ctx, mkInput("idem-0003"))
	if err != nil || !created {
		t.Fatalf("create 3: %v", err)
	}
	out, err := store.RecallTask(ctx, "tenant-A", task3.ID)
	if err != nil {
		t.Fatalf("recall running: %v", err)
	}
	if out.RecallStatus != RecallCancelled || out.Task.Status != StatusCancelled {
		t.Errorf("recall running: status=%s task=%s", out.RecallStatus, out.Task.Status)
	}
	if out.Packet.Goal != "goal-1" || out.Packet.NextOwner != "recall_caller" {
		t.Errorf("packet wrong: %+v", out.Packet)
	}
	// 事件序列：accepted → cancel_requested → cancelled → recalled。
	evs, err := store.ListEvents(ctx, "tenant-A", task3.ID, 50)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(evs) != 4 || evs[3].Type != EventRecalled || evs[3].Seq != out.EventSeq {
		t.Fatalf("events wrong: n=%d last=%+v seq=%d", len(evs), evs[len(evs)-1], out.EventSeq)
	}
	// 回调台账已按 EventID(taskID, seq) 幂等键重置为 pending（§6.1 复用）。
	var cbEventID, cbStatus string
	if err := admin.QueryRow(ctx, `
		SELECT event_id, status FROM hosted_task_callbacks WHERE task_id = $1
	`, task3.ID).Scan(&cbEventID, &cbStatus); err != nil {
		t.Fatalf("load callback row: %v", err)
	}
	if want := EventID(task3.ID, out.EventSeq); cbEventID != want || cbStatus != "pending" {
		t.Errorf("callback = %s/%s, want %s/pending", cbEventID, cbStatus, want)
	}

	// 3b. 终态再召回：already_terminal，只补 recalled 事件。
	out2, err := store.RecallTask(ctx, "tenant-A", task3.ID)
	if err != nil {
		t.Fatalf("recall terminal: %v", err)
	}
	if out2.RecallStatus != RecallAlreadyTerminal || out2.EventSeq != out.EventSeq+1 {
		t.Errorf("recall terminal: status=%s seq=%d", out2.RecallStatus, out2.EventSeq)
	}

	// 3c. missing/跨租户 → ErrNotFound。
	if _, err := store.RecallTask(ctx, "tenant-B", task3.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-tenant recall err=%v, want ErrNotFound", err)
	}
}
