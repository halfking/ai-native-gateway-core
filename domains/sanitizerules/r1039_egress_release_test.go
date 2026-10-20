package sanitizerules

// r1039（D4，2026-10-20）：egress 放行表的语义。
//
// ## 契约
//
//   - 放行必须**绑定租户**：租户为空 ⇒ 拒绝（没有边界 = 全网放行）。
//   - 规则**精确**匹配，不接受通配/前缀族。
//   - 放行记录可带载荷摘要：带了就只对**那一次**载荷生效。
//   - 过期记录不生效。
//   - 放行表只存在于**网关进程内**（服务端持有）——客户端读不到也写不了，
//     这正是「客户端自带头就能绕过」的教训要防的。

import (
	"testing"
	"time"
)

// withEgressReleases 临时替换放行表，跑完恢复。
func withEgressReleases(t *testing.T, releases ...EgressRelease) {
	t.Helper()
	prev := EgressReleases()
	t.Cleanup(func() { SetEgressReleases(prev) })
	SetEgressReleases(releases)
}

// TestR1039EgressReleaseRequiresTenant 钉住「必须有租户」。
func TestR1039EgressReleaseRequiresTenant(t *testing.T) {
	if err := AddEgressRelease(EgressRelease{Scope: "egress", Rule: "builtin.egress_sink"}); err == nil {
		t.Error("空租户的放行记录应被拒绝（那等于全网放行）")
	}
	withEgressReleases(t)
	if EgressReleaseAllowed("", "egress", "builtin.egress_sink", "") {
		t.Error("空租户不应命中任何放行记录")
	}
	if EgressReleaseAllowed("tenant-a", "egress", "builtin.egress_sink", "") {
		t.Error("没有放行记录时不应放行")
	}
}

// TestR1039EgressReleaseIsTenantScoped 钉住租户隔离。
func TestR1039EgressReleaseIsTenantScoped(t *testing.T) {
	withEgressReleases(t, EgressRelease{TenantID: "tenant-a", Scope: "egress", Rule: "builtin.egress_sink"})
	if !EgressReleaseAllowed("tenant-a", "egress", "builtin.egress_sink", "") {
		t.Error("被放行的租户未被命中")
	}
	if EgressReleaseAllowed("tenant-b", "egress", "builtin.egress_sink", "") {
		t.Error("放行串到了别的租户")
	}
	if EgressReleaseAllowed("tenant-a", "egress", "builtin.network_destination", "") {
		t.Error("放行串到了别的规则")
	}
	if EgressReleaseAllowed("tenant-a", "input", "builtin.egress_sink", "") {
		t.Error("放行串到了别的 scope")
	}
}

// TestR1039EgressReleaseDigestBinding 钉住「放行只对这一次载荷生效」。
func TestR1039EgressReleaseDigestBinding(t *testing.T) {
	withEgressReleases(t, EgressRelease{
		TenantID: "tenant-a", Scope: "egress", Rule: "builtin.egress_sink", PayloadDigest: "digest-a",
	})
	if !EgressReleaseAllowed("tenant-a", "egress", "builtin.egress_sink", "digest-a") {
		t.Error("摘要匹配的载荷未被放行")
	}
	if EgressReleaseAllowed("tenant-a", "egress", "builtin.egress_sink", "digest-b") {
		t.Error("摘要不匹配的载荷被放行了")
	}
	// 无摘要的记录覆盖该租户在该规则下的全部载荷。
	withEgressReleases(t, EgressRelease{TenantID: "tenant-a", Scope: "egress", Rule: "builtin.egress_sink"})
	if !EgressReleaseAllowed("tenant-a", "egress", "builtin.egress_sink", "digest-b") {
		t.Error("无摘要的放行记录未覆盖其它载荷")
	}
}

// TestR1039EgressReleaseExpiry 钉住过期语义。
func TestR1039EgressReleaseExpiry(t *testing.T) {
	withEgressReleases(t, EgressRelease{
		TenantID: "tenant-a", Scope: "egress", Rule: "builtin.egress_sink",
		ExpiresAt: time.Now().Add(-time.Minute),
	})
	if EgressReleaseAllowed("tenant-a", "egress", "builtin.egress_sink", "") {
		t.Error("过期记录仍在放行")
	}
	if err := AddEgressRelease(EgressRelease{
		TenantID: "tenant-a", Scope: "egress", Rule: "builtin.egress_sink",
		ExpiresAt: time.Now().Add(-time.Minute),
	}); err == nil {
		t.Error("已过期的放行记录应被拒绝写入")
	}
}

// TestR1039EgressReleaseAddAndRemove 钉住增删语义（同键覆盖）。
func TestR1039EgressReleaseAddAndRemove(t *testing.T) {
	withEgressReleases(t)
	release := EgressRelease{TenantID: "tenant-a", Scope: "egress", Rule: "builtin.egress_sink", ApprovedBy: "ops"}
	if err := AddEgressRelease(release); err != nil {
		t.Fatalf("AddEgressRelease: %v", err)
	}
	if err := AddEgressRelease(release); err != nil {
		t.Fatalf("AddEgressRelease (dup): %v", err)
	}
	if got := len(EgressReleases()); got != 1 {
		t.Errorf("重复写入应覆盖同键，实际 %d 条", got)
	}
	if !RemoveEgressRelease("tenant-a", "egress", "builtin.egress_sink", "") {
		t.Error("RemoveEgressRelease 未命中")
	}
	if got := len(EgressReleases()); got != 0 {
		t.Errorf("删除后应为空，实际 %d 条", got)
	}
	if RemoveEgressRelease("tenant-a", "egress", "builtin.egress_sink", "") {
		t.Error("重复删除不应再命中")
	}
}

// TestR1039EgressReleaseRejectsUnknownScope 钉住 scope 白名单。
func TestR1039EgressReleaseRejectsUnknownScope(t *testing.T) {
	if err := AddEgressRelease(EgressRelease{TenantID: "tenant-a", Scope: "input", Rule: "x"}); err == nil {
		t.Error("未知 scope 应被拒绝")
	}
	if err := AddEgressRelease(EgressRelease{TenantID: "tenant-a", Scope: "egress", Rule: ""}); err == nil {
		t.Error("空规则应被拒绝")
	}
}
