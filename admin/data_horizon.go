// data_horizon.go — 「流量数据地平线」告示的单一来源（2026-10-15 r1033 重写）。
//
// # 本文件在解决什么
//
// 总览页（看板/统计）的流量数字从 session_turns 族出发：request_stats_minute
// 的唯一写入者 bg/stats_minute_rollup 扫描 canonical 视图（视图族自 S4 起由
// session 分支供数），看板 fallback 也已换到 session 族原生源
// （boardRequestLogsFromClause）。告示要回答的因此只剩一个问题：
//
//	**网关仍在接收请求时，session_turns 是否在记录新数据？**
//
// 是 → 数据是新的，不告示。网关空闲（近期没有任何请求）→ 没有新数据是
// 正常状态，同样不告示。只有「网关近期有请求、而 session_turns 没跟上」
// 才是真正的断流——那才是需要一条横幅让人看见的事。
//
// # 与前一代（v1_freeze_notice，2026-10-03）的关系
//
// 前一代盯着 `storage.request_logs_write_enabled`（v1 停写门），在 S4 停写后
// 常显「流量数据已停更：本页数字只反映停写之前」。那个前提在统计链全面
// session 化之后已经**不成立**：页面数字经视图 session 分支持续更新，横幅
// 在描述一个不复存在的冻结（生产实测：停写两日后 request_stats_minute 的
// bucket 仍在推进）。按 2026-10-15 用户裁决「检查验证以 session_turns 为准、
// 弃用 request_logs」，本文件彻底不再读 v1 停写门。
//
// 前一代的三态纪律（live/frozen/unavailable + 「读到 true」与「根本没读到」
// 必须可区分，§9.79.8）保留为 live/stale/unknown：
//   · live    —— session 数据新鲜，或网关空闲（无可判流量）。
//   · stale   —— 网关近期有请求（hub lastActivity），而 session_turns_hot
//                的 MAX(ts) 落后该时刻超过容忍窗 ⇒ 断流，告示。
//   · unknown —— session_turns 读不上来（DB 故障/表缺失）⇒ 不能说 live。
//
// # 判据的两个信号
//
//   · `session_turns_hot` 的 MAX(ts)：会话族的系统记录，ts 索引尾扫，毫秒级。
//   · hub 的 lastActivity：每次请求广播（in_progress 投影 + 终态补偿）都会
//     推进，是「本进程见过请求」的进程内真值——不依赖任何存储，因此能
//     区分「没数据因为没流量」与「没数据因为断流」。旧版刻意不做 MAX(ts)
//     推断（「表空/首写未发生时给反结论」）；单看 MAX(ts) 确实不能判，
//     **两个信号对齐后才能判**，这正是本版与前代的差别。
//
// # 为什么不再有「中央响应头」
//
// 前一代把 X-LLM-Gateway-V1-Data-Frozen 挂在 writeJSON 中央出口（设置读取
// 足够便宜）。新判据要查 DB——挂中央出口等于每个 JSON 响应都打一次库，
// 不可接受。头改为只在 /api/admin/data-horizon 端点自身携带，供抓包/非浏
// 览消费者判别。
package admin

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// DataHorizonHeader 携带告示的响应头（仅 data-horizon 端点自身）。
// stale=断流告示；unknown=无法确认；未设置=数据新鲜/空闲。
const DataHorizonHeader = "X-LLM-Gateway-Data-Horizon"

// dataHorizonRecentTrafficWindow 是「网关近期有请求」的判定窗：
// lastActivity 落在窗内才触发新鲜度比对，窗外视为空闲。
const dataHorizonRecentTrafficWindow = 10 * time.Minute

// dataHorizonLagTolerance 是 session_turns 对最近请求的落后容忍：
// 镜像写在请求路径上（write_timeout 5s 量级），分钟级容忍已极宽。
const dataHorizonLagTolerance = 5 * time.Minute

// DataHorizonNotice 是告示的机器可读形态。
//
// `Stale` 为 true 表示**确证断流**；`Unknown` 为 true 表示**读不到 session
// 数据**——两者互斥，且都为 false 时不产生对象（healthy 无载荷，与旧版
// 「未冻结不返回 frozen=false 对象」同一契约：调用方无法把「没对象」误读
// 成「有对象且说没事」）。
type DataHorizonNotice struct {
	Stale   bool   `json:"stale"`
	Unknown bool   `json:"unknown"`
	// Source 是被检查的数据源族。
	Source string `json:"source"`
	// Effect 说明「看到这些数字意味着什么」——给人读的。
	Effect string `json:"effect"`
	// Silence 是最要紧的一句：这类断流**没有错误信号**，接口照常返回。
	Silence string `json:"silence"`
	// Hint 给运维指出排查方向。
	Hint string `json:"hint"`
}

// dataHorizonNoticeFor 是**纯函数**形态的判定器（测试逐格调它；接线层
// dataHorizonNotice() 负责两个信号的真实读取）。
//
// sessionMaxTS 为零值 = session_turns_hot 一行都没有；lastRequestSeen 为零值
// = 本进程从未见过请求（hub 信号缺失）。后者不触发 stale：没有流量就没有
// 「该到而未到的数据」。
func dataHorizonNoticeFor(sessionMaxTS, lastRequestSeen, now time.Time) (*DataHorizonNotice, bool) {
	// 空闲（或信号缺失）：lastRequestSeen 非零但已滑出近期窗 = 网关最近
	// 没收到请求，同样不告示。
	if lastRequestSeen.IsZero() || now.Sub(lastRequestSeen) > dataHorizonRecentTrafficWindow {
		return nil, false
	}
	// 近期有请求 ⇒ session 数据必须跟上。零值（无行）与落后同样判 stale：
	// 一个刚接过请求的网关，会话族里连一行近期数据都没有，就是断流本身。
	if sessionMaxTS.IsZero() || sessionMaxTS.Before(lastRequestSeen.Add(-dataHorizonLagTolerance)) {
		return &DataHorizonNotice{
			Stale:   true,
			Source:  "session_turns_hot",
			Effect:  "网关仍在接收请求，但 session_turns 未记录到新数据；本页及派生统计正在滞后于此刻。",
			Silence: "这类断流没有任何错误信号：统计接口照常返回、数字齐全。接口没报错不代表数据是新的。",
			Hint:    "排查方向：session 镜像写入路径（sessionv2mirror）与 sessions_v2 写门；对照网关日志中的 V2 shadow write 失败计数。",
		}, true
	}
	return nil, false
}

// dataHorizonNoticeForUnknown 是「读不到 session 数据」的保守分支：不说
// stale（没证据），也绝不说 live（同样没证据）——与旧版 §9.79.8 同一纪律。
func dataHorizonNoticeForUnknown() (*DataHorizonNotice, bool) {
	return &DataHorizonNotice{
		Unknown: true,
		Source:  "session_turns_hot",
		Effect:  "无法确认流量数据状态：session_turns 读取失败。",
		Silence: "两种可能都未经验证：记录可能仍在继续，也可能已经断流。本页数字请按「可能已过期」对待。",
		Hint:    "排查方向：数据库连通性 / session_turns_hot 表是否存在。",
	}, true
}

// horizonQueryRower 是 sessionTurnsHotMaxTS 的最小查询面——*pgxpool.Pool 与
// pgxmock 池都满足它，让空表 NULL 扫描路径可以在单测里逐格驱动。
type horizonQueryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// sessionTurnsHotMaxTSFrom 读会话族热表的最大时刻。热表带 ts 索引（798），
// MAX 走索引尾扫。表缺失（未迁移/lite）归 unknown 分支，不是 stale。
//
// ⚠ 空表时 MAX 返回 SQL NULL——必须扫进可空类型再归一为零值；直接扫
// time.Time 会报 "cannot scan NULL" → 空库被误报成 unknown（本地 2646
// 首测抓到）。零值由纯函数按「有流量但无记录 = 断流」裁决。
func sessionTurnsHotMaxTSFrom(ctx context.Context, q horizonQueryRower) (time.Time, error) {
	var maxTS sql.NullTime
	err := q.QueryRow(ctx, `SELECT MAX(ts) FROM public.session_turns_hot`).Scan(&maxTS)
	if err != nil || !maxTS.Valid {
		return time.Time{}, err
	}
	return maxTS.Time.UTC(), nil
}

func (h *Handler) sessionTurnsHotMaxTS(ctx context.Context) (time.Time, error) {
	if h.db == nil {
		return time.Time{}, errors.New("no database pool")
	}
	return sessionTurnsHotMaxTSFrom(ctx, h.db)
}

// dataHorizonNotice 是接线层：读两个真信号，交给纯函数裁决。
func (h *Handler) dataHorizonNotice(ctx context.Context) (*DataHorizonNotice, bool) {
	if h.db == nil {
		// lite / 未接库：判据无法成立，归 unknown——不谎报健康。
		return dataHorizonNoticeForUnknown()
	}
	sessionMax, err := h.sessionTurnsHotMaxTS(ctx)
	if err != nil {
		return dataHorizonNoticeForUnknown()
	}
	var lastSeen time.Time
	if h.liveStreamHub != nil {
		lastSeen = h.liveStreamHub.LastRequestSeenAt()
	}
	return dataHorizonNoticeFor(sessionMax.UTC(), lastSeen, time.Now().UTC())
}

// applyDataHorizonHeader 把告示头写到 data-horizon 端点自身的响应上。
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

// handleDataHorizon 是 UI 拉取告示的端点（GET /api/admin/data-horizon）。
//
// 与旧版同约：未断流时**不**返回 stale=false 的对象（键恒在、值为 null），
// 「没有对象」与「有对象但说没事」在前端是两种路径，混起来就会出现
// 「横幅永远显示」这类静默错误。
func (h *Handler) handleDataHorizon(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	notice, ok := h.dataHorizonNotice(ctx)
	applyDataHorizonHeader(w, notice)
	if ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"data_horizon": notice,
			"//":           "stale=true 表示网关有流量而 session_turns 未记录；本对象不存在（null）即表示数据新鲜或网关空闲。",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data_horizon": nil,
		"//":           "stale=true 表示网关有流量而 session_turns 未记录；本对象不存在（null）即表示数据新鲜或网关空闲。",
	})
}
