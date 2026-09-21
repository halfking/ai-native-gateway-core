package pluginruntime

// nav_validate.go — 2026-09-05 审计闭环8：远程插件菜单（plugin nav）的
// schema 与权限裁剪校验。
//
// 审计风险：插件 manifest 的 pages/nav 之前不做任何校验，直接进入
// /api/v1/plugin-nav 提供给前端渲染：
//   - page path 未约束 → RouteURL = "/plugins/{id}/{path}" 可携带
//     ".."、前导 "/"、协议片段等，形成前端路由注入面；
//   - nav.group / label_key 为自由文本 → 低基数维度被污染、i18n key
//     任意字符串进入前端翻译查找；
//   - order 无界 → 前端排序被单个插件操纵；
//   - 页面数量无上限 → 菜单膨胀（DoS 面）。
//
// 本文件在 manifest.validate() 中补齐 pages/nav 的受控词表校验：
// 校验失败 = manifest 无效 = 插件不注册菜单（fail closed）。

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// 导航 schema 约束（低基数 + 有界）。
const (
	maxNavPages      = 64
	maxNavGroupLen   = 64
	maxNavLabelKey   = 128
	maxNavOrderValue = 10000
)

var (
	// navPagePathRe 页面相对路径：小写词 + 层级分隔；禁止前导 /、
	// ".." 段、空段、结尾 /。
	navPagePathRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*(/[a-z0-9][a-z0-9_-]*)*$`)
	// navGroupRe 菜单分组：低基数小写词表值。
	navGroupRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	// navLabelKeyRe i18n key：点分层级词（允许 camelCase，前端
	// labelKey 惯例如 nav.item.sessionPlugin）。
	navLabelKeyRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*(\.[a-zA-Z0-9][a-zA-Z0-9_-]*)+$`)
)

// validateNavPages 校验 manifest.pages 的 schema。pluginID 仅用于错误信息。
func validateNavPages(pluginID string, pages []Page) error {
	if len(pages) > maxNavPages {
		return fmt.Errorf("pages: %d exceeds the limit of %d", len(pages), maxNavPages)
	}
	seen := make(map[string]struct{}, len(pages))
	for i, p := range pages {
		if err := validateNavPage(p); err != nil {
			return fmt.Errorf("pages[%d]: %w", i, err)
		}
		key := path.Clean("/" + strings.TrimPrefix(p.Path, "/"))
		if _, dup := seen[key]; dup {
			return fmt.Errorf("pages[%d]: duplicate page path %q", i, p.Path)
		}
		seen[key] = struct{}{}
	}
	// pluginID 目前只用于签名一致性；保留参数以便将来按插件放宽。
	_ = pluginID
	return nil
}

func validateNavPage(p Page) error {
	if p.Path == "" {
		return fmt.Errorf("path required")
	}
	if strings.HasPrefix(p.Path, "/") || strings.Contains(p.Path, "//") ||
		strings.HasSuffix(p.Path, "/") {
		return fmt.Errorf("path %q must be relative with single separators", p.Path)
	}
	for _, seg := range strings.Split(p.Path, "/") {
		if seg == ".." || seg == "." {
			return fmt.Errorf("path %q must not contain . or .. segments", p.Path)
		}
	}
	if !navPagePathRe.MatchString(p.Path) {
		return fmt.Errorf("path %q uses characters outside [a-z0-9._-]", p.Path)
	}
	// path.Clean 不改变语义校验通过者；防 "..\.."（Windows 分隔符混入）。
	if strings.Contains(p.Path, `\`) {
		return fmt.Errorf("path %q must not contain backslashes", p.Path)
	}
	if p.Nav == nil {
		return nil // 非 nav 页面（插件内页）只需路径安全
	}
	if len(p.Nav.Group) > maxNavGroupLen {
		return fmt.Errorf("nav.group exceeds %d bytes", maxNavGroupLen)
	}
	if p.Nav.Group != "" && !navGroupRe.MatchString(p.Nav.Group) {
		return fmt.Errorf("nav.group %q must be a low-cardinality [a-z0-9._-] token", p.Nav.Group)
	}
	if p.Nav.LabelKey == "" {
		return fmt.Errorf("nav.label_key required for nav pages")
	}
	if len(p.Nav.LabelKey) > maxNavLabelKey {
		return fmt.Errorf("nav.label_key exceeds %d bytes", maxNavLabelKey)
	}
	if !navLabelKeyRe.MatchString(p.Nav.LabelKey) {
		return fmt.Errorf("nav.label_key %q must be a dotted i18n key (e.g. nav.plugin.title)", p.Nav.LabelKey)
	}
	if p.Nav.Order < -maxNavOrderValue || p.Nav.Order > maxNavOrderValue {
		return fmt.Errorf("nav.order %d out of ±%d", p.Nav.Order, maxNavOrderValue)
	}
	// 权限位是受控三元组（super/platform_ops/tenant_only），无自由值；
	// tenant_only 与 platform_ops 同时置位属契约矛盾（租户门户页不可能
	// 是平台运维页），拒绝以避免前端裁剪歧义。
	if p.Nav.TenantOnly && p.Nav.PlatformOps {
		return fmt.Errorf("nav.tenant_only and nav.platform_ops are mutually exclusive")
	}
	return nil
}
