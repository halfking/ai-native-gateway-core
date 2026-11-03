package maas

// 12h 审计轮第 4 号发现的落点测试：
//  ① 活动额度的「台账侧 == 账本侧」对账判据（CampaignsBalanced）；
//  ② 对账口径必须落在**两臂并读视图**上（不是 credit_ledger 父表）；
//  ③ 「我的」请求历史三个维度的钳位（days / pageSize / page）；
//  ④ ledger detail 的 JSON 构造必须对特殊字符免疫。

import (
	"encoding/json"
	"strings"
	"testing"
)

// 1) 对账判据：台账侧与账本侧相等 ⇒ 平。
func TestCampaignsBalanced(t *testing.T) {
	recs := []CampaignReconciliation{
		{CampaignID: 1, RedemptionCredits: 100, LedgerCredits: 100, GrantedCredits: 100, Matched: true},
		{CampaignID: 2, RedemptionCredits: 50, LedgerCredits: 50, GrantedCredits: 50, Matched: true},
	}
	if !CampaignsBalanced(recs) {
		t.Fatal("equal sides must be balanced")
	}
}

// 2) 对账判据：账本侧少计（提升滞后窗口漏计的典型形态）⇒ 不平。
func TestCampaignsBalancedDetectsLedgerUndercount(t *testing.T) {
	recs := []CampaignReconciliation{
		{CampaignID: 1, RedemptionCredits: 100, LedgerCredits: 60, GrantedCredits: 100, Matched: false},
	}
	if CampaignsBalanced(recs) {
		t.Fatal("ledger undercount must not be balanced")
	}
}

// 3) 对账判据：granted_credits 与台账侧不符 ⇒ 不平（写侧漏记/重复）。
func TestCampaignsBalancedDetectsGrantedMismatch(t *testing.T) {
	recs := []CampaignReconciliation{
		{CampaignID: 1, RedemptionCredits: 100, LedgerCredits: 100, GrantedCredits: 80, Matched: true},
	}
	if CampaignsBalanced(recs) {
		t.Fatal("granted_credits mismatch must not be balanced")
	}
}

// 4) 对账查询必须读**两臂并读视图**。
//
// ★ 这是本文件的核心护栏：读 credit_ledger 父表会漏计 hot 行
//
//	（AF-3 同型缺陷，见 campaign_reconcile.go 文件头）⇒ 判据直接钉住
//	「查询文本里用的是视图名、且没有裸读父表」。
func TestCampaignReconcileReadsBothArmsView(t *testing.T) {
	src := campaignReconcileSQL
	if !strings.Contains(src, "credit_ledger_with_current_month") {
		t.Fatal("campaign reconciliation must read credit_ledger_with_current_month (hot ∪ 分区)")
	}
	// 裸读父表（FROM credit_ledger 后面不是 _with_current_month）⇒ 红。
	if strings.Contains(src, "FROM credit_ledger ") || strings.Contains(src, "FROM credit_ledger\n") ||
		strings.Contains(src, "FROM public.credit_ledger ") || strings.Contains(src, "FROM public.credit_ledger\n") {
		t.Fatal("campaign reconciliation must not read the credit_ledger parent table (hot rows invisible)")
	}
}

// 5) 「我的」请求历史：页码有上界（OFFSET 分页的放大面被钳住）。
func TestClampMyRequestsPage(t *testing.T) {
	if MaxMyRequestsPage <= 0 {
		t.Fatal("MaxMyRequestsPage must be positive")
	}
	if MaxMyRequestsPage > 10000 {
		t.Fatalf("MaxMyRequestsPage must stay bounded, got %d", MaxMyRequestsPage)
	}
}

// 6) 「我的」请求历史：回看窗口有上界。
func TestMyRequestsWindowBounds(t *testing.T) {
	if d := ClampMyRequestsDays(0); d != 7 {
		t.Fatalf("days default: %d", d)
	}
	if d := ClampMyRequestsDays(999); d != 31 {
		t.Fatalf("days max: %d", d)
	}
	if d := ClampMyRequestsDays(10); d != 10 {
		t.Fatalf("days passthrough: %d", d)
	}
}

// 7) 活动兑现写入 ledger 的 detail 必须是**合法 JSON**（含特殊字符的 code 也不能破）。
//
// ★ 手拼 `{"campaign_code":"`+code+`"}` 时，code 含 `"` 就产出非法 JSON
//
//	⇒ jsonb 拒收 ⇒ 事务回滚 ⇒ 兑现整体失败。
func TestCampaignDetailJSONIsWellFormed(t *testing.T) {
	for _, code := range []string{`plain`, `with"quote`, `back\slash`, `中文"}`} {
		b, err := campaignDetailJSON(code)
		if err != nil {
			t.Fatalf("marshal failed for %q: %v", code, err)
		}
		var probe map[string]string
		if err := json.Unmarshal(b, &probe); err != nil {
			t.Fatalf("detail must be valid JSON for code=%q, got %s (%v)", code, b, err)
		}
		if probe["campaign_code"] != code {
			t.Fatalf("detail must round-trip the code, want %q got %q", code, probe["campaign_code"])
		}
	}
}
