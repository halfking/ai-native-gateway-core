// hostedtask/callbacks.go — 回调投递编排（§3.1 ⑤/§6.1）。
//
// 与 internal/hostedcallback 的分工：那边是纯投递机制（SSRF 防线 + HMAC
// 签名），这里是台账编排：ClaimDueCallbacks → 解密 URL/secret（secret
// Keyring AES-GCM）→ 重建投递体（result 以 PG 为权威，event_id 固定幂等）
// → Deliver → RecordCallbackOutcome（2xx delivered / 4xx DLQ / 5xx&网络退避）。
package hostedtask

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/hostedcallback"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// CallbackBackoff 返回第 attempt 次失败后的退避（30s 起、×2、封顶 1h）。
func CallbackBackoff(attempt int) time.Duration {
	base := 30 * time.Second
	if attempt < 0 {
		attempt = 0
	}
	d := time.Duration(float64(base) * math.Pow(2, float64(attempt)))
	if d > time.Hour {
		return time.Hour
	}
	return d
}

// CallbackDeps 是回调投递循环的依赖集。
type CallbackDeps struct {
	Store     *Store
	Deliverer *hostedcallback.Deliverer
	Keyring   *secret.Keyring // 解密 url_enc/secret_enc；nil 时跳过投递并告警
	Logger    *slog.Logger
}

// DeliverDueCallbacks 扫描并投递一批到期回调。返回投递成功的条数。
// 每条结论独立回写；单条失败不阻断其余（矩阵 E）。
func DeliverDueCallbacks(ctx context.Context, deps CallbackDeps, now time.Time, limit int) (int, error) {
	if deps.Store == nil || deps.Deliverer == nil {
		return 0, nil
	}
	log := deps.Logger
	if log == nil {
		log = slog.Default()
	}
	if deps.Keyring == nil {
		// 无法解密即无法投递：不认领，等运维配好 keyring（避免误 DLQ）。
		log.Warn("hostedtask: callback keyring not configured; due callbacks left pending")
		return 0, nil
	}

	jobs, err := deps.Store.ClaimDueCallbacks(ctx, now, limit)
	if err != nil {
		return 0, fmt.Errorf("claim callbacks: %w", err)
	}
	delivered := 0
	for _, job := range jobs {
		select {
		case <-ctx.Done():
			return delivered, ctx.Err()
		default:
		}
		out := deliverOne(ctx, deps, job)
		if out.delivered {
			delivered++
		}
		if err := deps.Store.RecordCallbackOutcome(ctx, job, CallbackOutcome{
			Delivered:  out.delivered,
			Retryable:  out.retryable,
			StatusCode: out.statusCode,
			Err:        out.errText,
			NextAfter:  CallbackBackoff(job.Attempts),
		}); err != nil {
			log.Error("hostedtask: record callback outcome failed",
				"task_id", job.TaskID, "event_id", job.EventID, "error", err)
		}
	}
	return delivered, nil
}

// deliveryOutcome 是单条投递的结论（对齐 store.CallbackOutcome 语义）。
type deliveryOutcome struct {
	delivered  bool
	retryable  bool
	statusCode int
	errText    string
}

// deliverOne 投递单条：解密 → 重建投递体 → POST。
func deliverOne(ctx context.Context, deps CallbackDeps, job CallbackJob) deliveryOutcome {
	fail := func(retryable bool, err error) deliveryOutcome {
		return deliveryOutcome{retryable: retryable, errText: err.Error()}
	}

	urlPlain, err := secret.DecryptAESGCM(job.URLEnc, deps.Keyring)
	if err != nil {
		return fail(false, fmt.Errorf("decrypt callback url: %w", err)) // 密文/keyring 问题不因重试消失
	}
	secretPlain, err := secret.DecryptAESGCM(job.SecretEnc, deps.Keyring)
	if err != nil {
		return fail(false, fmt.Errorf("decrypt callback secret: %w", err))
	}

	// 投递体以 PG 当前权威状态重建（result/终态在 hosted_tasks 行上），
	// event_id 固定 = hosted_<id>_ev<seq>，重投由接收方幂等。
	task, err := deps.Store.GetTask(ctx, job.TenantID, job.TaskID)
	if err != nil {
		return fail(true, fmt.Errorf("load task for callback: %w", err))
	}
	eventType := "hosted_task." + string(task.Status)
	payload := map[string]any{
		"status":  task.Status.APIStatus(),
		"result":  task.Result,
		"attempt": job.Attempts + 1,
	}
	body, err := hostedcallback.BuildEnvelope(job.EventID, eventType, job.TaskID, job.TenantID, payload)
	if err != nil {
		return fail(true, fmt.Errorf("build envelope: %w", err))
	}

	res, derr := deps.Deliverer.Deliver(ctx, string(urlPlain), string(secretPlain), body, job.TaskID)
	return deliveryOutcome{
		delivered:  res.Delivered,
		retryable:  res.Retryable,
		statusCode: res.StatusCode,
		errText:    errString(derr),
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
