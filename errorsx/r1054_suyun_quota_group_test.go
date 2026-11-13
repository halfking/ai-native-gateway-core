// r1054_suyun_quota_group_test.go —— 2026-11-14 suyun 轮。
//
// ★★★ owner 报障：routing-v2 resolve 页查 claude-opus-5-5，suyun 节点显示
// 不可用（鉴权失败），但请求日志里大量流量打向 suyun。211 取证定案：
//
//	· dispatch 侧**守约**：cred 39 auth_failed 窗口（01:32:56→01:47:56）
//	  内零流量，恢复（01:48:37 ready）后流量立即回来——「不可用却有流量」
//	  是时间错位 + resolve 页**报错理由**（虚报鉴权失败）。
//	· 根因＝两个 new_api 家族 403 形态漏分类：
//	  F1 余额预扣失败（insufficient_user_quota /「预扣费额度失败」）被 403
//	    状态门兜底成 KindAuth ⇒ 整凭据 auth_failed 15 分钟（cred 39，
//	    探针 150 连胜 + 96 笔真实流量全成功的健康凭据被误标）；
//	  F2 单模型分组权限拒绝（「无权访问 X 分组」）同样落 KindAuth ⇒
//	    整凭据 auth_failed 按探针退避梯推到 24h（cred 71，同凭据
//	    opus-5-5 探针 95 连胜完全健康）。
//
// 本文件把两个真实生产体钉成新裁决，并钉住阴性对照（不放宽真 auth）。
//
// 生产体逐字来源：
//   · F1: credentials.state_reason_detail（cred 39，state_updated_at
//     2026-11-14 01:32:56+08）+ candidate_failure_logs 11-12/11-13 同族行
//     （error_kind='auth'、upstream_status_code=403）。
//   · F2: credentials.state_reason_detail（cred 71，state_updated_at
//     2026-11-14 00:21:34+08，recover_at 2026-11-15 00:21:34）。
package errorsx

import "testing"

// r1054InsufficientUserQuotaBody 是 cred 39 的 403 体（逐字，request id
// 原文含时间戳 173256＝UTC 17:32:56＝+08 01:32:56）。
const r1054InsufficientUserQuotaBody = `{"error":{"message":"预扣费额度失败, 用户剩余额度: ¥1.474664, 需要预扣费额度: ¥2.497896 (request id: 202611131732566241518818268d9d6tYC26mfG)","type":"new_api_error","param":"","code":"insufficient_user_quota"}}`

// r1054GroupAccessBody 是 cred 71 的 403 体（逐字）。
const r1054GroupAccessBody = `{"error":{"code":"","message":"无权访问 claude不降智稳定版 分组 (request id: 202611131606324762222058268d9d6wSz51QPF)","type":"new_api_error"}}`

// F1：余额预扣失败 403 ⇒ quota 族（KindQuotaPermanent，与既有
// insufficient_user_quota 钉测 bg/credential_probe_classification_211_test
// 的 creds67-70 形态同族同判；两分类器同判）。
func TestR1054InsufficientUserQuota403IsQuota(t *testing.T) {
	for status := range map[int]bool{402: true, 403: true, 429: true} {
		if got := ClassifyErrorWithBody(status, []byte(r1054InsufficientUserQuotaBody)); got != KindQuotaPermanent {
			t.Fatalf("status=%d 余额预扣失败应判 quota_permanent（此前误判 auth ⇒ 整凭据 auth_failed），got=%q", status, got)
		}
		if got := ClassifyResponseBody(status, []byte(r1054InsufficientUserQuotaBody)); got != KindQuotaPermanent {
			t.Fatalf("status=%d 正文分类器未同判 quota_permanent（锁步纪律），got=%q", status, got)
		}
	}
}

// F1 阴性对照：裸 OpenAI 码 insufficient_quota **不**被新模式吞——429 上
// 保持既有 KindRateLimit 口径（characterization 测试钉过的形状）。
func TestR1054BareOpenAIQuotaCodeNotCaptured(t *testing.T) {
	const body = `{"error":{"code":"insufficient_quota","message":"insufficient_quota","type":"insufficient_quota"}}`
	if got := ClassifyErrorWithBody(429, []byte(body)); got != KindRateLimit {
		t.Fatalf("裸 insufficient_quota 在 429 上必须保持 rate_limit 既有口径，got=%q", got)
	}
}

// F2：分组权限拒绝 403 ⇒ KindModelUnavailable（绑定级；两分类器同判）。
func TestR1054GroupAccess403IsBindingScoped(t *testing.T) {
	if got := ClassifyErrorWithBody(403, []byte(r1054GroupAccessBody)); got != KindModelUnavailable {
		t.Fatalf("分组权限拒绝应判 model_unavailable（此前误判 auth ⇒ 整凭据 auth_failed 24h），got=%q", got)
	}
	if got := ClassifyResponseBody(403, []byte(r1054GroupAccessBody)); got != KindModelUnavailable {
		t.Fatalf("正文分类器未同判 model_unavailable（锁步纪律），got=%q", got)
	}
}

// F2 阴性对照 ①：裸「无权访问」无「分组」＝账号级拒绝 ⇒ 保持 KindAuth
// 凭据级处置（本模式不得放宽账号级面）。
func TestR1054AccountLevelDenialStaysAuth(t *testing.T) {
	const body = `{"error":{"message":"无权访问该资源","type":"new_api_error"}}`
	if got := ClassifyErrorWithBody(403, []byte(body)); got != KindAuth {
		t.Fatalf("账号级拒绝（无「分组」）必须保持 auth 凭据级处置，got=%q", got)
	}
}

// F2 阴性对照 ②：只提「分组」没有「无权访问」的正文不命中（例如
// 分组配置回显类文案），403 仍落既有 auth 口径。
func TestR1054GroupWordAloneNotCaptured(t *testing.T) {
	const body = `{"error":{"message":"分组 claude默认 不存在","type":"new_api_error"}}`
	if got := ClassifyErrorWithBody(403, []byte(body)); got != KindAuth {
		t.Fatalf("仅含「分组」的正文不得命中绑定级模式（保持既有 auth 口径），got=%q", got)
	}
}

// F1/F2 语义对照：两个新裁决的 nodehealth 作用域——quota_permanent 保持
// 凭据级（余额是账号维度），model_unavailable 是绑定级（nodehealth
// isBindingScoped）。钉住 reducer 的映射输入侧（ErrorKind 投影）。
func TestR1054KindScopesUnchanged(t *testing.T) {
	if IsCredentialFatal(KindQuotaPermanent) != true {
		t.Fatalf("quota_permanent 必须保持凭据级 fatal（余额是账号维度）")
	}
	if !IsNodeFaultPenalty(KindQuotaPermanent) {
		t.Fatalf("quota_permanent 必须保持凭据级罚则（IsNodeFaultPenalty=SeverityCredential，余额是账号维度）")
	}
	if IsNodeFaultPenalty(KindModelUnavailable) {
		t.Fatalf("model_unavailable 不得落凭据级罚则（绑定级）")
	}
	if SeverityOf(KindModelUnavailable) != SeverityBinding {
		t.Fatalf("model_unavailable 的严重度必须是绑定级，got=%v", SeverityOf(KindModelUnavailable))
	}
}
