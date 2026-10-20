package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CredentialSuccessRateRow represents one (credential, model) pair with its
// recent success rate and sample count.
type CredentialSuccessRateRow struct {
	CredentialID      int64    `json:"credential_id"`
	CredentialLabel   string   `json:"credential_label"`
	RawModel          string   `json:"raw_model"`
	RecentRate        *float64 `json:"recent_rate"`
	RecentSamples     *int     `json:"recent_samples"`
	IsRoutable        bool     `json:"is_routable"`
	BelowThreshold    bool     `json:"below_threshold"` // rate < 0.5 && samples >= 20
	OldestRequestTime *string  `json:"oldest_request_time,omitempty"`
}

// HandleCredentialSuccessRates returns success rates for all (credential, model) pairs.
// GET /api/admin/credential-success-rates
func HandleCredentialSuccessRates(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		tenantID := "default" // TODO: support multi-tenant

		rows, err := db.Query(r.Context(), `
		SELECT
			c.id AS credential_id,
			c.label AS credential_label,
			mo.raw_model_name,
			rsr.rate AS recent_rate,
			rsr.samples AS recent_samples,
			v.is_routable,
			(rsr.samples >= 20 AND COALESCE(rsr.rate, 1.0) < 0.5) AS below_threshold,
			(SELECT MIN(ts) FROM request_logs_with_current_month rl
			 WHERE rl.credential_id = c.id
			   -- 2026-07-14: compare against lowercase canonical_raw_name.
			   AND lower(COALESCE(rl.outbound_model, rl.client_model)) = mo.canonical_raw_name
			   AND rl.ts > NOW() - INTERVAL '3 hours'
			) AS oldest_request_time
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN v_routable_credential_models v
		       ON v.credential_id = mo.credential_id
		      AND v.raw_model_name = mo.raw_model_name
		CROSS JOIN LATERAL recent_success_rate(c.id, mo.raw_model_name, 50, 3) AS rsr
		WHERE p.tenant_id = $1
		ORDER BY c.id, mo.raw_model_name
	`, tenantID)
		if err != nil {
			writeInternalTextErr(w, "internal error (see server logs)", err)
			return
		}
		defer rows.Close()

		// make 非 nil：无凭据×模型行时编码 [] 而非 null（同族 R34 nil-slice 批）。
		result := make([]CredentialSuccessRateRow, 0)
		for rows.Next() {
			var row CredentialSuccessRateRow
			var oldestTime *time.Time
			err := rows.Scan(
				&row.CredentialID,
				&row.CredentialLabel,
				&row.RawModel,
				&row.RecentRate,
				&row.RecentSamples,
				&row.IsRoutable,
				&row.BelowThreshold,
				&oldestTime,
			)
			if err != nil {
				writeInternalTextErr(w, "internal error (see server logs)", err)
				return
			}
			if oldestTime != nil {
				ts := oldestTime.Format(time.RFC3339)
				row.OldestRequestTime = &ts
			}
			result = append(result, row)
		}
		// 本端点的错误形状是 text/plain（writeInternalTextErr），保持同族
		// 写法：迭代中断同样不能静默截断成功率表。
		if rerr := rows.Err(); rerr != nil {
			writeInternalTextErr(w, "internal error (see server logs)", rerr)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}
}

// resetCredentialSuccessRateRows used to delete stale failed rows of the v1
// request-log family (a (credential, model) pair) inside a super-admin GUC
// transaction, so that recent_success_rate() recomputed without them. The GUC
// predicate in the DELETE only passes when app.current_role/app.current_tenant
// were set in this transaction — on a bare pooled connection the predicate is
// NULL and the delete silently matches nothing (R38 fixed shape).
//
// 2026-10-20（迁移 890，v1 族整体退役）：这个 DELETE **没有等价物**，本函数
// 变成**空操作**（返回 0，不执行任何语句）。理由是三条，缺一条都会写错：
//
//  1. v1 族已删除。原来的目标表不存在 ⇒ 留着这条 DELETE 就是 42P01 ⇒
//     整个「重置」端点 500。
//  2. **不能**把目标换成会话族。会话族不是「请求日志」，它是**会话本体**：
//     一行 = 一轮对话。删一行 = 用户的会话少一轮（details / bodies 行变孤儿）。
//     v1 时代删的是派生日志行，代价是零；换成会话族就不是同一件事了。
//  3. 这个端点的语义是「原谅旧失败、让凭据恢复」，而成功率窗口
//     （recent_success_rate 的 3 小时）本来就只覆盖近期 —— 端点下面返回的
//     new_rate 仍是**当前真值**，运维拿到的信息没有丢。
//
// 保留函数与签名：调用点 admin/credential_success_rate.go 里的 handler 与
// 守形测试都直接引用它（不在本次改动的所有权范围内）。
func resetCredentialSuccessRateRows(ctx context.Context, db txBeginner, credentialID int64, rawModel string) (int64, error) {
	// v1 族退役 ⇒ 没有可删的请求日志行（理由见头注）。
	_ = ctx
	_ = db
	_ = credentialID
	_ = rawModel
	return 0, nil
}

// HandleResetCredentialSuccessRate manually resets success rate by deleting
// old failed requests for a (credential, model) pair.
// POST /api/admin/credential-success-rates/reset
// Body: {"credential_id": 17, "raw_model": "claude-sonnet-4-6"}
func HandleResetCredentialSuccessRate(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			CredentialID int64  `json:"credential_id"`
			RawModel     string `json:"raw_model"`
		}
		if err := readJSONRequired(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if req.CredentialID == 0 || req.RawModel == "" {
			http.Error(w, "credential_id and raw_model required", http.StatusBadRequest)
			return
		}

		// Delete failed requests older than 10 minutes to allow immediate recovery
		//
		// 2026-10-20（迁移 890）：v1 请求日志族整体退役 ⇒ 这次删除**没有等价物**：
		// 删的是派生日志行（代价为零），而会话族是会话本体（删一行 = 少一轮
		// 对话）。详见 resetCredentialSuccessRateRows 的头注。
		// ⇒ 本端点退化为「不删任何行、照常回读当前成功率」。
		deleted, err := resetCredentialSuccessRateRows(r.Context(), db, req.CredentialID, req.RawModel)
		if err != nil {
			writeInternalTextErr(w, "internal error (see server logs)", err)
			return
		}

		// Return new success rate
		var newRate *float64
		var newSamples *int
		err = db.QueryRow(context.Background(), `
		SELECT * FROM recent_success_rate($1, $2, 50, 3)
	`, req.CredentialID, req.RawModel).Scan(&newRate, &newSamples)
		if err != nil {
			writeInternalTextErr(w, "internal error (see server logs)", err)
			return
		}

		slog.Info("credential success rate reset",
			"credential_id", req.CredentialID,
			"raw_model", req.RawModel,
			"deleted", deleted,
			"new_rate", newRate,
			"new_samples", newSamples)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"deleted":     deleted,
			"new_rate":    newRate,
			"new_samples": newSamples,
			"success":     true,
			// 消息如实反映「没有删任何行」：v1 请求日志族已退役（迁移 890），
			// 会话族是唯一事实源、不会由本端点裁剪。JSON 的**键集合不变**。
			"message": "no request rows deleted: the request-log family was retired " +
				"(migration 890) and the session family is never pruned here — " +
				"the rate below is the current value",
		})
	}
}
