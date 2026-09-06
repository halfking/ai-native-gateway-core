# 客户端属性解析逻辑审计报告

**审计日期**: 2026-09-01  
**审计范围**: 客户端类型识别、客户端IP提取、地域显示、会话属性存储、Dashboard统计

---

## 一、客户端类型识别 (Client Type Detection)

### 1.1 实现现状

客户端类型识别通过三层机制实现：

#### **层级 1: HTTP 头检测** (`telemetry/request_metadata.go:299-349`)
- `X-Gw-Client-Type` 头（最高优先级，显式指定）
- `User-Agent` 模式匹配
- 支持的客户端类型：cursor, claude-code, opencode, zcode, codex, roocode, vscode, copilot, windsurf, zed, jetbrains, postman, insomnia, python-client, go-client, curl

#### **层级 2: 系统提示词语义识别** (`telemetry/request_metadata.go:32-61`)
- 从首条系统消息中提取智能体自我介绍
- 模式注册表支持运行时扩展
- 已注册模式：
  - ZCode: "zcode"
  - OpenCode: "opencode"
  - Codex: "openai codex", "codex cli", "you are codex"
  - Claude Code: "claude code", "you are claude code"
  - RooCode: "roocode", "you are roo code"
  - Windsurf: "windsurf", "you are windsurf"
  - Zed: "zed editor", "you are zed"
  - Copilot: "github copilot", "you are copilot"
  - Cline: "you are cline", "cline coding"
  - Aider: "you are aider", "aider chat"
  - Continue: "you are continue", "continue dev"
  - Kiro: "you are kiro", "kiro ide"
  - Cursor: "you are an ai assistant in cursor", "operate in cursor"
  - VSCode: "visual studio code", "vscode"
  - Claude: "you are claude" (兜底匹配)

#### **层级 3: 智能兜底策略** (`domains/streaming/request_meta.go:131-142`)
- 当 header 路径返回 "unknown" 或低质量通用名时，使用系统提示词覆盖
- 低质量名单：unknown, go-client, python-client, curl, postman, insomnia

### 1.2 数据流路径

```
HTTP Request
    ↓
extractClientType(r) → header-based detection
    ↓
fillAttemptMeta() → store in meta.AgentName
    ↓
shouldOverrideAgentName() → check if override needed
    ↓
DetectAgentFromSystemPrompt() → system prompt fallback
    ↓
enrichRequestLogFromMeta() → write to request_logs.agent_name
    ↓
BuildContextAttrsEntry() → write to request_context_attrs.agent_name
```

### 1.3 存储位置

1. **主表**: `request_logs.agent_name` (VARCHAR(255))
2. **侧表**: `request_context_attrs.agent_name` (VARCHAR(255))
3. **热表**: `request_logs_hot.agent_name` (VARCHAR(255))
4. **统计维度表**: `request_stats_dim_minute` (dim_type='client_profile')

### 1.4 **发现的问题**

#### ❌ **问题 1: Dashboard 统计使用错误的字段**

**位置**: `bg/stats_minute_rollup.go:222`

```go
{"client_profile", `COALESCE(NULLIF(r.client_profile, ''), '__unknown__')`},
```

**问题**: Dashboard 的 "clients" 饼图使用 `client_profile` 字段进行统计，但这个字段存储的是设备指纹相关信息（如 "mac|darwin|arm64"），而不是客户端类型（如 "cursor", "zcode"）。

**影响**: 
- Dashboard 的客户端类型统计完全错误
- 无法看到各智能体的真实使用分布
- 用户看到的是设备指纹而非有意义的客户端名称

**正确字段**: 应该使用 `agent_name` 字段

---

## 二、客户端 IP 提取与地域处理

### 2.1 IP 提取实现

#### **基础提取** (`telemetry/request_metadata.go:169-186`)
```go
func ExtractClientIP(r *http.Request) string {
    // 优先级: X-Real-IP > X-Forwarded-For[0] > RemoteAddr
}
```

#### **信任列表提取** (`telemetry/request_metadata.go:207-240`)
```go
func ExtractClientIPTrusted(r *http.Request, trusted []*net.IPNet) string {
    // 仅当 TCP peer 在信任 CIDR 列表时才信任 X-Real-IP/XFF 头
    // 防止公网客户端伪造源 IP
}
```

### 2.2 中间件集成

**OriginMiddleware** (`middleware/origin_mw.go`)
- 使用信任列表验证反向代理
- 将解析后的 IP 存入 context
- 通过 `middleware.ContextClientIP(ctx)` 获取

### 2.3 数据流路径

```
HTTP Request
    ↓
OriginMiddleware → trust-list validated IP extraction
    ↓
context.WithValue(clientIPKey, ip)
    ↓
fillAttemptMeta() → middleware.ContextClientIP(ctx) fallback to telemetry.ExtractClientIP(r)
    ↓
meta.ClientIP → store in requestAttemptMeta
    ↓
BuildContextAttrsEntry() → write to request_context_attrs.client_ip
```

### 2.4 存储位置

1. **主表**: `request_logs.client_ip` (INET)
2. **侧表**: `request_context_attrs.client_ip` (INET)

### 2.5 **发现的问题**

#### ⚠️ **问题 2: 缺少地域信息提取**

**需求**: 内网显示 IP，外网显示地域（国家/省/市）

**现状**: 
- ✅ IP 提取已实现
- ❌ 地域解析未实现
- ❌ 内外网区分逻辑未实现
- ❌ 地域信息未存储

**建议方案**:
1. 引入 GeoIP 库（如 maxminddb-golang）
2. 在 `fillAttemptMeta` 中判断 IP 是否为内网（RFC1918）
3. 外网 IP 通过 GeoIP 查询地域
4. 存储到扩展属性：`geo_country`, `geo_region`, `geo_city`
5. Dashboard 展示时根据 IP 类型决定显示内容

#### ⚠️ **问题 3: Dashboard 未统计客户端 IP/地域**

**位置**: `admin/dashboard_board_queries.go:202-210`

```go
types := map[string]string{
    "clients":         "client_profile",  // ❌ 错误字段
    "virtual_ips":     "virtual_ip",      // ✅ 虚拟IP（派生）
    "identity_hashes": "identity_hash",
    "models":          "model",
    "errors":          "error_kind",
    "tenants":         "tenant",
    "providers":       "provider",
}
```

**问题**: 
- `virtual_ips` 饼图统计的是**派生的虚拟 IP**（10.x.x.x），而非真实客户端 IP
- 缺少真实客户端 IP/地域的统计维度
- 前端 `web/src/api/board.ts:61` 定义了 `virtual_ips` 但未定义 `client_ips` 或 `client_locations`

**建议方案**:
1. 添加新的统计维度 `client_location`（地域）或 `client_ip`（内网 IP）
2. 在 rollup 时根据 IP 类型决定 dim_key：
   - 内网: `10.1.2.3`
   - 外网: `中国/广东/深圳` 或 `United States/California/San Francisco`

---

## 三、会话属性存储验证

### 3.1 请求扩展属性 (`request_context_attrs`)

**表结构**: `sql/migrations/startup/407_request_context_attrs.sql`

```sql
CREATE TABLE public.request_context_attrs (
    request_id         VARCHAR(64) PRIMARY KEY,
    tenant_id          VARCHAR(255),
    
    -- 客户端感知
    client_ip          INET,
    client_forwarded_for TEXT,
    agent_name         VARCHAR(255),  -- ✅ 存储了客户端类型
    agent_type         VARCHAR(50),
    virtual_client_id  VARCHAR(64),
    virtual_ip         VARCHAR(15),
    virtual_mac        VARCHAR(17),
    ...
);
```

**写入路径**: `domains/streaming/context_attrs.go:19-62`

```go
func BuildContextAttrsEntry(c *RequestLogContext, keyInfo *KeyInfo, meta *requestAttemptMeta, ctx context.Context) *ContextAttrsEntry {
    // fillFromMeta() 将 meta.AgentName 写入 entry.AgentName
    // fillFromMeta() 将 meta.ClientIP 写入 entry.ClientIP
}
```

### 3.2 会话扩展属性

**表结构**: `sessions_v2.extended_attrs` (JSONB)

**查询**: 在 `admin/session_detail_v2.go` 中通过 JOIN 获取

```sql
SELECT s.*, 
       -- 关联请求上下文属性
       (SELECT jsonb_agg(jsonb_build_object(
           'client_ip', a.client_ip,
           'agent_name', a.agent_name,
           'agent_type', a.agent_type,
           ...
       )) FROM request_context_attrs a WHERE a.gw_session_id = s.id) AS context_attrs
FROM sessions_v2 s
```

### 3.3 **发现的问题**

#### ✅ **问题 4: 扩展属性存储已正确实现**

- `agent_name` 和 `client_ip` 都正确写入了 `request_context_attrs`
- 会话详情页面可以通过 JOIN 获取这些属性
- 前端可以展示单个请求的详细客户端信息

**但需要验证**:
- [ ] 前端是否正确展示这些字段
- [ ] 会话详情页的扩展属性显示逻辑

---

## 四、Dashboard 统计验证

### 4.1 统计数据流

```
request_logs_with_current_month
    ↓ (bg/stats_minute_rollup.go)
rollupDims() → GROUP BY client_profile / virtual_ip / ...
    ↓
INSERT INTO request_stats_dim_minute
    ↓ (admin/dashboard_board_queries.go)
queryDimPie() → SELECT FROM request_stats_dim_minute WHERE dim_type = 'client_profile'
    ↓
pies.clients → frontend display
```

### 4.2 Frontend 接口

**类型定义**: `web/src/api/board.ts:3-9`

```typescript
export interface BoardPieItem {
  key: string          // 饼图条目的键（如 "cursor", "zcode"）
  requests: number
  tokens: number
  credits: number
  cost_usd: number
}
```

**数据结构**: `web/src/api/board.ts:59-67`

```typescript
pies: {
  clients: BoardPieItem[]        // ❌ 统计 client_profile（错误）
  virtual_ips: BoardPieItem[]    // ✅ 统计虚拟 IP
  identity_hashes: BoardPieItem[]
  models: BoardPieItem[]
  errors: BoardPieItem[]
  tenants: BoardPieItem[]
  providers: BoardPieItem[]
}
```

### 4.3 **发现的问题**

#### ❌ **问题 5: Dashboard "clients" 饼图统计错误字段**

**根因**: Rollup 使用了 `client_profile` 而非 `agent_name`

**修复方案**:
1. 修改 `bg/stats_minute_rollup.go:222` 的 dim_key 表达式：
   ```go
   // 修改前
   {"client_profile", `COALESCE(NULLIF(r.client_profile, ''), '__unknown__')`},
   
   // 修改后
   {"agent_name", `COALESCE(NULLIF(r.agent_name, ''), '__unknown__')`},
   ```

2. 同步修改 `admin/dashboard_board_queries.go:203` 的映射：
   ```go
   // 修改前
   "clients": "client_profile",
   
   // 修改后
   "clients": "agent_name",
   ```

3. 重新 backfill 历史数据（可选）：
   ```sql
   DELETE FROM request_stats_dim_minute WHERE dim_type = 'client_profile';
   -- 触发 rollup 重新计算
   UPDATE request_stats_rollup_cursor SET last_ts = now() - INTERVAL '7 days';
   ```

---

## 五、修复优先级与计划

### P0 - 立即修复（影响核心统计）

1. **修复 Dashboard 客户端类型统计**
   - [ ] `bg/stats_minute_rollup.go:222` 改用 `agent_name`
   - [ ] `admin/dashboard_board_queries.go:203` 改用 `agent_name`
   - [ ] 验证 rollup 重新执行后统计正确

### P1 - 本轮完成（需求明确要求）

2. **实现客户端 IP 地域显示**
   - [ ] 引入 GeoIP 库（选型：maxminddb-golang）
   - [ ] 在 `fillAttemptMeta` 中添加地域解析
   - [ ] 存储 `geo_country`, `geo_region`, `geo_city` 到扩展属性
   - [ ] 添加 `client_location` 统计维度到 rollup
   - [ ] Dashboard 添加"客户端地域"饼图
   - [ ] 内网显示 IP，外网显示地域

### P2 - 后续优化

3. **前端展示验证**
   - [ ] 验证会话详情页显示 `agent_name` 和 `client_ip`
   - [ ] 优化 Dashboard 客户端类型图例（中文翻译）
   - [ ] 添加客户端类型的趋势图

---

## 六、测试计划

### 6.1 单元测试

- [ ] `telemetry.ExtractClientIPTrusted` 边界测试
- [ ] `telemetry.DetectAgentFromSystemPrompt` 模式匹配测试
- [ ] `shouldOverrideAgentName` 逻辑测试

### 6.2 集成测试

- [ ] 发送不同 User-Agent 的请求，验证 `agent_name` 正确识别
- [ ] 发送包含系统提示词的请求，验证兜底机制生效
- [ ] 验证不同来源 IP 的地域解析正确

### 6.3 Dashboard 验证

- [ ] 查询 `/api/admin/dashboard/board?days=1`
- [ ] 验证 `pies.clients` 显示真实客户端类型
- [ ] 验证 `pies.virtual_ips` 或新的 `pies.client_locations` 显示地域

---

## 七、总结

### ✅ 已正确实现

1. 客户端类型三层识别机制（HTTP 头 → 系统提示词 → 智能兜底）
2. 客户端 IP 信任列表提取（防伪造）
3. 扩展属性存储到 `request_context_attrs` 表
4. 请求日志主表 `agent_name` 和 `client_ip` 字段

### ❌ 需要修复

1. **Dashboard 客户端类型统计使用错误字段**（P0，影响核心功能）
2. **缺少地域信息提取与存储**（P1，需求明确要求）
3. **Dashboard 缺少真实 IP/地域统计维度**（P1）

### 📋 下一步行动

1. 立即修复 `bg/stats_minute_rollup.go` 和 `admin/dashboard_board_queries.go` 的字段映射
2. 实现 GeoIP 地域解析
3. 添加地域统计维度
4. 验证并提交代码
