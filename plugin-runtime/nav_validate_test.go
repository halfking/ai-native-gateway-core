package pluginruntime

// nav_validate_test.go — 2026-09-05 审计闭环8回归：远程插件菜单的
// schema/权限裁剪校验。校验失败 = manifest 无效 = 不注册菜单（fail closed）。

import (
	"strings"
	"testing"
)

func baseNavPage(path string) Page {
	return Page{
		Path: path,
		Type: "page",
		Nav: &Nav{
			Group:    "plugins",
			LabelKey: "nav.plugin.title",
			Order:    10,
		},
	}
}

func TestValidateNavPagesAcceptsWellFormed(t *testing.T) {
	pages := []Page{
		baseNavPage("dashboard"),
		baseNavPage("settings/general"),
		{Path: "hidden-page", Type: "page"}, // 非 nav 内页：仅路径校验
	}
	if err := validateNavPages("session-manager", pages); err != nil {
		t.Fatalf("well-formed pages rejected: %v", err)
	}
}

func TestValidateNavPagesRejectsPathInjection(t *testing.T) {
	cases := map[string]string{
		"absolute path":  "/admin",
		"parent segment": "../../admin",
		"dot segment":    "./settings",
		"double slash":   "a//b",
		"trailing slash": "dashboard/",
		"backslash":      `dashboard\settings`,
		"empty path":     "",
		"uppercase":      "Dashboard",
		"shell metachar": "dashboard?next=//evil",
		"space":          "dashboard settings",
	}
	for name, p := range cases {
		if err := validateNavPages("p", []Page{baseNavPage(p)}); err == nil {
			t.Errorf("%s: path %q must be rejected", name, p)
		}
	}
}

func TestValidateNavPagesRejectsFreeTextGroupAndLabelKey(t *testing.T) {
	// group 是低基数词表：自由文本（含空格/大写/超长）必须拒绝。
	p := baseNavPage("dashboard")
	p.Nav.Group = "Not A Cardinality Safe Group"
	if err := validateNavPages("p", []Page{p}); err == nil {
		t.Fatal("free-text group must be rejected")
	}

	long := baseNavPage("dashboard")
	long.Nav.Group = strings.Repeat("g", maxNavGroupLen+1)
	if err := validateNavPages("p", []Page{long}); err == nil {
		t.Fatal("oversized group must be rejected")
	}

	// label_key 必须是点分 i18n key（允许 camelCase）。
	noKey := baseNavPage("dashboard")
	noKey.Nav.LabelKey = ""
	if err := validateNavPages("p", []Page{noKey}); err == nil {
		t.Fatal("empty label_key must be rejected")
	}
	notDotted := baseNavPage("dashboard")
	notDotted.Nav.LabelKey = "justatoken"
	if err := validateNavPages("p", []Page{notDotted}); err == nil {
		t.Fatal("non-dotted label_key must be rejected")
	}
	camelOK := baseNavPage("dashboard")
	camelOK.Nav.LabelKey = "nav.item.sessionPlugin"
	if err := validateNavPages("p", []Page{camelOK}); err != nil {
		t.Fatalf("camelCase dotted label_key must be accepted: %v", err)
	}
}

func TestValidateNavPagesRejectsUnboundedOrderAndDuplicates(t *testing.T) {
	huge := baseNavPage("dashboard")
	huge.Nav.Order = maxNavOrderValue + 1
	if err := validateNavPages("p", []Page{huge}); err == nil {
		t.Fatal("unbounded order must be rejected")
	}

	dup := []Page{baseNavPage("dashboard"), baseNavPage("dashboard")}
	if err := validateNavPages("p", dup); err == nil {
		t.Fatal("duplicate page path must be rejected")
	}
}

func TestValidateNavPagesRejectsExcessPagesAndContradictoryFlags(t *testing.T) {
	pages := make([]Page, 0, maxNavPages+1)
	for i := 0; i <= maxNavPages; i++ {
		pages = append(pages, baseNavPage("page-"+strings.Repeat("a", i+1)))
	}
	if err := validateNavPages("p", pages); err == nil {
		t.Fatal("page count over the limit must be rejected")
	}

	// tenant_only 与 platform_ops 矛盾位：前端裁剪歧义，fail closed。
	contradiction := baseNavPage("dashboard")
	contradiction.Nav.TenantOnly = true
	contradiction.Nav.PlatformOps = true
	if err := validateNavPages("p", []Page{contradiction}); err == nil {
		t.Fatal("tenant_only+platform_ops must be rejected")
	}
}
