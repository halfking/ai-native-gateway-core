// Package hostedtask implements the Hosted Task Delegation facade (P0):
// the gateway-side projection + notification ledger described in
// docs/design/hosted-task-delegation-design.md.
//
// 单写者原则（§1.1/§1.3）：执行真相在 ACC（Runtime Control dispatch + lease/
// fencing），存储真相在 Memora，通知真相在网关。本包只做三件事：
//
//  1. 接入面：/v1/hosted-tasks 门面（委托/查询/结果/取消）；
//  2. 投影：reconciler 订阅 ACC 事件 + 轮询兜底，CAS 写 hosted_tasks；
//  3. 通知：签名回调（safehttpclient + outbox.SignPayload）+ 拉取兜底。
//
// 网关不建第二套执行 owner：hosted_tasks 是关联投影（D4），dispatch 重试 =
// 同 Idempotency-Key 重放，租约/围栏由 ACC 与 companion 持有。
package hostedtask

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ─── 状态机（§4.2）────────────────────────────────────────────────────────

// Status 是 hosted_tasks 状态机的状态。与迁移 711 的
// hosted_tasks_status_check 严格一一对应；改任一侧必须同步另一侧。
type Status string

const (
	StatusDelegated   Status = "delegated"
	StatusDispatching Status = "dispatching"
	StatusRunning     Status = "running"
	StatusCompleted   Status = "completed"
	StatusFailed      Status = "failed"
	StatusNeedsReview Status = "needs_review"
	StatusCancelled   Status = "cancelled"
	StatusExpired     Status = "expired"
)

// Terminal 报告 s 是否为终态。终态 sticky（§4.2）：CAS 抢占后不可再迁移，
// cancel 与 complete 竞争只活一个。
func (s Status) Terminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusNeedsReview, StatusCancelled, StatusExpired:
		return true
	}
	return false
}

// transitionMatrix 是 §4.2 状态机的纯函数权威。表中未列出的迁移一律非法。
// delegated → dispatching → running → completed | failed | needs_review
//
// 任意非终态 → cancelled | expired(deadline reaper)
var transitionMatrix = map[Status][]Status{
	StatusDelegated:   {StatusDispatching, StatusCancelled, StatusExpired},
	StatusDispatching: {StatusRunning, StatusFailed, StatusCancelled, StatusExpired},
	StatusRunning:     {StatusCompleted, StatusFailed, StatusNeedsReview, StatusCancelled, StatusExpired},
	// 终态：无出边（sticky）。
	StatusCompleted:   {},
	StatusFailed:      {},
	StatusNeedsReview: {},
	StatusCancelled:   {},
	StatusExpired:     {},
}

// CanTransition 报告 from → to 是否为合法迁移（纯函数，表驱动测试）。
func CanTransition(from, to Status) bool {
	if from == to {
		return false
	}
	for _, next := range transitionMatrix[from] {
		if next == to {
			return true
		}
	}
	return false
}

// ValidStatus 报告 s 是否为已知状态（handler 入参白名单等场景）。
func ValidStatus(s Status) bool {
	_, ok := transitionMatrix[s]
	return ok
}

// APIStatus 把 DB 状态映射为 API 暴露状态（§3.1：needs_review 是人工对账
// 态，P0 对前端暴露为 failed(unknown)，事件时间线保留 needs_review 证据）。
func (s Status) APIStatus() string {
	if s == StatusNeedsReview {
		return string(StatusFailed)
	}
	return string(s)
}

// ─── 事件（§4.3）────────────────────────────────────────────────────────────

// EventType 是 hosted_task_events 的事件类型。与迁移 711 的
// hosted_task_events_type_check 严格一一对应；P1 事件（budget_exceeded /
// recalled / phase_changed）需要先改迁移再改这里。
type EventType string

const (
	EventAccepted         EventType = "accepted"
	EventDispatchDegraded EventType = "dispatch_degraded"
	EventRunning          EventType = "running"
	EventProgress         EventType = "progress"
	EventCancelRequested  EventType = "cancel_requested"
	EventCompleted        EventType = "completed"
	EventFailed           EventType = "failed"
	EventExpired          EventType = "expired"
	EventCancelled        EventType = "cancelled"
	EventCallbackDone     EventType = "callback_delivered"
	EventCallbackDLQ      EventType = "callback_dlq"
)

// Event 是事件时间线中的一行（append-only，唯一 (task_id, seq)）。
type Event struct {
	TaskID    string         `json:"task_id"`
	Seq       int64          `json:"seq"`
	Type      EventType      `json:"event_type"`
	Payload   map[string]any `json:"payload,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// EventID 返回回调投递使用的固定幂等键（§6.1：event_id=hosted_<id>_ev<seq>）。
func EventID(taskID string, seq int64) string {
	return fmt.Sprintf("hosted_%s_ev%d", taskID, seq)
}

// ─── 任务实体与创建输入 ────────────────────────────────────────────────────

// Task 是 hosted_tasks 的一行投影。
type Task struct {
	ID               string         `json:"hosted_task_id"`
	TenantID         string         `json:"-"`
	APIKeyID         int64          `json:"-"`
	Goal             string         `json:"goal"`
	DoneWhen         string         `json:"done_when,omitempty"`
	Context          map[string]any `json:"context,omitempty"`
	Environment      map[string]any `json:"environment,omitempty"`
	Status           Status         `json:"-"`
	AccCommandID     string         `json:"-"`
	AccRunID         string         `json:"-"`
	DispatchKey      string         `json:"-"`
	DispatchAttempts int            `json:"-"`
	GwSessionID      string         `json:"gw_session_id,omitempty"`
	SSECursor        string         `json:"-"`
	CallbackURLHash  string         `json:"-"`
	Result           map[string]any `json:"result,omitempty"`
	ResultVersion    int64          `json:"-"`
	Revision         int64          `json:"-"`
	IdempotencyKey   string         `json:"-"`
	RequestHash      string         `json:"-"`
	DeadlineAt       time.Time      `json:"deadline_at"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	CompletedAt      *time.Time     `json:"completed_at,omitempty"`
}

// View 是 API 暴露形态（§4.1 GET）：status 经 APIStatus 映射，
// ACC 内部标识与租户字段不出网。
type View struct {
	HostedTaskID string         `json:"hosted_task_id"`
	Status       string         `json:"status"`
	Goal         string         `json:"goal"`
	DoneWhen     string         `json:"done_when,omitempty"`
	GwSessionID  string         `json:"gw_session_id,omitempty"`
	DeadlineAt   time.Time      `json:"deadline_at"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	CompletedAt  *time.Time     `json:"completed_at,omitempty"`
	Result       map[string]any `json:"result,omitempty"`
}

// View 渲染 API 投影。
func (t *Task) View() View {
	return View{
		HostedTaskID: t.ID,
		Status:       t.Status.APIStatus(),
		Goal:         t.Goal,
		DoneWhen:     t.DoneWhen,
		GwSessionID:  t.GwSessionID,
		DeadlineAt:   t.DeadlineAt,
		CreatedAt:    t.CreatedAt,
		UpdatedAt:    t.UpdatedAt,
		CompletedAt:  t.CompletedAt,
		Result:       t.Result,
	}
}

// CreateInput 是委托创建的已验证输入。handler 负责校验与白名单映射；
// CallbackURLEnc/CallbackSecretEnc 是 handler 用 secret.Keyring 加密后的
// AES-GCM 信封（明文不进存储层，明文仅参与 RequestHash 计算）。
type CreateInput struct {
	TenantID       string
	APIKeyID       int64
	Goal           string
	DoneWhen       string
	Context        map[string]any
	Environment    map[string]any
	GwSessionID    string
	CallbackURLEnc string // ''=无回调
	CallbackSecEnc string // ''=无回调
	CallbackHash   string
	IdempotencyKey string
	RequestHash    string
	Deadline       time.Time
}

// ErrNotFound 统一表示“不存在或跨租户”（handler 一律回 404，§4.1）。
var ErrNotFound = errors.New("hostedtask: task not found")

// ErrIdempotencyConflict 同键异体（§4.1/矩阵 B：同键同体 200、同键异体 409）。
var ErrIdempotencyConflict = errors.New("hostedtask: idempotency key reuse with different body")

// ErrTerminal 请求与终态冲突（cancel after terminal → 409）。
var ErrTerminal = errors.New("hostedtask: task already in terminal state")

// ErrTransition 非法状态迁移（CAS 竞争失败，调用方应放弃本轮投影）。
var ErrTransition = errors.New("hostedtask: illegal state transition")

// HashRequest 计算创建体的规范化 hash（同键异体检测的依据）。
func HashRequest(goal, doneWhen string, context, environment map[string]any, callbackURL string) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "goal\x00%s\x00done_when\x00%s\x00callback\x00%s\x00",
		strings.TrimSpace(goal), strings.TrimSpace(doneWhen), callbackURL)
	_, _ = fmt.Fprintf(h, "context\x00%v\x00environment\x00%v", context, environment)
	return hex.EncodeToString(h.Sum(nil))
}

// HashCallbackURL 计算 callback URL 的诊断 hash（明文 URL 不落 hosted_tasks）。
func HashCallbackURL(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:])
}

// BuildPrompt 组装 dispatch payload 的 prompt（§3.1 ②：goal+done_when+context
// 摘要）。P0 限制（§5）：companion MemoraSearch tenant 硬编码 default，跨租户
// 上下文注入修复列 P1，因此这里内联 context.summary + scope 引用文本。
func BuildPrompt(in CreateInput) string {
	var b strings.Builder
	b.WriteString("任务目标：\n")
	b.WriteString(strings.TrimSpace(in.Goal))
	b.WriteString("\n")
	if dw := strings.TrimSpace(in.DoneWhen); dw != "" {
		b.WriteString("\n完成判定（done_when）：\n")
		b.WriteString(dw)
		b.WriteString("\n")
	}
	if in.Context != nil {
		if s, ok := in.Context["summary"].(string); ok && strings.TrimSpace(s) != "" {
			b.WriteString("\n上下文摘要：\n")
			b.WriteString(strings.TrimSpace(s))
			b.WriteString("\n")
		}
		if m, ok := in.Context["memora"].(map[string]any); ok && len(m) > 0 {
			b.WriteString("\n上下文引用（Memora scope，由宿主环境负责召回）：\n")
			_, _ = fmt.Fprintf(&b, "%v\n", m)
		}
		if arts, ok := in.Context["artifacts"].([]any); ok && len(arts) > 0 {
			b.WriteString("\n关联产物引用：\n")
			_, _ = fmt.Fprintf(&b, "%v\n", arts)
		}
	}
	if dl := in.Deadline; !dl.IsZero() {
		b.WriteString("\n截止时间（UTC）：")
		b.WriteString(dl.UTC().Format(time.RFC3339))
		b.WriteString("\n")
	}
	return b.String()
}
