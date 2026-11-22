package security

// hot_reload_test.go —— security.enabled / security.mode 的 TTL 热加载
// （2026-11-23，12h 变更审计 B）。
//
// 背景：spec 里 security.enabled / security.mode 都声明 HotReload:true，
// 但本 hook 的 loadConfig() **只在构造时调用一次** ⇒ 配置被快照进闭包 ⇒
// 运行时改 settings 不生效（运维关不掉安全检测）。
//
// 本文件钉住三条契约：
//   ① 构造后立刻请求用的是**当前**配置（立即生效语义不被破坏）；
//   ② TTL 内改配置 ⇒ 行为不变（TTL 语义）；
//   ③ 超过 TTL 后 ⇒ 行为跟随新配置。

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// memBackend 是可在运行时改值的内存 settings 后端（模拟运维改配置）。
type memBackend struct {
	mu    sync.Mutex
	store map[string][]byte
}

func (b *memBackend) Get(_ settings.Scope, key string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.store[key]
	if !ok {
		return nil, nil // 未设置
	}
	return v, nil
}

func (b *memBackend) Set(_ settings.Scope, key string, value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	prev := b.store[key]
	b.store[key] = raw
	return prev, nil
}

func (b *memBackend) GetTenant(_, key string) ([]byte, error) {
	return b.Get(settings.ScopeTenant, key)
}

func (b *memBackend) SetTenant(_, key string, value any) ([]byte, error) {
	return b.Set(settings.ScopeTenant, key, value)
}

// set 覆盖一个键的值（JSON 编码）。
func (b *memBackend) set(t *testing.T, key string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.store[key] = raw
}

// fakeClock 是可推进的时钟（测试用；不必真等 5 秒 TTL）。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newHotReloadRegistry 造一个只带 security.enabled / security.mode 两个
// spec 的 settings 注册表 + 可变后端。
func newHotReloadRegistry(t *testing.T) (*settings.Registry, *memBackend) {
	t.Helper()
	reg := settings.NewRegistry()
	for _, sp := range []*settings.Spec{
		{Key: "security.enabled", Type: settings.TypeBool, Scope: settings.ScopePlatform, Default: true},
		{Key: "security.mode", Type: settings.TypeString, Scope: settings.ScopePlatform, Default: "enforce"},
	} {
		if err := reg.RegisterSpec(sp); err != nil {
			t.Fatalf("register spec %s: %v", sp.Key, err)
		}
	}
	backend := &memBackend{store: map[string][]byte{}}
	reg.RegisterBackend(settings.ScopePlatform, backend)
	return reg, backend
}

func TestSecurityHookHotReloadEnabled(t *testing.T) {
	reg, backend := newHotReloadRegistry(t)
	clock := &fakeClock{t: time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)}
	h := newSecurityHook(nil, Scope{}, reg, clock.Now)
	env := &domain.PipelineRequest{}

	// ① 构造后立刻生效：用的是当前配置（security.enabled 缺省 true）。
	if !h.Enabled(context.Background(), env) {
		t.Fatal("构造后立刻请求应使用当前配置（security.enabled 缺省 true）")
	}

	// ② 运行时改配置；TTL 内行为不变。
	backend.set(t, "security.enabled", false)
	if !h.Enabled(context.Background(), env) {
		t.Fatal("TTL 内行为不应改变（热加载按 TTL 节流）")
	}

	// ③ 超过 TTL 后：下一次请求触发刷新，之后行为跟随新配置。
	clock.Advance(securityConfigTTL)
	h.Enabled(context.Background(), env) // 触发异步刷新
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !h.Enabled(context.Background(), env) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if h.Enabled(context.Background(), env) {
		t.Fatal("超过 TTL 后应跟随新配置（security.enabled=false）")
	}
}

func TestSecurityHookHotReloadMode(t *testing.T) {
	reg, backend := newHotReloadRegistry(t)
	backend.set(t, "security.mode", "observe")

	clock := &fakeClock{t: time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)}
	h := newSecurityHook(nil, Scope{}, reg, clock.Now)

	// ① 构造时读到的是**当前**值（不是 spec 默认值）。
	if got := h.mode.Load(); got != "observe" {
		t.Fatalf("构造时 mode = %v, want observe", got)
	}

	// ② TTL 内改配置 ⇒ 行为不变。
	backend.set(t, "security.mode", "enforce")
	if got := h.mode.Load(); got != "observe" {
		t.Fatalf("TTL 内 mode = %v, want observe（行为不应改变）", got)
	}

	// ③ 超过 TTL 后 ⇒ 跟随新配置。
	clock.Advance(securityConfigTTL)
	h.Enabled(context.Background(), &domain.PipelineRequest{}) // 触发异步刷新
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := h.mode.Load(); got == "enforce" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if got := h.mode.Load(); got != "enforce" {
		t.Fatalf("超过 TTL 后 mode = %v, want enforce", got)
	}
}

// TestSecurityHookNoSettingsKeepsDefault —— 没有 settings 后端时保持构造
// 时的默认（启用 / enforce），且不会因为热加载路径而 panic。
func TestSecurityHookNoSettingsKeepsDefault(t *testing.T) {
	h := NewSecurityHook(nil, Scope{}, nil)
	env := &domain.PipelineRequest{}
	if !h.Enabled(context.Background(), env) {
		t.Fatal("无 settings 时应默认启用")
	}
	if got := h.mode.Load(); got != "enforce" {
		t.Fatalf("无 settings 时 mode = %v, want enforce", got)
	}
}
