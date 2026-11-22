package main

import (
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// TestRegisterNativeApprovalProviderRegisters 钉死 2026-11-23 的 211 生产修复：
//
// 生产启动日志恒有一条 ERROR：
//
//	failed to initialize plugin registry
//	plugin registry not fully configured: approval provider not configured:
//	authplugin: approval provider "native" not found (available: [])
//
// 根因：loadPluginConfig 的默认值是 ApprovalProvider="native"，但
// registerNativeProviders 里 native approval provider 的注册整块被注释
// ⇒ registry 里永远没有名为 "native" 的 approval provider。
//
// ★ 本测试走**真实调用点**（具名函数 registerNativeApprovalProvider），
// 不是去源码里 grep 字符串——grep 型守卫对「构造了但不注册」这种退化
// 是假绿（2026-11-23 本轮实测踩过：if false { ... } 包住注册语句，
// 字符串守卫仍绿）。
func TestRegisterNativeApprovalProviderRegisters(t *testing.T) {
	registry := authplugin.NewRegistry(&authplugin.PluginConfig{
		AuthProvider:     "native",
		UserManager:      "native",
		TenantManager:    "native",
		ApprovalProvider: "native",
		Native:           &authplugin.NativeConfig{},
	})

	// 前置断言：未注册 approval provider 时，IsConfigured 的错误串里**不含**
	// "approval provider" 字样（此时它先报 auth provider 未注册，属正常）。
	// 这条前置保证后面的断言不是空断言。
	preErr := registry.IsConfigured()
	if preErr == nil {
		t.Fatal("IsConfigured() = nil before registering approval provider; " +
			"the guard below would be vacuous — registry semantics changed")
	}
	if mentionsApprovalProvider(preErr.Error()) {
		t.Fatalf("precondition changed: IsConfigured() already mentions the approval "+
			"provider before registration: %v", preErr)
	}

	// 走真实调用点。nil pool / nil redis 对本函数是安全的：
	// NewPGApprovalStore 只包装指针，NewConfigManager 只存指针，
	// NewNativeApprovalProvider 只包装 configManager —— 三者都不触碰 DB。
	if err := registerNativeApprovalProvider(registry, nil, nil); err != nil {
		t.Fatalf("registerNativeApprovalProvider() error = %v", err)
	}

	// 关键断言：注册后 registry 能取到名为 "native" 的 approval provider。
	got, err := registry.GetApprovalProvider()
	if err != nil {
		t.Fatalf("GetApprovalProvider() error = %v — the native approval provider "+
			"was not registered (211 production regression reintroduced)", err)
	}
	if got == nil || got.Name() != "native" {
		t.Fatalf("GetApprovalProvider() name = %v, want \"native\"", got)
	}

	// IsConfigured 不应再抱怨 approval provider（其它 provider 未注册时
	// 仍可能因 auth/user/tenant 报错，但不应再出现 approval provider 字样）。
	if postErr := registry.IsConfigured(); postErr != nil &&
		mentionsApprovalProvider(postErr.Error()) {
		t.Fatalf("IsConfigured() still reports approval provider not configured after "+
			"registerNativeApprovalProvider(): %v", postErr)
	}
}

// TestRegisterNativeProvidersCallsApprovalRegistration 钉死**接线**：
// registerNativeProviders（真正的生产调用者）必须调用
// registerNativeApprovalProvider。这条防的是"有人把调用点删了/注释回去"。
//
// 之所以用源码扫描而不是纯行为：registerNativeProviders 的第一步
// NewNativeAuthProvider(db, ...) 在 db==nil 时就返回错误，跑不到 approval
// 那一段 ⇒ 无法用 nil DB 做纯行为测试。这里扫的是**调用点是否存在**，
// 与上面的行为测试互补：行为测试证明"注册逻辑本身对"，扫描证明"它被接上了"。
func TestRegisterNativeProvidersCallsApprovalRegistration(t *testing.T) {
	src, err := readSourceFile("plugin_init.go")
	if err != nil {
		t.Fatalf("read plugin_init.go: %v", err)
	}
	code := stripComments(src)
	if !strings.Contains(code, "registerNativeApprovalProvider(registry, db, redisClient)") {
		t.Fatal("plugin_init.go: registerNativeProviders no longer calls " +
			"registerNativeApprovalProvider(registry, db, redisClient) — " +
			"the 211 production fix would be silently reverted")
	}
}

// mentionsApprovalProvider 判断错误串是否在抱怨 approval provider 未配置。
func mentionsApprovalProvider(msg string) bool {
	return strings.Contains(msg, "approval provider not configured") ||
		strings.Contains(msg, "approval provider \"native\" not found")
}

// readSourceFile 读取同目录源码文件。
func readSourceFile(name string) (string, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// stripComments 去掉 // 行注释与 /* */ 块注释，让断言只看可执行代码。
func stripComments(src string) string {
	out := make([]rune, 0, len(src))
	runes := []rune(src)
	inBlock := false
	for i := 0; i < len(runes); i++ {
		if inBlock {
			if runes[i] == '*' && i+1 < len(runes) && runes[i+1] == '/' {
				inBlock = false
				i++
			}
			continue
		}
		if runes[i] == '/' && i+1 < len(runes) && runes[i+1] == '/' {
			for i < len(runes) && runes[i] != '\n' {
				i++
			}
			out = append(out, '\n')
			continue
		}
		if runes[i] == '/' && i+1 < len(runes) && runes[i+1] == '*' {
			inBlock = true
			i++
			continue
		}
		out = append(out, runes[i])
	}
	return string(out)
}
