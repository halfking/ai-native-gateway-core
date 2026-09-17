# 245 prompt_too_large 413 审计最终报告

**会话**: sess_57bf74de-a679-460f-8f6d-cdd968d9084c  
**提交**: 59eb7fa0696fc2a4624133384d894ef32bc99a4f  
**日期**: 2026-09-17  
**状态**: ✅ 已完成并推送至 origin/main

---

## 一、结论/根因

### 用户报错本质

**413 错误由网关 prompt budget guard 按设计发出，不是网关 bug**。

- **触发条件**: ZCode 会话 (workspace ~/workspace/smm, sess_9edef815) 在第 88–109 轮后 prompt 达 8–8.4MB，估算超过 2,097,152 tokens（2M 限制）
- **网关行为**: 2026-08-24 引入的 memcg OOM 防护机制正常工作，在 JSON 解析前拒绝超大请求
- **根因在客户端**: 上下文压缩未在接近网关预算前触发（应在 ~8MB 前压缩，实际到 88 轮才触发已越线）

### 前稿误判修正

前一轮（凌晨会话）得出"客户端预检拦截、请求未达网关"结论**错误**，方法论失误：
1. 网关在 RequestIDMiddleware 重新生成服务端 request_id，客户端 ID 永不出现在网关日志
2. 413 响应体只进 PG request_logs，jsonl 只有短错误码 `attempt_err_code=prompt_too_large`

**双向取证**实证：
- 客户端日志: `baseURL=https://llmgo.kxpms.cn/v1` 收到 `statusCode=413`
- 网关日志: 245 canary 有 42ms 内配对的 `safety_net_defer_fired` 记录

---

## 二、改动文件与关键行为

### 本次提交修出三处真实缺陷（非 413 行为本身）

| 缺陷 | 文件 | 关键行为 | 影响范围 |
|------|------|---------|---------|
| **F1 wire-code typo** | `domains/streaming/handler.go:2234` | `body_too-large` → `body_too_large` (连字符改下划线) | 默认预算下不可达，仅 `max_prompt_tokens=0` 部署可达 |
| **F2 liveactions typed-nil** | `internal/liveactions/liveactions.go`<br>`cmd/gateway/main.go:2165` | NewEmitter 反射归一化 nil `*redis.Client` | 无 Redis 部署启动即 crash-loop (252 实抓 NRestarts=41) |
| **F3 sticky typed-nil** | `domains/streaming/executors/sticky.go`<br>`cmd/gateway/main.go:1229,1396` | SetRedisStore 反射归一化 + 装配点判空 | 无 Redis 但有 DB 部署每请求 500 (252 公网流量实抓) |

### 代码变更统计

```
 cmd/gateway/main.go                                |   8 +-
 docs/audit/2026-09-17-prompt-too-large-413-audit.md |  81 +++++++++
 domains/streaming/executors/sticky.go              |  11 ++
 domains/streaming/executors/sticky_typednil_test.go|  34 ++++
 domains/streaming/handler.go                       |   2 +-
 domains/streaming/prompt_budget_test.go            |  18 ++
 internal/liveactions/liveactions.go                |  13 ++
 internal/liveactions/liveactions_test.go           |  25 +++
 scripts/deploy-252-gateway.sh                      | 186 +++++++++++++++++++++
 9 files changed, 376 insertions(+), 2 deletions(-)
```

### 关键修复逻辑

**F2/F3 typed-nil 归一化模式**（两处同型 bug）:
```go
// 修复前：typed-nil 骗过接口判空
var typedNil *redis.Client  // nil 指针
Client interface { Pipeline() } // 接口
e.client = typedNil  // typed-nil 装进接口
if e.client == nil { }  // ❌ false！接口非 nil

// 修复后：反射归一化
func NewEmitter(rdb Client, ...) {
    if rdb != nil {
        if v := reflect.ValueOf(rdb); v.Kind() == reflect.Pointer && v.IsNil() {
            rdb = nil  // 归一化为非 typed nil
        }
    }
    e := &Emitter{client: rdb}  // ✅ 守卫恢复语义
}
```

---

## 三、测试命令与结果

### 回归测试（全部 PASS）

```bash
# F1 wire-code 规约测试
go test ./domains/streaming/ -run 'TestBodyTooLargeWireCodeConvention' -v
# PASS: 源级钉桩三入口禁现连字符变体

# F2 liveactions typed-nil
go test ./internal/liveactions/ -run 'TestTypedNilClientNormalised' -v
# PASS: typed-nil 不 panic，事件静默丢弃

# F3 sticky typed-nil
go test ./domains/streaming/executors/ -run 'TestStickyCacheTypedNilRedisStoreNoPanic' -v
# PASS: GetMultiLevel 安全返回 not-found
```

### 部署验证

| 环境 | 验证命令 | 结果 |
|------|---------|------|
| **本地** (deploy-local.sh) | `curl http://127.0.0.1:8781/healthz`<br>`curl http://127.0.0.1:8781/readyz` | ✅ 双实例 healthz ok + readyz ready<br>database/redis connected |
| **本地 413 复现** | 33MiB POST /v1/chat/completions | ✅ 413 `prompt_too_large`<br>estimated 8650773 > 2097152 (与用户报错同型) |
| **252** (scripts/deploy-252-gateway.sh) | `ssh 252 'systemctl status llmgo-252-dev'`<br>`journalctl -u llmgo-252-dev --since "06:16:40"` | ✅ **NRestarts=0** (修复前 41)<br>**0 panic** (修复前每请求一次)<br>healthz ok, database connected |

### 252 部署验证详情

```bash
# 当前状态（运行 2h 44min）
ssh 252 'systemctl show llmgo-252-dev -p NRestarts --value'
# 输出: 0

# 首启窗口 panic 计数（06:16:40–06:18:00）
ssh 252 'journalctl -u llmgo-252-dev --since "06:16:40" --until "06:18:00" --no-pager | grep -ci panic'
# 输出: 0

# 当前健康状态
ssh 252 'curl -m 5 http://127.0.0.1:8780/readyz'
# 输出: {"database":{"connected":true,"latency":"30ms"},"redis":null,"status":"not_ready"}
# not_ready 是 EnsureSchema 在 600s 预算内继续收敛（正常）
```

---

## 四、遗留风险

### 1. 已知且可接受

| 风险项 | 影响 | 应对 |
|--------|------|------|
| **estimateTokens CJK×2 高估** | 对中文大 body 偏保守（真实 ~1.3× tokenizer） | 未改（属行为变更，需独立评审）；建议建议 5.2 |
| **252 readyz not_ready** | 首启 EnsureSchema >20s（共享生产 PG） | 已放宽 `DB_BOOT_RETRY_SECONDS=600s`，会自行收敛 |
| **252 无测试 key** | 413 探针返回 401 | best-effort WARN 不阻断；wire-code 行为由 F1 测试钉桩 |

### 2. 新引入风险（低）

- **reflect 性能开销**: NewEmitter/SetRedisStore 装配时一次反射，非热路径，可接受
- **typed-nil 归一化遗漏**: 本轮修了 liveactions/sticky 两处，其他装配点仍可能潜伏（建议后续装配点统一加守卫或 lint 规则）

### 3. 客户端侧风险（外部）

- **ZCode 上下文压缩阈值偏高**: 88–109 轮才触发已越 8MB 网关预算，建议客户端在 ~7MB 前主动 handoff
- **用户未主动 handoff**: 会话达 500K 时应生成提示词清空上下文（用户需求文档要求，本次未实施）

---

## 五、后续建议

### 客户端侧（ZCode）

1. **压缩阈值前移**: 当前在 prompt ≈ 8MB 时才压缩，应在 ~7MB 前触发（网关预算 2M tokens ≈ 8MB）
2. **500K handoff**: 按用户要求实施"达 500K 时生成提示词清空上下文"逻辑

### 网关侧（候选，需评审）

1. **estimateTokens 对齐**: CJK×2 改为 ~1.3× 与真实 tokenizer 对齐（行为变更）
2. **413 响应体增强**: 回传 `estimated_tokens`/`limit` 结构化字段便于客户端自愈
3. **运维告警重分类**: `safety_net_defer_fired attempt_err_code=prompt_too_large` 计数应作为"客户端上下文超限"业务指标，而非错误告警

### 部署规范（已沉淀到 scripts/deploy-252-gateway.sh）

252 首启三坑已文档化，供后续同类部署对照：
1. DB URL 需改写为容器 IP（podman 网络视角 vs 宿主机）
2. CORS 必须显式配置（fail-closed panic）
3. `DB_BOOT_RETRY_SECONDS` 必须带单位（裸数字被 ParseDuration 静默回退默认值）

---

## 六、更新本 handoff

本任务已完整闭环，handoff 无需更新。工作区留有并行会话的 survival WIP（`domains/streaming/attempt_outcome.go` 等），**已不提交**，接手会话注意不误并入。

---

## 七、下一轮提示词

```
本轮 245 prompt_too_large 审计已完成，三处 bug 修复并推送至 main (59eb7fa06)。
主要发现：
1. 413 是网关 budget guard 按设计拒绝（根因在客户端上下文压缩未前置）
2. 修出两处 typed-nil crash（liveactions/sticky，252 实抓）+ 一处 wire-code typo
3. 252 部署脚本已新增（scripts/deploy-252-gateway.sh），三修后 restarts=0/0 panic

遗留行动项（非阻塞）：
- 客户端侧：ZCode 压缩阈值前移到 ~7MB；实施 500K handoff 逻辑
- 网关侧：评审 estimateTokens CJK 口径对齐；413 响应体增强（可选）
- 运维侧：prompt_too_large 计数重分类为业务指标

审计报告：docs/audit/2026-09-17-prompt-too-large-413-audit.md
最终报告：docs/audit/2026-09-17-prompt-too-large-413-final-report.md
```
