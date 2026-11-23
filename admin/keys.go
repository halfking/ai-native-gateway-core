package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kaixuan/llm-gateway-go/secret"
)

// limitField distinguishes JSON null (use tier/default), 0 (explicit unlimited),
// and positive integers (custom limits). All three limit fields must be present
// on PATCH /api/keys/:id/limits so the handler can always write a full row.
type limitField struct {
	set    bool
	isNull bool
	value  int
}

func (f *limitField) UnmarshalJSON(data []byte) error {
	f.set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		f.isNull = true
		return nil
	}
	var n int
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}
	f.value = n
	return nil
}

func (f limitField) sqlArg(max int, fieldName string) (any, error) {
	if !f.set {
		return nil, fmt.Errorf("%s is required", fieldName)
	}
	if f.isNull {
		return nil, nil
	}
	if f.value == 0 {
		return 0, nil
	}
	if f.value < 1 || f.value > max {
		return nil, fmt.Errorf("%s must be 0 (unlimited), or between 1 and %d", fieldName, max)
	}
	return f.value, nil
}

// keyLimitCaps 是创建（createKey）与更新（updateKeyLimits）两路**共用**的
// per-key 限流/预算上限帽。
//
// 2026-11-15（限流统一按 key 轮）：此前只有更新路径有帽，创建路径非负校验
// 后照单全收（keys 自助管理 212825977 审计登记的「budget/rpm 无上限帽」）
// ——自助入口可签出 rate_limit_rpm=0（显式无限）或任意大 budget 的 key，
// 两路口径不齐。现在创建与更新走同一组常量，数值可调归 owner。
// NULL（不设）语义不变——帽只约束显式给出的值。
const (
	maxKeyRPM        = 10000
	maxKeyConcurrent = 1000
	maxKeyTPM        = 1_000_000_000
	maxKeyBudgetUSD  = 100_000.0
)

type keyActionRoute struct {
	kind    string
	idPart  string
	subPath string
}

// keyConflictQuerier is the minimum surface area of *pgxpool.Pool needed by
// findActiveKeyConflict.  Defined as an interface so unit tests can supply a
// stub without spinning up Postgres.
type keyConflictQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// keyCreateQuerier 返回 createKey 的落库查询面：窄注入面 keyCreateDB 优先，nil ⇒ h.db
// （生产路径零变化）。开这个口子的理由见 Handler.keyCreateDB 字段注释。
func (h *Handler) keyCreateQuerier() keyConflictQuerier {
	if h.keyCreateDB != nil {
		return h.keyCreateDB
	}
	return h.db
}

// existingKeyConflict describes an api_keys row that already represents a
// "valid, active, non-expired" key for the (application, tenant, alias) group
// — i.e. one that the new key creation must refuse to duplicate.
type existingKeyConflict struct {
	ID       int
	Prefix   string
	IsSystem bool
}

// findActiveKeyConflict returns the lowest-id api_keys row matching the given
// (application_id, tenant_id, key_alias) group that is currently enabled,
// non-revoked, and not expired — *or* a system key regardless of the alias
// match.  Returns (nil, nil) when no conflict exists; only returns a non-nil
// error for unexpected DB failures (pgx.ErrNoRows is collapsed to "no
// conflict").
//
// The function is the single source of truth for the "is this createKey call
// going to duplicate an existing valid key?" check, and is intentionally
// conservative: any live system key in the same group blocks the creation
// even if its alias differs, because the frontend groups by (tenant, app,
// alias) and an attacker re-using an admin alias must not be able to slip
// past by giving an empty/different alias.
func findActiveKeyConflict(ctx context.Context, db keyConflictQuerier, appID int, tenantID, alias string) (*existingKeyConflict, error) {
	const sqlText = `
		SELECT id, key_prefix, COALESCE(is_system, FALSE)
		FROM api_keys
		WHERE application_id = $1
		  AND tenant_id = $2
		  AND COALESCE(key_alias, '') = COALESCE($3, '')
		  AND (
		      (enabled = TRUE
		        AND COALESCE(status, 'active') = 'active'
		        AND (expires_at IS NULL OR expires_at > now()))
		      OR (COALESCE(is_system, FALSE) = TRUE
		        AND enabled = TRUE
		        AND (expires_at IS NULL OR expires_at > now()))
		  )
		ORDER BY id ASC
		LIMIT 1
	`
	var c existingKeyConflict
	err := db.QueryRow(ctx, sqlText, appID, tenantID, alias).Scan(&c.ID, &c.Prefix, &c.IsSystem)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

const defaultApplicationTenant = "default"

// getOrCreateApplication returns the applications.id for (code, default tenant),
// auto-inserting a row when absent — mirrors Python _get_or_create_app().
func (h *Handler) getOrCreateApplication(ctx context.Context, code, ownerUser string) (int, error) {
	return h.getOrCreateApplicationForTenant(ctx, code, ownerUser, defaultApplicationTenant)
}

func (h *Handler) getOrCreateApplicationForTenant(ctx context.Context, code, ownerUser, tenantID string) (int, error) {
	return getOrCreateApplicationOn(ctx, h.db, code, ownerUser, tenantID)
}

// getOrCreateApplicationOn 是 getOrCreateApplicationForTenant 的本体，只是在给定的查询面上
// 执行（createKey 的落库面要能注入 pgxmock，见 keyCreateQuerier）。
func getOrCreateApplicationOn(ctx context.Context, q keyConflictQuerier, code, ownerUser, tenantID string) (int, error) {
	if code == "" {
		code = "default"
	}
	if ownerUser == "" {
		ownerUser = "admin"
	}

	var appID int
	err := q.QueryRow(ctx,
		`SELECT id FROM applications WHERE code = $1 AND tenant_id = $2`,
		code, tenantID,
	).Scan(&appID)
	if err == nil {
		return appID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}

	err = q.QueryRow(ctx, `
		INSERT INTO applications (tenant_id, code, display_name, owner_user)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, code) DO UPDATE SET code = EXCLUDED.code
		RETURNING id
	`, tenantID, code, code, ownerUser).Scan(&appID)
	if err != nil {
		return 0, err
	}
	slog.Info("auto-created application", "code", code, "id", appID)
	return appID, nil
}

func parseKeyActionRoute(remaining string) keyActionRoute {
	if remaining == "" {
		return keyActionRoute{kind: "root"}
	}

	switch remaining {
	case "verify", "budget-check", "apply", "lookup":
		return keyActionRoute{kind: "action", subPath: remaining}
	}

	idPart := remaining
	subPath := ""
	for i, c := range remaining {
		if c == '/' {
			idPart = remaining[:i]
			subPath = remaining[i+1:]
			break
		}
	}

	return keyActionRoute{kind: "resource", idPart: idPart, subPath: subPath}
}

func (h *Handler) handleKeys(w http.ResponseWriter, r *http.Request) {
	if !requireUserManager(w, r) {
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	stripPrefix := "/api/keys/"
	remaining := r.URL.Path[len(stripPrefix):]
	route := parseKeyActionRoute(remaining)

	if route.kind == "root" {
		switch r.Method {
		case http.MethodPost:
			h.createKey(w, r)
		case http.MethodGet:
			h.listKeys(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	switch route.subPath {
	case "verify":
		h.verifyKey(w, r)
		return
	case "budget-check":
		h.budgetCheck(w, r)
		return
	case "apply":
		if r.Method == http.MethodPost {
			h.adminApplyForKey(w, r)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	case "lookup":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.lookupKeyConflict(w, r)
		return
	}

	idStr := route.idPart
	subPath := route.subPath

	// Handle /api/keys/detail/{id} where idPart="detail" and subPath is the numeric ID
	if idStr == "detail" && subPath != "" {
		id, err := strconv.Atoi(subPath)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		h.getKeyDetail(w, r, id)
		return
	}

	switch subPath {
	case "reveal":
		id, err := strconv.Atoi(idStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		h.revealKey(w, r, id)
	case "approve":
		id, err := strconv.Atoi(idStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		h.approveKey(w, r, id)
	case "disable":
		id, err := strconv.Atoi(idStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		h.setKeyEnabled(w, r, id, false)
	case "enable":
		id, err := strconv.Atoi(idStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		h.setKeyEnabled(w, r, id, true)
	case "limits":
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		id, err := strconv.Atoi(idStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		h.updateKeyLimits(w, r, id)
	case "detail":
		// idPart is "detail", actual ID is in subPath (e.g. /api/keys/detail/146)
		if subPath != "" {
			id, err := strconv.Atoi(subPath)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid id")
				return
			}
			h.getKeyDetail(w, r, id)
			return
		}
		rest := ""
		for i, c := range idStr {
			if c == '/' {
				rest = idStr[i+1:]
				idStr = idStr[:i] //nolint:staticcheck // rebind for clarity, downstream uses 'rest' not 'idStr'
				break
			}
		}
		if rest != "" {
			id, err := strconv.Atoi(rest)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid id")
				return
			}
			h.getKeyDetail(w, r, id)
		} else {
			http.NotFound(w, r)
		}
	case "applications":
		if idStr != "" && r.Method == http.MethodPatch {
			h.patchApplicationProfile(w, r, idStr)
		} else {
			http.NotFound(w, r)
		}
	default:
		if subPath == "" {
			id, err := strconv.Atoi(idStr)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid id")
				return
			}
			switch r.Method {
			case http.MethodDelete:
				h.deleteKey(w, r, id)
			case http.MethodGet:
				h.getKeyDetail(w, r, id)
			case http.MethodPatch:
				h.patchKey(w, r, id)
			default:
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
		} else {
			http.NotFound(w, r)
		}
	}
}

func (h *Handler) handleKeysRoot(w http.ResponseWriter, r *http.Request) {
	if !requireUserManager(w, r) {
		return
	}
	// 2026-08-06: 与 handleKeys(带尾斜杠) 保持一致。无尾斜杠根路由
	// 此前缺少 nil-db 守卫，DB 不可用(no-DB 模式)时 listKeys 空指针 panic，
	// 恢复中间件将其转成 {"code":"panic"} 500。
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	switch r.Method {
	case http.MethodPost:
		h.createKey(w, r)
	case http.MethodGet:
		h.listKeys(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func resolveCreateKeyTenantID(r *http.Request, requestedTenantID string) string {
	requestedTenantID = strings.TrimSpace(requestedTenantID)
	if IsTenantAdmin(r) {
		return GetTenantID(r)
	}
	if requestedTenantID == "" {
		return "default"
	}
	return requestedTenantID
}

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ApplicationCode string   `json:"application_code"`
		TenantID        string   `json:"tenant_id"`
		KeyAlias        string   `json:"key_alias"`
		OwnerUser       *string  `json:"owner_user"`
		BudgetUSD       *float64 `json:"budget_usd"`
		RateLimitRPM    *int     `json:"rate_limit_rpm"`
		Remark          *string  `json:"remark"`
		// ★ 2026-11-23（12h 修订审计轮）：团队归属（W2-Q2）。
		// team_id 可选：NULL = 个人 key（存量行为零变化）；非 NULL = 团队 key。
		// ★ 为什么必须能写：迁移 057 建了 api_keys.team_id 列，
		//   `maas/team_budget_gate.go` 的团队花费聚合走
		//   `usage_facts JOIN api_keys ON ... JOIN teams ON t.id = ak.team_id`
		//   ⇒ **全仓没有任何代码写这一列** ⇒ 团队聚合恒为 0
		//   ⇒ 团队预算门永远判「未超限」⇒ 整条团队预算链是死的。
		//   写入这一列是让该链真正闭环的**唯一缺口**。
		TeamID *int64 `json:"team_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.ApplicationCode == "" {
		req.ApplicationCode = "default"
	}
	req.TenantID = resolveCreateKeyTenantID(r, req.TenantID)
	if req.TenantID == "" {
		writeError(w, http.StatusForbidden, "tenant scope unavailable")
		return
	}
	if (req.BudgetUSD != nil && *req.BudgetUSD < 0) || (req.RateLimitRPM != nil && *req.RateLimitRPM < 0) {
		writeError(w, http.StatusBadRequest, "budget and rate limit must be nonnegative")
		return
	}
	// 2026-11-15：与 updateKeyLimits 同一组帽（keyLimitCaps）——创建路径
	// 不再照单全收任意大的限流/预算值。
	if req.RateLimitRPM != nil && *req.RateLimitRPM > maxKeyRPM {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("rate_limit_rpm must be between 0 and %d", maxKeyRPM))
		return
	}
	if req.BudgetUSD != nil && *req.BudgetUSD > maxKeyBudgetUSD {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("budget_usd must be between 0 and %.0f", maxKeyBudgetUSD))
		return
	}
	// tenant_admin callers may only create keys for their own tenant.
	if IsTenantAdmin(r) && req.TenantID != GetTenantID(r) {
		writeError(w, http.StatusForbidden, "forbidden: cannot create key for another tenant")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// ★ 2026-11-23（12h 修订审计轮）：团队归属校验（W2-Q2）。
	//
	// ★ 判据（三条同时成立才允许挂团队）：
	//   ① team_id > 0；
	//   ② 该团队存在且**属于本租户**（teams.tenant_id = 本次 key 的 tenant_id）
	//      —— 迁移 055 的 teams 表带 tenant_id，且 (tenant_id, name) 唯一；
	//   ③ 调用方是该团队的 owner 或 user_teams 里的 admin/member
	//      —— teams.owner_all RLS 策略用的也是 owner_id 口径。
	//
	// ★ 为什么必须校验：api_keys.team_id 有 ON DELETE CASCADE 指向 teams(id)，
	//   若不校验租户归属，租户 A 的管理员就能把自己的 key 挂到租户 B 的团队上
	//   ⇒ 该团队预算门会把**别家租户的消费**算进本团队（与
	//   maas/team_budget_gate.go 里那条 "ak.tenant_id = t.tenant_id" 的
	//   跨租户防串条件同一族问题）。
	var teamID *int64
	if req.TeamID != nil && *req.TeamID > 0 {
		allowed, err := h.callerMayUseTeam(ctx, r, *req.TeamID, req.TenantID)
		if err != nil {
			slog.Error("createKey: team validation failed", "error", err, "team_id", *req.TeamID)
			writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "forbidden: cannot attach key to this team")
			return
		}
		tid := *req.TeamID
		teamID = &tid
	}

	ownerUser := "admin"
	if req.OwnerUser != nil && *req.OwnerUser != "" {
		ownerUser = *req.OwnerUser
	}
	// ★ 2026-11-23（211 建 key 全量 500 回归根修）：落库面走窄注入面 keyCreateQuerier()
	// （nil ⇒ h.db，生产路径零变化）。此前 createKey 的三条查询都直接摸具体的
	// *pgxpool.Pool ⇒ 单测注入不了 pgxmock ⇒ 「落库语句长什么样」没有任何东西在钉。
	db := h.keyCreateQuerier()
	appID, err := getOrCreateApplicationOn(ctx, db, req.ApplicationCode, ownerUser, req.TenantID)
	if err != nil {
		slog.Error("createKey: getOrCreateApplication failed", "error", err, "code", req.ApplicationCode)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	alias := req.KeyAlias
	existing, err := findActiveKeyConflict(ctx, db, appID, req.TenantID, alias)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if existing != nil {
		prefix := existing.Prefix
		if len(prefix) > 8 {
			prefix = prefix[:8]
		}
		if existing.IsSystem {
			writeError(w, http.StatusConflict, fmt.Sprintf(
				"该租户+应用+别名组合已存在有效的系统密钥 (id=%d)，请勿重复创建。如需新建，请先禁用或吊销现有系统密钥。",
				existing.ID,
			))
			return
		}
		writeError(w, http.StatusConflict, fmt.Sprintf(
			"该租户+应用+别名组合已有可用密钥 (id=%d, prefix=%s****)，请勿重复创建。如需新建，请先禁用或吊销现有密钥。",
			existing.ID, prefix,
		))
		return
	}

	remark := "admin manual creation"
	if req.Remark != nil && *req.Remark != "" {
		remark = *req.Remark
	}
	raw, keyHash, keyPrefix, keyCiphertext := h.generateAdminKey(h.secret)

	var id int
	// 2026-11-17 审计修复：owner_user 必须写**已解析后的** ownerUser（默认
	// 'admin'），不能写 req.OwnerUser 指针 —— 指针为 nil 时 pgx 传 NULL，
	// 而 owner_user 列可空 ⇒ 落库成 NULL。这类密钥：
	//   - 不出现在任何用户名下（owner_user = username 关联不上）
	//   - 不计入 GET /api/admin/users/{id}/stats 的 key_count
	//   - 在「用户明细 → API 密钥」里完全隐身
	// 即：调用方不传 owner_user 时，代码算出了 'admin' 却没有落库。
	//
	// ★ 2026-11-23（211 建 key 全量 500 回归根修）：个人 key（teamID == nil）的 INSERT
	// **不引用 team_id 列**。加这一列的迁移 057 尚未在 211 / 252 / 新装环境应用（投递链
	// 缺失，见 AGENTS.md L22）⇒ 无条件列出该列的语句在那些库上必然
	// `column "team_id" of relation "api_keys" does not exist (SQLSTATE 42703)` ⇒
	// POST /api/keys 全量 500（211 自 build_seq 3240 起实测）。列默认 NULL 与显式写 NULL
	// 等价 ⇒ 个人 key 不依赖迁移；只有团队 key 才引用该列（它的归属校验
	// callerMayUseTeam 本来就要查 teams 表，本来就更依赖迁移 055–057）。
	args := []any{appID, req.TenantID, keyHash, keyPrefix, keyCiphertext, ownerUser, req.BudgetUSD, req.RateLimitRPM, remark, alias}
	stmt := insertAPIKeySQL(false)
	if teamID != nil {
		stmt = insertAPIKeySQL(true)
		args = append(args, teamID)
	}
	err = db.QueryRow(ctx, stmt, args...).Scan(&id)
	if err != nil {
		slog.Error("createKey insert failed", "error", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	// ★ 2026-11-17（12h 审计）：补上 key_alias / remark。
	//   简易模式签发页（SimpleKeysView）按「客户端」「备注」两行展示这两个字段，
	//   前端 API 契约（KeyCreatedResponse）也声明了它们 ⇒ 但本响应从不发 ⇒
	//   前端读到 undefined ⇒ 「客户端」行永远空白、「备注」行永不渲染。
	//   数据本来就在手上（alias / remark 就是刚插入的那两个列）⇒ 直接回。
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         id,
		"api_key":    raw,
		"key_prefix": keyPrefix,
		"key_alias":  alias,
		"remark":     remark,
		"message":    "ok",
	})
}

// insertAPIKeySQL 是 createKey 的落库语句；withTeam 为真才带 team_id 列（第 11 个参数）。
//
// ★ 两条语句的列清单是钉测的判据（TestCreateKey_PersonalKeyInsertOmitsTeamColumn /
// TestCreateKey_TeamKeyInsertWritesTeamColumn）——个人 key 那条不得出现 team_id：它依赖
// 的迁移 057 在 211 / 252 / 新装环境都还没应用。要改列清单，先让那两条测试过。
func insertAPIKeySQL(withTeam bool) string {
	if withTeam {
		return `
		INSERT INTO api_keys (application_id, tenant_id, key_hash, key_prefix, key_ciphertext, owner_user, enabled, budget_usd, rate_limit_rpm, status, remark, key_alias, team_id)
		VALUES ($1, $2, $3, $4, $5, $6, TRUE, $7, $8, 'active', $9, $10, $11)
		RETURNING id
	`
	}
	return `
		INSERT INTO api_keys (application_id, tenant_id, key_hash, key_prefix, key_ciphertext, owner_user, enabled, budget_usd, rate_limit_rpm, status, remark, key_alias)
		VALUES ($1, $2, $3, $4, $5, $6, TRUE, $7, $8, 'active', $9, $10)
		RETURNING id
	`
}

// callerMayUseTeam 判断当前调用方是否有权把一张 api key 挂到指定团队上。
//
// ★ 2026-11-23（12h 修订审计轮）：补齐 api_keys.team_id 写入路径的最后一道闸。
//
// ★ 判据（四条同时成立才放行）：
//
//	① 调用方是一个真实登录用户（AuthContext.UserID > 0）——legacy admin key
//	   认证没有用户身份（UserID = 0），无法证明成员关系 ⇒ 不放行；
//	② 团队存在，且 teams.tenant_id = 本次 key 的 tenant_id（跨租户防串，
//	   与 maas/team_budget_gate.go 里 "ak.tenant_id = t.tenant_id" 同一族口径）；
//	③ 调用方是该团队的 owner（teams.owner_id）**或** user_teams 中的成员
//	   （role ∈ owner/admin/member）——迁移 056 的 teams_member_select 策略
//	   用的也是这个成员口径；
//	④ 以上判定与 RLS 可见行同口径（见下方 set_config 说明）。
//
// ★ 为什么这里要显式 set_config('app.current_user_id')：
//
//	teams / user_teams 都 ALTER TABLE ... ENABLE ROW LEVEL SECURITY，策略基于
//	current_setting('app.current_user_id', true)（迁移 055:24 / 056:24 / 057:16）。
//	本仓其它地方**从不设置**该 GUC（既有惯例是 app.current_tenant / app.current_role
//	/ app.bypass_rls，见 apihub/pg_store.go:744、domains/sessionaudit/approval_manager.go:521）
//	⇒ RLS 判定与 Go 侧显式校验各说各话。
//	这里按 apihub/pg_store.go:744 的既有范式在事务内设置它，让「RLS 看得见的行」
//	与「Go 侧判定的成员关系」是同一口径；若部署库用的是表属主连接（RLS 被绕过），
//	则本函数的显式校验就是唯一生效的闸。
func (h *Handler) callerMayUseTeam(ctx context.Context, r *http.Request, teamID int64, tenantID string) (bool, error) {
	auth := GetAuthContext(r)
	if auth == nil || auth.UserID <= 0 {
		return false, nil
	}
	userID := int64(auth.UserID)

	// 窄注入口优先（单测注入 pgxmock）；生产走 h.db。
	var pool teamGuardDB
	switch {
	case h.teamGuardDB != nil:
		pool = h.teamGuardDB
	case h.db != nil:
		pool = h.db
	default:
		return false, errors.New("database not configured")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_user_id', $1, true)",
		strconv.FormatInt(userID, 10),
	); err != nil {
		return false, err
	}

	var allowed bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM teams t
			WHERE t.id = $1
			  AND t.tenant_id = $2
			  AND (
			    t.owner_id = $3
			    OR EXISTS (
			      SELECT 1 FROM user_teams ut
			      WHERE ut.user_id = $3 AND ut.team_id = t.id
			    )
			  )
		)
	`, teamID, tenantID, userID).Scan(&allowed)
	if err != nil {
		return false, err
	}
	return allowed, nil
}

// lookupKeyConflict answers GET /api/keys/lookup?application_code=&tenant_id=&key_alias=
// with the active (or system) key already occupying that group, if any.
// It is a read-only probe intended for the Keys management UI: the frontend
// calls it while the user is typing the (tenant, application, alias) tuple
// of a new key, so the user can see WHICH key is in the way before clicking
// "签发".  The route is mounted under admin() in handler.go, so the request
// must present a valid admin api key (Authorization: Bearer …) — there is no
// anonymous / unauthenticated path to this endpoint.
//
// Response shape:
//   - 200 + {conflict: null} when no row matches the guard
//   - 200 + {conflict: {id, key_prefix, is_system, ...}} when a row matches
//   - 400 when alias is empty or application_code is missing
//   - 200 + {conflict: null} when application_code is unknown (no app ⇒ no keys)
func (h *Handler) lookupKeyConflict(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	appCode, tenantID, alias, vErr := extractLookupParams(r.URL.Query())
	if vErr != nil {
		writeError(w, http.StatusBadRequest, vErr.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var appID int
	err := h.db.QueryRow(ctx,
		`SELECT id FROM applications WHERE code = $1 AND tenant_id = $2`,
		appCode, defaultApplicationTenant,
	).Scan(&appID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, map[string]any{
				"conflict":         nil,
				"application_code": appCode,
				"tenant_id":        tenantID,
				"key_alias":        alias,
			})
			return
		}
		slog.Error("lookupKeyConflict: applications lookup failed", "error", err, "code", appCode)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	existing, err := findActiveKeyConflict(ctx, h.db, appID, tenantID, alias)
	if err != nil {
		slog.Error("lookupKeyConflict: findActiveKeyConflict failed", "error", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	if existing == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"conflict":         nil,
			"application_code": appCode,
			"tenant_id":        tenantID,
			"key_alias":        alias,
		})
		return
	}

	conflictResp := map[string]any{
		"id":         existing.ID,
		"key_prefix": existing.Prefix,
		"is_system":  existing.IsSystem,
		"status":     "active",
		"enabled":    true,
		"expires_at": nil,
		"owner_user": "",
	}

	var (
		status    *string
		enabled   *bool
		expiresAt *time.Time
		owner     *string
	)
	err = h.db.QueryRow(ctx, `
		SELECT COALESCE(status, 'active'), enabled, expires_at, owner_user
		FROM api_keys
		WHERE id = $1
	`, existing.ID).Scan(&status, &enabled, &expiresAt, &owner)
	if err != nil {
		slog.Warn("lookupKeyConflict: hydrate conflict row failed, using defaults", "error", err, "id", existing.ID)
	} else {
		conflictResp["status"] = derefStr(status)
		conflictResp["enabled"] = derefBool(enabled)
		conflictResp["expires_at"] = expiresAt
		conflictResp["owner_user"] = derefStr(owner)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"conflict":         conflictResp,
		"application_code": appCode,
		"tenant_id":        tenantID,
		"key_alias":        alias,
	})
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefBool(b *bool) bool {
	if b == nil {
		return false
	}
	return *b
}

// extractLookupParams pulls (application_code, tenant_id, key_alias) out of a
// url.Values, normalises whitespace, fills in the default tenant, and
// surfaces a descriptive error when a required field is missing.
//
// Kept as a pure function so the validation contract is unit-testable
// without spinning up Postgres.
func extractLookupParams(q url.Values) (appCode, tenantID, alias string, err error) {
	appCode = strings.TrimSpace(q.Get("application_code"))
	tenantID = strings.TrimSpace(q.Get("tenant_id"))
	alias = strings.TrimSpace(q.Get("key_alias"))

	switch {
	case appCode == "":
		return "", "", "", errors.New("application_code is required")
	case alias == "":
		return "", "", "", errors.New("key_alias is required")
	}
	if tenantID == "" {
		tenantID = "default"
	}
	return appCode, tenantID, alias, nil
}

func includeRevokedKeys(r *http.Request) bool {
	return strings.EqualFold(queryString(r, "include_revoked"), "true")
}

// assertKeyTenantScope verifies that, for tenant_admin callers, the given
// api_keys row belongs to the caller's own tenant. Super admins and legacy
// admin keys pass through unrestricted. Returns true when access is allowed;
// on denial it writes the appropriate 403/404 response and returns false, so
// callers should `return` immediately on false.
func (h *Handler) assertKeyTenantScope(w http.ResponseWriter, r *http.Request, id int) bool {
	if !IsTenantAdmin(r) {
		return true // super_admin or legacy - no tenant restriction
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	var tenantID string
	err := h.db.QueryRow(ctx, `SELECT tenant_id FROM api_keys WHERE id = $1`, id).Scan(&tenantID)
	if err != nil {
		writeLookupErr(w, "key not found", err)
		return false
	}
	if tenantID != GetTenantID(r) {
		writeError(w, http.StatusForbidden, "forbidden: key belongs to another tenant")
		return false
	}
	return true
}

func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	appCode := queryString(r, "application")
	tenantFilter := queryString(r, "tenant")
	// 2026-11-17 审计修复：新增 owner 过滤（按 api_keys.owner_user 精确匹配）。
	// 之前只有 application/tenant 两个过滤口，「用户明细 → API 密钥」这类
	// 场景只能把**全站**密钥拉回浏览器再前端筛 —— super_admin 侧等于把
	// 其它租户的密钥元数据（owner_user / 前缀 / 用量 / 备注）下发到客户端。
	// 服务端过滤同时修掉「无谓下载全表」的性能问题。
	ownerFilter := queryString(r, "owner")
	includeRevoked := includeRevokedKeys(r)
	// tenant_admin callers may only ever see their own tenant's keys.
	if IsTenantAdmin(r) {
		tenantFilter = GetTenantID(r)
	}
	rows, err := h.db.Query(ctx, `
		SELECT ak.id, ak.key_prefix, ak.owner_user, ak.enabled,
		       COALESCE(ak.status, 'active') AS status,
		       ak.expires_at,
		       ak.budget_usd::float8, ak.rate_limit_rpm,
		       app.code AS application_code,
		       COALESCE(ak.is_system, FALSE) AS is_system,
		       COALESCE(ak.default_client_profile, app.default_client_profile),
		       ak.last_used_at, ak.remark,
		       ak.total_requests, ak.total_prompt_tokens, ak.total_completion_tokens,
		       COALESCE(ak.total_cost_usd, 0)::float8, ak.last_request_at,
		       ak.tenant_id, ak.key_alias, ak.created_at
		FROM api_keys ak
		JOIN applications app ON app.id = ak.application_id
		WHERE ($1 = '' OR app.code = $1)
		  AND ($2 = '' OR ak.tenant_id = $2)
		  AND ($3 = '' OR ak.owner_user = $3)
		  AND ($4 OR COALESCE(ak.status, 'active') <> 'revoked')
		ORDER BY ak.id DESC
	`, appCode, tenantFilter, ownerFilter, includeRevoked)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type key struct {
		ID                    int        `json:"id"`
		KeyPrefix             string     `json:"key_prefix"`
		OwnerUser             *string    `json:"owner_user"`
		Enabled               bool       `json:"enabled"`
		Status                string     `json:"status"`
		ExpiresAt             *time.Time `json:"expires_at"`
		BudgetUSD             *float64   `json:"budget_usd"`
		RateLimitRPM          *int       `json:"rate_limit_rpm"`
		ApplicationCode       string     `json:"application_code"`
		IsSystem              bool       `json:"is_system"`
		DefaultClientProfile  *string    `json:"default_client_profile"`
		LastUsedAt            *time.Time `json:"last_used_at"`
		Remark                *string    `json:"remark"`
		TotalRequests         int64      `json:"total_requests"`
		TotalPromptTokens     int64      `json:"total_prompt_tokens"`
		TotalCompletionTokens int64      `json:"total_completion_tokens"`
		TotalCostUSD          float64    `json:"total_cost_usd"`
		LastRequestAt         *time.Time `json:"last_request_at"`
		TenantID              string     `json:"tenant_id"`
		KeyAlias              *string    `json:"key_alias"`
		CreatedAt             *time.Time `json:"created_at"`
	}
	keys := make([]key, 0)
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.ID, &k.KeyPrefix, &k.OwnerUser, &k.Enabled,
			&k.Status, &k.ExpiresAt,
			&k.BudgetUSD, &k.RateLimitRPM, &k.ApplicationCode,
			&k.IsSystem, &k.DefaultClientProfile,
			&k.LastUsedAt, &k.Remark,
			&k.TotalRequests, &k.TotalPromptTokens, &k.TotalCompletionTokens,
			&k.TotalCostUSD, &k.LastRequestAt,
			&k.TenantID, &k.KeyAlias, &k.CreatedAt); err != nil {
			warnRowSkip("keys.list", err)
			continue
		}
		keys = append(keys, k)
	}
	if writeAggRowsErr(w, "keys.list", rows.Err()) {
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

func (h *Handler) deleteKey(w http.ResponseWriter, r *http.Request, id int) {
	if !h.assertKeyTenantScope(w, r, id) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	cmd, err := h.db.Exec(ctx, `
		UPDATE api_keys
		SET status = 'revoked', enabled = FALSE
		WHERE id = $1 AND COALESCE(status, 'active') <> 'revoked'
	`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	if cmd.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	h.invalidateKeyCache(id)
	writeJSON(w, http.StatusOK, map[string]string{"message": "revoked"})
}

func (h *Handler) revealKey(w http.ResponseWriter, r *http.Request, id int) {
	if !h.assertKeyTenantScope(w, r, id) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var ciphertext string
	err := h.db.QueryRow(ctx, `
		SELECT key_ciphertext FROM api_keys
		WHERE id = $1 AND COALESCE(status, 'active') <> 'revoked'
	`, id).Scan(&ciphertext)
	if err != nil {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	if ciphertext == "" {
		writeError(w, http.StatusNotFound, "no ciphertext stored")
		return
	}
	if !isRevealableKeyCiphertext(ciphertext) {
		writeError(w, http.StatusConflict, "no ciphertext stored")
		return
	}

	plaintext, err := h.decryptCredStr(ciphertext)
	if err != nil {
		slog.Warn("revealKey: decryption failed", "key_id", id, "error", err)
		writeError(w, http.StatusConflict, "no ciphertext stored")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key_id": id, "api_key": plaintext})
	// 与 credential.secret_revealed 对称（三十七轮审计 P2）：API key 明文
	// reveal 此前零审计，取证链断裂。审计写在响应之后（同凭据 reveal 口径）。
	h.writeAuditLog(r, "apikey.secret_revealed", "api_key", id, nil)
}

// isRevealableKeyCiphertext returns false for empty or placeholder values that
// are neither v1 AES-GCM envelopes nor plausible Fernet tokens (e.g. probe rows
// with key_ciphertext='test-cipher' that would otherwise fail decrypt).
func isRevealableKeyCiphertext(ciphertext string) bool {
	if ciphertext == "" {
		return false
	}
	if secret.IsV1Envelope(ciphertext) {
		return true
	}
	// Fernet wire tokens are base64url; shortest valid token is ~57 bytes.
	return len(ciphertext) >= 57
}

func (h *Handler) approveKey(w http.ResponseWriter, r *http.Request, id int) {
	if !h.assertKeyTenantScope(w, r, id) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	//nolint:errcheck // best-effort exec, non-critical
	h.db.Exec(ctx, `UPDATE api_keys SET enabled = TRUE WHERE id = $1`, id)
	h.invalidateKeyCache(id)
	writeJSON(w, http.StatusOK, map[string]string{"message": "approved"})
}

func (h *Handler) setKeyEnabled(w http.ResponseWriter, r *http.Request, id int, enabled bool) {
	if !h.assertKeyTenantScope(w, r, id) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	// 2026-11-17 审计修复：**启用**被吊销的密钥原本是「静默失败」。
	// 事实：请求路径的鉴权谓词是
	//   AND ak.enabled = TRUE AND COALESCE(ak.status,'active') NOT IN ('revoked','disabled')
	//   （domains/authentication/verifier.go 的 callVerifyDB）
	// 而本函数只写 enabled、**不写 status** ⇒ 对 status='revoked' 的密钥
	// 「启用」把 enabled 置回 TRUE 后，status 仍是 'revoked' ⇒ 认证照样拒绝。
	// 前端却收到 200 + "updated" 并把徽章显示成「已启用」⇒ 管理员以为修好了，
	// 密钥实际上仍然不可用（无声的假成功）。
	// 修法：启用时若 status='revoked' ⇒ 明确 409 拒绝并说明原因；
	// （'revoked' 语义是「不可撤销」，故不自动改写 status，只是不再假装成功。）
	if enabled {
		var status string
		err := h.db.QueryRow(ctx,
			`SELECT COALESCE(status, 'active') FROM api_keys WHERE id = $1`, id).Scan(&status)
		if err != nil {
			writeLookupErr(w, "key not found", err)
			return
		}
		if status == "revoked" {
			writeError(w, http.StatusConflict,
				"cannot enable a revoked key: revoked is a terminal state, please issue a new key instead")
			return
		}
	}
	if _, err := h.db.Exec(ctx, `UPDATE api_keys SET enabled = $1 WHERE id = $2`, enabled, id); err != nil {
		writeInternalErr(w, "update failed", err)
		return
	}
	h.invalidateKeyCache(id)
	writeJSON(w, http.StatusOK, map[string]string{"message": "updated"})
}

func (h *Handler) updateKeyLimits(w http.ResponseWriter, r *http.Request, id int) {
	if !h.assertKeyTenantScope(w, r, id) {
		return
	}
	var body struct {
		RateLimitRPM        limitField `json:"rate_limit_rpm"`
		RateLimitConcurrent limitField `json:"rate_limit_concurrent"`
		RateLimitTPM        limitField `json:"rate_limit_tpm"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	// 帽值与创建路径共用（keyLimitCaps，2026-11-15 统一口径）。
	rpmArg, err := body.RateLimitRPM.sqlArg(maxKeyRPM, "rate_limit_rpm")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	concurrentArg, err := body.RateLimitConcurrent.sqlArg(maxKeyConcurrent, "rate_limit_concurrent")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tpmArg, err := body.RateLimitTPM.sqlArg(maxKeyTPM, "rate_limit_tpm")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var exists bool
	if err := h.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM api_keys WHERE id = $1)`, id).Scan(&exists); err != nil {
		writeError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "api_key not found")
		return
	}

	if _, err := h.db.Exec(ctx, `
		UPDATE api_keys
		SET rate_limit_rpm = $1,
		    rate_limit_concurrent = $2,
		    rate_limit_tpm = $3
		WHERE id = $4
	`, rpmArg, concurrentArg, tpmArg, id); err != nil {
		writeInternalErr(w, "update failed", err)
		return
	}

	// Drop the cached KeyInfo so EffectiveRPM/EffectiveConcurrent pick up the
	// new values on the next request instead of after the 60s TTL. Also lets
	// the credential Limiter resize this key's per-key semaphore (see
	// Limiter.Key).
	h.invalidateKeyCache(id)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":                "ok",
		"id":                    id,
		"rate_limit_rpm":        rpmArg,
		"rate_limit_concurrent": concurrentArg,
		"rate_limit_tpm":        tpmArg,
	})
}

func (h *Handler) verifyKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		APIKey string `json:"api_key"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	keyHash := hashAPIKey(h.secret, req.APIKey)
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	var result struct {
		Valid                bool     `json:"valid"`
		KeyID                *int     `json:"key_id,omitempty"`
		TenantID             *string  `json:"tenant_id,omitempty"`
		ApplicationID        *int     `json:"application_id,omitempty"`
		ApplicationCode      *string  `json:"application_code,omitempty"`
		DefaultClientProfile *string  `json:"default_client_profile,omitempty"`
		OwnerUser            *string  `json:"owner_user,omitempty"`
		RateLimitRPM         *int     `json:"rate_limit_rpm,omitempty"`
		BudgetUSD            *float64 `json:"budget_usd,omitempty"`
		KeyAlias             *string  `json:"key_alias,omitempty"`
	}
	var id, appID int
	var tenantID, appCode string
	var dcp, owner, keyAlias *string
	var rpm *int
	var budget *float64
	// R46 multi-tenant: tenant_admin callers may only verify keys in their own
	// tenant. Without this scope, a tenant_admin who obtains another tenant's
	// raw key could learn its tenant_id/application/owner metadata. Super_admin
	// and legacy admin_key verify across all tenants.
	query := `
		SELECT ak.id, ak.tenant_id, ak.application_id, app.code,
		       COALESCE(ak.default_client_profile, app.default_client_profile), ak.owner_user,
		       ak.rate_limit_rpm, ak.budget_usd, ak.key_alias
		FROM api_keys ak
		JOIN applications app ON app.id = ak.application_id
		WHERE ak.key_hash = $1 AND ak.enabled = TRUE
		  AND COALESCE(ak.status, 'active') <> 'revoked'
		  AND (ak.expires_at IS NULL OR ak.expires_at > now())
	`
	args := []any{keyHash}
	if IsTenantAdmin(r) {
		query += ` AND ak.tenant_id = $2`
		args = append(args, GetTenantID(r))
	}
	err := h.db.QueryRow(ctx, query, args...).Scan(&id, &tenantID, &appID, &appCode, &dcp, &owner, &rpm, &budget, &keyAlias)
	if err == nil {
		result.Valid = true
		result.KeyID = &id
		result.TenantID = &tenantID
		result.ApplicationID = &appID
		result.ApplicationCode = &appCode
		result.DefaultClientProfile = dcp
		result.OwnerUser = owner
		result.RateLimitRPM = rpm
		result.BudgetUSD = budget
		result.KeyAlias = keyAlias
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `UPDATE api_keys SET last_used_at = now() WHERE id = $1`, id)
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) budgetCheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		APIKeyID int `json:"api_key_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// 租户围栏与 verifyKey（本文件 R46 形态）一致：tenant_admin 只能查本
	// 租户；super_admin/admin_key 放行全租户。旧实现硬编码 tenant_id=
	// 'default'——AdminMiddleware 只验 JWT 不验角色，任意租户的 tenant_admin
	// 可用数字 ID 枚举 default 租户任意 key 的预算与实时花费（跨租户泄露
	// 面），非 default 租户自己的 key 反而一律 404（第三十轮收口）。
	budgetQuery := `
		SELECT budget_usd FROM api_keys
		WHERE id = $1 AND COALESCE(status, 'active') <> 'revoked'`
	budgetArgs := []any{req.APIKeyID}
	if IsTenantAdmin(r) {
		budgetQuery += ` AND tenant_id = $2`
		budgetArgs = append(budgetArgs, GetTenantID(r))
	}
	var budgetUSD *float64
	err := h.db.QueryRow(ctx, budgetQuery, budgetArgs...).Scan(&budgetUSD)
	if err != nil {
		writeError(w, http.StatusNotFound, "api_key not found")
		return
	}

	// 花费必须从 usage_ledger_with_current_month（= hot ∪ 已 promote 的月分区）
	// 读取：两条台账写方都直接写 hot、promote 按批调度，读裸 usage_ledger
	// 父表意味着任何时刻都存在一个滚动的 7 天盲区，恰好覆盖预算闸门最该
	// 起作用的窗口（R89-133 实证：最近 8h 裸父表 0 行、hot 2,162 行）。
	// DISTINCT ON 与对账读路径（pg_reconciliation_store）同款：防 promote
	// 异常形态在 hot 与父表残留同一 request_id 的双份，取 ts 最新的一份。
	var spent float64
	err = h.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(cost_usd), 0) FROM (
			SELECT DISTINCT ON (request_id) cost_usd
			FROM usage_ledger_with_current_month
			WHERE api_key_id = $1
			ORDER BY request_id, ts DESC
		) deduped`, req.APIKeyID).Scan(&spent)
	if err != nil {
		// 预算闸门必须 fail-closed：查询失败时 spent 保持零值会被下游当成
		// 「没超预算」放行并回 200——DB 故障的语义是「不知道超没超」，
		// 不是「没超」（R89-133 缺陷 B）。
		//
		// 第八轮只动「哪一种失败可诊断」，fail-closed 这一条一个字没改。
		status, code, detail := classifyBudgetSpendErr(err)
		if code == "" {
			writeError(w, status, detail)
			return
		}
		// 42P01 额外走服务端日志通道，与 admin 包内其余三门一致
		// （此前此处只有客户端响应，服务端无痕）。
		ReportMissingRelation(slog.Default(), "keys budget-check", err)
		writeErrorWithCode(w, status, code, detail)
		return
	}

	exceeded := budgetUSD != nil && spent >= *budgetUSD
	var remainingUSD *float64
	if budgetUSD != nil {
		r := *budgetUSD - spent
		remainingUSD = &r
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"api_key_id":    req.APIKeyID,
		"budget_usd":    budgetUSD,
		"spent_usd":     spent,
		"remaining_usd": remainingUSD,
		"exceeded":      exceeded,
	})
}

// classifyBudgetSpendErr 把「花费查询失败」分类成响应形状。
//
// 2026-10-03（第八轮）：此前一切错误统一 500 "usage query failed"，
// 把两件对处置方式完全不同的事混成一件——usage_ledger 视图压根没初始化
// （可修复：补迁移 344）与数据库真的挂了。运维从响应无法区分该做什么。
// 现在沿用 admin 包内既有的三门约定（aggregate_read_guard.go writeAggRowsErr /
// session_analytics_mv_guard.go writeAnalyticsQueryErr 都是 42P01 → 503）。
//
// 刻意做成**纯函数**（不碰 ResponseWriter、不碰 DB）：分类逻辑本身必须能在
// 没有 LLM_GATEWAY_PG_URL 的环境下被真实验证。集成测试（keys_budget_test.go
// 靠改名视图触发真 42P01）在没设该环境变量时是 SKIP —— 也就是说**只靠集成
// 测试，这一层分类等于没人验**。两层都要留着。
//
// ⚠ 引导文案**故意不复用** analyticsViewMigrationHint：那个把迁移号写死成 357
// （session-analytics 族），而本查询缺的是 344。抄过来会给出错的迁移号 ——
// 那比不给更坏：运维照着 357 跑完，视图仍然缺，下一轮更难查。
func classifyBudgetSpendErr(err error) (status int, code, detail string) {
	if IsMissingRelationError(err) {
		return http.StatusServiceUnavailable, "usage_ledger_view_missing",
			"usage_ledger 视图 " + ExtractMissingRelationName(err) +
				" 不存在（迁移 344 未执行），请先应用 344_usage_ledger_hot_independence.sql。" +
				"注意：本端点 fail-closed（回 503，不产出任何预算数字），" +
				"但认证面预算闸门在同条件下是 fail-open（请求会被放行、不拦支出），" +
				"见 domains/authentication/verifier.go"
	}
	// 其余（连接断/超时/42703 等）维持 500 原语义。
	return http.StatusInternalServerError, "", "usage query failed"
}

func (h *Handler) getKeyDetail(w http.ResponseWriter, r *http.Request, id int) {
	if !h.assertKeyTenantScope(w, r, id) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	includeRevoked := includeRevokedKeys(r)

	var k struct {
		ID                  int      `json:"id"`
		KeyPrefix           string   `json:"key_prefix"`
		OwnerUser           *string  `json:"owner_user"`
		Enabled             bool     `json:"enabled"`
		BudgetUSD           *float64 `json:"budget_usd"`
		RateLimitRPM        *int     `json:"rate_limit_rpm"`
		RateLimitConcurrent *int     `json:"rate_limit_concurrent"`
		RateLimitTPM        *int     `json:"rate_limit_tpm"`
		ApplicationCode     string   `json:"application_code"`
		TenantID            string   `json:"tenant_id"`
		KeyAlias            *string  `json:"key_alias"`
	}
	err := h.db.QueryRow(ctx, `
		SELECT ak.id, COALESCE(ak.key_prefix,''), ak.owner_user, ak.enabled,
		       ak.budget_usd::float8, ak.rate_limit_rpm,
		       ak.rate_limit_concurrent, ak.rate_limit_tpm,
		       app.code, ak.tenant_id, ak.key_alias
		FROM api_keys ak
		JOIN applications app ON app.id = ak.application_id
		WHERE ak.id = $1
		  AND ($2 OR COALESCE(ak.status, 'active') <> 'revoked')
	`, id, includeRevoked).Scan(&k.ID, &k.KeyPrefix, &k.OwnerUser, &k.Enabled,
		&k.BudgetUSD, &k.RateLimitRPM,
		&k.RateLimitConcurrent, &k.RateLimitTPM,
		&k.ApplicationCode, &k.TenantID, &k.KeyAlias)
	if err != nil {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	writeJSON(w, http.StatusOK, k)
}

func (h *Handler) handleKeyApplicationsList(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.listKeyApplications(w, r)
	} else {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleKeyApplications(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	remaining := r.URL.Path[len("/api/key-applications/"):]
	if remaining == "" {
		if r.Method == http.MethodGet {
			h.listKeyApplications(w, r)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	idStr := remaining
	subPath := ""
	for i, c := range remaining {
		if c == '/' {
			idStr = remaining[:i]
			subPath = remaining[i+1:]
			break
		}
	}
	appID, err := uuid.Parse(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid application id")
		return
	}

	switch subPath {
	case "approve":
		if r.Method == http.MethodPost {
			h.approveKeyApplication(w, r, appID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "reject":
		if r.Method == http.MethodPost {
			h.rejectKeyApplication(w, r, appID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	default:
		if r.Method == http.MethodGet {
			h.getKeyApplication(w, r, appID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func (h *Handler) listKeyApplications(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	statusFilter := queryString(r, "status")
	var rows pgx.Rows
	var err error
	if statusFilter != "" {
		rows, err = h.db.Query(ctx, `
			SELECT id::text, client_ip::text, COALESCE(contact,''), COALESCE(purpose,''),
			       status, issued_key_id, COALESCE(admin_notes,''), COALESCE(reviewed_by,''),
			       reviewed_at, created_at, expires_at
			FROM key_applications WHERE status = $1
			ORDER BY created_at DESC LIMIT 500
		`, statusFilter)
	} else {
		rows, err = h.db.Query(ctx, `
			SELECT id::text, client_ip::text, COALESCE(contact,''), COALESCE(purpose,''),
			       status, issued_key_id, COALESCE(admin_notes,''), COALESCE(reviewed_by,''),
			       reviewed_at, created_at, expires_at
			FROM key_applications
			ORDER BY created_at DESC LIMIT 500
		`)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type app struct {
		ID          string     `json:"id"`
		ClientIP    string     `json:"client_ip"`
		Contact     string     `json:"contact"`
		Purpose     string     `json:"purpose"`
		Status      string     `json:"status"`
		IssuedKeyID *int       `json:"issued_key_id"`
		AdminNotes  string     `json:"admin_notes"`
		ReviewedBy  string     `json:"reviewed_by"`
		ReviewedAt  *time.Time `json:"reviewed_at"`
		CreatedAt   *time.Time `json:"created_at"`
		ExpiresAt   *time.Time `json:"expires_at"`
	}
	apps := make([]app, 0)
	for rows.Next() {
		var a app
		if err := rows.Scan(&a.ID, &a.ClientIP, &a.Contact, &a.Purpose,
			&a.Status, &a.IssuedKeyID, &a.AdminNotes, &a.ReviewedBy,
			&a.ReviewedAt, &a.CreatedAt, &a.ExpiresAt); err != nil {
			warnRowSkip("keys.listApplications", err)
			continue
		}
		apps = append(apps, a)
	}
	if writeAggRowsErr(w, "keys.listApplications", rows.Err()) {
		return
	}
	writeJSON(w, http.StatusOK, apps)
}

func (h *Handler) getKeyApplication(w http.ResponseWriter, r *http.Request, appID uuid.UUID) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var a struct {
		ID          string     `json:"id"`
		ClientIP    string     `json:"client_ip"`
		Contact     string     `json:"contact"`
		Purpose     string     `json:"purpose"`
		Status      string     `json:"status"`
		IssuedKeyID *int       `json:"issued_key_id"`
		AdminNotes  string     `json:"admin_notes"`
		ReviewedBy  string     `json:"reviewed_by"`
		ReviewedAt  *time.Time `json:"reviewed_at"`
		CreatedAt   *time.Time `json:"created_at"`
		ExpiresAt   *time.Time `json:"expires_at"`
	}
	err := h.db.QueryRow(ctx, `
		SELECT id::text, client_ip::text, COALESCE(contact,''), COALESCE(purpose,''),
		       status, issued_key_id, COALESCE(admin_notes,''), COALESCE(reviewed_by,''),
		       reviewed_at, created_at, expires_at
		FROM key_applications WHERE id = $1
	`, appID.String()).Scan(&a.ID, &a.ClientIP, &a.Contact, &a.Purpose,
		&a.Status, &a.IssuedKeyID, &a.AdminNotes, &a.ReviewedBy,
		&a.ReviewedAt, &a.CreatedAt, &a.ExpiresAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "application not found")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (h *Handler) approveKeyApplication(w http.ResponseWriter, r *http.Request, appID uuid.UUID) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var status, contact string
	err := h.db.QueryRow(ctx, `SELECT status, COALESCE(contact,'') FROM key_applications WHERE id = $1::uuid`, appID.String()).Scan(&status, &contact)
	if err != nil {
		writeLookupErr(w, "application not found", err)
		return
	}
	if status != "pending" {
		writeError(w, http.StatusConflict, "can only approve 'pending' applications, current: "+status)
		return
	}

	var dbAppID int
	err = h.db.QueryRow(ctx, `SELECT id FROM applications WHERE code = 'applicant' AND tenant_id = 'default'`).Scan(&dbAppID)
	if err != nil {
		err = h.db.QueryRow(ctx, `
			INSERT INTO applications (tenant_id, code, display_name, owner_user)
			VALUES ('default', 'applicant', '自助申请', 'admin') RETURNING id
		`).Scan(&dbAppID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create applicant application")
			return
		}
	}

	var req struct {
		AdminNotes *string `json:"admin_notes"`
		ReviewedBy *string `json:"reviewed_by"`
	}
	//nolint:errcheck // best-effort
	readJSON(r, &req)
	notes := ""
	if req.AdminNotes != nil {
		notes = *req.AdminNotes
	}
	reviewer := "admin"
	if req.ReviewedBy != nil {
		reviewer = *req.ReviewedBy
	}
	noteSummary := notes
	if len(noteSummary) > 200 {
		noteSummary = noteSummary[:200]
	}
	remark := fmt.Sprintf("approved by %s: %s", reviewer, noteSummary)

	// Check for existing active key for this applicant (prevent duplicate)
	var existingKeyID int
	var existingPrefix string
	err = h.db.QueryRow(ctx, `
		SELECT id, key_prefix FROM api_keys
		WHERE application_id = $1 AND tenant_id = 'default'
		  AND enabled = TRUE AND COALESCE(status, 'active') = 'active'
		  AND (expires_at IS NULL OR expires_at > now())
		ORDER BY id ASC LIMIT 1
	`, dbAppID).Scan(&existingKeyID, &existingPrefix)
	if err == nil {
		writeError(w, http.StatusConflict, fmt.Sprintf(
			"该申请人的应用已有可用密钥 (id=%d, prefix=%s****)，请勿重复签发。如需新建，请先禁用或吊销现有密钥。",
			existingKeyID, existingPrefix[:8],
		))
		return
	}

	_, keyHash, keyPrefix, keyCiphertext := h.generateAdminKey(h.secret)
	var keyID int
	err = h.db.QueryRow(ctx, `
		INSERT INTO api_keys (application_id, tenant_id, key_hash, key_prefix, key_ciphertext, owner_user, enabled, status, remark)
		VALUES ($1, 'default', $2, $3, $4, $5, TRUE, 'active', $6) RETURNING id
	`, dbAppID, keyHash, keyPrefix, keyCiphertext, contact, remark).Scan(&keyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create key")
		return
	}

	//nolint:errcheck // best-effort exec, non-critical
	h.db.Exec(ctx, `
		UPDATE key_applications
		SET status = 'approved', issued_key_id = $1, admin_notes = $2, reviewed_by = $3, reviewed_at = now(), updated_at = now()
		WHERE id = $4::uuid
	`, keyID, notes, reviewer, appID.String())

	writeJSON(w, http.StatusOK, map[string]any{
		"application_id": appID.String(),
		"status":         "approved",
		"key_id":         keyID,
		"key_prefix":     keyPrefix,
		"message":        fmt.Sprintf("Key issued (prefix=%s). Retrieve full key via GET /api/keys/%d/reveal", keyPrefix, keyID),
	})
}

func (h *Handler) rejectKeyApplication(w http.ResponseWriter, r *http.Request, appID uuid.UUID) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var status string
	err := h.db.QueryRow(ctx, `SELECT status FROM key_applications WHERE id = $1::uuid`, appID.String()).Scan(&status)
	if err != nil {
		writeLookupErr(w, "application not found", err)
		return
	}
	if status != "pending" {
		writeError(w, http.StatusConflict, "can only reject 'pending' applications, current: "+status)
		return
	}

	var req struct {
		AdminNotes *string `json:"admin_notes"`
		ReviewedBy *string `json:"reviewed_by"`
	}
	//nolint:errcheck // best-effort
	readJSON(r, &req)
	notes := ""
	if req.AdminNotes != nil {
		notes = *req.AdminNotes
	}
	reviewer := "admin"
	if req.ReviewedBy != nil {
		reviewer = *req.ReviewedBy
	}

	//nolint:errcheck // best-effort exec, non-critical
	h.db.Exec(ctx, `
		UPDATE key_applications
		SET status = 'rejected', admin_notes = $1, reviewed_by = $2, reviewed_at = now(), updated_at = now()
		WHERE id = $3::uuid
	`, notes, reviewer, appID.String())

	writeJSON(w, http.StatusOK, map[string]any{
		"application_id": appID.String(),
		"status":         "rejected",
		"message":        "Application rejected",
	})
}

func (h *Handler) adminApplyForKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ApplicationCode string  `json:"application_code"`
		OwnerUser       *string `json:"owner_user"`
		Description     *string `json:"description"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.ApplicationCode == "" {
		req.ApplicationCode = "default"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	owner := "applicant"
	if req.OwnerUser != nil && *req.OwnerUser != "" {
		owner = *req.OwnerUser
	}
	appID, err := h.getOrCreateApplication(ctx, req.ApplicationCode, owner)
	if err != nil {
		slog.Error("adminApplyForKey: getOrCreateApplication failed", "error", err, "code", req.ApplicationCode)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	_, keyHash, keyPrefix, keyCiphertext := h.generateAdminKey(h.secret)
	var keyID int
	err = h.db.QueryRow(ctx, `
		INSERT INTO api_keys (application_id, tenant_id, key_hash, key_prefix, key_ciphertext, owner_user, enabled, status)
		VALUES ($1, 'default', $2, $3, $4, $5, FALSE, 'pending') RETURNING id
	`, appID, keyHash, keyPrefix, keyCiphertext, owner).Scan(&keyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create key")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":               keyID,
		"key_prefix":       keyPrefix,
		"application_code": req.ApplicationCode,
		"status":           "pending",
		"message":          "Key application submitted. Please wait for admin approval.",
	})
}

func (h *Handler) patchApplicationProfile(w http.ResponseWriter, r *http.Request, code string) {
	// R46 multi-tenant: this mutates the platform-default tenant's application
	// profile (tenant_id='default'), which is a super-admin-only operation.
	// The route is mounted under admin() (tenant_admin reachable), so an
	// explicit role gate is required to prevent a tenant_admin from changing
	// the default tenant's default_client_profile (cross-tenant write).
	if IsTenantAdmin(r) {
		writeError(w, http.StatusForbidden, "super_admin role required to modify application profile")
		return
	}
	var req struct {
		DefaultClientProfile *string `json:"default_client_profile"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if req.DefaultClientProfile == nil || *req.DefaultClientProfile == "" {
		writeError(w, http.StatusBadRequest, "default_client_profile required")
		return
	}

	var id int
	var retCode, retProfile string
	err := h.db.QueryRow(ctx, `
		UPDATE applications
		SET default_client_profile = $1, updated_at = now()
		WHERE code = $2 AND tenant_id = 'default'
		RETURNING id, code, default_client_profile
	`, *req.DefaultClientProfile, code).Scan(&id, &retCode, &retProfile)
	if err != nil {
		writeError(w, http.StatusNotFound, "application not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":                     id,
		"code":                   retCode,
		"default_client_profile": retProfile,
	})
}

func (h *Handler) patchKey(w http.ResponseWriter, r *http.Request, id int) {
	// R46 multi-tenant: tenant_admin may only patch keys in their own tenant.
	// Without this guard a tenant_admin could PATCH /api/keys/<id> for any key
	// id and modify default_client_profile/owner_user/remark/key_alias of
	// another tenant's key (cross-tenant write, Pattern A violation).
	if !h.assertKeyTenantScope(w, r, id) {
		return
	}
	var req struct {
		DefaultClientProfile *string `json:"default_client_profile"`
		OwnerUser            *string `json:"owner_user"`
		Remark               *string `json:"remark"`
		KeyAlias             *string `json:"key_alias"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var sets []string
	var args []any
	argIdx := 1

	if req.DefaultClientProfile != nil {
		sets = append(sets, fmt.Sprintf("default_client_profile = $%d", argIdx))
		args = append(args, *req.DefaultClientProfile)
		argIdx++
	}
	if req.OwnerUser != nil {
		sets = append(sets, fmt.Sprintf("owner_user = $%d", argIdx))
		args = append(args, *req.OwnerUser)
		argIdx++
	}
	if req.Remark != nil {
		sets = append(sets, fmt.Sprintf("remark = $%d", argIdx))
		args = append(args, *req.Remark)
		argIdx++
	}
	if req.KeyAlias != nil {
		sets = append(sets, fmt.Sprintf("key_alias = $%d", argIdx))
		args = append(args, *req.KeyAlias)
	}

	if len(sets) == 0 {
		writeError(w, http.StatusBadRequest, "no fields to update")
		return
	}

	args = append(args, id)
	idIdx := len(args)

	query := fmt.Sprintf(
		"UPDATE api_keys SET %s WHERE id = $%d",
		strings.Join(sets, ", "), idIdx,
	)

	cmd, err := h.db.Exec(ctx, query, args...)
	if err != nil {
		slog.Error("patchKey SQL failed", "query", query, "args", args, "error", err)
		writeInternalErr(w, "update failed", err)
		return
	}
	if cmd.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	h.invalidateKeyCache(id)
	writeJSON(w, http.StatusOK, map[string]string{"message": "updated"})
}
