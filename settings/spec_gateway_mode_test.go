package settings

import (
	"strings"
	"testing"
)

// 2026-11-17：工作模式（gateway.mode）注册项的钉测。
//
// 为什么单独一个文件：
//   「工作模式」是**界面投影层**的唯一开关，它一旦被误注册成别的语义
//   （例如和 security.mode 混成同一个 key、或者默认值取成 simple 导致
//   现有用户一升级就看到新界面），后果是全局的。
//   ⇒ 这里把「注册形态」「取值域」「命名纪律」三条钉死。

func findGatewaySpec(t *testing.T, key string) *Spec {
	t.Helper()
	for _, sp := range GatewaySpecs() {
		if sp.Key == key {
			return sp
		}
	}
	t.Fatalf("spec %q not registered in GatewaySpecs()", key)
	return nil
}

func TestGatewayModeSpec_Registered(t *testing.T) {
	sp := findGatewaySpec(t, "gateway.mode")
	if sp.Type != TypeString {
		t.Fatalf("gateway.mode type = %v, want TypeString", sp.Type)
	}
	if sp.Scope != ScopePlatform {
		t.Fatalf("gateway.mode scope = %v, want ScopePlatform", sp.Scope)
	}
	if sp.Default != "expert" {
		t.Fatalf("gateway.mode default = %v, want expert (现状必须是默认，升级不能改变既有用户的界面)", sp.Default)
	}
	if !sp.HotReload {
		t.Error("gateway.mode must be hot-reloadable：它只决定界面投影，不该要求重启")
	}
	if sp.DangerLevel >= Dangerous {
		t.Errorf("gateway.mode danger level = %v, want < Dangerous：它是界面偏好，不是危险开关", sp.DangerLevel)
	}
	if sp.Description == "" || sp.DescriptionLong == "" {
		t.Error("gateway.mode must carry both Description and DescriptionLong")
	}
}

func TestGatewayModeSpec_Options(t *testing.T) {
	sp := findGatewaySpec(t, "gateway.mode")
	if len(sp.Options) != 2 {
		t.Fatalf("gateway.mode options = %v, want exactly [simple expert]", sp.Options)
	}
	seen := map[string]bool{}
	for _, o := range sp.Options {
		seen[o] = true
	}
	if !seen["simple"] || !seen["expert"] {
		t.Fatalf("gateway.mode options = %v, want both simple and expert", sp.Options)
	}
	// TypeString 只校验「是字符串」，**不校验取值域**（Options 由 UI 渲染成提示，
	// 与 security.mode 同款现状）⇒ 非字符串必须被拒。
	for _, bad := range []any{true, 1, 1.5, []string{"simple"}} {
		if err := sp.Validate(bad); err == nil {
			t.Errorf("Validate(%v) should fail for gateway.mode", bad)
		}
	}
	for _, ok := range []any{"simple", "expert", "Simple", "anything"} {
		if err := sp.Validate(ok); err != nil {
			t.Errorf("Validate(%v) failed: %v", ok, err)
		}
	}
	// ★ 因为平台不校验取值域，**读侧必须做白名单降级**（未知值 ⇒ expert）：
	//   见 web/src/composables/useWorkMode.ts 的 coerceWorkMode()，
	//   那里有对应的前端钉测。本测试记录这条跨端约定，避免后人误以为平台会拦。
}

// ★ 命名纪律：与 security.mode 绝不能是同一个 key，也绝不能共用 env 名。
//
//	（security.mode 绑定 observe/enforce 语义；混用会让用户误判
//	「改了工作模式，安全检测就变成 observe 了」—— v1 设计稿已记这条。）
func TestGatewayModeSpec_NamingDiscipline(t *testing.T) {
	sp := findGatewaySpec(t, "gateway.mode")
	if sp.Key == "security.mode" {
		t.Fatal("gateway.mode must not reuse the security.mode key")
	}
	if sp.EnvName == "LLM_GATEWAY_SECURITY_MODE" {
		t.Fatal("gateway.mode must not reuse the security.mode env name")
	}
	if strings.Contains(sp.EnvName, "GATEWAY_GATEWAY") {
		t.Errorf("env name %q must not double the GATEWAY prefix", sp.EnvName)
	}
	if !strings.HasPrefix(sp.EnvName, "LLM_GATEWAY_") {
		t.Errorf("env name %q must keep the LLM_GATEWAY_ prefix", sp.EnvName)
	}
}

// ★ 工作模式必须真的出现在平台注册表里（否则设置页看不到、GET 读不到）。
func TestGatewayModeSpec_InPlatformRegistry(t *testing.T) {
	all := PlatformSpecs()
	for _, sp := range all {
		if sp.Key == "gateway.mode" {
			if sp.Default != "expert" {
				t.Fatalf("registered gateway.mode default = %v, want expert", sp.Default)
			}
			return
		}
	}
	t.Fatal("gateway.mode is not registered in PlatformSpecs()")
}
