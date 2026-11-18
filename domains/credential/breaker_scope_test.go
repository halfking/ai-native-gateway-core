package credential

import (
	"reflect"
	"testing"
	"time"
)

// ★ P0-a（2026-11-19 审计轮）熔断器键空间统一钉测。
//
// 背景：熔断器曾同时存在两套键空间——
//
//	· 准入（AllowPerModel）查 "providerID/model"（**丢弃 credentialID**）
//	· 记账（RecordFailure*/RecordSuccess*）写 "providerID/credentialID"
//	· 探针释放（ReleaseProbe）/ 状态查询 / 管理面 Reset 各走各的
//
// 后果：chat 主链路熔断**永不跳闸**（准入查 A、记账写 B），管理面
// circuit_state 恒显 closed，探针槽永不释放（breaker 永久卡 HALF_OPEN），
// 同 provider 下不同凭据互相连坐，DB 上卷归错凭据。
//
// 现统一为 "providerID/credentialID/model" 三元组（breakerScopeKey）。
// 本文件把该性质钉成可执行的门禁。

// TestBreakerScopeKeyFormat 钉住键格式与「模型名为空 ⇒ 整凭据作用域」。
func TestBreakerScopeKeyFormat(t *testing.T) {
	cases := []struct {
		name        string
		providerID  int
		credentialI int
		model       string
		want        string
	}{
		{"标准三元组", 1, 100, "gpt-4", "1/100/gpt-4"},
		{"模型名去空格", 1, 100, "  gpt-4  ", "1/100/gpt-4"},
		{"不同凭据不同键", 1, 200, "gpt-4", "1/200/gpt-4"},
		{"不同 provider 不同键", 2, 100, "gpt-4", "2/100/gpt-4"},
		{"空模型 ⇒ 整凭据作用域", 1, 100, "", "1/100/_legacy_"},
		{"纯空格模型 ⇒ 整凭据作用域", 1, 100, "   ", "1/100/_legacy_"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := breakerScopeKey(tc.providerID, tc.credentialI, tc.model); got != tc.want {
				t.Fatalf("breakerScopeKey(%d, %d, %q) = %q, want %q",
					tc.providerID, tc.credentialI, tc.model, got, tc.want)
			}
		})
	}
}

// TestBreakerScopeAllPathsHitSameInstance 钉住核心性质：
// **准入、记账、探针释放、状态查询、恢复时刻查询必须落在同一把 breaker 上**。
//
// 这正是修复前 chat 主链路熔断永不跳闸的根因——准入查的 breaker 与记账
// 写的 breaker 不是同一把。任何一处分裂都会让熔断失效。
func TestBreakerScopeAllPathsHitSameInstance(t *testing.T) {
	m := NewManager()
	const (
		providerID   = 7
		credentialID = 42
	)
	const model = "gpt-4"

	// 路径①：准入（创建 breaker 并消费一个探针槽）。
	if !m.AllowPerModel(providerID, credentialID, model) {
		t.Fatal("首次准入必须放行")
	}

	// 路径②：失败记账（nodehealth / 协议腿的生产入口）。
	m.RecordFailureWithBillingMode(providerID, credentialID, model, KindUpstreamDown, "per_token")

	// 路径③：状态查询——必须能看到路径②记下的失败（同一把 breaker）。
	b := m.GetPerModel(providerID, credentialID, model)
	if b == nil {
		t.Fatal("GetPerModel 必须能取到准入创建的 breaker")
	}
	if got := b.ConsecutiveFailures(); got != 1 {
		t.Fatalf("记账与准入落在不同 breaker 上：consecutive=%d, want 1", got)
	}
	if got := m.GetModelState(providerID, credentialID, model); got != StateClosed {
		t.Fatalf("GetModelState 与 GetPerModel 不一致：%s vs %s", got, b.State())
	}

	// 再吃一次失败 ⇒ 必须跳闸（这是修复前永不发生的那一步）。
	m.RecordFailureWithBillingMode(providerID, credentialID, model, KindUpstreamDown, "per_token")
	if got := m.GetModelState(providerID, credentialID, model); got != StateOpen {
		t.Fatalf("两次失败后 breaker 必须 OPEN（记账必须落在准入所查的那把上），got %s", got)
	}

	// 路径④：准入必须被拒——主链路熔断真正生效。
	if m.AllowPerModel(providerID, credentialID, model) {
		t.Fatal("熔断后准入必须被拒（修复前此处恒放行：准入查 per-model 键、记账写 legacy 键）")
	}

	// 路径⑤：探针释放必须作用在同一把 breaker 上。
	// 冷却过期后 breaker 进入 HALF_OPEN，Allow 会消费探针槽；
	// 释放后必须能再探一次（修复前释放走 legacy 键 ⇒ 槽位永不释放）。
	b.mu.Lock()
	b.coolingExpires = time.Now().Add(-time.Second)
	b.mu.Unlock()
	if !m.AllowPerModel(providerID, credentialID, model) {
		t.Fatal("冷却过期后必须放行一次（消费探针槽）")
	}
	// 此刻探针槽被占住，第二次准入必须被拒。
	if m.AllowPerModel(providerID, credentialID, model) {
		t.Fatal("探针槽被占时第二次准入必须被拒")
	}
	m.ReleaseProbe(providerID, credentialID, model)
	if !m.AllowPerModel(providerID, credentialID, model) {
		t.Fatal("ReleaseProbe 必须作用在准入所查的那把 breaker 上（否则槽位永不释放）")
	}

	// 路径⑥：恢复时刻查询必须命中同一把 breaker。
	m.RecordSuccessPerModel(providerID, credentialID, model)
	if at := m.RecoveredAt(providerID, credentialID, model); at.IsZero() {
		t.Fatal("RecoveredAt 必须命中准入所查的那把 breaker")
	}
}

// TestBreakerScopeCrossCredentialIsolation 钉住跨凭据隔离：
// 同 provider、同模型的不同凭据**不得**共享 breaker（修复前共享 ⇒ 跨凭据连坐）。
func TestBreakerScopeCrossCredentialIsolation(t *testing.T) {
	m := NewManager()
	const model = "gpt-4"

	b1 := m.GetOrCreatePerModel(1, 100, model)
	b2 := m.GetOrCreatePerModel(1, 200, model)
	if b1 == b2 {
		t.Fatal("同 provider 同模型的不同凭据不得共享 breaker（跨凭据连坐）")
	}

	b1.RecordFailure(KindUpstreamDown)
	b1.RecordFailure(KindUpstreamDown)
	if b1.State() != StateOpen {
		t.Fatalf("凭据 100 的 breaker 应 OPEN，got %s", b1.State())
	}
	if b2.State() != StateClosed {
		t.Fatalf("凭据 200 的 breaker 不受影响，got %s", b2.State())
	}
	// 准入也必须隔离：凭据 200 仍可放行。
	if !m.AllowPerModel(1, 200, model) {
		t.Fatal("凭据 200 的准入必须放行（熔断不得跨凭据连坐）")
	}
}

// TestBreakerScopeCredentialRollupAttribution 钉住 DB 上卷归因：
// GetCredentialState 必须把状态算到**真正出错的凭据**头上
// （修复前 per-model breaker 的 credentialID 取决于「谁先创建」⇒ 归错凭据）。
func TestBreakerScopeCredentialRollupAttribution(t *testing.T) {
	m := NewManager()
	const model = "gpt-4"

	// 凭据 200 先创建（若 breaker 的 credentialID 取「首个创建者」，
	// 状态就会记到 200 头上）。
	m.GetOrCreatePerModel(1, 200, model)
	m.GetOrCreatePerModel(1, 100, model)

	m.RecordFailurePerModel(1, 100, model, KindUpstreamDown)
	m.RecordFailurePerModel(1, 100, model, KindUpstreamDown)

	if got := m.GetCredentialState(100); got != StateOpen {
		t.Fatalf("凭据 100 的上卷态应为 OPEN，got %s", got)
	}
	if got := m.GetCredentialState(200); got != StateClosed {
		t.Fatalf("凭据 200 的上卷态必须为 CLOSED（不得归错凭据），got %s", got)
	}
}

// TestBreakerScopeResetClearsEveryModelScope 钉住管理面紧急修复：
// Reset(provider, credential, "") 必须清掉该凭据下**所有**模型作用域
// （修复前只重置 legacy 键 ⇒ per-model breaker 仍是 OPEN ⇒
// 管理面「清除熔断」返回 200 却毫无效果）。
func TestBreakerScopeResetClearsEveryModelScope(t *testing.T) {
	m := NewManager()
	m.RecordFailurePerModel(1, 100, "gpt-4", KindUpstreamDown)
	m.RecordFailurePerModel(1, 100, "gpt-4", KindUpstreamDown)
	m.RecordFailurePerModel(1, 100, "gpt-3.5-turbo", KindUpstreamDown)
	m.RecordFailurePerModel(1, 100, "gpt-3.5-turbo", KindUpstreamDown)

	if got := m.GetModelState(1, 100, "gpt-4"); got != StateOpen {
		t.Fatalf("前提不成立：gpt-4 应 OPEN，got %s", got)
	}

	m.Reset(1, 100, "")

	if got := m.GetModelState(1, 100, "gpt-4"); got != StateClosed {
		t.Fatalf("整凭据 Reset 后 gpt-4 应 CLOSED，got %s", got)
	}
	if got := m.GetModelState(1, 100, "gpt-3.5-turbo"); got != StateClosed {
		t.Fatalf("整凭据 Reset 后 gpt-3.5-turbo 应 CLOSED，got %s", got)
	}
}

// TestBreakerScopeResetSingleModelScope 钉住单模型 Reset 不误伤兄弟模型。
func TestBreakerScopeResetSingleModelScope(t *testing.T) {
	m := NewManager()
	m.RecordFailurePerModel(1, 100, "gpt-4", KindUpstreamDown)
	m.RecordFailurePerModel(1, 100, "gpt-4", KindUpstreamDown)
	m.RecordFailurePerModel(1, 100, "gpt-3.5-turbo", KindUpstreamDown)
	m.RecordFailurePerModel(1, 100, "gpt-3.5-turbo", KindUpstreamDown)

	m.Reset(1, 100, "gpt-4")

	if got := m.GetModelState(1, 100, "gpt-4"); got != StateClosed {
		t.Fatalf("目标模型应 CLOSED，got %s", got)
	}
	if got := m.GetModelState(1, 100, "gpt-3.5-turbo"); got != StateOpen {
		t.Fatalf("兄弟模型不应被单模型 Reset 影响，got %s", got)
	}
}

// TestBreakerScopeManagerExposesNoModelLessAPI 把「生产路径禁用 legacy
// 凭据级 API」从注释纪律升级为**可执行门禁**：
//
// Manager 不得再暴露任何缺少 model 分量的熔断方法。历史上正是这些方法
// （Get/GetOrCreate/RecordFailure/RecordSuccess/Allow）把记账引到了
// 与准入不同的键空间上。
//
// 这条门禁的意义：一旦有人重新加回一个「不带 model 的方法」，
// 本测试立刻转红——不需要人去读源码找。
func TestBreakerScopeManagerExposesNoModelLessAPI(t *testing.T) {
	mt := reflect.TypeOf(&Manager{})

	// 允许的方法（都带 model 分量或与作用域无关）。
	allowed := map[string]bool{
		"SetObserver":                  true,
		"Stats":                        true,
		"ResetAll":                     true,
		"GetCredentialState":           true,
		"GetOrCreatePerModel":          true,
		"GetPerModel":                  true,
		"GetModelState":                true,
		"ReleaseProbe":                 true,
		"PeekAllowed":                  true,
		"Reset":                        true,
		"RecordFailurePerModel":        true,
		"RecordFailureWithBillingMode": true,
		"RecordFailureWithReset":       true,
		"RecordSuccessPerModel":        true,
		"RecordModelScopedFailure":     true,
		"RecoveredAt":                  true,
		"AllowPerModel":                true,
	}

	for i := 0; i < mt.NumMethod(); i++ {
		method := mt.Method(i)
		if allowed[method.Name] {
			continue
		}
		t.Fatalf("Manager 暴露了未登记的方法 %s —— 熔断器方法必须显式声明 model 分量，"+
			"否则会重演「准入查 A、记账写 B」的键空间分裂", method.Name)
	}

	// 显式钉住被禁用的旧方法名（它们**必须不存在**，而非仅仅不被调用）。
	for _, banned := range []string{"Get", "GetOrCreate", "RecordFailure", "RecordSuccess", "Allow"} {
		if _, ok := mt.MethodByName(banned); ok {
			t.Fatalf("Manager 仍暴露 legacy 方法 %s —— 生产路径一旦调用即会分裂键空间", banned)
		}
	}
}
