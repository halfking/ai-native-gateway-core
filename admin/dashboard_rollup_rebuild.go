package admin

// dashboard_rollup_rebuild.go —— 看板统计分钟表的历史回填端点。
//
// 端点（superAdmin——重写的是全租户聚合数据）：
//
//	POST /api/admin/dashboard/rollup/rebuild {"from":"YYYY-MM-DD","to":"YYYY-MM-DD"}
//
// 存在的理由：分钟聚合游标只前进不回退，每个分钟只被聚合一次。被探测排除
// 之前写入的行会带着探测计数一直留到 statsRetentionDays（90 天）自然老化。
// 只部署代码修复的话，看板的 7 天 / 30 天页签仍然不真实——代码对了、存量数据
// 还没对。这个端点把存量按天重算一遍。
//
// 语义：from/to 都是 YYYY-MM-DD（UTC，**闭区间**），内部按 [from 00:00, to+1 00:00)
// 重算。缺省 = 最近 7 天（与 statsRetentionDays=90 相比保守，且覆盖看板 7d 页签）。
//
// 幂等且可中断后重跑：request_logs 才是事实源，聚合表是派生数据。
//
// Redis 侧不需要在这里清缓存：boardcache 每 15 分钟无条件 RebuildScope 回读
// PG（domains/stats/boardcache/rebuild.go:44），回填完成后最多 15 分钟看板
// 自动刷新。

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// statsRebuildMaxDays 限制单次回填跨度。与 statsRetentionDays（90）对齐：
// 更早的分钟行已被清理，重算没有意义，只会白扫。
const statsRebuildMaxDays = 90

// statsRebuildWorker 由 cmd/gateway 注入 StatsMinuteRollup。
type statsRebuildWorker interface {
	RebuildRangeStatsDetached(from, to time.Time) error
}

// SetStatsMinuteRebuilder 注入分钟表回填 worker。
func (h *Handler) SetStatsMinuteRebuilder(w statsRebuildWorker) {
	h.statsMinuteRebuilder = w
}

func (h *Handler) handleDashboardRollupRebuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	if h.statsMinuteRebuilder == nil {
		writeError(w, http.StatusServiceUnavailable, "stats minute rollup worker not wired")
		return
	}

	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := readJSONRequired(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	from, to, err := statsRebuildRange(req.From, req.To)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 与请求生命周期解耦（对齐 report-rollup/run）：回填多天，客户端断开或
	// 网关超时不该把一次跑到一半的日切片打断。
	go func() {
		if err := h.statsMinuteRebuilder.RebuildRangeStatsDetached(from, to); err != nil {
			slog.Error("stats minute manual rebuild failed",
				"from", from.Format("2006-01-02"), "to", to.Format("2006-01-02"), "error", err)
			return
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":    "accepted",
		"from":      from.Format("2006-01-02"),
		"to":        to.Format("2006-01-02"),
		"note":      "re-aggregating one day at a time; boardcache refreshes within ~15 minutes",
		"retention": statsRebuildMaxDays,
	})
}

// statsRebuildRange 解析闭区间（YYYY-MM-DD, UTC），并夹在保留窗口内。
// 缺省最近 7 天；to 不得早于 from；跨度不得超过 statsRebuildMaxDays。
func statsRebuildRange(fromRaw, toRaw string) (time.Time, time.Time, error) {
	now := time.Now().UTC()
	// 结束日取「今天」：当日分钟仍在被实时累加器写入，重算到今天为止即可，
	// 更早的存量才是需要修的。
	toDay := now
	fromDay := now.AddDate(0, 0, -6)

	if toRaw != "" {
		parsed, err := time.ParseInLocation("2006-01-02", toRaw, time.UTC)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid to (want YYYY-MM-DD)")
		}
		toDay = parsed
	}
	if fromRaw != "" {
		parsed, err := time.ParseInLocation("2006-01-02", fromRaw, time.UTC)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid from (want YYYY-MM-DD)")
		}
		fromDay = parsed
	}

	if toDay.Before(fromDay) {
		return time.Time{}, time.Time{}, fmt.Errorf("to (%s) must not precede from (%s)",
			toDay.Format("2006-01-02"), fromDay.Format("2006-01-02"))
	}
	if span := int(toDay.Sub(fromDay).Hours()/24) + 1; span > statsRebuildMaxDays {
		return time.Time{}, time.Time{}, fmt.Errorf(
			"range too wide: %d days exceeds the %d-day retention window", span, statsRebuildMaxDays)
	}

	fromDay = time.Date(fromDay.Year(), fromDay.Month(), fromDay.Day(), 0, 0, 0, 0, time.UTC)
	toEnd := time.Date(toDay.Year(), toDay.Month(), toDay.Day(), 0, 0, 0, 0, time.UTC).
		AddDate(0, 0, 1) // 闭区间 → 右开边界
	return fromDay, toEnd, nil
}
