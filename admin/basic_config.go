package admin

// basic_config.go — 基础设置聚合端点（2026-11-20 基础设置轮）。
//
// owner 需求：把「与业务无关的系统级配置」整合成一个基础设置分组，
// 直观地分组呈现在网页上，可快速设置并更新系统的公共配置。
//
// GET /api/admin/basic-config
//   返回分组后的键清单 + 现值 + 来源 + 只读基础设施实况：
//     groups:   可编辑键（platform scope，走既有 settingsPut 通道写入）
//     infra:    启动期 env 配置的只读实况（redis / 数据库 / 实例标识 /
//               缓存目录 / 附件存储），UI 标注「修改需重启」。
//
// 写通道不另造：前端保存仍走 PUT /api/admin/settings/{key}
//（校验 + 审计 + 权限门 + applyRuntimeSetting 热应用全都在那条路上），
// 本端点只做「读取与分组」——一个语义化、一次拉齐的聚合面。
//
// ★ 设计纪律（承接 AGENTS.md 通用式「列在 schema 里 ≠ 通道存在」）：
//   redis/数据库连接是进程生命周期绑定的启动期配置，改了不可能不重启
//   生效 ⇒ 如实标注只读，不伪造可编辑入口。真值来源：
//     - redis:  config.Config.RedisAddr/Password/DB（cmd/gateway 装配面）
//     - 数据库: DATABASE_URL 的 host/db 名（口令掩码）
//     - 实例:   internal/instanceid.Resolve()（LLM_GATEWAY_INSTANCE_ID →
//               hostname → "llm-gateway"，多实例对待节点的身份真源）

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/instanceid"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// basicSettingDef 一个可编辑基础设置的元描述（前端渲染分组卡片用）。
type basicSettingDef struct {
	Key      string   `json:"key"`
	Group    string   `json:"group"` // identity | storage | schedules
	Type     string   `json:"type"`
	Options  []string `json:"options,omitempty"`
	Default  any      `json:"default"`
	Editable bool     `json:"editable"`
}

// basicGroups 分组定义：与前端 i18n 的 settings.basicConfig.group.* 对齐。
var basicGroups = []basicSettingDef{
	// ── 标识与外观 ──
	{Key: settings.GatewayDisplayNameKey, Group: "identity", Type: "string", Editable: true},
	{Key: settings.SystemSkinKey, Group: "identity", Type: "enum",
		Options: []string{"auto", "light", "dark"}, Default: settings.DefaultSystemSkin, Editable: true},
	{Key: settings.SystemTimezoneKey, Group: "identity", Type: "string", Editable: true},
	{Key: "gateway.mode", Group: "identity", Type: "enum",
		Options: []string{"simple", "expert"}, Default: "expert", Editable: true},
	{Key: settings.AttachmentPublicBaseURLKey, Group: "storage", Type: "string", Editable: true},
	{Key: "storage.attachment_dir_override", Group: "storage", Type: "string", Editable: true},
	{Key: "reports.daily_rollup.hour", Group: "schedules", Type: "int", Editable: true},
}

// registerBasicConfigRoutes mounts GET /api/admin/basic-config.
func (h *Handler) registerBasicConfigRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/admin/basic-config", h.admin(h.basicConfigGet))
}

func (h *Handler) basicConfigGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if settings.Global == nil {
		writeError(w, http.StatusServiceUnavailable, "settings registry not initialised")
		return
	}

	type entry struct {
		basicSettingDef
		Value       any    `json:"value"`
		Source      string `json:"source"`
		HotReload   bool   `json:"hot_reload"`
		DangerLevel int    `json:"danger_level"`
		Description string `json:"description"`
	}
	byKey := map[string]*entry{}
	for _, g := range basicGroups {
		sp := settings.Global.Spec(g.Key)
		if sp == nil {
			continue // 未注册的键不进面板（键面以 registry 为真源）
		}
		e := &entry{
			basicSettingDef: basicSettingDef{
				Key: g.Key, Group: g.Group, Type: g.Type,
				Options: g.Options, Default: g.Default, Editable: g.Editable,
			},
			HotReload:   sp.HotReload,
			DangerLevel: int(sp.DangerLevel),
			Description: sp.Description,
		}
		// Spec 上的 default/options 比分组定义更新（单一真源），覆盖之。
		e.Default = sp.Default
		if len(sp.Options) > 0 {
			e.Options = sp.Options
		}
		if sp.Scope == settings.ScopePlatform {
			raw, src, err := settings.Global.EffectiveValue(sp.Scope, sp.Key, "")
			if err != nil {
				continue
			}
			var v any
			if jsonErr := json.Unmarshal(raw, &v); jsonErr == nil {
				e.Value = v
			}
			e.Source = src
		}
		byKey[g.Key] = e
	}

	// 按组输出（保持定义顺序）。
	groupsOut := []map[string]any{}
	for _, g := range []string{"identity", "storage", "schedules"} {
		items := []*entry{}
		for _, def := range basicGroups {
			if def.Group == g {
				if e, ok := byKey[def.Key]; ok {
					items = append(items, e)
				}
			}
		}
		groupsOut = append(groupsOut, map[string]any{"id": g, "items": items})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"groups": groupsOut,
		"infra":  basicInfraFacts(),
	})
}

// basicInfraFacts 只读基础设施实况（启动期 env 配置；UI 标注「修改需重启」）。
// 口令/DSN 一律掩码：只暴露 host/db 名与「是否已配置」，不暴露凭据。
func basicInfraFacts() map[string]any {
	// 数据库：从 DATABASE_URL 解析 host/db（解析失败只报 configured=true）。
	dbInfo := map[string]any{"configured": false, "source": "env:DATABASE_URL"}
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		dbInfo["configured"] = true
		if u, err := url.Parse(dsn); err == nil && u.Host != "" {
			name := strings.TrimPrefix(u.Path, "/")
			dbInfo["host"] = u.Host
			dbInfo["database"] = name
			if u.User != nil {
				dbInfo["user"] = u.User.Username()
			}
		}
	}

	// Redis：与 cmd/gateway 装配面同源 env（LLM_GATEWAY_REDIS_ADDR）。
	redisAddr := strings.TrimSpace(os.Getenv("LLM_GATEWAY_REDIS_ADDR"))
	redisInfo := map[string]any{
		"configured": redisAddr != "",
		"addr":       redisAddr, // 地址不是凭据；口令永不回传
		"db":         os.Getenv("LLM_GATEWAY_REDIS_DB"),
	}

	return map[string]any{
		"instance_id": instanceid.Resolve(),
		"redis":       redisInfo,
		"database":    dbInfo,
		// 缓存目录与附件目录的 env 真值（storage_config.go 的解析逻辑在
		// domains/attachments 包内，进程内不重复实现——只报 env 原值）。
		"attachment_dir_env": os.Getenv("LLM_GATEWAY_ATTACHMENT_DIR"),
		"cache_dir_env":      os.Getenv("LLM_GATEWAY_CACHE_DIR"),
		"reload_hint":        "startup_env_readonly_restart_required",
	}
}
