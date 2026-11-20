package settings

// system_config.go — 系统级公共配置键（2026-11-20 基础设置轮）。
//
// owner 需求：/admin/settings 页面把「与业务无关的系统级配置」整合成
// 一个基础设置分组直观呈现：时区、运行模式、多实例对待节点、redis、
// 数据库、缓存文件目录、附件存储的域名及访问路径、默认皮肤、网关的
// 个性名称、对帐周期、周期性时间设置等。
//
// 设计（与仓内既有机制同构，不另造第二套配置面）：
//   - 全部走 settings_kv 注册面（Spec + Category + HotReload），
//     DB > env > default，写入走既有 settingsPut（校验/审计/权限门）；
//   - 与时区轮同构：新键各自有**直读 helper**（调用方跟从 TTL 窗口语义），
//     admin 写入后按需失效进程内缓存实现即时生效；
//   - redis / 数据库连接是**启动期 env 配置**（进程生命周期绑定连接池，
//     改了不可能不重启生效）⇒ 它们不注册为可编辑 spec，而是通过
//     GET /api/admin/basic-config 的只读区如实展示（掩码口令），
//     UI 标注「修改需重启」。这是「列在 schema 里 ≠ 通道存在」纪律的
//     正向应用：没有热加载通道的配置不伪造可编辑入口。
//
// 三个新键：
//   1. gateway.display_name —— 网关个性名称（对外展示名，空 = 语言包默认）。
//   2. system.skin          —— 默认皮肤 light|dark|auto（未个性化用户的默认）。
//   3. storage.attachment_public_base_url —— 附件公开访问域名前缀；
//      留空 = /api/attachments/（网关自身托管）。
//      消费方 domains/session/v2 的 SetGatewayURLBase 在装配点读取，
//      admin 写入后经 applyRuntimeSetting 即时重置（热加载）。

import (
	"log/slog"
	"strings"
	"sync/atomic"
)

// GatewayDisplayNameKey 网关个性名称的平台设置键。
const GatewayDisplayNameKey = "gateway.display_name"

// SystemSkinKey 默认皮肤的平台设置键。
const SystemSkinKey = "system.skin"

// DefaultSystemSkin 未个性化用户的默认皮肤（跟随系统）。
const DefaultSystemSkin = "auto"

// AttachmentPublicBaseURLKey 附件公开访问域名前缀的平台设置键。
const AttachmentPublicBaseURLKey = "storage.attachment_public_base_url"

// DefaultAttachmentPublicBaseURL 与 domains/session/v2 defaultGatewayURLBase
// 同源：留空 = 网关自身托管（/api/attachments）。
const DefaultAttachmentPublicBaseURL = "/api/attachments"

// SystemConfigSpecs returns the platform specs for the basic-config group.
func SystemConfigSpecs() []*Spec {
	return []*Spec{
		{
			Key:         GatewayDisplayNameKey,
			Type:        TypeString,
			Scope:       ScopePlatform,
			Category:    CategoryGeneral,
			Default:     "",
			Description: "网关个性名称（对外展示名）",
			DescriptionLong: "管理端顶栏与登录页品牌名旁的网关个性名称；留空 = 使用语言包默认品牌文案。" +
				"纯展示层配置，不影响任何路由与业务逻辑。通过系统配置修改后即时生效（下次前端拉取 /api/admin/basic-config 时可见）。",
			Unit:        "",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         SystemSkinKey,
			Type:        TypeEnum,
			Scope:       ScopePlatform,
			Category:    CategoryGeneral,
			Options:     []string{"auto", "light", "dark"},
			Default:     DefaultSystemSkin,
			Description: "默认皮肤（未个性化用户的初始主题）",
			DescriptionLong: "管理端默认皮肤：auto（跟随系统亮暗偏好）｜light｜dark。" +
				"只影响**没有个性化主题选择**的浏览器会话（已把主题写入本机 localStorage 的用户不受影响，URL ?theme= 参数优先级最高）。" +
				"通过系统配置修改后即时生效（下次进入管理端时应用）。",
			Unit:        "",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         AttachmentPublicBaseURLKey,
			Type:        TypeString,
			Scope:       ScopePlatform,
			Category:    CategoryGeneral,
			Default:     DefaultAttachmentPublicBaseURL,
			Description: "附件公开访问域名前缀",
			DescriptionLong: "生成附件下载链接 / 出站多模态 URL 时使用的前缀。留空或填 /api/attachments = 网关自身托管（默认）；" +
				"填完整域名（如 https://files.example.com/attachments）= 走 CDN/对象存储自定义域。" +
				"带域名的新值只影响**此后生成的**链接，已有历史 URL 不回溯改写。通过系统配置修改后约 5 秒内热生效（进程内原子指针）。",
			Unit:          "URL",
			DangerLevel:   Warning,
			HotReload:     true,
			Observability: "/api/admin/basic-config",
		},
	}
}

// ── 直读 helper（调用方跟随 TTL/热加载窗口语义） ─────────────────────────

// GatewayDisplayName returns the configured display name ("" = unset →
// frontend falls back to its i18n brand text).
func GatewayDisplayName() string {
	return GetPlatformString(GatewayDisplayNameKey, "")
}

// SystemSkin returns the configured default skin (auto|light|dark).
func SystemSkin() string {
	v := GetPlatformString(SystemSkinKey, DefaultSystemSkin)
	switch v {
	case "auto", "light", "dark":
		return v
	default:
		return DefaultSystemSkin
	}
}

// ── 附件公开 URL 前缀：进程级原子指针（与时区轮同构的读面） ────────────────

var attachmentURLBase atomic.Pointer[string]

// AttachmentPublicBaseURL returns the effective public base for attachment
// URLs. Resolution order: process pointer (set at boot / admin write) →
// LLM_GATEWAY_ATTACHMENT_PUBLIC_URL env → default "/api/attachments".
// 后缀一律无斜杠（与 domains/session/v2 的 trim 语义一致）。
// ★ 空串指针 = 「未发布」，落回 env/default 链——不把空串当生效值。
func AttachmentPublicBaseURL() string {
	if p := attachmentURLBase.Load(); p != nil && *p != "" {
		return *p
	}
	if env := strings.TrimRight(strings.TrimSpace(GetEnvAttachmentPublicURL()), "/"); env != "" {
		return env
	}
	return DefaultAttachmentPublicBaseURL
}

// PublishAttachmentPublicBaseURL republishes the process pointer from a raw
// setting value (PUT / rollback / boot seeding). Empty → pointer unset
// （落回 env/default 链，不把空串当生效值）。写路径直接把**手上的 raw 值**
// 发布进来，不回读注册表——消灭写后读竞态，也允许无 DB 后端的单测验证。
func PublishAttachmentPublicBaseURL(raw string) {
	v := strings.TrimRight(strings.TrimSpace(raw), "/")
	if v == "" {
		attachmentURLBase.Store(nil)
		return
	}
	attachmentURLBase.Store(&v)
	slog.Info("settings: attachment public base url published",
		"key", AttachmentPublicBaseURLKey, "value", v)
}

// SeedAttachmentPublicBaseURLFromRegistry boot-seeds the pointer from the
// registry chain (DB > env > default)。DB 未配置时发布空 → 落回 env/default。
func SeedAttachmentPublicBaseURLFromRegistry() {
	PublishAttachmentPublicBaseURL(GetPlatformString(AttachmentPublicBaseURLKey, ""))
}

// GetEnvAttachmentPublicURL reads the boot env override (kept separate so
// the resolution chain stays: setting → env → default).
func GetEnvAttachmentPublicURL() string {
	if v := strings.TrimSpace(envString("LLM_GATEWAY_ATTACHMENT_PUBLIC_URL", "")); v != "" {
		return v
	}
	return ""
}
