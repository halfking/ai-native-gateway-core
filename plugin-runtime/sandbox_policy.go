package pluginruntime

import (
	"fmt"
	"strings"
)

// SandboxPolicy declares the isolation and resource limits applied to a plugin
// process before it receives any request data. Workflow B (execution sandbox and
// isolation) relies on this policy to refuse write capabilities, strip sensitive
// environment variables and gate the DTO exposure level until a real sandbox is
// established. A nil or invalid policy means "no sandbox": the enforcer must
// fail-closed for any privileged operation.
type SandboxPolicy struct {
	// Available reports whether the runtime actually provides isolation.
	// Until this is true, write capabilities and credential-bearing env are refused.
	Available bool

	// OSUser is the unprivileged account the plugin runs as. Empty = inherited (unsafe).
	OSUser string

	// MemoryBytes caps the plugin resident set. 0 = unset / unbounded.
	MemoryBytes int64

	// CPUShares is the relative CPU weight (cgroup semantics). 0 = unset.
	CPUShares int64

	// MaxConcurrent caps parallel adapter invocations for one plugin.
	MaxConcurrent int

	// NetworkEgressAllowlist is the set of host:port the plugin may dial.
	// Empty = no egress allowed (deny by default).
	NetworkEgressAllowlist []string

	// FilesystemAllowlist is the set of paths the plugin may read/write.
	// Empty = no filesystem access beyond its own sandbox root.
	FilesystemAllowlist []string

	// EnvAllowlist is the set of environment variable NAMES exposed to the plugin.
	// Only names present here are copied from the gateway environment. All
	// DSN/credential-bearing variables are dropped regardless of this list.
	EnvAllowlist []string
}

// DefaultSandboxPolicy returns a deny-by-default policy that provides no
// privilege. It is safe to use as the zero-trust baseline before any real
// sandbox is wired up.
//
// # ★ 2026-10-24 P0-2：「谁负责置 true」的答案是**目前没有人**
//
// 计划书（docs/research/2026-10-24-magpie-v0.1.1367-…md §6-P0-2）要求
// "把 `SandboxPolicy.Available` 的语义写清：谁负责置 true"。审计后的答案：
//
//	① **生产路径上没有任何代码构造 `SandboxPolicy`** —— `NewSandboxEnforcer`
//	   在非测试代码里**零调用方**（实测：`grep NewSandboxEnforcer` 只命中
//	   sandbox.go 的定义与 sandbox_test.go）⇒ 连"恒 false 的拒绝"这条
//	   防御层**都还没接线**。
//	② 因此 `Available = true` 在生产上**不可达** ⇒ 数据面写能力
//	   （request.mutate / request.block / response.mutate / response.block /
//	   tool.mutate / tool.block / tool.execute）当前**没有任何路径**能让它通过。
//
// ⇒ **未来要置 true 时的责任划分**（这是本注释存在的目的）：
//
//	① **必须**先有一个真沙箱：独立 OS 用户 + 内存上限（`ValidateSandboxPolicy`
//	   已经强制这两项，`Available=true` 而缺任何一项 ⇒ 直接报错）；
//	② 置 true 的**唯一**合法位置是**配置装载处**（把管理员显式配置翻译成
//	   policy），**不是**某个业务调用点 —— 业务代码只读 policy，不得改它；
//	③ 置 true 的那一刻**必须**同时有一条可观测证据（否则又是一处
//	   "声明与实现不同源"）⇒ 见门禁
//	   scripts/ursmcheck/magpie_p0_plugin_capability_holes_test.go 的 G3。
func DefaultSandboxPolicy() SandboxPolicy {
	return SandboxPolicy{
		Available:              false,
		OSUser:                 "",
		MemoryBytes:            0,
		CPUShares:              0,
		MaxConcurrent:          1,
		NetworkEgressAllowlist: nil,
		FilesystemAllowlist:    nil,
		EnvAllowlist:           nil,
	}
}

// ValidateSandboxPolicy checks internal consistency. A policy that claims
// availability must at least run as a non-empty OS user and set a memory cap;
// otherwise it is not a real sandbox and must not be trusted.
func ValidateSandboxPolicy(p SandboxPolicy) error {
	if !p.Available {
		return nil
	}
	if strings.TrimSpace(p.OSUser) == "" {
		return fmt.Errorf("sandbox: available policy requires a non-empty os_user")
	}
	if p.MemoryBytes <= 0 {
		return fmt.Errorf("sandbox: available policy requires memory_bytes > 0")
	}
	if p.MaxConcurrent <= 0 {
		return fmt.Errorf("sandbox: max_concurrent must be >= 1")
	}
	return nil
}

// sensitiveEnvKey reports whether an environment variable name carries
// credentials, DSNs or cross-tenant connection secrets that must never reach a
// plugin process. Matching is case-insensitive on known suffixes.
func sensitiveEnvKey(name string) bool {
	upper := strings.ToUpper(name)
	for _, suffix := range []string{
		"PASSWORD", "SECRET", "TOKEN", "APIKEY", "API_KEY",
		"PRIVATEKEY", "PRIVATE_KEY", "DSN", "DATABASE_URL",
		"CREDENTIAL", "AUTH", "CERT", "KEY",
	} {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	return false
}
