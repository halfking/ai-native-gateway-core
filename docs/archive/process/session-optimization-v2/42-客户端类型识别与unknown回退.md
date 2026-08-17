# 客户端类型识别与 unknown 回退

> **文档版本**: v1.0
> **创建日期**: 2026-07-27
> **适用范围**: Token 资源管理方案中"clientType"字段的识别与兜底
> **关联文档**: [41-Token资源管理体系说明.md](./41-Token资源管理体系说明.md)

## 1. 设计原则：隐式枚举

### 1.1 为什么选择"隐式枚举"

候选方案对比：

| 方案 | 优点 | 缺点 |
|------|------|------|
| 代码内枚举常量 + 热加载 | 集中 | 需 RWMutex 保护热重载 |
| DB / settings 表存储 | 运维可加新类型 | 增加查表热点 |
| **隐式枚举（采纳）** | 零运维成本 | 新客户端需发版 |

我们选择"不被代码识别的 clientType 一律归类为 unknown"，原因：

1. **客户端类型相对稳定**：cursor / claude-code / opencode 等不会频繁新增
2. **新增客户端需要重新评估**：不仅是识别，还需要同步调整客户端 token 拼接语义、配额策略
3. **未知即统一**：所有未识别客户端汇总到 `unknown`，避免出现"未识别客户端挤占专属槽位"问题

### 1.2 隐式枚举的边界

代码识别清单（截至 2026-07-27）：

```yaml
# domains/streaming/client_fingerprint.go:extractClientType
识别关键词:
  X-Gw-Client-Type 头（最高优先级）
  User-Agent 关键词:
    - "cursor/" 或 "cursor-"        → cursor
    - "claude-code/" 或 "claude-code-" → claude-code
    - "opencode/" 或 "opencode-"    → opencode
    - "zcode/" 或 "zcode-"          → zcode
    - "codex/" 或 "codex-"          → codex
    - "roocode/" 或 "roo-code/"     → roocode
    - "vscode/" 或 "visual-studio-code/" → vscode
    - "github-copilot/" 或 "copilot/" → copilot
    - "windsurf/"                   → windsurf
    - "zed/"                        → zed
    - "jetbrains/" 或 "intellij/" 或 "pycharm/" 或 "webstorm/" → jetbrains

未匹配 → 空字符串 → 兜底为 "unknown"
```

---

## 2. 三层识别优先级

```
Priority 1 (最高)：X-Gw-Client-Type HTTP 头
   ↓ 空
Priority 2：User-Agent 关键词匹配
   ↓ 不匹配
Priority 3：系统提示词语义匹配（仅 extractClientTypeWithPrompt，executor.go 不使用）
   ↓ 不匹配
Priority 4：兜底为空字符串
   ↓ 调用方
最终兜底："unknown"
```

### 2.1 为什么 executor 不使用 Priority 3

`executor.go:extractClientType` 是精简版，**仅覆盖 header-only 路径**：

- **原因**：系统提示词语义匹配需要 `telemetry.DetectAgentFromSystemPrompt`，会引入对 `streaming` 包的依赖。executor 包历史上不依赖父包
- **影响**：executor 路径的 clientType 比 streaming 路径稍弱（少了系统提示词补全）。但 FpSlot holder 拼接路径不需要那么精确，未识别 = `unknown`，业务无损

### 2.2 两份 extractClientType 的同步

| 文件 | 路径 | 用途 |
|------|------|------|
| `domains/streaming/client_fingerprint.go` | 完整版（含 Prompt 兜底） | 路由评分、auto_route 决策 |
| `domains/streaming/executors/executor.go` | 精简版（header-only） | FpSlot holder 拼接 |

**维护规则**：新增客户端类型时，必须同时更新两份。`extractClientTypeWithPrompt` 的语义补全逻辑可在 executor 路径上"漏识别" — 接受这个权衡，因为：
1. 漏识别的成本只是归类到 `unknown`，不会阻塞路由
2. 用户可以通过 `X-Gw-Client-Type` 头主动修正

---

## 3. unknown 回退的具体规则

### 3.1 何时进入"unknown"

| 输入 | 结果 |
|------|------|
| 没有 X-Gw-Client-Type 头 | 看 User-Agent |
| User-Agent 也不匹配任何关键词 | `""` |
| `""` 被 `clientTokenOf` 兜底 | `"unknown"` |
| 整体 holder 变为 | `"{userKey}\|unknown"` |

### 3.2 unknown 的资源行为

- **独立槽位**：`alice|unknown` 与 `alice|cursor` 各占独立 slot
- **汇总统计**：所有未识别客户端共享 `unknown` 槽位池
- **监控可见**：监控 `unknown` 占比，可以发现客户端 UA 变化或新客户端出现

### 3.3 主动修正建议

对于希望精确识别的客户端：
- 客户端开发者主动加 `X-Gw-Client-Type: my-client` HTTP 头
- 网关运维发现 `unknown` 占比超过 30% → 排查客户端 UA 是否变化

---

## 4. 已知边界情况

### 4.1 User-Agent 子串冲突

例如 `CursorDesktop-Plus/1.0` 包含 `cursor` 但同时包含 `-Plus`，目前的关键词匹配会先命中 `cursor`，视为 cursor。这是预期行为。

### 4.2 同名不同版本

`Cursor/1.5.0` 与 `Cursor/2.0.0` 都被识别为 `cursor`，共享同一 token。这是有意的：版本是同一客户端。

### 4.3 SDK 调用无 UA

通过 OpenAI SDK 发起请求时 UA 通常是 `OpenAI-Python/1.0` 之类，不会被识别为 cursor/claude-code，归类为 `unknown`。客户端 token 在这种场景下粒度退化到 userKey 单维度。

### 4.4 多客户端共用同一 userKey

如果同一用户在不同设备/不同 IDE 同时使用 cursor 和 claude-code，两边得到不同 token，相互不挤占。这是**设计意图**。

---

## 5. 监控与运维

### 5.1 推荐监控指标

```yaml
gateway_client_token_unknown_ratio:
  type: gauge
  description: "未识别 clientType 在所有请求中的占比"
  alert: > 0.3 持续 5min
```

### 5.2 排查清单

| 现象 | 排查方向 |
|------|---------|
| 某凭证 fp_slot 经常饱和 | 查 `gateway_client_token_active_slots{client_type=...}`，定位是哪种客户端抢占 |
| 用户报"切换客户端后丢失会话" | 这是预期行为（不同 token = 不同 slot = 不同虚拟身份），属设计意图 |
| `unknown` 占比 > 50% | 客户端未正确发送 UA / 网关识别规则过时 |

---

## 6. 未来扩展

### 6.1 当客户端类型需要可配置时

如果未来需要让运维动态添加客户端类型（不需发版），可演进为：

```yaml
方案 A：DB 表
  gateway.client_types:
    - name: "cursor"
      display_name: "Cursor IDE"
      max_fp_slots: 20
      keywords: ["cursor/", "cursor-"]

方案 B：settings 表
  settings["client_types.cursor"] = JSON{...}
```

这两种方案会在后续 Phase（待立项）实施。本轮采用"代码内枚举 + unknown 兜底"是足够简单且可维护的方案。

### 6.2 与客户端画像系统的关系

`domains/clientprofile` 包维护客户端画像（device seed、machine id、runtime name 等）。这些指纹目前用于 EgressIdentity 派生（虚拟 IP/MAC），不进入 FpSlot holder 拼接路径。

未来可以让 `extractClientType` 在 X-Gw-Client-Type 与 UA 都不命中时，查询 clientprofile 缓存补全。这属于优化项，不在本轮范围。

---

**最后更新**: 2026-07-27
**下次审查**: Phase 4 启动时