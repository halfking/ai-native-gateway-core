// data_horizon.go — traffic-data freshness notice backed by per-instance
// session-turn canaries (2026-10-16 audit correction).
//
// The first session-family implementation compared a live-stream activity time
// with the GLOBAL MAX(session_turns_hot.ts). That is not a valid multi-instance
// proof: 154 and 245 share PostgreSQL, and either instance can keep the global
// maximum fresh while the other instance loses every mirror write. Redis
// live-stream fan-out made the signal even more cluster-wide.
//
// The corrected contract is identity-based, not timestamp-based:
//  1. each gateway records one LOCAL, mirror-eligible terminal request_id per
//     minute from its telemetry persisted hook;
//  2. the horizon endpoint checks whether mature canaries exist in
//     session_turns_hot UNION session_turns;
//  3. another gateway's row can never satisfy this instance's request_id.
//
// A bounded ten-minute ring plus a five-minute maturity delay detects a
// continuous mirror outage without retaining per-request state or flagging the
// normal async mirror/outbox window. Empty/idle instances remain healthy.
package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const DataHorizonHeader = "X-LLM-Gateway-Data-Horizon"

const (
	dataHorizonRecentTrafficWindow = 10 * time.Minute
	dataHorizonLagTolerance        = 5 * time.Minute
)

type requestMirrorCanary struct {
	RequestID string
	SeenAt    time.Time
}

type DataHorizonNotice struct {
	Stale   bool   `json:"stale"`
	Unknown bool   `json:"unknown"`
	Source  string `json:"source"`
	Effect  string `json:"effect"`
	Silence string `json:"silence"`
	Hint    string `json:"hint"`
}

// dataHorizonNoticeFor is the pure verdict layer. Only mature canaries can
// prove staleness; recent canaries are still inside the async mirror/outbox
// budget. Old canaries outside the observation window do not make an idle
// gateway noisy.
func dataHorizonNoticeFor(canaries []requestMirrorCanary, mirrored map[string]bool, now time.Time) (*DataHorizonNotice, bool) {
	for _, canary := range canaries {
		requestID := strings.TrimSpace(canary.RequestID)
		if requestID == "" || canary.SeenAt.IsZero() {
			continue
		}
		age := now.Sub(canary.SeenAt)
		if age < dataHorizonLagTolerance || age > dataHorizonRecentTrafficWindow {
			continue
		}
		if mirrored[requestID] {
			continue
		}
		return &DataHorizonNotice{
			Stale:   true,
			Source:  "session_turns_hot ∪ session_turns",
			Effect:  "网关已完成业务请求，但对应 request_id 仍未写入 session_turns；本页及派生统计正在漏记流量。",
			Silence: "这类断流没有任何错误信号：统计接口照常返回、数字齐全。另一台网关的新数据也不能证明本机请求已被记录。",
			Hint:    "排查方向：sessionv2mirror / session_mirror_outbox 与 sessions_v2 写门；按缺失 request_id 对照 V2 shadow write 失败日志。",
		}, true
	}
	return nil, false
}

func dataHorizonNoticeForUnknown() (*DataHorizonNotice, bool) {
	return &DataHorizonNotice{
		Unknown: true,
		Source:  "session_turns_hot ∪ session_turns",
		Effect:  "无法确认流量数据状态：session_turns canary 查询失败。",
		Silence: "两种可能都未经验证：记录可能仍在继续，也可能已经断流。本页数字请按「可能已过期」对待。",
		Hint:    "排查方向：数据库连通性，以及 session_turns_hot / session_turns 与 request_id 索引是否存在。",
	}, true
}

type horizonQueryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// sessionTurnCanaryPresenceFrom resolves exact request identities across both
// session-family legs. Both relations have request_id indexes; ARRAY(...) keeps
// this to one round trip for the bounded canary set.
func sessionTurnCanaryPresenceFrom(ctx context.Context, q horizonQueryRower, canaries []requestMirrorCanary) (map[string]bool, error) {
	ids := make([]string, 0, len(canaries))
	seen := make(map[string]struct{}, len(canaries))
	for _, canary := range canaries {
		id := strings.TrimSpace(canary.RequestID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return map[string]bool{}, nil
	}

	var present []string
	err := q.QueryRow(ctx, `
		SELECT ARRAY(
			SELECT DISTINCT request_id
			FROM (
				SELECT request_id FROM public.session_turns_hot WHERE request_id = ANY($1::text[])
				UNION ALL
				SELECT request_id FROM public.session_turns WHERE request_id = ANY($1::text[])
			) turns
		)
	`, ids).Scan(&present)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(present))
	for _, id := range present {
		out[id] = true
	}
	return out, nil
}

func (h *Handler) sessionTurnCanaryPresence(ctx context.Context, canaries []requestMirrorCanary) (map[string]bool, error) {
	if h.db == nil {
		return nil, errors.New("no database pool")
	}
	return sessionTurnCanaryPresenceFrom(ctx, h.db, canaries)
}

func (h *Handler) dataHorizonNotice(ctx context.Context) (*DataHorizonNotice, bool) {
	if h.db == nil {
		return dataHorizonNoticeForUnknown()
	}
	var canaries []requestMirrorCanary
	if h.liveStreamHub != nil {
		canaries = h.liveStreamHub.DataHorizonCanaries()
	}
	if len(canaries) == 0 {
		return nil, false
	}
	mirrored, err := h.sessionTurnCanaryPresence(ctx, canaries)
	if err != nil {
		return dataHorizonNoticeForUnknown()
	}
	return dataHorizonNoticeFor(canaries, mirrored, time.Now().UTC())
}

func applyDataHorizonHeader(w http.ResponseWriter, notice *DataHorizonNotice) {
	if notice == nil {
		return
	}
	if notice.Unknown {
		w.Header().Set(DataHorizonHeader, "unknown")
		return
	}
	w.Header().Set(DataHorizonHeader, "stale")
}

func (h *Handler) handleDataHorizon(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	notice, ok := h.dataHorizonNotice(ctx)
	applyDataHorizonHeader(w, notice)
	if ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"data_horizon": notice,
			"//":           "stale=true 表示本机终态业务 request_id 在容忍窗后仍未进入 session_turns；null 表示近期 canary 已记录或网关空闲。",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data_horizon": nil,
		"//":           "stale=true 表示本机终态业务 request_id 在容忍窗后仍未进入 session_turns；null 表示近期 canary 已记录或网关空闲。",
	})
}
