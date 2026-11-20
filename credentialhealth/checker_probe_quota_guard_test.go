package credentialhealth

import (
	"strings"
	"testing"
)

// TestCmbExpiredRecoverySQL_ProbeQuotaAttributionNeedsReadyCredential
// pins the 2026-11-20 node-probing round guard extension.
//
// ★ 背景：探针的配额族落账（bg/node_probe_write_through.go quotaCredentialSQL）
//
//	写出的绑定理由是 `probe_quota_<kind>`（例如 probe_quota_permanent）——
//	与业务面的 `auto_credential_<kind>` 同属**凭据级归因**。
//
// ★ 为什么必须加守卫：凭据被配额停用（availability_state='suspended'）时，
//
//	它的绑定冷却到期后会被本语句翻绿 ⇒ 又一次「绑定面说可用、凭据面说
//	不可用」的面间漂移（F4 守卫当初就是为了防这个而加的，只是当时只有
//	auto_credential_% 这一族）。
//
// ⇒ 口径：**凭据级归因的绑定，只有在凭据回到 ready 之后才允许翻绿**。
func TestCmbExpiredRecoverySQL_ProbeQuotaAttributionNeedsReadyCredential(t *testing.T) {
	sql := cmbExpiredRecoverySQL()

	// ★ 模式用 'probe_quota%'（**不带**尾部下划线）：探针的配额族里
	//   errorsx.KindQuota 的 reason 就是裸 "quota" ⇒ 绑定理由 = "probe_quota"
	//   （11 字符）⇒ 'probe_quota_%' 匹配不到它（LIKE 里 '_' 是单字符通配，
	//   该模式至少要 12 字符）⇒ 裸 quota 族会漏掉守卫 ⇒ 又一次面间漂移。
	if !strings.Contains(sql, "COALESCE(cmb.unavailable_reason, '') NOT LIKE 'probe_quota%'") {
		t.Fatalf("cmb 恢复语句缺少探针配额族（probe_quota%%）的凭据级守卫：\n%s", sql)
	}

	// ★ 反向钉：守卫必须是**配对**的（两个 NOT LIKE 在同一组括号里）——
	//   否则 SQL 的 AND/OR 优先级会把守卫变成「永远放行」。
	groupStart := strings.Index(sql, "NOT LIKE 'auto_credential_%'")
	if groupStart < 0 {
		t.Fatalf("cmb 恢复语句缺少 auto_credential_%% 守卫：\n%s", sql)
	}
	group := sql[groupStart:]
	if !strings.Contains(group[:strings.Index(group, "OR EXISTS")], "NOT LIKE 'probe_quota%'") {
		t.Fatalf("probe_quota%% 守卫必须与 auto_credential_%% 守卫同组（AND 优先级）：\n%s", sql)
	}

	// 凭据 ready 守卫必须仍然在（两个族共用它）。
	if !strings.Contains(sql, "c2.availability_state = 'ready'") {
		t.Fatalf("cmb 恢复语句缺少「凭据已 ready」守卫：\n%s", sql)
	}
}
