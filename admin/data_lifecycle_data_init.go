package admin

// 数据初始化（data initialization）—— /admin/data-lifecycle 的「数据初始化」面板。
//
// # 这个面板解决什么
//
// 交付/运维同学拿到一台新环境（或者跑完一轮压测、演示、故障复现）之后，
// 需要回答两个问题：
//
//	① 「这台机器上现在各类数据有多少？」—— 统计
//	② 「把这些数据清干净，回到初始状态」—— 重置
//
// # ★ 三条不可让步的安全属性（本文件的所有设计都服务于它们）
//
// 1. **客户端永远不能指定表名。**
//    请求体里只有「类别 key」（request_records / probe_records / …），
//    类别 → 表清单的映射写死在本文件的注册表里（dataInitCategories）。
//    ⇒ 不存在「传个表名就能删任意表」的攻击面。
//
// 2. **先预览、后执行，且执行必须带上预览签发的令牌。**
//    preview 返回 affected 行数 + confirm_token（HMAC，绑定类别集合与删除计划，
//    5 分钟有效）；execute 必须原样回传该令牌。
//    ⇒ 执行的永远是「刚刚被预览过的那一批类别 + 同一个删除计划」。
//
// 3. **受保护行不删。**
//    每个表可以声明「保留谓词」（KEEP 条件）：系统 key、超级管理员、
//    default 租户等骨架行永远不删 ⇒ 一次重置不会把环境变成「谁也登录不了」。
//
// ★ 权限：全部 superAdmin（SuperAdminMiddleware）。数据初始化是本仓破坏性最强的
//   运维动作之一（能删掉全部用户/租户/凭据），不该给 tenant_admin。
//
// # ★ 执行顺序（外键安全）
//
// dataInitCategoryOrder 是**唯一**的执行顺序，按「子表先于父表」排：
//
//	request_records → probe_records → routing_records → credentials →
//	models → providers → api_keys → users → tenants
//
//	· session_summaries 在 tenants 之前删（它是 tenants 的 CASCADE 子表，
//	  先删干净 ⇒ tenants 的级联不会「偷跑」掉本该计入预览的行数）；
//	· credential_probe_configs / credential_probes 是 credentials 的
//	  **NO ACTION** 子表 ⇒ 必须先删，否则 DELETE FROM credentials 直接撞外键；
//	· providers 在 credentials / provider_models 之后 ⇒ 不会出现悬空外键。
//
// ★ 预览行数与实际删除行数可能不同（级联 / 并发写入）⇒ 响应里同时给出
//   target_rows（预览值）与 rows_deleted（实际值），不把两者混为一谈。

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ── 破坏强度分级 ────────────────────────────────────────────────────────────

const (
	// dataInitLevelOperational = 运行数据（请求/探测/路由记录）。
	//   整类可清空 —— 这些是运行期产生的历史/遥测，清掉不改变系统可用性。
	dataInitLevelOperational = "operational"
	// dataInitLevelConfig = 配置数据（key/用户/租户/凭据/模型/供应商）。
	//   清空但保留「骨架行」（见各表的 Keep 谓词）—— 不把环境删成登不进去。
	dataInitLevelConfig = "configuration"
)

// ── 注册表 ──────────────────────────────────────────────────────────────────

// dataInitActor 是「当前操作者」——保留谓词要靠它保住操作者自己的账号/租户。
type dataInitActor struct {
	Username string
	TenantID string
	UserID   int
	Role     string
}

// dataInitKeepSpec 是一条「保留谓词」。
//
// ★ Where 是不带 WHERE 关键字的 SQL 片段，表名与列名全部来自本文件的静态
//
//	注册表，字面量全部走绑定参数 ⇒ 不存在注入面。
//
// ★ 空 Where = 整表可删（运行数据类别）。
type dataInitKeepSpec struct {
	Where   string
	Args    []any
	NoteKey string // i18n key：说明保留了什么
}

// dataInitTable 是「一个物理表」及其元数据。
type dataInitTable struct {
	// Name 是物理表名（分区父表写父表名；DELETE 会覆盖全部分区）。
	Name string
	// TimeCol 是 MIN/MAX 用的时间列；空串 = 该表无时间列。
	TimeCol string
	// Keep 是保留谓词；nil = 该表整表可删。
	Keep func(a dataInitActor) dataInitKeepSpec
}

// dataInitCategory 是一个「数据类别」——面板上的一个卡片。
type dataInitCategory struct {
	Key      string
	LabelKey string
	Level    string
	Tables   []dataInitTable
}

// dataInitCategoryOrder 是**执行顺序**，也是面板上的展示顺序。
//
// ★ 改这个顺序前先看文件头「执行顺序」小节：外键安全依赖它。
var dataInitCategoryOrder = []string{
	"request_records",
	"probe_records",
	"routing_records",
	"credentials",
	"models",
	"providers",
	"api_keys",
	"users",
	"tenants",
}

// dataInitCategories 是类别注册表（类别 → 表清单）。
//
// ★ 表清单**互不重叠** ⇒ 预览行数不会重复计数，删一次也不会出现
//
//	「同一张表被两个类别算两遍」的对不上账。
var dataInitCategories = []dataInitCategory{
	{
		Key:      "request_records",
		LabelKey: "dataLifecycle.dataInit.categories.requestRecords",
		Level:    dataInitLevelOperational,
		Tables: []dataInitTable{
			{Name: "session_turn_details", TimeCol: "ts"},
			{Name: "session_bodies", TimeCol: "ts"},
			{Name: "session_turns", TimeCol: "ts"},
			{Name: "sessions", TimeCol: "created_at"},
			{Name: "request_wal", TimeCol: "created_at"},
			// ★ session_summaries 是 tenants 的 CASCADE 子表 ⇒ 必须在 tenants 之前删。
			{Name: "session_summaries", TimeCol: "created_at"},
			{Name: "request_stats_minute", TimeCol: "bucket"},
			{Name: "request_stats_dim_minute", TimeCol: "bucket"},
		},
	},
	{
		Key:      "probe_records",
		LabelKey: "dataLifecycle.dataInit.categories.probeRecords",
		Level:    dataInitLevelOperational,
		Tables: []dataInitTable{
			{Name: "node_probe_runs", TimeCol: "created_at"},
			{Name: "credential_probe_model_log", TimeCol: "created_at"},
			{Name: "credential_health_checks", TimeCol: "created_at"},
			{Name: "model_probe_runs", TimeCol: "created_at"},
			{Name: "system_probe_runs", TimeCol: "created_at"},
			{Name: "provider_metrics_minute", TimeCol: "bucket"},
			{Name: "provider_metrics_hour", TimeCol: "created_at"},
		},
	},
	{
		Key:      "routing_records",
		LabelKey: "dataLifecycle.dataInit.categories.routingRecords",
		Level:    dataInitLevelOperational,
		Tables: []dataInitTable{
			{Name: "routing_decision_log", TimeCol: "ts"},
			{Name: "route_decisions", TimeCol: "ts"},
			{Name: "routing_audit_log", TimeCol: "created_at"},
			{Name: "route_incidents", TimeCol: "created_at"},
			{Name: "candidate_failure_logs", TimeCol: "ts"},
			{Name: "routing_health_checks", TimeCol: "created_at"},
		},
	},
	{
		Key:      "credentials",
		LabelKey: "dataLifecycle.dataInit.categories.credentials",
		Level:    dataInitLevelConfig,
		Tables: []dataInitTable{
			{Name: "credential_state_log", TimeCol: "updated_at"},
			{Name: "credential_state_nodes", TimeCol: "created_at"},
			// ★ credential_probe_configs / credential_probes 是 credentials 的
			//   **NO ACTION** 子表 ⇒ 必须先删，否则父表删除撞外键。
			{Name: "credential_probe_configs", TimeCol: "created_at"},
			{Name: "credential_probes", TimeCol: "created_at"},
			{Name: "credential_keys", TimeCol: "created_at"},
			{Name: "credential_quotas", TimeCol: "created_at"},
			{Name: "credential_quota_usage", TimeCol: "window_started_at"},
			{Name: "credential_model_bindings", TimeCol: "created_at"},
			{Name: "credentials", TimeCol: "created_at"},
		},
	},
	{
		Key:      "models",
		LabelKey: "dataLifecycle.dataInit.categories.models",
		Level:    dataInitLevelConfig,
		Tables: []dataInitTable{
			{Name: "model_modality_verification", TimeCol: "created_at"},
			{Name: "model_aliases", TimeCol: "created_at"},
			{Name: "provider_models", TimeCol: "created_at"},
			{Name: "model_families", TimeCol: "created_at"},
			{Name: "models_canonical", TimeCol: "created_at"},
		},
	},
	{
		Key:      "providers",
		LabelKey: "dataLifecycle.dataInit.categories.providers",
		Level:    dataInitLevelConfig,
		Tables: []dataInitTable{
			{Name: "provider_endpoint_protocols", TimeCol: "created_at"},
			{Name: "provider_quality_configs", TimeCol: "created_at"},
			{Name: "provider_quality_profiles", TimeCol: "created_at"},
			{Name: "provider_header_profiles", TimeCol: "created_at"},
			{Name: "provider_catalog", TimeCol: "created_at"},
			{Name: "provider_settings", TimeCol: "created_at"},
			{Name: "providers", TimeCol: "created_at"},
		},
	},
	{
		Key:      "api_keys",
		LabelKey: "dataLifecycle.dataInit.categories.apiKeys",
		Level:    dataInitLevelConfig,
		Tables: []dataInitTable{
			{Name: "key_rpm_daily", TimeCol: "day_bucket"},
			{Name: "key_applications", TimeCol: "created_at"},
			{
				Name:    "api_keys",
				TimeCol: "created_at",
				// ★ 系统 key 是网关自身在用的（内部服务调用）⇒ 永不删。
				Keep: func(dataInitActor) dataInitKeepSpec {
					return dataInitKeepSpec{
						Where:   "is_system = true",
						NoteKey: "dataLifecycle.dataInit.keep.systemKeys",
					}
				},
			},
		},
	},
	{
		Key:      "users",
		LabelKey: "dataLifecycle.dataInit.categories.users",
		Level:    dataInitLevelConfig,
		Tables: []dataInitTable{
			{
				Name:    "users",
				TimeCol: "created_at",
				// ★ 保留全部超级管理员 + 当前操作者自己
				//   ⇒ 重置后一定还登得进去。
				Keep: func(a dataInitActor) dataInitKeepSpec {
					return dataInitKeepSpec{
						Where:   "role = 'super_admin' OR username = $1",
						Args:    []any{a.Username},
						NoteKey: "dataLifecycle.dataInit.keep.superAdmins",
					}
				},
			},
		},
	},
	{
		Key:      "tenants",
		LabelKey: "dataLifecycle.dataInit.categories.tenants",
		Level:    dataInitLevelConfig,
		Tables: []dataInitTable{
			{
				Name:    "tenants",
				TimeCol: "created_at",
				// ★ 保留 default 租户 + 当前操作者所属租户。
				Keep: func(a dataInitActor) dataInitKeepSpec {
					return dataInitKeepSpec{
						Where:   "code = 'default' OR code = $1",
						Args:    []any{a.TenantID},
						NoteKey: "dataLifecycle.dataInit.keep.defaultTenant",
					}
				},
			},
		},
	},
}

// dataInitCategoryByKey 查类别；不存在返回 nil。
func dataInitCategoryByKey(key string) *dataInitCategory {
	for i := range dataInitCategories {
		if dataInitCategories[i].Key == key {
			return &dataInitCategories[i]
		}
	}
	return nil
}

// dataInitKnownKeys 返回全部合法类别 key（排序后）。
func dataInitKnownKeys() []string {
	keys := make([]string, 0, len(dataInitCategories))
	for _, c := range dataInitCategories {
		keys = append(keys, c.Key)
	}
	sort.Strings(keys)
	return keys
}

// ── 响应结构 ────────────────────────────────────────────────────────────────

// dataInitTableStat 单表统计。
type dataInitTableStat struct {
	Name       string     `json:"name"`
	Exists     bool       `json:"exists"`
	Rows       int64      `json:"rows"`
	Bytes      int64      `json:"bytes"`
	BytesHuman string     `json:"bytes_human"`
	OldestAt   *time.Time `json:"oldest_at,omitempty"`
	NewestAt   *time.Time `json:"newest_at,omitempty"`
	// ProtectedRows 是该表里**不会**被删的行数（保留谓词命中）。
	ProtectedRows int64 `json:"protected_rows"`
	// TargetRows 是该表**会被**删的行数。
	TargetRows int64 `json:"target_rows"`
}

// dataInitCategoryStat 类别统计（面板一个卡片）。
type dataInitCategoryStat struct {
	Key           string              `json:"key"`
	LabelKey      string              `json:"label_key"`
	Level         string              `json:"level"`
	Tables        []dataInitTableStat `json:"tables"`
	Rows          int64               `json:"rows"`
	Bytes         int64               `json:"bytes"`
	BytesHuman    string              `json:"bytes_human"`
	OldestAt      *time.Time          `json:"oldest_at,omitempty"`
	NewestAt      *time.Time          `json:"newest_at,omitempty"`
	ProtectedRows int64               `json:"protected_rows"`
	TargetRows    int64               `json:"target_rows"`
	// KeepNoteKey 说明「哪些行会被保留」；空 = 该类别整类可清空。
	KeepNoteKey string `json:"keep_note_key,omitempty"`
	// MissingTables 是本环境里不存在的表（老部署没迁移到位）⇒ 如实列出。
	MissingTables []string `json:"missing_tables,omitempty"`
}

// dataInitCatalogResponse GET /api/admin/data-lifecycle/data-init/catalog
type dataInitCatalogResponse struct {
	GeneratedAt time.Time              `json:"generated_at"`
	Categories  []dataInitCategoryStat `json:"categories"`
}

// dataInitPreviewRequest POST .../data-init/preview
type dataInitPreviewRequest struct {
	Categories []string `json:"categories"`
}

// dataInitPreviewCategory 单个类别的预览。
type dataInitPreviewCategory struct {
	Key           string              `json:"key"`
	LabelKey      string              `json:"label_key"`
	Level         string              `json:"level"`
	Tables        []dataInitTableStat `json:"tables"`
	Rows          int64               `json:"rows"`
	ProtectedRows int64               `json:"protected_rows"`
	TargetRows    int64               `json:"target_rows"`
	KeepNoteKey   string              `json:"keep_note_key,omitempty"`
}

// dataInitPreviewResponse POST .../data-init/preview
type dataInitPreviewResponse struct {
	Categories      []dataInitPreviewCategory `json:"categories"`
	TotalRows       int64                     `json:"total_rows"`
	TotalProtected  int64                     `json:"total_protected_rows"`
	TotalTargetRows int64                     `json:"total_target_rows"`
	ConfirmToken    string                    `json:"confirm_token"`
	TokenExpiresAt  time.Time                 `json:"token_expires_at"`
	Warnings        []string                  `json:"warnings,omitempty"`
}

// dataInitExecuteRequest POST .../data-init/execute
type dataInitExecuteRequest struct {
	Categories   []string `json:"categories"`
	ConfirmToken string   `json:"confirm_token"`
}

// dataInitExecuteCategory 单个类别的执行结果。
type dataInitExecuteCategory struct {
	Key         string `json:"key"`
	RowsDeleted int64  `json:"rows_deleted"`
}

// dataInitExecuteResponse POST .../data-init/execute
type dataInitExecuteResponse struct {
	Actor            string                    `json:"actor"`
	TotalRowsDeleted int64                     `json:"total_rows_deleted"`
	Results          []dataInitExecuteCategory `json:"results"`
	ExecutedAt       time.Time                 `json:"executed_at"`
}

// ── 删除计划 ────────────────────────────────────────────────────────────────

// dataInitDelete 是一条待执行的 DELETE。
type dataInitDelete struct {
	Category string
	Table    string
	SQL      string
	Args     []any
}

// dataInitPlan 是一次重置的完整删除计划（可复现 ⇒ 可被令牌绑定）。
type dataInitPlan struct {
	Deletes []dataInitDelete
}

// buildDataInitPlan 按 dataInitCategoryOrder 生成删除计划。
//
// ★ 纯函数（不碰 DB）⇒ 可单测；也是令牌绑定的对象。
//
// ★ 类别重复传入会被去重；未知类别直接报错（**不**静默忽略 ——
//
//	静默忽略会让操作者以为「重置了 A」，实际只重置了 B）。
func buildDataInitPlan(keys []string, actor dataInitActor) (*dataInitPlan, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("no categories selected")
	}
	seen := map[string]bool{}
	selected := make([]string, 0, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if dataInitCategoryByKey(k) == nil {
			return nil, fmt.Errorf("unknown category %q", k)
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		selected = append(selected, k)
	}
	// ★ 全是空白项 ⇒ 等于「没选」⇒ 必须报错（不能当成空计划静默通过）。
	if len(selected) == 0 {
		return nil, fmt.Errorf("no categories selected")
	}
	// 按全局顺序重排（dataInitCategoryOrder 决定外键安全）。
	ordered := make([]string, 0, len(selected))
	for _, k := range dataInitCategoryOrder {
		if seen[k] {
			ordered = append(ordered, k)
		}
	}

	plan := &dataInitPlan{}
	for _, key := range ordered {
		cat := dataInitCategoryByKey(key)
		if cat == nil {
			continue
		}
		for _, t := range cat.Tables {
			d := dataInitDelete{Category: key, Table: t.Name}
			if t.Keep != nil {
				spec := t.Keep(actor)
				if strings.TrimSpace(spec.Where) != "" {
					d.SQL = fmt.Sprintf("DELETE FROM public.%s WHERE NOT (%s)", t.Name, spec.Where)
					d.Args = spec.Args
				}
			}
			if d.SQL == "" {
				d.SQL = fmt.Sprintf("DELETE FROM public.%s", t.Name)
			}
			plan.Deletes = append(plan.Deletes, d)
		}
	}
	return plan, nil
}

// digest 返回删除计划的稳定摘要（令牌绑定用）。
//
// ★ 刻意**不**绑行数：运行数据每时每刻都在变，把行数绑进去会让
//
//	「预览完点执行」几乎必然失败 ⇒ 令牌只绑「删什么」这件事。
func (p *dataInitPlan) digest() string {
	h := sha256.New()
	for _, d := range p.Deletes {
		fmt.Fprintf(h, "%s|%s|%s|%v\n", d.Category, d.Table, d.SQL, d.Args)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ── 令牌 ────────────────────────────────────────────────────────────────────

// dataInitTokenTTL 是预览令牌的有效期。
const dataInitTokenTTL = 5 * time.Minute

var (
	dataInitSignerOnce sync.Once
	dataInitSignerKey  []byte
	dataInitSignerErr  error
)

// dataInitSigner 返回进程私有的 HMAC 密钥。
//
// ★ crypto/rand 失败 ⇒ **功能关闭**（返回错误）⇒ 不退化成一个可预测的常量密钥
//
//	（那会让「令牌不可伪造」这条属性失效）。
func dataInitSigner() ([]byte, error) {
	dataInitSignerOnce.Do(func() {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			dataInitSignerErr = fmt.Errorf("data-init token key unavailable: %w", err)
			return
		}
		dataInitSignerKey = b
	})
	return dataInitSignerKey, dataInitSignerErr
}

// dataInitTokenPayload 是令牌载荷。
type dataInitTokenPayload struct {
	Categories []string `json:"categories"`
	Digest     string   `json:"digest"`
	IssuedAt   int64    `json:"issued_at"`
}

// issueDataInitToken 签发预览令牌。
func issueDataInitToken(plan *dataInitPlan, keys []string) (string, time.Time, error) {
	key, err := dataInitSigner()
	if err != nil {
		return "", time.Time{}, err
	}
	payload := dataInitTokenPayload{
		Categories: append([]string(nil), keys...),
		Digest:     plan.digest(),
		IssuedAt:   time.Now().Unix(),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", time.Time{}, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(raw)
	token := hex.EncodeToString(raw) + "." + hex.EncodeToString(mac.Sum(nil))
	return token, time.Now().Add(dataInitTokenTTL), nil
}

// verifyDataInitToken 校验令牌：类别集合一致 + 计划摘要一致 + 未过期。
func verifyDataInitToken(token string, plan *dataInitPlan, keys []string) error {
	key, err := dataInitSigner()
	if err != nil {
		return err
	}
	rawPart, macPart, ok := strings.Cut(token, ".")
	if !ok || rawPart == "" || macPart == "" {
		return fmt.Errorf("malformed confirm token")
	}
	raw, err := hex.DecodeString(rawPart)
	if err != nil {
		return fmt.Errorf("malformed confirm token")
	}
	want, err := hex.DecodeString(macPart)
	if err != nil {
		return fmt.Errorf("malformed confirm token")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(raw)
	if !hmac.Equal(mac.Sum(nil), want) {
		return fmt.Errorf("confirm token signature mismatch")
	}
	var payload dataInitTokenPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("malformed confirm token")
	}
	if time.Since(time.Unix(payload.IssuedAt, 0)) > dataInitTokenTTL {
		return fmt.Errorf("confirm token expired; run preview again")
	}
	if payload.Digest != plan.digest() {
		return fmt.Errorf("confirm token does not match the current reset plan; run preview again")
	}
	if !sameStringSet(payload.Categories, keys) {
		return fmt.Errorf("confirm token was issued for a different category set; run preview again")
	}
	return nil
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// ── DB 访问 ─────────────────────────────────────────────────────────────────

// dataInitStore 是本功能需要的**最小** DB 面。
//
// ★ 收窄是刻意的：整个功能是破坏性的 ⇒ 可测面必须小且完全可替换
//
//	（Handler 持的是具体 *pgxpool.Pool，单测里换不进去）。
type dataInitStore interface {
	// TableExists 报告表是否存在（老部署可能没迁移到位）。
	TableExists(ctx context.Context, table string) (bool, error)
	// TablesExist 批量报告表是否存在（一次往返）。
	TablesExist(ctx context.Context, tables []string) (map[string]bool, error)
	// TableStat 取单表统计（行数 / 字节 / 时间范围）。
	TableStat(ctx context.Context, t dataInitTable) (rows int64, bytes int64, oldest, newest *time.Time, err error)
	// CountWhere 按谓词计数（算受保护行数）。
	CountWhere(ctx context.Context, table, where string, args ...any) (int64, error)
	// Apply 执行删除计划 + 审计行（同一事务）。
	//
	// ★ 返回值按**类别 key 索引**（不是按位置对齐的切片）——
	//
	//	本实现返回计划里**每一个**类别；某个类别的表在本环境一张都不存在时
	//	它的值是 0（那种场景是**明确支持**的：老部署没迁移到位 ⇒ 跳过不存在的表）。
	//	按位置对齐则会在这种场景下**整体错位**：该类别**之后**的每个类别
	//	都会拿到别人的数字（面板上就会显示「探测记录删了 3 行」，
	//	而那 3 行其实是**凭据**的）。
	Apply(ctx context.Context, plan *dataInitPlan, actor, action string, audit map[string]any) (map[string]int64, error)
}

// pgDataInitStore 是基于 *pgxpool.Pool 的实现。
type pgDataInitStore struct {
	pool interface {
		Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
		QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
		Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
		Begin(ctx context.Context) (pgx.Tx, error)
	}
}

// newPGDataInitStore 用真实连接池构造 store。
func newPGDataInitStore(pool *pgxpool.Pool) *pgDataInitStore {
	return &pgDataInitStore{pool: pool}
}

// TableExists 报告表是否存在。
func (s *pgDataInitStore) TableExists(ctx context.Context, table string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT to_regclass('public.' || $1) IS NOT NULL`, table,
	).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// TablesExist 批量报告表是否存在（一次往返 ⇒ 不做 N 次往返）。
func (s *pgDataInitStore) TablesExist(ctx context.Context, tables []string) (map[string]bool, error) {
	out := make(map[string]bool, len(tables))
	if len(tables) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT t.name, to_regclass('public.' || t.name) IS NOT NULL
		   FROM unnest($1::text[]) AS t(name)`, tables,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var exists bool
		if err := rows.Scan(&name, &exists); err != nil {
			return nil, err
		}
		out[name] = exists
	}
	return out, rows.Err()
}

// TableStat 取单表统计。
//
// ★ 字节数按「父表 + 全部子分区」求和：分区父表的 pg_total_relation_size
//
//	恒为 0（实测），只查父表会让所有分区表的大小显示成 0。
func (s *pgDataInitStore) TableStat(ctx context.Context, t dataInitTable) (int64, int64, *time.Time, *time.Time, error) {
	var rows, bytes int64
	var oldest, newest *time.Time

	sizeSQL := `(SELECT COALESCE(sum(pg_total_relation_size(x.oid)), 0)::bigint
	              FROM pg_class x
	             WHERE x.oid = to_regclass('public.' || $1)
	                OR x.oid IN (SELECT inhrelid FROM pg_inherits
	                              WHERE inhparent = to_regclass('public.' || $1)))`

	if t.TimeCol == "" {
		err := s.pool.QueryRow(ctx,
			`SELECT count(*)::bigint, `+sizeSQL+` FROM public.`+t.Name,
			t.Name,
		).Scan(&rows, &bytes)
		if err != nil {
			return 0, 0, nil, nil, err
		}
		return rows, bytes, nil, nil, nil
	}

	err := s.pool.QueryRow(ctx,
		`SELECT count(*)::bigint, min(`+t.TimeCol+`), max(`+t.TimeCol+`), `+sizeSQL+` FROM public.`+t.Name,
		t.Name,
	).Scan(&rows, &oldest, &newest, &bytes)
	if err != nil {
		return 0, 0, nil, nil, err
	}
	return rows, bytes, oldest, newest, nil
}

// CountWhere 按谓词计数。
func (s *pgDataInitStore) CountWhere(ctx context.Context, table, where string, args ...any) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx,
		`SELECT count(*)::bigint FROM public.`+table+` WHERE `+where, args...,
	).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// Apply 在**一个事务**里执行删除计划并写入审计行。
//
// ★ 审计行与删除同事务 ⇒ 不会出现「删了但没留痕」或「留了痕但没删」。
// ★ 不存在的表直接跳过（老部署没迁移到位 ⇒ 不该整单失败）。
// ★ 返回每个类别实际删除的行数（可能与预览值不同：级联 / 并发写入）。
func (s *pgDataInitStore) Apply(ctx context.Context, plan *dataInitPlan, actor, action string, audit map[string]any) (map[string]int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	// 事务内再确认一次表存在（预览到执行之间可能发生迁移）。
	perCat := map[string]int64{}
	// ★ 计划里出现过的类别**全部先占位 0**——
	//   「本环境没有该类别的任何表」是受支持场景（老部署没迁移到位 ⇒ 跳过不存在的表），
	//   若这里只登记真正执行过的类别，调用方按位置对齐就会整体错位
	//   （见 dataInitStore.Apply 的契约注释）。
	for _, d := range plan.Deletes {
		if _, ok := perCat[d.Category]; !ok {
			perCat[d.Category] = 0
		}
	}
	for _, d := range plan.Deletes {
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT to_regclass('public.' || $1) IS NOT NULL`, d.Table,
		).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		tag, err := tx.Exec(ctx, d.SQL, d.Args...)
		if err != nil {
			return nil, fmt.Errorf("delete %s: %w", d.Table, err)
		}
		perCat[d.Category] += tag.RowsAffected()
	}

	// 审计行（与删除同事务）。
	if err := logAuditExec(ctx, tx, actor, action, audit); err != nil {
		return nil, fmt.Errorf("audit insert failed: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return perCat, nil
}

// dataInitPerCategoryCounts 把 Apply 的返回值按计划里的类别对齐。
//
// ★ 按**类别 key** 取值，**不按位置** ——
//
//	Apply 返回计划里**每一个**类别；某个类别在本环境一张表都没有时它的值是 0。
//	若这里改成按位置取值 ⇒ 那个类别**之后**的每个类别都会拿到别人的数字
//	（面板上就会显示「探测记录删了 12,345 行」，而那行数其实是路由记录的）。
//	⇒ 按 key 取值 ⇒ 缺席的类别就是 0，不存在错位这一形态。
func dataInitPerCategoryCounts(order []string, deleted map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(order))
	for _, key := range order {
		out[key] = deleted[key]
	}
	return out
}

// dataInitCategoryOrderOf 返回执行结果与类别顺序对齐。
func alignDataInitResults(order []string, perCat map[string]int64) []dataInitExecuteCategory {
	out := make([]dataInitExecuteCategory, 0, len(order))
	for _, c := range order {
		out = append(out, dataInitExecuteCategory{Key: c, RowsDeleted: perCat[c]})
	}
	return out
}

// ── handler ────────────────────────────────────────────────────────────────

// dataInitActorFromRequest 提取当前操作者。
func dataInitActorFromRequest(r *http.Request) dataInitActor {
	a := dataInitActor{}
	if auth := GetAuthContext(r); auth != nil {
		a.Username = auth.Username
		a.TenantID = auth.TenantID
		a.UserID = auth.UserID
		a.Role = auth.Role
	}
	if a.Username == "" {
		a.Username = requestActor(r)
	}
	if a.TenantID == "" {
		a.TenantID = "default"
	}
	return a
}

// handleDataInitCatalog GET /api/admin/data-lifecycle/data-init/catalog
//
// 返回每个类别的行数 / 占用 / 时间范围 / 受保护行数。
func (h *Handler) handleDataInitCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	store := newPGDataInitStore(h.db)
	actor := dataInitActorFromRequest(r)

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	cats, err := dataInitCategoryStats(ctx, store, actor, dataInitCategories)
	if err != nil {
		writeInternalErr(w, "data-init catalog", err)
		return
	}

	writeJSON(w, http.StatusOK, dataInitCatalogResponse{
		GeneratedAt: time.Now(),
		Categories:  cats,
	})
}

// dataInitStatWorkers 是统计的并发度。
//
// ★ 真库实测：全量盘点（9 类 / 40 张表）串行约 4s ⇒ 面板打开会「卡一下」。
//
//	4 路并发把墙钟压到单表最慢的那张（~0.5s 量级）。
const dataInitStatWorkers = 4

// dataInitCategoryStats 计算一组类别的统计。
//
// ★ **并行**：每张表一条统计查询（COUNT + MIN/MAX + 字节），4 路并发。
//
//	每个 goroutine 只写自己那一格 ⇒ 不需要锁。
func dataInitCategoryStats(ctx context.Context, store dataInitStore, actor dataInitActor, cats []dataInitCategory) ([]dataInitCategoryStat, error) {
	// ① 一次性确认所有表是否存在（一次往返）。
	allTables := make([]string, 0, 64)
	for _, c := range cats {
		for _, t := range c.Tables {
			allTables = append(allTables, t.Name)
		}
	}
	exists, err := store.TablesExist(ctx, allTables)
	if err != nil {
		return nil, err
	}

	// ② 铺开成「一格一表」的任务表。
	type slot struct {
		catIdx  int
		tblIdx  int
		tbl     dataInitTable
		stat    dataInitTableStat
		keep    dataInitKeepSpec
		hasKeep bool
	}
	slots := make([]slot, 0, len(allTables))
	out := make([]dataInitCategoryStat, len(cats))
	for ci := range cats {
		out[ci] = dataInitCategoryStat{
			Key:      cats[ci].Key,
			LabelKey: cats[ci].LabelKey,
			Level:    cats[ci].Level,
			Tables:   make([]dataInitTableStat, 0, len(cats[ci].Tables)),
		}
		for ti, t := range cats[ci].Tables {
			s := slot{catIdx: ci, tblIdx: ti, tbl: t}
			s.stat = dataInitTableStat{Name: t.Name, Exists: exists[t.Name]}
			if t.Keep != nil {
				s.keep = t.Keep(actor)
				s.hasKeep = strings.TrimSpace(s.keep.Where) != ""
			}
			slots = append(slots, s)
		}
	}

	// ③ 4 路并发跑统计。
	workers := dataInitStatWorkers
	if workers > len(slots) {
		workers = len(slots)
	}
	if workers > 0 {
		var wg sync.WaitGroup
		errCh := make(chan error, len(slots))
		jobCh := make(chan int)
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for idx := range jobCh {
					s := &slots[idx]
					if !s.stat.Exists {
						continue
					}
					rows, bytes, oldest, newest, err := store.TableStat(ctx, s.tbl)
					if err != nil {
						errCh <- fmt.Errorf("%s: %w", s.tbl.Name, err)
						return
					}
					s.stat.Rows = rows
					s.stat.Bytes = bytes
					s.stat.BytesHuman = humanBytes(bytes)
					s.stat.OldestAt = oldest
					s.stat.NewestAt = newest
					if s.hasKeep {
						kept, err := store.CountWhere(ctx, s.tbl.Name, s.keep.Where, s.keep.Args...)
						if err != nil {
							errCh <- fmt.Errorf("%s keep-count: %w", s.tbl.Name, err)
							return
						}
						s.stat.ProtectedRows = kept
					}
					s.stat.TargetRows = rows - s.stat.ProtectedRows
					if s.stat.TargetRows < 0 {
						s.stat.TargetRows = 0
					}
				}
			}()
		}
		go func() {
			defer close(jobCh)
			for i := range slots {
				jobCh <- i
			}
		}()
		wg.Wait()
		close(errCh)
		for err := range errCh {
			if err != nil {
				return nil, err
			}
		}
	}

	// ④ 汇总。
	for i := range slots {
		s := &slots[i]
		cat := &out[s.catIdx]
		cat.Tables = append(cat.Tables, s.stat)
		if !s.stat.Exists {
			cat.MissingTables = append(cat.MissingTables, s.tbl.Name)
			continue
		}
		cat.Rows += s.stat.Rows
		cat.Bytes += s.stat.Bytes
		cat.ProtectedRows += s.stat.ProtectedRows
		cat.TargetRows += s.stat.TargetRows
		cat.OldestAt = earlierTime(cat.OldestAt, s.stat.OldestAt)
		cat.NewestAt = laterTime(cat.NewestAt, s.stat.NewestAt)
		if s.hasKeep && cat.KeepNoteKey == "" {
			cat.KeepNoteKey = s.keep.NoteKey
		}
	}
	for i := range out {
		out[i].BytesHuman = humanBytes(out[i].Bytes)
	}
	return out, nil
}

// handleDataInitPreview POST /api/admin/data-lifecycle/data-init/preview
//
// 预览一次重置：返回将要删除的行数 + 受保护行数 + 副作用告警 + 确认令牌。
func (h *Handler) handleDataInitPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req dataInitPreviewRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if len(req.Categories) == 0 {
		writeError(w, http.StatusBadRequest, "categories is required")
		return
	}

	actor := dataInitActorFromRequest(r)
	plan, err := buildDataInitPlan(req.Categories, actor)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	store := newPGDataInitStore(h.db)
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	// ★ 与 catalog 走**同一套**统计口径（同一个 dataInitCategoryStats）
	//   ⇒ 预览看到的行数与面板上看到的行数必然一致（两处各写一遍 ⇒ 迟早漂移）。
	selectedCats := make([]dataInitCategory, 0, len(plan.Deletes))
	for _, key := range planCategories(plan) {
		if cat := dataInitCategoryByKey(key); cat != nil {
			selectedCats = append(selectedCats, *cat)
		}
	}
	stats, err := dataInitCategoryStats(ctx, store, actor, selectedCats)
	if err != nil {
		writeInternalErr(w, "data-init preview", err)
		return
	}

	resp := dataInitPreviewResponse{Categories: []dataInitPreviewCategory{}}
	for _, st := range stats {
		pc := dataInitPreviewCategory{
			Key:           st.Key,
			LabelKey:      st.LabelKey,
			Level:         st.Level,
			Tables:        st.Tables,
			Rows:          st.Rows,
			ProtectedRows: st.ProtectedRows,
			TargetRows:    st.TargetRows,
			KeepNoteKey:   st.KeepNoteKey,
		}
		resp.Categories = append(resp.Categories, pc)
		resp.TotalRows += pc.Rows
		resp.TotalProtected += pc.ProtectedRows
		resp.TotalTargetRows += pc.TargetRows
	}

	resp.Warnings = dataInitWarnings(plan, resp)

	token, expires, err := issueDataInitToken(plan, planCategories(plan))
	if err != nil {
		writeInternalErr(w, "data-init preview", err)
		return
	}
	resp.ConfirmToken = token
	resp.TokenExpiresAt = expires

	writeJSON(w, http.StatusOK, resp)
}

// handleDataInitExecute POST /api/admin/data-lifecycle/data-init/execute
//
// 执行一次重置。必须带上 preview 签发的 confirm_token。
func (h *Handler) handleDataInitExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req dataInitExecuteRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if len(req.Categories) == 0 {
		writeError(w, http.StatusBadRequest, "categories is required")
		return
	}
	if strings.TrimSpace(req.ConfirmToken) == "" {
		writeError(w, http.StatusBadRequest, "confirm_token is required; run preview first")
		return
	}

	actor := dataInitActorFromRequest(r)
	plan, err := buildDataInitPlan(req.Categories, actor)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := verifyDataInitToken(req.ConfirmToken, plan, planCategories(plan)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	store := newPGDataInitStore(h.db)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	audit := map[string]any{
		"categories":   planCategories(plan),
		"actor":        actor.Username,
		"tenant_id":    actor.TenantID,
		"plan_digest":  plan.digest(),
		"endpoint":     "data-init/execute",
		"delete_count": len(plan.Deletes),
	}
	deleted, err := store.Apply(ctx, plan, actor.Username, "data_init_reset", audit)
	if err != nil {
		writeInternalErr(w, "data-init execute", err)
		return
	}

	order := planCategories(plan)
	perCat := dataInitPerCategoryCounts(order, deleted)

	var total int64
	for _, v := range perCat {
		total += v
	}

	writeJSON(w, http.StatusOK, dataInitExecuteResponse{
		Actor:            actor.Username,
		TotalRowsDeleted: total,
		Results:          alignDataInitResults(order, perCat),
		ExecutedAt:       time.Now(),
	})
}

// planCategories 返回计划涉及的类别（按执行顺序）。
func planCategories(plan *dataInitPlan) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, d := range plan.Deletes {
		if seen[d.Category] {
			continue
		}
		seen[d.Category] = true
		out = append(out, d.Category)
	}
	return out
}

// dataInitWarnings 产出预览阶段的副作用告警。
func dataInitWarnings(plan *dataInitPlan, resp dataInitPreviewResponse) []string {
	warnings := []string{}
	selected := map[string]bool{}
	for _, c := range resp.Categories {
		selected[c.Key] = true
	}
	// 删租户 ⇒ 级联删掉租户策略 / 计费单据（不是本面板的类别，属副作用）。
	if selected["tenants"] {
		warnings = append(warnings, "dataLifecycle.dataInit.warning.tenantCascade")
	}
	// 删供应商 ⇒ 凭据/模型里的 provider_id 会变成悬空引用（无外键约束挡着）。
	if selected["providers"] {
		warnings = append(warnings, "dataLifecycle.dataInit.warning.providerDangling")
	}
	// 删凭据 ⇒ 模型绑定一并消失。
	if selected["credentials"] {
		warnings = append(warnings, "dataLifecycle.dataInit.warning.credentialBindings")
	}
	return warnings
}

// ── 小工具 ──────────────────────────────────────────────────────────────────

// ★ humanBytes 复用 admin/data_lifecycle_storage.go 里已有的实现（本包同名函数只能有一个）。

// earlierTime 取更早的时间。
func earlierTime(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.Before(*a):
		return b
	default:
		return a
	}
}

// laterTime 取更晚的时间。
func laterTime(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.After(*a):
		return b
	default:
		return a
	}
}
