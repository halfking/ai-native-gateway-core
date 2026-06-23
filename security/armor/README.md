# security/armor — 设计意图（3 行话）

> 配套：[`docs/产品方案/2026-06-23-llmgw-domain-architecture-refactor.md`](../../../../docs/产品方案/2026-06-23-llmgw-domain-architecture-refactor.md) §2.2 ⑧ security/

## 做什么

- **LLM-as-judge 抽象**：用一个 cheap 模型给 prompt 打分（0-1），返回 `Decision{Safe,Warn,Block}`
- **Policy 加载**：per-tenant 配置（存 `settings_kv`），默认全关
- **v1 hard rule**：**始终 observe 模式**，即使 policy 写了 enforce 也强制降级为 warn（`Normalize()` 单一 chokepoint + `resolveDecision()` 双重保险 + `design_intent_test.go` 不变量守护）

## 不做什么（领域边界）

- 不做请求中继（归 `gateway/relay/`）
- 不做审计持久化（归 `observability/audit/`）
- 不做鉴权（归 `platform/auth/`）
- 不直接调真实 LLM（v1 用 mock + httptest；真实接入 Q4 B1）
- 不做 PII 脱敏（归 `security/sdp/`，待建）
- 不做 TLS 伪装（归 `security/disguise/`，Phase 2 迁移）

## 风险与缓解

| 风险 | 缓解 |
|------|------|
| LLM 返回非法 JSON | `parseJudgeJSON` 三层兜底（envelope / bare / malformed-error）+ 64KB cap |
| LLM 返回越界分数（1.5/-0.1） | `clampScore` 全局兜底到 [0,1] |
| API key 泄露到日志 | `httpJudge` 注释明确 "never logged" + `design_intent_test.go` grep 守护 |
| v1 误开 enforce 拦截生产流量 | `Normalize()` 单一 chokepoint 强制 observe + 不变量测试 |
| Judge 超时拖垮 relay | 默认 5s timeout + ctx 透传；v1 不集成到 relay 同步路径（Q4 B0-4 异步 worker） |

## 测试覆盖

- **39 个测试 PASS**，0 FAIL
- **覆盖率 91.8%**（> 80% 目标）
- `go vet` clean，`-race` clean
- 不变量测试（design_intent_test.go）：7 个守护 v1 hard rule

## 后续（Q4 B1 衔接）

- B0-3：`middleware/armor_mw.go` 把 Judge 接入 relay（仍 observe）
- B0-4：`bg/armor_judge.go` 异步批量推理（避免同步阻塞）
- B1-1：`armor_judgments` 表（migration 043）
- B1-2：`patterns.go` 30+ 攻击模式正则（中文双语）
- B1-3：双层判定（patterns → block / LLM judge → block）
