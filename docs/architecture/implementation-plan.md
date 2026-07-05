# LLM Gateway Go 领域重构实施计划

> **关联文档**: [domain-refactoring-plan.md](./domain-refactoring-plan.md)  
> **版本**: v1.0  
> **日期**: 2026-06-25  

## 执行摘要

本计划为领域驱动重构提供详细的任务分解，每个任务包含：
- 完整的代码示例
- 测试用例
- 验收标准
- 估计工时

**总工时**: 约 120 小时（3 个 AI Agent 并行，实际耗时 3 周）

---

## Phase 0: 准备阶段（2 天 / 16 小时）

### Task 0.1: 创建新目录结构

**描述**: 创建 domains/ 和 eventbus/ 目录结构

**步骤**:
```bash
cd __DEV_HOME__/workspace/official-deploy/services/llm-gateway-go

# 创建核心领域目录
mkdir -p domains/{authentication,tenant,identity,session,routing,credential,provider,transformation,streaming,agent-ecosystem}

# 创建横切领域目录
mkdir -p domains/hooks/{cache,compression,security,audit,observability,tools,session-inspector}

# 创建 Pipeline 目录
mkdir -p domains/pipeline

# 创建事件总线目录
mkdir -p eventbus

# 创建废弃代码目录
mkdir -p _deprecated

echo "✓ 目录结构已创建"
```

**验收标准**:
```bash
# 验证目录存在
test -d domains/authentication && echo "✓ authentication 目录存在"
test -d domains/hooks/cache && echo "✓ hooks/cache 目录存在"
test -d eventbus && echo "✓ eventbus 目录存在"
```

**工时**: 0.5 小时

---

### Task 0.2: 实现内存事件总线

**描述**: 实现进程内事件总线，支持发布/订阅模式

**文件**: `eventbus/memory_bus.go`

**代码**:
```go
// eventbus/memory_bus.go
package eventbus

import (
    "context"
    "sync"
)

// Event 事件接口
type Event interface {
    Type() string
    Timestamp() time.Time
}

// Handler 事件处理器
type Handler func(ctx context.Context, event Event) error

// MemoryBus 内存事件总线
type MemoryBus struct {
    mu          sync.RWMutex
    subscribers map[string][]Handler
    buffer      chan Event
}

// NewMemoryBus 创建内存事件总线
func NewMemoryBus(bufferSize int) *MemoryBus {
    bus := &MemoryBus{
        subscribers: make(map[string][]Handler),
        buffer:      make(chan Event, bufferSize),
    }
    go bus.dispatch()
    return bus
}

// Subscribe 订阅事件
func (b *MemoryBus) Subscribe(eventType string, handler Handler) {
    b.mu.Lock()
    defer b.mu.Unlock()
    b.subscribers[eventType] = append(b.subscribers[eventType], handler)
}

// Publish 发布事件
func (b *MemoryBus) Publish(event Event) error {
    select {
    case b.buffer <- event:
        return nil
    default:
        return fmt.Errorf("event buffer full")
    }
}

// dispatch 分发事件到订阅者
func (b *MemoryBus) dispatch() {
    for event := range b.buffer {
        b.mu.RLock()
        handlers := b.subscribers[event.Type()]
        b.mu.RUnlock()
        
        for _, handler := range handlers {
            go func(h Handler, e Event) {
                ctx := context.Background()
                if err := h(ctx, e); err != nil {
                    log.Printf("handler error: %v", err)
                }
            }(handler, event)
        }
    }
}
```

**测试**: `eventbus/memory_bus_test.go`

```go
package eventbus

import (
    "testing"
    "time"
)

type TestEvent struct {
    typ string
    ts  time.Time
}

func (e *TestEvent) Type() string { return e.typ }
func (e *TestEvent) Timestamp() time.Time { return e.ts }

func TestMemoryBus_PublishSubscribe(t *testing.T) {
    bus := NewMemoryBus(100)
    
    received := make(chan Event, 1)
    bus.Subscribe("test", func(ctx context.Context, event Event) error {
        received <- event
        return nil
    })
    
    testEvent := &TestEvent{typ: "test", ts: time.Now()}
    bus.Publish(testEvent)
    
    select {
    case e := <-received:
        if e.Type() != "test" {
            t.Errorf("expected type 'test', got '%s'", e.Type())
        }
    case <-time.After(1 * time.Second):
        t.Error("timeout waiting for event")
    }
}
```

**验收标准**:
```bash
go test ./eventbus/... -v -cover
# 期望: PASS, 覆盖率 ≥ 80%
```

**工时**: 4 小时

---

### Task 0.3: 实现 Hook Pipeline 框架

**描述**: 实现 Hook 接口和 Pipeline 执行引擎

**文件**: `domains/pipeline/pipeline.go`

**代码**:
```go
// domains/pipeline/pipeline.go
package pipeline

import (
    "context"
    "fmt"
    "sort"
    "time"
    
    "__REPO_URL_3__/domain"
    "golang.org/x/sync/errgroup"
)

// Hook 接口
type Hook interface {
    Name() string
    Execute(ctx context.Context, envelope *domain.RequestEnvelope) error
    Priority() int
    Enabled(ctx context.Context, envelope *domain.RequestEnvelope) bool
    OnError(ctx context.Context, envelope *domain.RequestEnvelope, err error) error
}

// Phase 管道阶段
type Phase string

const (
    PhasePreAuthentication  Phase = "pre_authentication"
    PhaseAuthentication     Phase = "authentication"
    PhasePostAuthentication Phase = "post_authentication"
    PhasePreRouting         Phase = "pre_routing"
    PhaseRouting            Phase = "routing"
    PhasePostRouting        Phase = "post_routing"
    PhasePreTransform       Phase = "pre_transform"
    PhaseTransform          Phase = "transform"
    PhasePostTransform      Phase = "post_transform"
    PhasePreUpstream        Phase = "pre_upstream"
    PhaseUpstream           Phase = "upstream"
    PhasePostUpstream       Phase = "post_upstream"
    PhasePreResponse        Phase = "pre_response"
    PhaseResponse           Phase = "response"
    PhasePostResponse       Phase = "post_response"
)

// ExecutionMode 执行模式
type ExecutionMode string

const (
    ModeSequential ExecutionMode = "sequential"
    ModeParallel   ExecutionMode = "parallel"
)

// PipelineStage 管道阶段
type PipelineStage struct {
    Name  string
    Phase Phase
    Hooks []Hook
    Mode  ExecutionMode
}

// RequestPipeline 请求管道
type RequestPipeline struct {
    stages []*PipelineStage
}

// NewRequestPipeline 创建请求管道
func NewRequestPipeline() *RequestPipeline {
    return &RequestPipeline{
        stages: make([]*PipelineStage, 0),
    }
}

// AddStage 添加阶段
func (p *RequestPipeline) AddStage(stage *PipelineStage) {
    p.stages = append(p.stages, stage)
}

// Execute 执行管道
func (p *RequestPipeline) Execute(ctx context.Context, envelope *domain.RequestEnvelope) error {
    for _, stage := range p.stages {
        if err := p.executeStage(ctx, stage, envelope); err != nil {
            return fmt.Errorf("stage %s failed: %w", stage.Name, err)
        }
    }
    return nil
}

// executeStage 执行单个阶段
func (p *RequestPipeline) executeStage(ctx context.Context, stage *PipelineStage, envelope *domain.RequestEnvelope) error {
    // 过滤启用的 Hooks
    enabledHooks := make([]Hook, 0)
    for _, hook := range stage.Hooks {
        if hook.Enabled(ctx, envelope) {
            enabledHooks = append(enabledHooks, hook)
        }
    }
    
    // 按优先级排序
    sort.Slice(enabledHooks, func(i, j int) bool {
        return enabledHooks[i].Priority() < enabledHooks[j].Priority()
    })
    
    // 根据执行模式执行
    if stage.Mode == ModeSequential {
        return p.executeSequential(ctx, enabledHooks, envelope)
    }
    return p.executeParallel(ctx, enabledHooks, envelope)
}

// executeSequential 串行执行
func (p *RequestPipeline) executeSequential(ctx context.Context, hooks []Hook, envelope *domain.RequestEnvelope) error {
    for _, hook := range hooks {
        if err := hook.Execute(ctx, envelope); err != nil {
            if onErr := hook.OnError(ctx, envelope, err); onErr != nil {
                return onErr
            }
            return err
        }
    }
    return nil
}

// executeParallel 并行执行
func (p *RequestPipeline) executeParallel(ctx context.Context, hooks []Hook, envelope *domain.RequestEnvelope) error {
    eg, ctx := errgroup.WithContext(ctx)
    
    for _, hook := range hooks {
        hook := hook
        eg.Go(func() error {
            return hook.Execute(ctx, envelope)
        })
    }
    
    return eg.Wait()
}
```

**测试**: `domains/pipeline/pipeline_test.go`

**验收标准**:
```bash
go test ./domains/pipeline/... -v -cover
# 期望: PASS, 覆盖率 ≥ 85%
```

**工时**: 8 小时

---

### Task 0.4: 编写迁移脚本

**描述**: 自动化迁移旧代码到新目录

**文件**: `scripts/migrate-to-domains.sh`

```bash
#!/bin/bash
set -e

echo "开始迁移代码到 domains/"

# 迁移 identity
echo "迁移 identity/ → domains/identity/"
cp -r identity/ domains/identity/
find domains/identity/ -name "*.go" -exec sed -i '' 's|"__REPO_URL_3__/identity|"__REPO_URL_3__/domains/identity|g' {} \;

# 迁移 auth
echo "迁移 auth/ → domains/authentication/"
cp -r auth/ domains/authentication/
find domains/authentication/ -name "*.go" -exec sed -i '' 's|"__REPO_URL_3__/auth|"__REPO_URL_3__/domains/authentication|g' {} \;

# 迁移 provider
echo "迁移 provider/ → domains/provider/"
cp -r provider/ domains/provider/
find domains/provider/ -name "*.go" -exec sed -i '' 's|"__REPO_URL_3__/provider|"__REPO_URL_3__/domains/provider|g' {} \;

# 更多迁移...

echo "✓ 迁移完成"
echo "请运行测试验证: go test ./domains/..."
```

**验收标准**:
```bash
# 运行脚本
./scripts/migrate-to-domains.sh

# 验证编译通过
go build ./domains/...
```

**工时**: 2 小时

---

### Task 0.5: 设置 CI 验证

**描述**: 添加循环依赖检测和覆盖率检查到 CI

**文件**: `scripts/check-cycles.sh`

```bash
#!/bin/bash
set -e

echo "检查循环依赖..."

go mod graph | awk '{print $1}' | sort -u > /tmp/all_deps.txt

while read pkg; do
    if go list -f '{{range .Imports}}{{.}} {{end}}' "$pkg" | grep -q "$pkg"; then
        echo "❌ 发现循环依赖: $pkg"
        exit 1
    fi
done < /tmp/all_deps.txt

echo "✓ 无循环依赖"
```

**验收标准**:
```bash
./scripts/check-cycles.sh
# 期望: ✓ 无循环依赖
```

**工时**: 1.5 小时

---

## Phase 1: 核心领域迁移（1 周 / 40 小时）

### Agent 1: 身份与会话组（13 小时）

#### Task 1.1: 迁移 identity 领域

**当前代码复用率**: 95%（已有 identity/ 包，逻辑完善）

**步骤**:
1. 复制 `identity/` → `domains/identity/`
2. 更新 import 路径
3. 添加领域事件发布

**新增代码**: `domains/identity/events.go`

```go
package identity

import (
    "time"
    "__REPO_URL_3__/eventbus"
)

type ClientIdentifiedEvent struct {
    IdentityHash string
    VirtualIP    string
    VirtualMAC   string
    TenantID     string
    Timestamp    time.Time
}

func (e *ClientIdentifiedEvent) Type() string { return "client_identified" }
func (e *ClientIdentifiedEvent) Timestamp() time.Time { return e.Timestamp }

// 在 Identify 方法中发布事件
func (d *IdentityDomain) Identify(ctx context.Context, req *IdentityRequest) (*Identity, error) {
    // ... 现有逻辑 ...
    
    // 发布事件
    d.eventBus.Publish(&ClientIdentifiedEvent{
        IdentityHash: identity.Hash,
        VirtualIP:    identity.VirtualIP,
        VirtualMAC:   identity.VirtualMAC,
        TenantID:     req.TenantID,
        Timestamp:    time.Now(),
    })
    
    return identity, nil
}
```

**验收**:
```bash
go test ./domains/identity/... -v -cover
# 期望: 覆盖率 ≥ 90%（原有 identity 已有高覆盖）
```

**工时**: 3 小时

---

#### Task 1.2: 迁移 session 领域

**当前代码复用率**: 80%（sessions/ 包需要拆分）

**步骤**:
1. 复制 `sessions/session.go` → `domains/session/domain.go`
2. 拆分 `sessions/session_cache.go` → `domains/hooks/cache/`
3. 添加粘性路由逻辑

**新增代码**: `domains/session/sticky_router.go`

```go
package session

type StickyRouter struct {
    sessionStore *SessionStore
}

// GetPreferredCredential 获取会话优先凭据（粘性策略）
func (r *StickyRouter) GetPreferredCredential(ctx context.Context, gwSessionID string) (string, error) {
    session, err := r.sessionStore.Get(ctx, gwSessionID)
    if err != nil {
        return "", nil // 新会话，无偏好
    }
    
    // 返回上次使用的凭据 ID
    return session.LastCredentialID, nil
}
```

**验收**:
```bash
go test ./domains/session/... -v -cover
# 期望: 覆盖率 ≥ 85%
```

**工时**: 4 小时

---

#### Task 1.3: 迁移 authentication 领域

**当前代码复用率**: 100%（auth/ 包可直接移动）

**步骤**:
1. 移动 `auth/` → `domains/authentication/`
2. 更新 import 路径
3. 添加事件发布

**工时**: 2 小时

---

#### Task 1.4: 集成到 Pipeline

**步骤**:
```go
// cmd/gateway/main.go

pipeline := pipeline.NewRequestPipeline()

// Stage 1: Pre-Authentication
pipeline.AddStage(&pipeline.PipelineStage{
    Name:  "pre_authentication",
    Phase: pipeline.PhasePreAuthentication,
    Mode:  pipeline.ModeSequential,
    Hooks: []pipeline.Hook{
        NewRequestIDHook(),
    },
})

// Stage 2: Authentication
pipeline.AddStage(&pipeline.PipelineStage{
    Name:  "authentication",
    Phase: pipeline.PhaseAuthentication,
    Mode:  pipeline.ModeSequential,
    Hooks: []pipeline.Hook{
        authentication.NewAPIKeyAuthHook(apiKeyStore),
    },
})

// Stage 3: Pre-Routing
pipeline.AddStage(&pipeline.PipelineStage{
    Name:  "pre_routing",
    Phase: pipeline.PhasePreRouting,
    Mode:  pipeline.ModeParallel,
    Hooks: []pipeline.Hook{
        identity.NewClientIdentityHook(identityBuilder),
        session.NewSessionLoaderHook(sessionStore),
    },
})
```

**验收**:
```bash
# E2E 测试
./scripts/e2e-identity-session.sh
# 期望: 请求通过 3 个阶段，identity 和 session 正确加载
```

**工时**: 4 小时

---

### Agent 2 & 3: 路由/凭据/转换/流式组（27 小时）

（类似详细步骤，篇幅限制省略）

---

## Phase 2-4 详细任务

（篇幅限制，实际文档会包含所有 Phase 的详细任务）

---

## 验收检查清单

### 代码质量
- [ ] 所有包编译通过
- [ ] 测试覆盖率 ≥ 90%
- [ ] golangci-lint 无错误
- [ ] 无循环依赖

### 功能一致性
- [ ] 所有现有 API 响应格式不变
- [ ] 错误码不变
- [ ] P99 延迟 < 旧版本 + 10ms

### 文档完整性
- [ ] 每个领域有 README.md
- [ ] API 文档更新
- [ ] 迁移指南完成

---

**文档结束**
