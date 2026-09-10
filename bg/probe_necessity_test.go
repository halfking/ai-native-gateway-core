package bg

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// staticEvidenceSource is a NodeHealthEvidenceSource stub returning a fixed
// evidence map (or error) and recording the last query identity so tests can
// assert the gate queried the right tenant/credential/model set.
type staticEvidenceSource struct {
	mu         sync.Mutex
	evidence   map[string]NodeHealthEvidence
	err        error
	lastTenant string
	lastModels []string
}

func (s *staticEvidenceSource) NodeHealthEvidence(_ context.Context, tenant string, _ int, models []string) (map[string]NodeHealthEvidence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastTenant = tenant
	s.lastModels = append([]string(nil), models...)
	if s.err != nil {
		return nil, s.err
	}
	return s.evidence, nil
}

type removalCapture struct {
	mu      sync.Mutex
	removed bool
	task    ProbeQueueTask
	reason  string
}

func (c *removalCapture) remove(_ context.Context, task ProbeQueueTask, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removed = true
	c.task = task
	c.reason = reason
}

func (c *removalCapture) single(t *testing.T) (ProbeQueueTask, string) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.removed {
		t.Fatal("skipped probe was not removed from queue/database")
	}
	return c.task, c.reason
}

// wasCalled is a non-panicking variant for negative assertions.
func (c *removalCapture) wasCalled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.removed
}

// necessityTask is an automatic node_probe task as Claim produces it.
func necessityTask() ProbeQueueTask {
	return ProbeQueueTask{
		ID:           77,
		CredentialID: 42,
		TenantID:     "default",
		RawModel:     "model-a",
		Command:      "node_probe",
		Automatic:    true,
		Attempt:      1,
		MaxAttempts:  7,
	}
}

// newNecessityTestService wires a ProbeService with the necessity gate seams
// and round recorders. roundsRun counts direct-round invocations so tests can
// assert whether the probe body executed.
func newNecessityTestService(src NodeHealthEvidenceSource, siblings []string, lastRun *nodeProbeRunSummary) (*ProbeService, *removalCapture, *int) {
	rounds := 0
	service := &ProbeService{
		worker: &NodeProbeWorker{},
		directRoundFn: func(context.Context, int, string) nodeProbeRoundResult {
			rounds++
			return nodeProbeRoundResult{ok: true, providerID: 7}
		},
		gatewayRoundFn: func(context.Context, int, string) gatewayProbeResult {
			return gatewayProbeResult{round: nodeProbeRoundResult{ok: true}, pinned: true}
		},
		applyOutcomeFn:         func(context.Context, probeOutcome) {},
		automaticEligibilityFn: func(context.Context, ProbeQueueTask) (bool, error) { return true, nil },
		healthEvidence:         src,
		siblingModelsFn: func(context.Context, int, string) ([]string, error) {
			return siblings, nil
		},
		lastProbeRunFn: func(context.Context, int, string) (*nodeProbeRunSummary, error) {
			return lastRun, nil
		},
	}
	removal := &removalCapture{}
	service.removeSkippedFn = removal.remove
	return service, removal, &rounds
}

func TestProbeNecessitySkipsWhenAllCredentialNodesHealthy(t *testing.T) {
	// 条件①: 当前模型 + 同一凭据下所有其它节点在 redis 缓存中都正常
	//（sibling-b 无缓存条目 = 没有请求 = 正常）。
	src := &staticEvidenceSource{evidence: map[string]NodeHealthEvidence{
		"model-a": {Known: true, Healthy: true},
		"model-b": {Known: true, Healthy: true},
	}}
	service, removal, rounds := newNecessityTestService(src, []string{"model-b"}, nil)

	task := necessityTask()
	_, err := service.Run(context.Background(), task)
	if !errors.Is(err, ErrProbeNotNecessary) {
		t.Fatalf("err = %v, want ErrProbeNotNecessary", err)
	}
	if *rounds != 0 {
		t.Fatalf("probe executed %d round(s), want 0", *rounds)
	}
	removedTask, reason := removal.single(t)
	if removedTask.ID != task.ID {
		t.Fatalf("removed task id = %d, want %d", removedTask.ID, task.ID)
	}
	if !strings.HasPrefix(reason, SkipReasonAllNodesHealthy) {
		t.Fatalf("removal reason = %q, want prefix %q", reason, SkipReasonAllNodesHealthy)
	}
	// The evidence query must have covered the current model AND its siblings.
	joined := strings.Join(src.lastModels, ",")
	if !strings.Contains(joined, "model-a") || !strings.Contains(joined, "model-b") {
		t.Fatalf("evidence query = %v, want current model + siblings", src.lastModels)
	}
	if src.lastTenant != "default" {
		t.Fatalf("evidence tenant = %q, want task tenant", src.lastTenant)
	}
}

func TestProbeNecessitySkipsWhenSiblingsIdleOrUnknown(t *testing.T) {
	// sibling-b 没有任何缓存条目、sibling-c 状态不可判定（无请求）——都视为正常。
	src := &staticEvidenceSource{evidence: map[string]NodeHealthEvidence{
		"model-a": {Known: true, Healthy: true},
		"model-c": {Known: false},
	}}
	service, removal, rounds := newNecessityTestService(src, []string{"model-b", "model-c"}, nil)

	if _, err := service.Run(context.Background(), necessityTask()); !errors.Is(err, ErrProbeNotNecessary) {
		t.Fatalf("err = %v, want ErrProbeNotNecessary", err)
	}
	if *rounds != 0 {
		t.Fatal("probe executed despite all nodes healthy")
	}
	removal.single(t)
}

func TestProbeNecessityRunsWhenSiblingErrored(t *testing.T) {
	// 条件① 的否定: 同一凭据下存在出错节点 → 必须探测。
	src := &staticEvidenceSource{evidence: map[string]NodeHealthEvidence{
		"model-a":   {Known: true, Healthy: true},
		"model-bad": {Known: true, Healthy: false},
	}}
	service, removal, rounds := newNecessityTestService(src, []string{"model-bad"}, nil)

	result, err := service.Run(context.Background(), necessityTask())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ProbeQueueSuccess {
		t.Fatalf("result = %+v", result)
	}
	if *rounds != 1 {
		t.Fatalf("probe rounds = %d, want 1", *rounds)
	}
	if removal.wasCalled() {
		t.Fatal("probe was removed although a sibling node is errored")
	}
}

func TestProbeNecessityRunsWhenCurrentNodeErrored(t *testing.T) {
	src := &staticEvidenceSource{evidence: map[string]NodeHealthEvidence{
		"model-a": {Known: true, Healthy: false},
	}}
	service, removal, rounds := newNecessityTestService(src, nil, nil)

	if _, err := service.Run(context.Background(), necessityTask()); err != nil {
		t.Fatal(err)
	}
	if *rounds != 1 {
		t.Fatal("probe skipped although current node is errored")
	}
	if removal.wasCalled() {
		t.Fatal("probe was removed although current node is errored")
	}
}

func TestProbeNecessitySkipsWhenLastProbeHealthyAndNoErrorsSince(t *testing.T) {
	// 条件②: 上一探测周期成功，且此后没有错误请求（错误水位早于该探测）。
	// 放一个出错 sibling 让条件①不成立，确保跳过确实由条件②触发。
	probeDone := time.Now().Add(-30 * time.Minute)
	src := &staticEvidenceSource{evidence: map[string]NodeHealthEvidence{
		"model-a": {
			Known:              true,
			Healthy:            true,
			LastRequestAt:      probeDone.Add(2 * time.Minute),
			LastRequestErrorAt: probeDone.Add(-5 * time.Minute), // last error BEFORE the probe
		},
		"model-bad": {Known: true, Healthy: false},
	}}
	service, removal, rounds := newNecessityTestService(src, []string{"model-bad"}, &nodeProbeRunSummary{Success: true, CompletedAt: probeDone})

	_, err := service.Run(context.Background(), necessityTask())
	if !errors.Is(err, ErrProbeNotNecessary) {
		t.Fatalf("err = %v, want ErrProbeNotNecessary", err)
	}
	if *rounds != 0 {
		t.Fatal("probe executed despite healthy previous cycle")
	}
	_, reason := removal.single(t)
	if !strings.HasPrefix(reason, SkipReasonLastProbeHealthy) {
		t.Fatalf("removal reason = %q, want prefix %q", reason, SkipReasonLastProbeHealthy)
	}
}

func TestProbeNecessityRunsWhenErrorRequestAfterLastProbe(t *testing.T) {
	// 条件② 的否定: 上一探测成功之后又出现了错误请求 → 必须探测。
	// （出错 sibling 让条件①不成立，确保走到条件②的错误水位检查。）
	probeDone := time.Now().Add(-30 * time.Minute)
	src := &staticEvidenceSource{evidence: map[string]NodeHealthEvidence{
		"model-a": {
			Known:              true,
			Healthy:            true,
			LastRequestErrorAt: probeDone.Add(10 * time.Minute), // error AFTER the probe
		},
		"model-bad": {Known: true, Healthy: false},
	}}
	service, removal, rounds := newNecessityTestService(src, []string{"model-bad"}, &nodeProbeRunSummary{Success: true, CompletedAt: probeDone})

	if _, err := service.Run(context.Background(), necessityTask()); err != nil {
		t.Fatal(err)
	}
	if *rounds != 1 {
		t.Fatal("probe skipped although an error request followed the last probe")
	}
	if removal.wasCalled() {
		t.Fatal("probe was removed although errors occurred after the last probe")
	}
}

func TestProbeNecessityRunsWhenLastProbeFailed(t *testing.T) {
	// （出错 sibling 让条件①不成立，确保走到条件②的上一周期结果检查。）
	probeDone := time.Now().Add(-30 * time.Minute)
	src := &staticEvidenceSource{evidence: map[string]NodeHealthEvidence{
		"model-a":   {Known: true, Healthy: true, LastRequestErrorAt: probeDone.Add(-time.Hour)},
		"model-bad": {Known: true, Healthy: false},
	}}
	service, removal, rounds := newNecessityTestService(src, []string{"model-bad"}, &nodeProbeRunSummary{Success: false, CompletedAt: probeDone})

	if _, err := service.Run(context.Background(), necessityTask()); err != nil {
		t.Fatal(err)
	}
	if *rounds != 1 {
		t.Fatal("probe skipped although the previous probe cycle failed")
	}
	if removal.wasCalled() {
		t.Fatal("probe was removed although the previous cycle failed")
	}
}

func TestProbeNecessityRunsWhenNoRedisEvidenceForCurrentNode(t *testing.T) {
	// 条件② 的否定: 当前节点在 redis 中没有可判定的状态（TTL 过期 ≠ 没有错误），
	// 即使上一探测周期成功也不能证明"期间没有错误请求"→ 必须探测。
	// （同时放入一个出错 sibling 让条件①也不成立。）
	probeDone := time.Now().Add(-30 * time.Minute)
	src := &staticEvidenceSource{evidence: map[string]NodeHealthEvidence{
		"model-bad": {Known: true, Healthy: false},
	}}
	service, removal, rounds := newNecessityTestService(src, []string{"model-bad"}, &nodeProbeRunSummary{Success: true, CompletedAt: probeDone})

	if _, err := service.Run(context.Background(), necessityTask()); err != nil {
		t.Fatal(err)
	}
	if *rounds != 1 {
		t.Fatal("probe skipped without redis evidence for the current node")
	}
	if removal.wasCalled() {
		t.Fatal("probe was removed without redis evidence")
	}
}

func TestProbeNecessityFailsOpenOnEvidenceError(t *testing.T) {
	// 证据读取失败（redis 故障等）→ 探测照常执行，绝不因检查故障而漏探。
	src := &staticEvidenceSource{err: errors.New("redis unavailable")}
	service, removal, rounds := newNecessityTestService(src, []string{"model-b"}, nil)

	if _, err := service.Run(context.Background(), necessityTask()); err != nil {
		t.Fatal(err)
	}
	if *rounds != 1 {
		t.Fatal("probe skipped although evidence read failed")
	}
	if removal.wasCalled() {
		t.Fatal("probe was removed although evidence read failed")
	}
}

func TestProbeNecessityBypassedForManualTasks(t *testing.T) {
	// 手动（admin）任务不经过必要性检查——运维显式要求证据。
	src := &staticEvidenceSource{evidence: map[string]NodeHealthEvidence{
		"model-a": {Known: true, Healthy: true},
	}}
	service, removal, rounds := newNecessityTestService(src, nil, nil)

	task := necessityTask()
	task.Automatic = false
	if _, err := service.Run(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if *rounds != 1 {
		t.Fatal("manual probe was skipped by the necessity gate")
	}
	if removal.wasCalled() {
		t.Fatal("manual probe was removed by the necessity gate")
	}
}

func TestProbeNecessityDisabledWithoutEvidenceSource(t *testing.T) {
	// 未接线 URSM 证据源（旧部署/测试）→ 检查整体禁用，探测照常。
	service, removal, rounds := newNecessityTestService(nil, nil, nil)

	if _, err := service.Run(context.Background(), necessityTask()); err != nil {
		t.Fatal(err)
	}
	if *rounds != 1 {
		t.Fatal("probe skipped although the gate is not wired")
	}
	if removal.wasCalled() {
		t.Fatal("probe was removed although the gate is not wired")
	}
}

func TestProbeNecessityRunsWhenCurrentNodeHasNoRedisState(t *testing.T) {
	// 条件① 的守护分支（P1 修复回归）: 当前节点在 redis 中没有可判定的状态
	// （哈希 TTL 过期 ≠ 没有错误——glm-5.2 节点键过期停机形态）。此时不能跳过，
	// 否则恢复探测会被无限"跳过并删除"，凭据永远无法经探测恢复。
	src := &staticEvidenceSource{evidence: map[string]NodeHealthEvidence{
		// model-a 故意不放条目
		"model-b": {Known: true, Healthy: true},
	}}
	service, removal, rounds := newNecessityTestService(src, []string{"model-b"}, nil)

	if _, err := service.Run(context.Background(), necessityTask()); err != nil {
		t.Fatal(err)
	}
	if *rounds != 1 {
		t.Fatal("probe skipped although the current node has no readable redis state")
	}
	if removal.wasCalled() {
		t.Fatal("probe was removed although the current node has no readable redis state")
	}
}

func TestProbeNecessityRunsWhenCurrentNodeEvidenceUndecodable(t *testing.T) {
	// 同上，但状态存在而不可判定（Known=false）→ 必须 fail-open 探测。
	src := &staticEvidenceSource{evidence: map[string]NodeHealthEvidence{
		"model-a": {Known: false},
		"model-b": {Known: true, Healthy: true},
	}}
	service, removal, rounds := newNecessityTestService(src, []string{"model-b"}, nil)

	if _, err := service.Run(context.Background(), necessityTask()); err != nil {
		t.Fatal(err)
	}
	if *rounds != 1 {
		t.Fatal("probe skipped although the current node evidence is undecodable")
	}
	if removal.wasCalled() {
		t.Fatal("probe was removed although the current node evidence is undecodable")
	}
}

func TestProbeQueueWorkerSkipsCompleteForUnnecessaryTask(t *testing.T) {
	// processTask 收到 ErrProbeNotNecessary 时必须直接返回：
	// 队列行已被 skip 路径删除，再 Complete 只会对着空行打转。
	// completeFn 缝让"没有调用 Complete"成为可断言事实，而不是
	// 依赖 nil-db Complete 报错后仅打 Warn 的间接信号。
	src := &staticEvidenceSource{evidence: map[string]NodeHealthEvidence{
		"model-a": {Known: true, Healthy: true},
	}}
	service, removal, rounds := newNecessityTestService(src, nil, nil)

	var completions int
	worker := NewProbeQueueWorker(ProbeQueueWorkerConfig{
		Queue:        &ProbeQueue{}, // db nil: a real Complete attempt errors loudly
		ProbeService: service,
	})
	worker.completeFn = func(context.Context, ProbeQueueTask, ProbeQueueResult) {
		completions++
	}
	worker.processTask(context.Background(), necessityTask())

	if *rounds != 0 {
		t.Fatal("worker executed a task the gate marked unnecessary")
	}
	if completions != 0 {
		t.Fatalf("Complete called %d time(s) for a necessity-skipped task, want 0", completions)
	}
	removal.single(t)
}

func TestRemoveSkippedProbeSQLGuard(t *testing.T) {
	sql := removeSkippedProbeSQL()
	for _, want := range []string{
		"DELETE FROM credential_probe_queue",
		"status='running'",
		"lease_token=$2::uuid",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("removeSkippedProbeSQL missing guard %q:\n%s", want, sql)
		}
	}
}

// recordingSkipQueue is a probeSkipQueue stub with a configurable Remove
// outcome; every observed step (Remove, mirror delete, SSE publish) is
// appended to one shared log so the ownership ordering is assertable.
type recordingSkipQueue struct {
	removeErr   error
	removed     bool
	record      func(string)
	removedTask ProbeQueueTask
	reason      string
}

func (q *recordingSkipQueue) Remove(_ context.Context, task ProbeQueueTask) (bool, error) {
	q.record("remove")
	q.removedTask = task
	if q.removeErr != nil {
		return false, q.removeErr
	}
	return q.removed, nil
}

func (q *recordingSkipQueue) publishRemovedTransition(task ProbeQueueTask, reason string) {
	q.record("publish")
	q.removedTask = task
	q.reason = reason
}

// newRemovalTestService wires a bare ProbeService whose skip removal runs
// through the stub queue, with the mirror delete recorded into the same step
// log instead of touching a worker DB. The returned snapshot fn returns the
// ordered step log.
func newRemovalTestService(q *recordingSkipQueue) (*ProbeService, func() []string) {
	var mu sync.Mutex
	var steps []string
	q.record = func(step string) {
		mu.Lock()
		defer mu.Unlock()
		steps = append(steps, step)
	}
	service := &ProbeService{worker: &NodeProbeWorker{}}
	service.skipQueue = q
	service.deleteStateFn = func(context.Context, int, string) { q.record("deleteState") }
	return service, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), steps...)
	}
}

func TestRemoveSkippedProbeRemoveErrorStopsBeforeMirrorAndSSE(t *testing.T) {
	// Remove 报错（DB 故障等）→ 只有一次 Remove 尝试；镜像与 SSE 都不能碰。
	q := &recordingSkipQueue{removeErr: errors.New("db down")}
	service, steps := newRemovalTestService(q)

	task := necessityTask()
	task.LeaseToken = "lease-1"
	service.removeSkippedProbe(context.Background(), task, SkipReasonAllNodesHealthy)

	if got := steps(); len(got) != 1 || got[0] != "remove" {
		t.Fatalf("steps = %v, want [remove] only (mirror/SSE must not fire)", got)
	}
}

func TestRemoveSkippedProbeLeaseLostLeavesMirrorAndSSE(t *testing.T) {
	// Remove 返回 removed=false（租约丢失/别处已完成）→ 新所有者可能正在执行，
	// 镜像行与 SSE 磁贴必须留给新所有者。
	q := &recordingSkipQueue{removed: false}
	service, steps := newRemovalTestService(q)

	task := necessityTask()
	task.LeaseToken = "lease-1"
	service.removeSkippedProbe(context.Background(), task, SkipReasonLastProbeHealthy)

	if got := steps(); len(got) != 1 || got[0] != "remove" {
		t.Fatalf("steps = %v, want [remove] only (state must be left to the new owner)", got)
	}
}

func TestRemoveSkippedProbeMissingQueueOrLease(t *testing.T) {
	// queue 未接线或租约为空：没有任何所有权凭证，三个副作用都不能发生。
	t.Run("queue nil", func(t *testing.T) {
		service := &ProbeService{worker: &NodeProbeWorker{}}
		var mirrorDeletes int
		service.deleteStateFn = func(context.Context, int, string) { mirrorDeletes++ }

		task := necessityTask()
		task.LeaseToken = "lease-1"
		service.removeSkippedProbe(context.Background(), task, SkipReasonAllNodesHealthy)
		if mirrorDeletes != 0 {
			t.Fatal("mirror deleted without any queue wired")
		}
	})
	t.Run("lease empty", func(t *testing.T) {
		q := &recordingSkipQueue{removed: true}
		service, steps := newRemovalTestService(q)

		task := necessityTask() // LeaseToken stays empty
		service.removeSkippedProbe(context.Background(), task, SkipReasonAllNodesHealthy)
		if got := steps(); len(got) != 0 {
			t.Fatalf("steps = %v, want none (no lease, no ownership proof)", got)
		}
	})
}

func TestRemoveSkippedProbeSuccessDeletesMirrorThenPublishes(t *testing.T) {
	// 成功路径编排: queue 行删除（所有权证明）→ 镜像删除 → SSE 终态。
	q := &recordingSkipQueue{removed: true}
	service, steps := newRemovalTestService(q)

	task := necessityTask()
	task.LeaseToken = "lease-1"
	service.removeSkippedProbe(context.Background(), task, SkipReasonAllNodesHealthy)

	got := steps()
	want := []string{"remove", "deleteState", "publish"}
	if len(got) != len(want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("steps = %v, want %v (order matters: ownership proof first)", got, want)
		}
	}
	if q.removedTask.ID != task.ID {
		t.Fatalf("published task id = %d, want %d", q.removedTask.ID, task.ID)
	}
	if !strings.HasPrefix(q.reason, skipReasonSSEPrefix+": "+SkipReasonAllNodesHealthy) {
		t.Fatalf("published reason = %q, want prefix %q", q.reason, skipReasonSSEPrefix+": "+SkipReasonAllNodesHealthy)
	}
}
