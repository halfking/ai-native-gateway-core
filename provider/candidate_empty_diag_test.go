// provider/candidate_empty_diag_test.go —— 「候选为空」归因的**判据**测试（2026-11-12）
//
// ★ 这道测试钉的是**归因结论**（Summarize 的输出），不是 SQL 本身：
// ★ SQL 只能在真库上验（realdb 腿），而**结论分类**是纯函数 ⇒ 可以且应该被机器执行。
//
// ★ 为什么值得钉：★ 归因的**价值**全在「结论是否指向正确的那道闸」——
// ★ 一旦把「模型没登记」和「被状态闸滤空」搞混，运营就会朝错的方向查半天。
package provider

import (
	"strings"
	"testing"
)

func TestEmptyCandidateBreakdownSummarize(t *testing.T) {
	cases := []struct {
		name    string
		in      EmptyCandidateBreakdown
		must    []string
		mustNot []string
	}{
		{
			name: "模型未登记 ⇒ 结论指向登记而不是状态",
			in:   EmptyCandidateBreakdown{},
			must: []string{"模型未登记", "登记模型"},
			// ★ 关键：不能说成「被状态闸滤掉」⇒ 运维会去查凭据，方向错。
			mustNot: []string{"credentials.status", "lifecycle_status"},
		},
		{
			name: "遗留 v1 status 闸 ⇒ 结论必须点名 credentials.status（211 真机实测形态）",
			in: EmptyCandidateBreakdown{
				OffersTotal: 3, BlockedByCredentialStatus: 2,
			},
			must:    []string{"模型已登记", "credentials.status", "3 offer", "2 条"},
			mustNot: []string{"模型未登记"},
		},
		{
			name: "生命周期闸 ⇒ 不能与遗留 status 闸混说",
			in: EmptyCandidateBreakdown{
				OffersTotal: 1, BlockedByCredentialLifecycle: 1,
			},
			must:    []string{"lifecycle_status"},
			mustNot: []string{"credentials.status"},
		},
		{
			name: "凭据缺失 ⇒ 结论指向改绑",
			in: EmptyCandidateBreakdown{
				OffersTotal: 2, BlockedByCredentialMissing: 1,
			},
			must:    []string{"缺失", "改绑"},
			mustNot: []string{"credentials.status"},
		},
		{
			name: "配额闸",
			in: EmptyCandidateBreakdown{
				OffersTotal: 1, BlockedByCredentialQuota: 1,
			},
			must: []string{"配额"},
		},
		{
			name: "供应商停用闸",
			in: EmptyCandidateBreakdown{
				OffersTotal: 4, BlockedByProviderDisabled: 4,
			},
			must: []string{"供应商"},
		},
		{
			name: "offer 人工下线闸",
			in: EmptyCandidateBreakdown{
				OffersTotal: 1, BlockedByUnavailableReason: 1,
			},
			must: []string{"unavailable_reason"},
		},
		{
			name: "★ 状态闸全开 ⇒ 必须明说「状态不是原因」（免得运维继续朝状态查）",
			in: EmptyCandidateBreakdown{
				OffersTotal: 5,
			},
			must:    []string{"状态不是原因"},
			mustNot: []string{"credentials.status", "lifecycle_status", "模型未登记"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.Summarize()
			for _, m := range tc.must {
				if !strings.Contains(got, m) {
					t.Errorf("归因结论缺少 %q\n实际：%s", m, got)
				}
			}
			for _, m := range tc.mustNot {
				if strings.Contains(got, m) {
					t.Errorf("归因结论不应出现 %q（会把运维引到错误的闸上）\n实际：%s", m, got)
				}
			}
		})
	}
}

// TestEmptyCandidateBreakdownSQLMatchesGatePredicates —— ★ 口径钉：
// 归因 SQL 里出现的**状态闸关键字**必须与候选 SQL 用的是同一批，
// 否则会出现「诊断说被某闸滤掉、实际闸在别处」的假归因。
//
// ★ 这里钉的是**判据面**（关键字集合），不是逐字 SQL——★ 逐字比对会被无意义的
// ★ 空格/换行改动打碎；★ 关键字集合才是「口径没分叉」的真正不变量。
func TestEmptyCandidateBreakdownSQLMatchesGatePredicates(t *testing.T) {
	mustHave := []string{
		"credentials",        // 闸的宿主表
		"lifecycle_status",   // 生命周期闸
		"quota_state",        // 配额闸
		"status",             // 遗留 v1 健康闸
		"manual_disabled",    // 人工停用闸
		"providers",          // 供应商闸
		"base_url",           // 供应商闸（空 base_url）
		"protocol",           // 供应商闸（脏 protocol）
		"unavailable_reason", // offer 人工下线闸
		"model_aliases",      // ★ 4 路模型匹配矩阵（别名）
		"models_canonical",   // ★ 4 路模型匹配矩阵（canonical）
		"canonical_raw_name", // ★ 4 路模型匹配矩阵（offer 侧）
		"standardized_name",  // ★ 4 路模型匹配矩阵（offer 侧）
	}
	for _, k := range mustHave {
		if !strings.Contains(emptyCandidateBreakdownSQL, k) {
			t.Errorf("归因 SQL 缺少关键字 %q ⇒ 诊断口径与候选闸集合分叉了", k)
		}
	}
	// ★ 「模型未登记」与「被闸滤空」必须能被区分 ⇒ 归因 SQL 必须同时给出
	// ★ 总数与各闸计数（不是只给闸计数）。
	for _, k := range []string{"COUNT(*)::int AS offers_total", "FILTER (WHERE"} {
		if !strings.Contains(emptyCandidateBreakdownSQL, k) {
			t.Errorf("归因 SQL 缺少 %q ⇒ 无法区分「模型没登记」与「被闸滤空」", k)
		}
	}
}
