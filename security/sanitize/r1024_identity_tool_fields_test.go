package sanitize

import (
	"context"
	"strings"
	"testing"
)

// r1024（2026-10-13）：多轮 tool E2E 里 run_ssh_command 被整体 output_policy_blocked
// 的回归判据。成因两层，判据各配一条「前提在场」：
//
//  1. 输出侧凭据键检查把身份族字段（username/account/login）与密码族一视同仁地
//     硬阻断非占位符值；而裸用户名不命中任何输入检测规则、结构性不可能以
//     占位符出现 → 一切带用户名的 SSH 类工具调用必被掐断。
//  2. YAML account 规则只认 username|user_name|login|account 确切拼写，
//     `ssh_user=deploy` 散文漏检 → 用户名明文出网关。
//
// 判据用生产 YAML 检测器（NewPatternDetectorFromFile）——内置 fallback 没有地址/
// account 规则，用它写这类判据是假绿（AGENTS 判据纪律第 2 条）。
func newR1024Detector(t *testing.T) *PatternDetector {
	t.Helper()
	det, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("production detector unavailable: %v", err)
	}
	return det
}

func newR1024Checker(t *testing.T) *outputSensitiveChecker {
	t.Helper()
	s, err := NewSanitizer(newR1024Detector(t))
	if err != nil {
		t.Fatalf("sanitizer: %v", err)
	}
	return &outputSensitiveChecker{sanitizer: s, action: OutputMask}
}

// 判据 A（事故原样回放）：模型回显的 run_ssh_command 参数——命令 + 私网 host +
// 密码占位符 + 明文 username——不得被阻断。password 是占位符（还原在前、
// 守卫在后），username 是身份族明文，都是合法形态。
func TestR1024IdentityFieldsDoNotBlockSSHStyleToolCall(t *testing.T) {
	c := newR1024Checker(t)
	args := `{"command":"cd ~/workspace/llm-gateway-go-2 && git status -sb && echo '---LOG---' && git log --oneline -3","host":"10.24.7.91","password":"{SENSITIVE:secret:1}","username":"deploy"}`
	res, err := c.CheckField(context.Background(), "default", toolJSONLabel("run_ssh_command"), args)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Blocked {
		t.Fatalf("identity-field tool call blocked: %+v", res)
	}
}

// 判据 B（逐字段隔离）：身份族字段的明文值不触发阻断；同型密码族字段照旧阻断。
// 允许侧（身份）与阻断侧（密码）必须成对出现，防「看不见」式假绿。
func TestR1024IdentityVersusSecretFieldMatrix(t *testing.T) {
	c := newR1024Checker(t)
	cases := []struct {
		name    string
		args    string
		blocked bool
	}{
		{"username_plain", `{"username":"deploy"}`, false},
		{"user_name_plain", `{"user_name":"deploy"}`, false},
		{"account_plain", `{"account":"deploy"}`, false},
		{"login_plain", `{"login":"deploy"}`, false},
		{"username_cn", `{"用户名":"deploy"}`, false},
		{"username_marker_echo", `{"username":"{SENSITIVE:account:0}"}`, false},
		{"password_plain", `{"password":"Xk9#mZ2pQ8"}`, true},
		{"token_plain", `{"token":"abc123"}`, true},
		{"api_key_plain", `{"api_key":"sk-x"}`, true},
		{"password_cn_plain", `{"密码":"123456"}`, true},
		{"password_number", `{"password":123456}`, true},
		{"password_map", `{"password":{"v":"x"}}`, true},
		// 混合形态：身份明文 + 密码明文 → 阻断由密码触发（既有契约不回退）。
		{"mixed_identity_and_secret", `{"username":"ops","password":"short"}`, true},
		// 畸形形状契约不随族分流：任何凭据字段（含身份族）下的非字符串形状
		// 仍 fail-closed（TestCriticalUnsupportedCredentialShapesFailClosed）。
		{"identity_number_shape", `{"account":42}`, true},
		{"identity_bool_shape", `{"username":true}`, true},
		{"identity_map_shape", `{"username":{"v":"x"}}`, true},
		// r1024b：模型自造的占位拼写不是泄漏真值，放行——实测自造词每次都
		// 不同（PLACEHOLDER / USE_DEPLOY_PASSWORD），判据按「自指涉字段名 +
		// 模板包裹 + 字面量集」三路覆盖；未覆盖的普通明文保持 fail-closed。
		{"password_model_placeholder", `{"password":"PLACEHOLDER"}`, false},
		{"password_use_deploy_password", `{"password":"USE_DEPLOY_PASSWORD"}`, false},
		{"password_angle_placeholder", `{"password":"<password>"}`, false},
		{"password_template_var", `{"password":"${password}"}`, false},
		{"password_todo", `{"password":"TODO"}`, false},
		{"password_empty", `{"password":""}`, false},
		{"real_production_audit_shape", `{"command":"git log --since=24h --oneline","host":"PLACEHOLDER","password":"PLACEHOLDER","username":"PLACEHOLDER"}`, false},
		{"real_production_audit_shape2", `{"command":"cd ~/x && git log","host":"USE_DEPLOY_HOST","password":"USE_DEPLOY_PASSWORD","username":"USE_DEPLOY_USER"}`, false},
		// 未收录、无自指涉、非模板的普通明文照旧拦截（真实密码主形态）。
		{"password_plain_still_blocked", `{"password":"Xk9#mZ2pQ8"}`, true},
		{"password_placeholderish_unlisted", `{"password":"hunter2"}`, true},
	}
	for _, tc := range cases {
		res, err := c.CheckField(context.Background(), "default", toolJSONLabel("connect"), tc.args)
		if err != nil {
			t.Fatalf("%s: err %v", tc.name, err)
		}
		if res.Blocked != tc.blocked {
			t.Errorf("%s: blocked=%v want %v", tc.name, res.Blocked, tc.blocked)
		}
	}
	// 非 tool 车道的同型标签检查也只对密码族阻断。
	res, err := c.CheckField(context.Background(), "default", "username", "deploy")
	if err != nil {
		t.Fatal(err)
	}
	if res.Blocked {
		t.Error("lane[username]=deploy must not block")
	}
	res, err = c.CheckField(context.Background(), "default", "password", "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Blocked {
		t.Error("lane[password]=hunter2 must block")
	}
}

// 判据 C（输入侧闭环前提在场）：`ssh_user=`/`db_user=`/`user=` 散文形态必须被
// account 规则检出（r1024 的 YAML 补丁）——用户名进占位符闭环，出向脱敏、
// 回程可还原。既有确切拼写（username=）不回退。
func TestR1024AccountRuleCoversUserKeyForms(t *testing.T) {
	det := newR1024Detector(t)
	for _, sample := range []string{
		"ssh_user=deploy",
		"db_user=gw_admin",
		"user=ops",
		"username=ops",
		`"username": "ops"`,
	} {
		fragments, err := det.Detect(context.Background(), sample)
		if err != nil {
			t.Fatalf("detect %q: %v", sample, err)
		}
		found := false
		for _, f := range fragments {
			if f.Type == TypeAccount && strings.Contains(sample, f.Value) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("account rule missed %q (fragments=%v)", sample, fragments)
		}
	}
	// 日常散文里的普通 "user" 单词不 followed by 赋值时不检出（形态白名单仍在）。
	fragments, _ := det.Detect(context.Background(), "every user of this system should rest")
	for _, f := range fragments {
		if f.Type == TypeAccount {
			t.Errorf("account rule false positive on prose: %+v", f)
		}
	}
}
