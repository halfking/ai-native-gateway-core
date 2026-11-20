package bg

import (
	"os"
	"strings"
	"testing"
)

// ★ 这道门存在的理由（2026-11-20 会话分析闭环审计轮，缺陷 D3）：
//
//	refreshSessionViews 失败此前只有指标与日志、从不触发 alertCallback——
//	alertCallback 只接 routing 族。而 252 生产恰处「981 台账记 applied、
//	四视图全缺失」状态 ⇒ session 族刷新每轮必失败却无任何告警面，坏了一个月
//	没人知道。钉住「session 族也接告警」的源码结构 = 防止有人再把这条线拆掉
//	（同构先例：alertCallback 最初就只给了 routing 族，session 族漏接）。
func TestSessionViewRefreshWiredToAlertCallback(t *testing.T) {
	b, err := os.ReadFile("materialized_view_refresher.go")
	if err != nil {
		t.Fatalf("read materialized_view_refresher.go: %v", err)
	}
	src := string(b)

	// refreshSessionViews 函数体内必须出现连续失败记账 + 告警触发。
	start := strings.Index(src, "func (r *MaterializedViewRefresher) refreshSessionViews")
	if start < 0 {
		t.Fatalf("refreshSessionViews 不在 materialized_view_refresher.go 里——函数改名了？判据要跟着搬")
	}
	body := src[start:]
	if cut := strings.Index(body, "\nfunc "); cut >= 0 {
		body = body[:cut] // 只钉本函数体，不越界
	}
	if !strings.Contains(body, "r.sessionFailureCount++") {
		t.Fatalf("refreshSessionViews 丢了连续失败记账 ⇒ session 族刷新失败回到「只有日志无告警」")
	}
	// ★ 钉到**调用行**而不是标识符出现：`r.alertCallback != nil` 的条件判断
	//   也包含标识符——钉标识符会让「删掉真正的告警调用、留着条件」的变异
	//   幸存（2026-11-20 变异验证 M4 首版实测幸存，本行是收紧后的形态）。
	if !strings.Contains(body, "r.alertCallback(failedView, r.sessionFailureCount, lastErr)") {
		t.Fatalf("refreshSessionViews 丢了告警调用 ⇒ 252 式「每轮必失败无告警」回归")
	}
	// 全绿必须清零（否则一次抖动后永远处于「已失败」态，告警阈值失真）。
	if !strings.Contains(body, "r.sessionFailureCount = 0") {
		t.Fatalf("refreshSessionViews 缺失败计数清零 ⇒ 连续失败计数永不复位")
	}
}
