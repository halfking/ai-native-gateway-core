# llm-gateway 插件开发指南（端到端）

## 文档信息

- **创建日期**: 2026-11-27
- **版本**: v1.0
- **状态**: 正式发布
- **目标读者**: 插件开发者
- **前置阅读**: [三套 Capability 词表设计说明](./capability-vocabularies-design.md)

## 1. 快速开始

### 1.1 什么是 llm-gateway 插件？

llm-gateway 插件是**独立的 Go 进程**，通过 **Unix Socket + JSON 协议**与 Gateway 通信，可以：
- 在请求生命周期的 16 个 phase 中执行自定义逻辑
- 通过 HTTP API 访问 Gateway 提供的会话、分析等数据
- 提供 Web UI 页面和静态资源

### 1.2 插件架构

```
┌─────────────────────────────────────────────┐
│          cmd/gateway (Gateway 进程)          │
│                                              │
│  ┌────────────────────────────────────┐    │
│  │   plugin-runtime/supervisor.go     │    │
│  │   - Start() 启动插件进程            │    │
│  │   - 管理 Socket 和环境变量          │    │
│  └───────────┬────────────────────────┘    │
│              │                               │
│              │ Unix Socket                  │
│              │ /tmp/<plugin-id>.sock        │
└──────────────┼───────────────────────────────┘
               │
               │ JSON over Unix Socket
               │ (16 MiB 消息上限)
               │
┌──────────────▼───────────────────────────────┐
│      你的插件进程 (独立 Go 二进制)             │
│                                               │
│  cmd/my-plugin/main.go                       │
│  - child.Serve(pluginID, configure)          │
│  - 实现 HTTP Handler 处理请求                │
│  - 读取 plugin-manifest.json 和配置          │
└───────────────────────────────────────────────┘
```

### 1.3 5 分钟示例：Hello World 插件

#### 步骤 1：创建项目结构

```bash
cd llm-gateway-go
mkdir -p cmd/hello-plugin
cd cmd/hello-plugin
```

#### 步骤 2：编写 main.go

```go
package main

import (
    "encoding/json"
    "log"
    "net/http"

    pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
    "github.com/kaixuan/llm-gateway-go/plugin-runtime/remediation/child"
)

const PluginID = "hello-plugin"

func main() {
    if err := child.Serve(PluginID, configure); err != nil {
        log.Fatal(err)
    }
}

func configure(manifest *pluginruntime.Manifest) (http.HandlerFunc, error) {
    return func(w http.ResponseWriter, r *http.Request) {
        response := map[string]string{
            "message": "Hello from plugin!",
            "version": manifest.PluginVersion,
        }
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(response)
    }, nil
}
```

#### 步骤 3：编写 plugin-manifest.json

```json
{
  "schema_version": 2,
  "plugin_id": "hello-plugin",
  "display_name": "Hello World Plugin",
  "plugin_version": "1.0.0",
  "build_seq": 1,
  "channel": "dev",
  "gateway_compatibility": {
    "api_contract": "gateway-plugin-v2"
  },
  "runtime": {
    "entrypoint": "hello-plugin",
    "protocol": "http-unix-socket",
    "health_path": "/healthz",
    "handshake_path": "/handshake"
  },
  "capabilities": [],
  "permissions": [],
  "pages": [],
  "activation": {
    "license_required": false
  }
}
```

#### 步骤 4：构建和测试

```bash
# 构建插件
go build -o hello-plugin ./cmd/hello-plugin

# 创建插件目录
mkdir -p /tmp/plugins/hello-plugin/1.0.0
cp hello-plugin /tmp/plugins/hello-plugin/1.0.0/
cp plugin-manifest.json /tmp/plugins/hello-plugin/1.0.0/
ln -s 1.0.0 /tmp/plugins/hello-plugin/current

# 配置 Gateway（gateway.yaml）
cat > /tmp/gateway.yaml <<EOF
server:
  host: localhost
  port: 8080
plugins:
  directory: /tmp/plugins
  enabled: [hello-plugin]
EOF

# 启动 Gateway（已有数据库配置）
LLM_GATEWAY_PLUGIN_RUNTIME_CONFIG=/tmp/gateway.yaml go run ./cmd/gateway
```

---

## 2. 核心概念

### 2.1 插件生命周期

```
1. 加载阶段（Gateway 启动时）
   ├─ LoadManifest() 读取 plugin-manifest.json
   ├─ ValidateBinding() 校验绑定声明
   ├─ RegisterManifest() 注册路由能力
   └─ VerifyManifestSignature() 签名校验（生产环境）

2. 启动阶段
   ├─ Supervisor.Start() 启动插件进程
   ├─ 插件进程调用 child.Serve(pluginID, configure)
   ├─ Gateway 发起 Handshake 请求
   └─ Gateway 发起 Health Check 请求

3. 运行阶段
   ├─ Gateway 通过 IPC 发送请求到插件
   ├─ 插件处理请求并返回 JSON 响应
   └─ Gateway 记录插件状态和错误

4. 停止阶段
   ├─ Supervisor.Stop() 发送 SIGTERM
   ├─ 插件优雅关闭（15秒超时）
   └─ 超时后发送 SIGKILL
```

### 2.2 Manifest 文件详解

#### 完整 Manifest 结构

```json
{
  "schema_version": 2,
  "plugin_id": "my-plugin",
  "display_name": "My Plugin",
  "plugin_version": "1.2.3",
  "build_seq": 5,
  "channel": "stable",
  "gateway_compatibility": {
    "api_contract": "gateway-plugin-v2",
    "min_gateway_version": "2.0.0"
  },
  "runtime": {
    "entrypoint": "my-plugin",
    "protocol": "http-unix-socket",
    "health_path": "/healthz",
    "handshake_path": "/handshake"
  },
  "capabilities": ["session.read", "analytics.read"],
  "permissions": ["session.read"],
  "bindings": [{
    "binding_id": "routing-check",
    "phase": "routing",
    "priority": 100,
    "execution_mode": "sequential",
    "capabilities": ["session.observe"],
    "timeout_millis": 5000,
    "concurrency_limit": 10,
    "failure_policy": "fail_closed",
    "dto_profile": "summary",
    "enabled": true
  }],
  "pages": [{
    "path": "/dashboard",
    "title": "Plugin Dashboard",
    "icon": "dashboard"
  }],
  "web": {
    "static_path": "/static",
    "assets_dir": "assets"
  },
  "activation": {
    "license_required": false,
    "required_features": []
  },
  "settings_schema": {
    "type": "object",
    "properties": {
      "api_key": {
        "type": "string",
        "title": "API Key",
        "description": "Your API key"
      }
    }
  }
}
```

#### 字段说明

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `schema_version` | int | ✅ | Manifest 格式版本，当前为 2 |
| `plugin_id` | string | ✅ | 全局唯一标识，建议使用 kebab-case |
| `display_name` | string | ✅ | 显示名称 |
| `plugin_version` | string | ✅ | 插件版本（语义化版本） |
| `build_seq` | int | ✅ | 构建序号，每次构建递增 |
| `channel` | string | ✅ | 发布渠道：dev / pilot / stable |
| `gateway_compatibility` | object | ✅ | Gateway 兼容性声明 |
| `runtime` | object | ✅ | 运行时配置 |
| `capabilities` | array | ✅ | **路由能力**声明（HTTP API 权限） |
| `permissions` | array | ✅ | 权限声明（向后兼容字段） |
| `bindings` | array | ❌ | **绑定能力**声明（生命周期挂载） |
| `pages` | array | ❌ | Web UI 页面注册 |
| `web` | object | ❌ | 静态资源配置 |
| `activation` | object | ❌ | 激活条件 |
| `settings_schema` | object | ❌ | 设置表单 JSON Schema |

### 2.3 能力声明（Capabilities）

**关键区别**：
- `manifest.capabilities`：声明**路由能力**（插件调用 HTTP API 的权限）
- `bindings[].capabilities`：声明**绑定能力**（插件挂载生命周期阶段的权限）

详见 [三套 Capability 词表设计说明](./capability-vocabularies-design.md)。

#### 路由能力示例

```json
{
  "capabilities": ["session.read", "analytics.read"]
}
```

这允许插件调用：
- `GET /_gateway/plugin/v1/sessions` - 读取会话
- `GET /_gateway/plugin/v1/analytics` - 读取分析数据

#### 绑定能力示例

```json
{
  "bindings": [{
    "binding_id": "observe-requests",
    "phase": "pre_routing",
    "capabilities": ["request.observe", "session.observe"]
  }]
}
```

这允许插件在 `pre_routing` 阶段：
- 观察请求数据（只读）
- 观察会话数据（只读）

### 2.4 生命周期阶段（Phase）

插件可以挂载到以下 16 个 phase：

| Phase | 说明 | 失败策略 | 典型用途 |
|-------|------|---------|---------|
| `authentication` | 认证 | fail_closed | 自定义认证逻辑 |
| `pre_routing` | 路由前 | fail_closed | 请求预处理、审计 |
| `routing` | 路由中 | fail_closed | 自定义模型路由规则 |
| `pre_upstream` | 上游前 | fail_open | 请求转换、压缩 |
| `upstream` | 上游调用 | fail_closed | 拦截上游调用 |
| `post_upstream` | 上游后 | fail_open | 响应缓存、脱敏 |
| `streaming` | 流式处理 | fail_open | 流式响应处理 |
| `post_response` | 响应后 | fail_open | 异步日志、指标上报 |
| `governance` | 治理 | fail_open | 配额检查、策略执行 |
| `transform` | 转换 | fail_open | 数据转换 |
| `tool` | 工具 | fail_open | 工具调用拦截 |
| `analysis` | 分析 | fail_open | 数据分析 |
| `session_close` | 会话关闭 | fail_open | 清理资源 |
| `request` | 请求处理 | fail_open | 通用请求处理 |
| `response` | 响应处理 | fail_open | 通用响应处理 |
| `stream` | 流处理 | fail_open | 通用流处理 |

---

## 3. 开发实战

### 3.1 场景 1：会话审计插件

**需求**：在每个请求到达时记录会话信息，并提供查询接口。

#### 步骤 1：设计能力需求

- **路由能力**：`session.read`（通过 HTTP 读取会话详情）
- **绑定能力**：`request.observe`（观察请求）、`session.observe`（观察会话）
- **Phase**：`pre_routing`（在路由前记录）

#### 步骤 2：编写 Manifest

```json
{
  "schema_version": 2,
  "plugin_id": "session-audit",
  "display_name": "Session Audit Plugin",
  "plugin_version": "1.0.0",
  "build_seq": 1,
  "channel": "pilot",
  "gateway_compatibility": {
    "api_contract": "gateway-plugin-v2"
  },
  "runtime": {
    "entrypoint": "session-audit",
    "protocol": "http-unix-socket",
    "health_path": "/healthz",
    "handshake_path": "/handshake"
  },
  "capabilities": ["session.read"],
  "bindings": [{
    "binding_id": "audit-pre-routing",
    "phase": "pre_routing",
    "priority": 100,
    "execution_mode": "sequential",
    "capabilities": ["request.observe", "session.observe"],
    "timeout_millis": 3000,
    "failure_policy": "fail_open",
    "dto_profile": "summary",
    "enabled": true
  }],
  "pages": [{
    "path": "/audit-dashboard",
    "title": "Audit Dashboard"
  }]
}
```

#### 步骤 3：实现插件逻辑

```go
package main

import (
    "encoding/json"
    "log"
    "net/http"
    "sync"
    "time"

    pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
    "github.com/kaixuan/llm-gateway-go/plugin-runtime/remediation/child"
)

const PluginID = "session-audit"

type AuditRecord struct {
    Timestamp  time.Time `json:"timestamp"`
    SessionID  string    `json:"session_id"`
    RequestID  string    `json:"request_id"`
    UserID     string    `json:"user_id"`
    Method     string    `json:"method"`
    Path       string    `json:"path"`
}

type AuditStore struct {
    mu      sync.RWMutex
    records []AuditRecord
}

func (s *AuditStore) Add(record AuditRecord) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.records = append(s.records, record)
    
    // 保留最近 1000 条记录
    if len(s.records) > 1000 {
        s.records = s.records[len(s.records)-1000:]
    }
}

func (s *AuditStore) GetRecords() []AuditRecord {
    s.mu.RLock()
    defer s.mu.RUnlock()
    result := make([]AuditRecord, len(s.records))
    copy(result, s.records)
    return result
}

func main() {
    if err := child.Serve(PluginID, configure); err != nil {
        log.Fatal(err)
    }
}

func configure(manifest *pluginruntime.Manifest) (http.HandlerFunc, error) {
    store := &AuditStore{}
    
    return func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/audit":
            handleAudit(w, r, store)
        case "/records":
            handleGetRecords(w, r, store)
        case "/healthz":
            w.WriteHeader(http.StatusOK)
            w.Write([]byte("OK"))
        case "/handshake":
            json.NewEncoder(w).Encode(map[string]interface{}{
                "plugin_id": PluginID,
                "version":   manifest.PluginVersion,
                "ready":     true,
            })
        default:
            http.NotFound(w, r)
        }
    }, nil
}

func handleAudit(w http.ResponseWriter, r *http.Request, store *AuditStore) {
    var payload struct {
        SessionID string `json:"session_id"`
        RequestID string `json:"request_id"`
        UserID    string `json:"user_id"`
        Method    string `json:"method"`
        Path      string `json:"path"`
    }
    
    if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
        http.Error(w, "invalid payload", http.StatusBadRequest)
        return
    }
    
    record := AuditRecord{
        Timestamp: time.Now(),
        SessionID: payload.SessionID,
        RequestID: payload.RequestID,
        UserID:    payload.UserID,
        Method:    payload.Method,
        Path:      payload.Path,
    }
    
    store.Add(record)
    
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{
        "success": true,
        "message": "Audit record saved",
    })
}

func handleGetRecords(w http.ResponseWriter, r *http.Request, store *AuditStore) {
    records := store.GetRecords()
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{
        "total":   len(records),
        "records": records,
    })
}
```

### 3.2 场景 2：模型路由优化插件

**需求**：在路由阶段根据请求特征选择最优模型。

#### Manifest

```json
{
  "schema_version": 2,
  "plugin_id": "smart-router",
  "display_name": "Smart Model Router",
  "plugin_version": "1.0.0",
  "bindings": [{
    "binding_id": "routing-optimizer",
    "phase": "routing",
    "priority": 50,
    "capabilities": ["request.observe", "session.observe"],
    "timeout_millis": 2000,
    "failure_policy": "fail_open"
  }],
  "capabilities": ["session.read"],
  "settings_schema": {
    "type": "object",
    "properties": {
      "cost_weight": {
        "type": "number",
        "title": "Cost Weight",
        "default": 0.5
      },
      "quality_weight": {
        "type": "number",
        "title": "Quality Weight",
        "default": 0.5
      }
    }
  }
}
```

#### 实现

```go
package main

import (
    "encoding/json"
    "log"
    "net/http"
    "path/filepath"

    pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
    "github.com/kaixuan/llm-gateway-go/plugin-runtime/remediation/child"
)

const PluginID = "smart-router"

type Settings struct {
    CostWeight    float64 `yaml:"cost_weight"`
    QualityWeight float64 `yaml:"quality_weight"`
}

type RoutingRequest struct {
    SessionID    string   `json:"session_id"`
    RequestText  string   `json:"request_text"`
    AvailableModels []string `json:"available_models"`
}

type RoutingResponse struct {
    RecommendedModel string  `json:"recommended_model"`
    Confidence       float64 `json:"confidence"`
    Reason           string  `json:"reason"`
}

func main() {
    if err := child.Serve(PluginID, configure); err != nil {
        log.Fatal(err)
    }
}

func configure(manifest *pluginruntime.Manifest) (http.HandlerFunc, error) {
    var settings Settings
    if err := child.ReadPolicy(
        filepath.Join(filepath.Dir(manifest.ManifestPath), "settings.yaml"),
        &settings,
    ); err != nil {
        // 使用默认值
        settings = Settings{CostWeight: 0.5, QualityWeight: 0.5}
    }
    
    return func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/route" {
            handleRoute(w, r, settings)
            return
        }
        http.NotFound(w, r)
    }, nil
}

func handleRoute(w http.ResponseWriter, r *http.Request, settings Settings) {
    var req RoutingRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "invalid request", http.StatusBadRequest)
        return
    }
    
    // 简单的路由逻辑：根据文本长度选择模型
    var model string
    var reason string
    textLen := len(req.RequestText)
    
    if textLen < 100 {
        model = "gpt-3.5-turbo"
        reason = "Short request, using fast model"
    } else if textLen < 1000 {
        model = "gpt-4"
        reason = "Medium request, using balanced model"
    } else {
        model = "gpt-4-turbo"
        reason = "Long request, using high-capacity model"
    }
    
    response := RoutingResponse{
        RecommendedModel: model,
        Confidence:       0.85,
        Reason:           reason,
    }
    
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(response)
}
```

---

## 4. IPC 通信协议

### 4.1 通信机制

- **协议**：JSON over Unix Socket
- **消息上限**：16 MiB
- **超时控制**：通过 context 传递
- **并发**：支持多个并发请求

### 4.2 请求格式

Gateway 发送到插件的请求：

```json
{
  "method": "POST",
  "path": "/audit",
  "headers": {
    "Content-Type": "application/json",
    "X-Request-ID": "req-123"
  },
  "body": {
    "session_id": "sess-456",
    "user_id": "user-789"
  }
}
```

### 4.3 响应格式

插件返回给 Gateway 的响应：

```json
{
  "status": 200,
  "headers": {
    "Content-Type": "application/json"
  },
  "body": {
    "success": true,
    "message": "Processed successfully"
  }
}
```

### 4.4 健康检查

Gateway 定期发送健康检查请求：

```
GET /healthz
```

插件应返回：

```
HTTP/1.1 200 OK

OK
```

### 4.5 握手协议

Gateway 启动插件后发送握手请求：

```
GET /handshake
```

插件应返回：

```json
{
  "plugin_id": "my-plugin",
  "version": "1.0.0",
  "ready": true,
  "capabilities": ["session.read"]
}
```

---

## 5. 本地开发与测试

### 5.1 开发环境设置

```bash
# 克隆仓库
git clone https://github.com/kaixuan/llm-gateway-go.git
cd llm-gateway-go

# 安装依赖
go mod download

# 创建插件开发目录
mkdir -p cmd/my-plugin
```

### 5.2 本地运行

#### 方式 1：独立运行插件（调试模式）

```bash
# 设置环境变量
export AI_SESSION_MANAGER_PLUGIN_SOCKET=/tmp/my-plugin.sock
export AI_SESSION_MANAGER_MANIFEST=$PWD/cmd/my-plugin/plugin-manifest.json

# 运行插件
go run ./cmd/my-plugin
```

#### 方式 2：通过 Gateway 启动

```bash
# 构建插件
go build -o /tmp/plugins/my-plugin/1.0.0/my-plugin ./cmd/my-plugin
cp cmd/my-plugin/plugin-manifest.json /tmp/plugins/my-plugin/1.0.0/
ln -s 1.0.0 /tmp/plugins/my-plugin/current

# 配置 Gateway
cat > /tmp/gateway.yaml <<EOF
plugins:
  directory: /tmp/plugins
  enabled: [my-plugin]
EOF

# 启动 Gateway
LLM_GATEWAY_PLUGIN_RUNTIME_CONFIG=/tmp/gateway.yaml go run ./cmd/gateway
```

### 5.3 测试

#### 单元测试

```go
package main

import (
    "bytes"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"

    pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

func TestHandleAudit(t *testing.T) {
    manifest := &pluginruntime.Manifest{
        PluginID:      "test-plugin",
        PluginVersion: "1.0.0",
    }
    
    handler, err := configure(manifest)
    if err != nil {
        t.Fatalf("configure failed: %v", err)
    }
    
    payload := map[string]string{
        "session_id": "test-session",
        "user_id":    "test-user",
    }
    body, _ := json.Marshal(payload)
    
    req := httptest.NewRequest(http.MethodPost, "/audit", bytes.NewReader(body))
    w := httptest.NewRecorder()
    
    handler(w, req)
    
    if w.Code != http.StatusOK {
        t.Errorf("expected status 200, got %d", w.Code)
    }
}
```

#### 集成测试

```bash
# 运行插件集成测试
go test ./cmd/gateway -run '^TestPlugin' -v

# 运行 plugin-runtime 测试
go test ./plugin-runtime -v

# 运行门禁测试
cd scripts/ursmcheck
go test -run TestMagpieP0PluginCapabilityHoles -v
```

---

## 6. 构建与部署

### 6.1 构建插件

```bash
# 设置构建参数
PLUGIN_NAME="my-plugin"
PLUGIN_VERSION="1.0.0"
BUILD_DIR="/opt/gateway/plugins/$PLUGIN_NAME/$PLUGIN_VERSION"

# 创建构建目录
install -d -m 0750 "$BUILD_DIR"

# 构建二进制（指定目标平台）
GOOS=linux GOARCH=amd64 go build \
    -ldflags "-s -w" \
    -o "$BUILD_DIR/$PLUGIN_NAME" \
    ./cmd/$PLUGIN_NAME-plugin

# 复制资源文件
install -m 0644 cmd/$PLUGIN_NAME-plugin/plugin-manifest.json "$BUILD_DIR/"
install -m 0644 cmd/$PLUGIN_NAME-plugin/settings.yaml "$BUILD_DIR/"

# 创建版本链接
ln -sf "$PLUGIN_VERSION" "/opt/gateway/plugins/$PLUGIN_NAME/current"
```

### 6.2 签名验证（生产环境）

#### 生成签名密钥对

```bash
# 生成 Ed25519 密钥对
openssl genpkey -algorithm ED25519 -out private.pem
openssl pkey -in private.pem -pubout -out public.pem

# 提取十六进制公钥
openssl pkey -pubin -in public.pem -text | \
    grep -A 3 'pub:' | tail -n 3 | tr -d ' :\n'
```

#### 签名 Manifest

```bash
# 签名 plugin-manifest.json
openssl dgst -sha256 -sign private.pem \
    -out plugin-manifest.json.sig \
    plugin-manifest.json

# 验证签名
openssl dgst -sha256 -verify public.pem \
    -signature plugin-manifest.json.sig \
    plugin-manifest.json
```

#### 配置 Gateway

```bash
# 设置公钥环境变量
export LLM_GATEWAY_PLUGIN_SIGNING_PUBKEY="<十六进制公钥>"

# 或在配置文件中
cat >> gateway.yaml <<EOF
plugins:
  signing_pubkey: "<十六进制公钥>"
EOF
```

### 6.3 部署配置

#### Gateway 配置（gateway.yaml）

```yaml
server:
  host: 0.0.0.0
  port: 8080

plugins:
  directory: /opt/gateway/plugins
  enabled:
    - my-plugin
    - session-audit
    - smart-router
  signing_pubkey: "hexadecimal-public-key"
  socket_dir: /var/run/gateway/plugins
```

#### 插件目录结构

```
/opt/gateway/plugins/
├── my-plugin/
│   ├── current -> 1.0.0
│   ├── 1.0.0/
│   │   ├── my-plugin
│   │   ├── plugin-manifest.json
│   │   ├── plugin-manifest.json.sig
│   │   └── settings.yaml
│   └── 0.9.0/  (上一版本，用于回滚)
├── session-audit/
│   └── current -> 1.0.0
└── smart-router/
    └── current -> 2.1.0
```

### 6.4 版本升级

```bash
# 1. 构建新版本
NEW_VERSION="1.1.0"
go build -o /opt/gateway/plugins/my-plugin/$NEW_VERSION/my-plugin ./cmd/my-plugin-plugin

# 2. 复制配置文件
cp plugin-manifest.json /opt/gateway/plugins/my-plugin/$NEW_VERSION/
cp settings.yaml /opt/gateway/plugins/my-plugin/$NEW_VERSION/

# 3. 更新 manifest 版本号
sed -i "s/\"plugin_version\": \"1.0.0\"/\"plugin_version\": \"$NEW_VERSION\"/" \
    /opt/gateway/plugins/my-plugin/$NEW_VERSION/plugin-manifest.json

# 4. 签名新版本
openssl dgst -sha256 -sign private.pem \
    -out /opt/gateway/plugins/my-plugin/$NEW_VERSION/plugin-manifest.json.sig \
    /opt/gateway/plugins/my-plugin/$NEW_VERSION/plugin-manifest.json

# 5. 原子切换版本
ln -sfn $NEW_VERSION /opt/gateway/plugins/my-plugin/current

# 6. Gateway 会自动探测新版本并重启插件
# 如果失败，自动回滚到上一版本
```

---

## 7. 最佳实践

### 7.1 错误处理

```go
func handleRequest(w http.ResponseWriter, r *http.Request) {
    // 1. 解析请求
    var req RequestPayload
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        log.Printf("failed to decode request: %v", err)
        http.Error(w, "invalid request", http.StatusBadRequest)
        return
    }
    
    // 2. 校验请求
    if err := req.Validate(); err != nil {
        log.Printf("invalid request: %v", err)
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    
    // 3. 处理业务逻辑
    result, err := processRequest(req)
    if err != nil {
        log.Printf("processing failed: %v", err)
        http.Error(w, "processing failed", http.StatusInternalServerError)
        return
    }
    
    // 4. 返回响应
    w.Header().Set("Content-Type", "application/json")
    if err := json.NewEncoder(w).Encode(result); err != nil {
        log.Printf("failed to encode response: %v", err)
    }
}
```

### 7.2 超时控制

```go
func processWithTimeout(ctx context.Context, work func() error) error {
    done := make(chan error, 1)
    go func() {
        done <- work()
    }()
    
    select {
    case err := <-done:
        return err
    case <-ctx.Done():
        return ctx.Err()
    }
}
```

### 7.3 并发控制

```go
type WorkerPool struct {
    sem chan struct{}
}

func NewWorkerPool(maxConcurrency int) *WorkerPool {
    return &WorkerPool{
        sem: make(chan struct{}, maxConcurrency),
    }
}

func (p *WorkerPool) Do(work func()) {
    p.sem <- struct{}{}
    go func() {
        defer func() { <-p.sem }()
        work()
    }()
}
```

### 7.4 日志记录

```go
import (
    "log"
    "os"
)

func setupLogger() {
    // 输出到 stderr，Gateway 会捕获
    log.SetOutput(os.Stderr)
    log.SetPrefix("[my-plugin] ")
    log.SetFlags(log.LstdFlags | log.Lshortfile)
}

func main() {
    setupLogger()
    log.Println("Plugin starting...")
    
    if err := child.Serve(PluginID, configure); err != nil {
        log.Fatalf("Failed to start plugin: %v", err)
    }
}
```

### 7.5 配置管理

```go
type Config struct {
    APIKey       string        `yaml:"api_key"`
    Timeout      time.Duration `yaml:"timeout"`
    MaxRetries   int           `yaml:"max_retries"`
    EnableDebug  bool          `yaml:"enable_debug"`
}

func loadConfig(manifestPath string) (*Config, error) {
    configPath := filepath.Join(filepath.Dir(manifestPath), "settings.yaml")
    
    data, err := os.ReadFile(configPath)
    if err != nil {
        return nil, fmt.Errorf("read config: %w", err)
    }
    
    var cfg Config
    if err := yaml.Unmarshal(data, &cfg); err != nil {
        return nil, fmt.Errorf("parse config: %w", err)
    }
    
    // 校验配置
    if cfg.APIKey == "" {
        return nil, errors.New("api_key is required")
    }
    if cfg.Timeout == 0 {
        cfg.Timeout = 5 * time.Second
    }
    if cfg.MaxRetries == 0 {
        cfg.MaxRetries = 3
    }
    
    return &cfg, nil
}
```

---

## 8. 常见问题

### Q1: 插件启动失败，日志显示 "signature verification failed"

**答**：检查以下几点：
1. `plugin-manifest.json.sig` 文件是否存在
2. Gateway 是否配置了正确的公钥（`LLM_GATEWAY_PLUGIN_SIGNING_PUBKEY`）
3. 签名是否使用正确的私钥生成
4. manifest 文件是否在签名后被修改

**临时解决**（开发环境）：
```bash
# 关闭签名验证
unset LLM_GATEWAY_PLUGIN_SIGNING_PUBKEY
```

### Q2: 插件声明了 `request.mutate` 但被拒绝

**答**：`request.mutate` 是**写能力**，需要真沙箱才能使用。当前 `DefaultSandboxPolicy().Available = false`，所有写能力都被拒绝。

**被拒的 7 个写能力**：
- `request.mutate`, `request.block`
- `response.mutate`, `response.block`
- `tool.mutate`, `tool.block`, `tool.execute`

**解决方案**：等待真沙箱实现，或使用只读能力（`*.observe`）。

### Q3: 如何在插件中调用 Gateway HTTP API？

**答**：
1. 在 `manifest.capabilities` 中声明路由能力（如 `session.read`）
2. 插件中通过 HTTP 客户端调用 `/_gateway/plugin/v1/*` 接口
3. 携带插件身份认证（Gateway 会自动注入）

```go
func querySession(sessionID string) (*Session, error) {
    url := fmt.Sprintf("http://gateway/_gateway/plugin/v1/sessions/%s", sessionID)
    resp, err := http.Get(url)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
    }
    
    var session Session
    if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
        return nil, err
    }
    return &session, nil
}
```

### Q4: 插件进程崩溃后如何恢复？

**答**：Supervisor 会自动重启崩溃的插件：
- 探测到进程退出后，等待 5 秒
- 尝试重新启动插件
- 最多重试 3 次
- 如果仍然失败，标记插件为 `failed` 状态

**开发建议**：
- 添加 `recover()` 处理 panic
- 使用健康检查（`/healthz`）报告插件状态
- 记录详细的错误日志

```go
func main() {
    defer func() {
        if r := recover(); r != nil {
            log.Printf("Plugin panicked: %v", r)
            os.Exit(1)
        }
    }()
    
    if err := child.Serve(PluginID, configure); err != nil {
        log.Fatalf("Plugin failed: %v", err)
    }
}
```

### Q5: 如何调试插件？

**答**：
1. **日志调试**：插件的 stderr 会被 Gateway 捕获，查看 Gateway 日志
2. **独立运行**：设置环境变量后单独运行插件进程
3. **模拟请求**：使用 `curl` 通过 Unix Socket 发送请求

```bash
# 独立运行插件
export AI_SESSION_MANAGER_PLUGIN_SOCKET=/tmp/my-plugin.sock
go run ./cmd/my-plugin-plugin &

# 使用 curl 测试（需要 socat）
echo '{"method":"POST","path":"/test","body":{}}' | \
    socat - UNIX-CONNECT:/tmp/my-plugin.sock
```

4. **使用 delve 调试器**：

```bash
# 安装 delve
go install github.com/go-delve/delve/cmd/dlv@latest

# 启动调试
export AI_SESSION_MANAGER_PLUGIN_SOCKET=/tmp/my-plugin.sock
dlv debug ./cmd/my-plugin-plugin
```

---

## 9. 参考资料

### 9.1 源码文件

- `plugin-runtime/` - 插件运行时核心
- `cmd/upstream-cooldown-plugin/` - 示例插件 1
- `cmd/config-drift-plugin/` - 示例插件 2
- `cmd/sse-break-analysis-plugin/` - 示例插件 3

### 9.2 相关文档

- [三套 Capability 词表设计说明](./capability-vocabularies-design.md)
- [plugin-runtime v2 ADR](../../adr/2026-09-04-plugin-runtime-v2-capability-lifecycle.md)
- [插件化框架审计报告](../../audit/llm-gateway-plugin-framework-audit-2026-11-26.html)

### 9.3 API 文档

- Gateway HTTP API: `/_gateway/plugin/v1/*`
- IPC 协议: `plugin-runtime/ipc/README.md`
- Manifest Schema: `plugin-runtime/types.go`

---

## 10. 更新日志

| 版本 | 日期 | 作者 | 变更 |
|------|------|------|------|
| v1.0 | 2026-11-27 | 插件化框架审计工作组 | 初始版本，基于审计报告和示例插件整理 |
