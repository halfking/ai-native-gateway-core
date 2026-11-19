package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// settings_timezone.go — 系统时区读/便捷写端点（2026-11-20 时区设置轮）。
//
// GET /api/admin/system-timezone →
//   { timezone, default, utc_offset, now, choices[] }
//   前端启动时读取：IANA 名交给 datetime.ts 格式化面与 kxDatePresets /
//   boardTimeRange 日切面，保证「显示的时区」「查询的日切口径」与后端一致。
//
// PUT /api/admin/system-timezone { timezone } →
//   便捷写：校验 IANA 可加载后走 StoreDB.Set + WriteAudit（与 settingsPut
//   平台分支同构），写成功即时失效进程内时区缓存。等价于
//   PUT /api/admin/settings/system.timezone {"value": "..."}，
//   但设置页之外（脚本/移动端）有一个语义化入口。
//
// 通用设置页照常可改 system.timezone（settings spec 已注册，HotReload，
// TTL ≤5 分钟）；本端点把生效窗口缩短为立即（写后 ResetSystemTimezoneCache）。

// timezoneEntry 常见时区候选（前端设置页下拉直接渲染）。表外合法 IANA 名
// 仍可写入（settings 通道只校验可加载，不做白名单限制）。
func timezoneEntry(name, label string) map[string]string {
	return map[string]string{"name": name, "label": label}
}

func systemTimezoneChoices() []map[string]string {
	return []map[string]string{
		timezoneEntry("Asia/Shanghai", "UTC+8 北京"),
		timezoneEntry("Asia/Hong_Kong", "UTC+8 香港"),
		timezoneEntry("Asia/Taipei", "UTC+8 台北"),
		timezoneEntry("Asia/Singapore", "UTC+8 新加坡"),
		timezoneEntry("Asia/Tokyo", "UTC+9 东京"),
		timezoneEntry("Asia/Seoul", "UTC+9 首尔"),
		timezoneEntry("Asia/Bangkok", "UTC+7 曼谷"),
		timezoneEntry("Asia/Kolkata", "UTC+5:30 德里"),
		timezoneEntry("Asia/Dubai", "UTC+4 迪拜"),
		timezoneEntry("Europe/Moscow", "UTC+3 莫斯科"),
		timezoneEntry("Europe/Berlin", "UTC+1 柏林"),
		timezoneEntry("Europe/Paris", "UTC+1 巴黎"),
		timezoneEntry("Europe/London", "UTC+0 伦敦"),
		timezoneEntry("UTC", "UTC"),
		timezoneEntry("America/New_York", "UTC-5 纽约"),
		timezoneEntry("America/Chicago", "UTC-6 芝加哥"),
		timezoneEntry("America/Denver", "UTC-7 丹佛"),
		timezoneEntry("America/Los_Angeles", "UTC-8 洛杉矶"),
		timezoneEntry("Australia/Sydney", "UTC+10 悉尼"),
		timezoneEntry("Pacific/Auckland", "UTC+12 奥克兰"),
	}
}

// registerTimezoneRoutes 挂载系统时区端点（Handler 路由装配处调用）。
func (h *Handler) registerTimezoneRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/admin/system-timezone", h.admin(h.systemTimezoneRouter))
}

func (h *Handler) systemTimezoneRouter(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.systemTimezoneGet(w)
	case http.MethodPut:
		h.systemTimezonePut(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func timezoneView(name string) map[string]any {
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	_, offsetSec := now.Zone()
	return map[string]any{
		"timezone":   name,
		"utc_offset": formatOffset(offsetSec),
		"now":        now.Format(time.RFC3339),
	}
}

// formatOffset 把秒偏移格式化为 "UTC+8:00" 形态。
func formatOffset(sec int) string {
	sign := "+"
	if sec < 0 {
		sign = "-"
		sec = -sec
	}
	h, m := sec/3600, (sec%3600)/60
	if m == 0 {
		return fmt.Sprintf("UTC%s%d:00", sign, h)
	}
	return fmt.Sprintf("UTC%s%d:%02d", sign, h, m)
}

func (h *Handler) systemTimezoneGet(w http.ResponseWriter) {
	name := settings.SystemTimezoneName()
	view := timezoneView(name)
	view["default"] = settings.DefaultSystemTimezone
	view["choices"] = systemTimezoneChoices()
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) systemTimezonePut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Timezone string `json:"timezone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	name := strings.TrimSpace(body.Timezone)
	if err := settings.ValidateTimezoneExported(name); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	if settings.Global == nil || h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "settings not available")
		return
	}
	store, ok := h.dbSettingsStore()
	if !ok {
		writeError(w, http.StatusInternalServerError, "settings store not wired")
		return
	}

	ctx := r.Context()
	oldVal, err := store.Set(settings.ScopePlatform, settings.SystemTimezoneKey, name)
	if err != nil {
		writeInternalErr(w, "save failed", err)
		return
	}
	// 即时生效：失效进程内缓存（区别于通用通道的 ≤5min TTL 窗口）。
	settings.ResetSystemTimezoneCacheForAdminWrite()

	user, role, ip := authIdentity(r)
	settings.WriteAudit(ctx, h.db, settings.AuditEntry{
		SettingKey:   settings.SystemTimezoneKey,
		Action:       "update",
		OldValue:     json.RawMessage(oldVal),
		NewValue:     json.RawMessage(`"` + name + `"`),
		OperatorUser: user,
		OperatorRole: role,
		ClientIP:     ip,
	})

	writeJSON(w, http.StatusOK, timezoneView(settings.SystemTimezoneName()))
}
