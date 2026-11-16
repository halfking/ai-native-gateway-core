package admin

// report_rollup_filter_parse_test.go —— 对账端点的**入参解析**回归门
//（2026-11-17 审计轮）。
//
// 为什么补这道门：三个 id 维度（provider / credential / api_key）共用
// 「-1 = 未落定」这一个哨兵，后端把它落成 `IS NULL`（否则筛未落定永远
// 0 行——真库实测凭据未落定桶 38,035 笔 = 全量 30.8%）。
// **这条链路的入口是 HTTP 查询参数**：前端下拉里那项的 value 是
// `Number("-1")` ⇒ 发 `credential_id=-1`。
// 若解析层把 -1 当成「非法 id」丢掉、或当成「不过滤」，用户点「未落定」
// 就会拿到全量或 0 行——而**解析层没有任何测试**（本文件之前）。

import (
	"net/http/httptest"
	"testing"
)

func TestReportViewFilter_UnassignedSentinelParsed(t *testing.T) {
	// 三个 id 维度各自的 -1 都必须原样进过滤条件（不能被吞、不能被拒）。
	type probe struct {
		query string
		want  int64
	}
	for _, tc := range []probe{
		{"provider_id=-1", -1},
		{"credential_id=-1", -1},
		{"api_key_id=-1", -1},
	} {
		r := httptest.NewRequest("GET", "/api/admin/report-rollup/summary?"+tc.query, nil)
		_, filter, err := reportViewFilter(r)
		if err != nil {
			t.Fatalf("%s: reportViewFilter: %v", tc.query, err)
		}
		got := int64(-999)
		switch {
		case filter.ProviderID != nil:
			got = *filter.ProviderID
		case filter.CredentialID != nil:
			got = *filter.CredentialID
		case filter.APIKeyID != nil:
			got = *filter.APIKeyID
		}
		if got != tc.want {
			t.Errorf("%s 解析成 %d，期望 %d（未落定哨兵必须原样透传，"+
				"被吞掉 ⇒ 点「未落定」会拿到全量；被当成非法 ⇒ 400 或空结果）", tc.query, got, tc.want)
		}
	}
}

func TestReportViewFilter_RealIDsParsed(t *testing.T) {
	r := httptest.NewRequest("GET",
		"/api/admin/report-rollup/summary?provider_id=587&credential_id=11&api_key_id=248", nil)
	_, filter, err := reportViewFilter(r)
	if err != nil {
		t.Fatalf("reportViewFilter: %v", err)
	}
	if filter.ProviderID == nil || *filter.ProviderID != 587 {
		t.Errorf("provider_id 解析错：%+v", filter.ProviderID)
	}
	if filter.CredentialID == nil || *filter.CredentialID != 11 {
		t.Errorf("credential_id 解析错：%+v", filter.CredentialID)
	}
	if filter.APIKeyID == nil || *filter.APIKeyID != 248 {
		t.Errorf("api_key_id 解析错：%+v", filter.APIKeyID)
	}
}

func TestReportViewFilter_InvalidIDRejected(t *testing.T) {
	// 非法 id 必须**报错**（400），不能静默当成「不过滤」——静默放行的
	// 后果是页面显示全量而用户以为筛过了。
	for _, q := range []string{"provider_id=abc", "api_key_id=1.5", "credential_id=12x"} {
		r := httptest.NewRequest("GET", "/api/admin/report-rollup/summary?"+q, nil)
		if _, _, err := reportViewFilter(r); err == nil {
			t.Errorf("%s 没有报错（非法 id 必须 400）", q)
		}
	}
}

func TestReportViewFilter_EmptyValueMeansNoFilter(t *testing.T) {
	// 空值 = **不过滤**（有意语义）：前端下拉是 clearable，清空后参数整个
	// 不带；带了空串也不该报错，更不该被当成「筛 id=空」。
	// 断言它**不进过滤条件**——若这里进了，下拉清空会变成「筛一个空 id」。
	r := httptest.NewRequest("GET",
		"/api/admin/report-rollup/summary?credential_id=&api_key_id=&provider_id=", nil)
	_, filter, err := reportViewFilter(r)
	if err != nil {
		t.Fatalf("空值不该报错：%v", err)
	}
	if filter.CredentialID != nil || filter.APIKeyID != nil || filter.ProviderID != nil {
		t.Errorf("空值被当成了过滤条件：%+v", filter)
	}
}

func TestReportRange_DefaultsAndBounds(t *testing.T) {
	// 缺省 = 昨日往前 7 天（UTC）。
	r := httptest.NewRequest("GET", "/api/admin/report-rollup/summary", nil)
	start, end, err := reportRange(r)
	if err != nil {
		t.Fatalf("reportRange: %v", err)
	}
	if d := end.Sub(start); d != 6*24*60*60*1e9 {
		t.Errorf("缺省区间长度 = %v，期望 6 天（start=%s end=%s）", d, start, end)
	}
	// end < start ⇒ 报错。
	r2 := httptest.NewRequest("GET",
		"/api/admin/report-rollup/summary?start=2026-11-15&end=2026-11-10", nil)
	if _, _, err := reportRange(r2); err == nil {
		t.Error("end 早于 start 没有报错")
	}
	// 区间超长（>366 天）⇒ 报错。
	r3 := httptest.NewRequest("GET",
		"/api/admin/report-rollup/summary?start=2020-01-01&end=2026-11-10", nil)
	if _, _, err := reportRange(r3); err == nil {
		t.Error("超长区间没有报错")
	}
}
