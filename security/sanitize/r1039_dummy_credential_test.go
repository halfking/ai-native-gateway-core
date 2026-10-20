package sanitize

// r1039（D7，2026-10-20）：凭据字段上的**示例值**不该被凭据字段规则拦下。
//
// ## 现象
//
//	{"token":"abc123"}     → 被 builtin.secret_field.egress 整条拦下
//	{"password":"abc123"}  → 同上
//
// 这两个值是**占位值**，不是泄漏的真值。拦它的代价由每个请求承担：模型
// 并行发起的其它工具调用会一起陪葬，客户端看到的是「模型未返回任何内容」。
//
// ## 口径
//
//   - 「一眼假的示例值」放行：abc123 / xxx / changeme / your-token-here /
//     *** / <redacted> / user0000 …
//   - 「像真密钥的」照拦：高熵、字母数字混合、长度 ≥ 16（aB3xY9zQ1mN7pL2k）；
//     含符号的混合值（R7#kQ?92!）；短但随机的（hunter2）。
//
// ## 判据纪律
//
//   - 两条路径都测：**非托管**（生产 YAML + 代码级凭据字段检查）与
//     **托管**（快照里的 builtin.secret_field.egress 字段规则）。只测一条
//     会漏掉另一半——211 生产跑的是托管快照。
//   - 每条「放行」配一条「前提仍在场」：同一形状的真密钥必须仍被拦，
//     否则放行可能只是「守卫压根没看见」。
//   - 判据与被测函数同层（走 CheckField / 真实拦截器链）。

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// r1039DummyCredentials 是必须放行的示例值（键 → 值）。
var r1039DummyCredentials = []struct {
	field, value string
}{
	{"token", "abc123"},
	{"password", "abc123"},
	{"api_key", "abc123"},
	{"secret", "xxx"},
	{"password", "changeme"},
	{"token", "your-token-here"},
	{"password", "***"},
	{"api_key", "<redacted>"},
	{"secret", "user0000"},
	{"token", "pass1234"},
}

// r1039GenuineSecrets 是必须仍被拦的真密钥形状。
//
// 边界照 owner 给的口径写：高熵、字母数字混合、长度 ≥ 16；含符号的混合值；
// 短但随机的弱口令。
var r1039GenuineSecrets = []struct {
	field, value string
}{
	{"token", "aB3xY9zQ1mN7pL2k"},
	{"password", "R7#kQ?92!"},
	{"api_key", "sk-live-9f8e7d6c5b4a3f2e"},
	{"password", "hunter2"},
	{"secret", "Xk9#mQ2!vL7@wZ"},
}

// TestR1039DummyCredentialValuesAreNotBlocked 是主契约（非托管路径）。
func TestR1039DummyCredentialValuesAreNotBlocked(t *testing.T) {
	c := r1028Checker(t) // 生产 YAML 的输出守卫
	for _, tc := range r1039DummyCredentials {
		args := `{"` + tc.field + `":"` + tc.value + `"}`
		res, err := c.CheckField(context.Background(), "default", "__tool_json@Bash", args)
		if err != nil {
			t.Fatalf("CheckField(%s): %v", args, err)
		}
		if res.Blocked {
			t.Errorf("示例值被凭据字段规则拦下（%s=%s）: issues=%+v", tc.field, tc.value, res.Issues)
		}
	}
}

// TestR1039ManagedDummyCredentialValuesAreNotBlocked 测**托管路径**。
//
// 211 生产跑的是托管快照（builtin.secret_field.egress 是条**按字段名**匹配的
// 规则），非托管路径绿了不代表生产绿——这条判据专门堵那个洞。
func TestR1039ManagedDummyCredentialValuesAreNotBlocked(t *testing.T) {
	d, _ := managedFixture(t)
	s, err := NewSanitizer(d)
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}
	guard := NewOutputSensitiveInterceptor(s, OutputMask)
	restore, err := NewSanitizeRestoreInterceptor(s, nil, 0)
	if err != nil {
		t.Fatalf("NewSanitizeRestoreInterceptor: %v", err)
	}
	chain := response.NewInterceptorChain(guard, restore)
	meta := &response.StreamMeta{SessionID: "gw_r1039_dummy", TenantID: "default", State: response.NewStreamState()}

	for _, tc := range r1039DummyCredentials {
		args := `{"` + tc.field + `":"` + tc.value + `"}`
		frame := toolArgumentsFrame("Bash", args)
		if _, err := chain.InterceptStreamChunk(context.Background(), []byte(frame), meta); err != nil {
			t.Fatalf("InterceptStreamChunk(%s): %v", args, err)
		}
		if _, ferr := chain.FlushStreamPending(context.Background(), meta); ferr != nil {
			t.Errorf("托管路径把示例值拦了（%s=%s）: %v", tc.field, tc.value, ferr)
		}
	}
}

// TestR1039GenuineSecretsAreStillBlocked 是「前提仍在场」判据。
//
// 放行有两种成因：(a) 判定认为它是示例值，(b) 守卫压根没看见这个字段。
// (b) 是假绿——它没改逻辑，只改了「看不见的范围」，变异验证天然测不到。
func TestR1039GenuineSecretsAreStillBlocked(t *testing.T) {
	c := r1028Checker(t)
	for _, tc := range r1039GenuineSecrets {
		args := `{"` + tc.field + `":"` + tc.value + `"}`
		res, err := c.CheckField(context.Background(), "default", "__tool_json@Bash", args)
		if err != nil {
			t.Fatalf("CheckField(%s): %v", args, err)
		}
		if !res.Blocked {
			t.Errorf("真密钥被放行（%s=%s）", tc.field, tc.value)
		}
	}

	// 托管路径同样必须拦。
	d, _ := managedFixture(t)
	s, err := NewSanitizer(d)
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}
	guard := NewOutputSensitiveInterceptor(s, OutputMask)
	restore, err := NewSanitizeRestoreInterceptor(s, nil, 0)
	if err != nil {
		t.Fatalf("NewSanitizeRestoreInterceptor: %v", err)
	}
	chain := response.NewInterceptorChain(guard, restore)
	meta := &response.StreamMeta{SessionID: "gw_r1039_real", TenantID: "default", State: response.NewStreamState()}
	for _, tc := range r1039GenuineSecrets {
		args := `{"` + tc.field + `":"` + tc.value + `"}`
		frame := toolArgumentsFrame("Bash", args)
		if _, err := chain.InterceptStreamChunk(context.Background(), []byte(frame), meta); err != nil {
			t.Fatalf("InterceptStreamChunk(%s): %v", args, err)
		}
		if _, ferr := chain.FlushStreamPending(context.Background(), meta); ferr == nil {
			t.Errorf("托管路径放过了真密钥（%s=%s）", tc.field, tc.value)
		}
	}
}

// TestR1039DummyJudgementBoundaries 把「示例值」的判据边界钉死。
//
// 这一条是**判据自身**的边界：将来有人把「短即假」写进去（hunter2 会被
// 放过），或把长度门槛放宽（16 位高熵真密钥会被放过），这里会先红。
//
// 断言分两层：`isPlaceholderCredentialValue` 是**对外的判定**（占位拼写的
// 完整口径：字面量集 + 示例值 + 自指涉 + 模板形态）；`isDummyCredentialValue`
// 是本轮新增的那一层（示例值形状）。
func TestR1039DummyJudgementBoundaries(t *testing.T) {
	placeholders := []string{
		"abc123", "xxx", "changeme", "your-token-here", "***", "<redacted>",
		"user0000", "pass1234", "xxxx", "****1234", "test123",
	}
	for _, v := range placeholders {
		if !isPlaceholderCredentialValue("token", v) {
			t.Errorf("示例值 %q 未被判为占位", v)
		}
	}
	notPlaceholders := []string{
		"",                         // 空值单独处理（isPlaceholderCredentialValue 里返回 true）
		"hunter2",                  // 短但随机
		"R7#kQ?92!",                // 含符号的混合值
		"aB3xY9zQ1mN7pL2k",         // 16 位高熵
		"sk-live-9f8e7d6c5b4a3f2e", // 供应商密钥形态
		"xY7#mQ2!vL7@wZ",           // 含符号
		"pass1234567890123",        // 长度 ≥ 16
	}
	for _, v := range notPlaceholders {
		if v != "" && isPlaceholderCredentialValue("token", v) {
			t.Errorf("非示例值 %q 被判成占位", v)
		}
	}
	// 新增的那一层（示例值形状）单独钉边界：字面量集里的历史拼写不在这一层，
	// 但形状族（掩码 / 字母+平凡数字）必须在。
	for _, v := range []string{"abc123", "xxxx", "****1234", "user0000", "pass1234", "test123"} {
		if !isDummyCredentialValue(v) {
			t.Errorf("示例值 %q 未被判为示例值", v)
		}
	}
	for _, v := range []string{"", "hunter2", "R7#kQ?92!", "aB3xY9zQ1mN7pL2k", "sk-live-9f8e7d6c5b4a3f2e", "xY7#mQ2!vL7@wZ", "pass1234567890123"} {
		if isDummyCredentialValue(v) {
			t.Errorf("非示例值 %q 被判成示例值", v)
		}
	}
}
