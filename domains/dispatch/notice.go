package dispatch

import (
	"fmt"
	"log/slog"
	"strconv"
	"time"
)

// DispatchNoticeKind is the closed vocabulary of client-visible dispatch
// progress events (架构优化 v6 G-Ⅲ, docs/架构优化v6/08-dispatch-executor-loop.md
// §R5). Notices ride the pre-stream `: thinking:` SSE comment channel so they
// never enter the conversation content.
type DispatchNoticeKind string

const (
	// NoticeKindRetry reports a same-credential retry was scheduled.
	NoticeKindRetry DispatchNoticeKind = "retry"
	// NoticeKindNodeSwitch reports a switch to a sibling credential.
	NoticeKindNodeSwitch DispatchNoticeKind = "node_switch"
	// NoticeKindModelSwitch reports a model change.
	NoticeKindModelSwitch DispatchNoticeKind = "model_switch"
	// NoticeKindQueued reports a capacity wait (all lanes saturated).
	NoticeKindQueued DispatchNoticeKind = "queued"
	// NoticeKindScheduled reports a scheduled request parked/due.
	NoticeKindScheduled DispatchNoticeKind = "scheduled"
)

// DispatchNotice is the structured payload handed to QueuedRequest.
// OnDispatchNotice on every requeue/schedule site. The executor bridges it to
// the handler's thinking-frame writer (params.OnNodeJump). Callbacks must be
// non-blocking: they run on dispatcher/failover goroutines.
type DispatchNotice struct {
	Kind      DispatchNoticeKind `json:"kind"`
	Message   string             `json:"message"`
	ErrorKind string             `json:"error_kind,omitempty"`
	// RetryAt / WaitHint describe the next execution window for waiting
	// notices (retry/queued/scheduled). Zero when not applicable.
	RetryAt  time.Time `json:"retry_at,omitempty"`
	WaitHint string    `json:"wait_hint,omitempty"`
	// From*/To* carry the routing transition for switch notices.
	FromModel        string `json:"from_model,omitempty"`
	ToModel          string `json:"to_model,omitempty"`
	FromCredentialID int    `json:"from_credential_id,omitempty"`
	ToCredentialID   int    `json:"to_credential_id,omitempty"`
	Vendor           string `json:"vendor,omitempty"`
	Attempt          int    `json:"attempt,omitempty"`
	Seq              int    `json:"attempt_seq,omitempty"`
}

// NextActionKind is the closed vocabulary for FailoverMarker.NextAction —
// "回队打标" (v6 G-Ⅴ): every requeue stamps what the next round will do.
type NextActionKind string

const (
	NextActionRetrySameCred NextActionKind = "retry_same_cred"
	NextActionSwitchCred    NextActionKind = "switch_cred"
	NextActionSwitchModel   NextActionKind = "switch_model"
	NextActionCapacityWait  NextActionKind = "capacity_wait"
	NextActionScheduledWait NextActionKind = "scheduled_wait"
)

// FailoverMarker summarizes the previous round's outcome plus the decided
// next action. Written under the QueuedRequest single-owner invariant at
// every requeue site; surfaced via DispatchNotice, DimensionIndex entries and
// logs so "为什么这个请求又排队了" is answerable without heap digging.
type FailoverMarker struct {
	ErrorKind    string         `json:"error_kind,omitempty"`
	HTTPStatus   int            `json:"http_status,omitempty"`
	Model        string         `json:"model,omitempty"`
	CredentialID int            `json:"credential_id,omitempty"`
	Vendor       string         `json:"vendor,omitempty"`
	NextAction   NextActionKind `json:"next_action"`
	Attempt      int            `json:"attempt,omitempty"`
	StampedAt    time.Time      `json:"stamped_at"`
}

// notifyDispatch delivers a notice to the request's transport bridge. It is
// deliberately defensive: a panicking or slow callback must never take down
// the scheduling goroutine. Notices are pre-firstbyte by construction (the
// failover ladder only runs before the first byte, ADR-Disp-003).
// prepareNotice stamps the per-request sequence number. Call while the
// scheduling goroutine still owns qr (single-owner invariant).
func (qr *QueuedRequest) prepareNotice(notice DispatchNotice) DispatchNotice {
	qr.noticeSeq++
	notice.Seq = qr.noticeSeq
	return notice
}

// deliverNotice hands an already-stamped notice to the transport bridge. The
// only qr field it touches is the immutable OnDispatchNotice callback, so it
// is safe to call AFTER the single-owner handoff (e.g. after a successful
// Tier-2 enqueue handed qr to the forwarder goroutine).
func (qr *QueuedRequest) deliverNotice(notice DispatchNotice) {
	if qr == nil {
		return
	}
	metricDispatchNotice.WithLabelValues(string(notice.Kind)).Inc()
	if qr.OnDispatchNotice == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("dispatch: dispatch notice callback panicked",
				"request_id", qr.ID, "kind", notice.Kind, "panic", r)
		}
	}()
	qr.OnDispatchNotice(notice)
}

// notifyDispatch prepares and delivers a notice in one step. Requires the
// caller to own qr (use prepareNotice/deliverNotice around ownership
// handoffs).
func (qr *QueuedRequest) notifyDispatch(notice DispatchNotice) {
	if qr == nil {
		return
	}
	qr.deliverNotice(qr.prepareNotice(notice))
}

// waitHint renders a human-friendly wait description for waiting notices.
func waitHint(d time.Duration) string {
	if d <= 0 {
		return "即将执行"
	}
	if d < time.Minute {
		return strconv.Itoa(int(d.Seconds())) + "s"
	}
	return d.Truncate(time.Second).String()
}

// scheduledAcceptedMessage builds the scheduled-request acceptance message.
func scheduledAcceptedMessage(dueAt time.Time) string {
	return fmt.Sprintf("定时请求已受理，将于 %s 执行…", dueAt.Format("15:04:05"))
}
