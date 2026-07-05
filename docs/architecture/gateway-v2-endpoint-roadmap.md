# cmd/gateway-v2 端点补全路线图

> **状态**: 进行中
> **当前进度**: 2/24 端点 (8.3%)
> **目标**: 24/24 端点 (100% OpenAI + Anthropic + Admin 兼容)
> **预计工时**: 1-2 周

## 背景

`cmd/gateway-v2/main.go` 是新的 Pipeline 架构入口（端口 __PORT_4__），独立运行 5+ 小时。当前是**演示级**实现，只有 2 个端点。生产入口 `cmd/gateway/main.go` 有 24 个端点，gateway-v2 距离生产可用还有 22 个端点待补全。

## 当前端点

| 端点 | 状态 | 备注 |
|------|------|------|
| `/healthz` | ✅ 已实现 | 200 OK, 1.5ms |
| `/v1/chat` | ⚠️ 演示级 | 只返回 `{"status":"ok"}`，未真正调用 LLM |
| **`/v1/models`** | ✅ **2026-06-26 新增** | **OpenAI 兼容，返回 3 个模型** |

## 缺失端点（22 个）

### P0 - 核心 LLM 端点（4 个）

| 端点 | 兼容性 | 难度 | 优先级 |
|------|--------|------|--------|
| `/v1/chat/completions` | OpenAI | 高 | P0 |
| `/v1/messages` | Anthropic | 高 | P0 |
| `/v1/responses` | OpenAI Responses | 高 | P0 |
| `/v1/completions` | OpenAI Legacy | 中 | P0 |

**P0 端点实现要点**:
- 集成 `domains/streaming/executors` 真实调用链
- 实现 SSE 流式响应
- 集成 `domains/credential` 凭据选择
- 集成 `domains/agent-ecosystem` 智能体发现
- 错误处理：401/429/500 → OpenAI 兼容错误码

### P1 - 管理端点（15 个）

| 端点 | 用途 |
|------|------|
| `/api/admin/policies/*` | 策略 CRUD (4 个) |
| `/api/admin/tools/*` | 工具注册 (5 个) |
| `/api/admin/credential-success-rates/*` | 凭据监控 (2 个) |
| `/api/admin/sessions/*` | 会话管理 (3 个) |
| `/api/meta-tools/*` | Meta-tool 加载 (3 个) |
| `/api/agents/*` | 智能体管理 (2 个) |

**P1 端点实现要点**:
- 复用 `admin/` 包（已实现）
- Wrap 成 HTTP handler
- 加认证（auth_disabled 时跳过）

### P2 - 监控端点（3 个）

| 端点 | 用途 |
|------|------|
| `/metrics` | Prometheus 导出 |
| `/admin/config/reload` | 热重载配置 |
| `/api/admin/session-handoff` | 会话交接 |

## 实施计划

### Phase A: P0 核心端点（3-5 天）

1. **/v1/chat/completions**（2 天）
   - 复用 `domains/streaming/handler.go:ChatHandler`
   - 集成 `domains/credential.Selector`
   - 实现真实 OpenAI 协议
   - 单元测试 + 集成测试

2. **/v1/messages**（1 天）
   - 复用 `domains/transformation/anthropic`
   - 协议转换：Anthropic → 内部 IR → OpenAI
   - 响应：OpenAI → Anthropic

3. **/v1/responses**（1 天）
   - 复用 `domains/streaming/responses.go`（已实现）
   - 适配 gateway-v2 Pipeline

4. **/v1/completions**（0.5 天）
   - 复用 chat/completions 逻辑
   - 旧版 OpenAI 兼容

### Phase B: P1 管理端点（2-3 天）

逐个 wrap admin/* handler，1-2 小时/端点

### Phase C: P2 监控端点（1 天）

复用 `domains/hooks/observability` 的 metrics 导出

## 风险评估

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| ChatHandler 集成复杂度 | 中 | 高 | 先做 PoC，验证 Pipeline 集成方式 |
| 协议转换错误 | 中 | 高 | 复用 `domains/transformation` 已有逻辑 |
| 凭据选择逻辑不一致 | 低 | 高 | 与 main.go 行为一致 |
| 流式响应丢失 | 中 | 中 | 完整 SSE 集成测试 |

## 验证策略

每个端点完成后必须：
- 单元测试（handler 行为）
- 集成测试（Pipeline 联动）
- E2E 测试（实际请求 → 响应）
- 与 cmd/gateway/main.go 行为对比

## 完成定义 (DoD)

- [ ] 24/24 端点全部实现
- [ ] OpenAI 兼容：与官方 OpenAI Python SDK 完整互通
- [ ] Anthropic 兼容：与官方 Anthropic SDK 完整互通
- [ ] 所有 E2E 测试通过
- [ ] 性能基线：P99 < 500ms
- [ ] 文档：API 文档 + 集成指南

## 决策点

是否切生产入口到 gateway-v2 取决于：
- 24/24 端点全部完成
- 跑过 2 周灰度（10% → 50% → 100% 流量切流）
- 错误率 < 0.1%
- P99 延迟 < 旧版 + 10ms

## 关联文件

- `cmd/gateway-v2/main.go` - 主入口
- `cmd/gateway-v2/e2e_test.go` - E2E 测试
- `cmd/gateway-v2/Dockerfile` - Docker 镜像
- `docker-compose.local-r112.yml` - 本地部署
- `docs/architecture/domain-refactoring-plan.md` - 架构设计
- `docs/architecture/legacy-replacement-audit-20260626.md` - 旧包审计

---

**Last updated**: 2026-06-26
**Next milestone**: 完成 /v1/chat/completions (Phase A step 1)
