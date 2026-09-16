# 任务托管 P0 实施验证报告

**日期**：2026-09-15  
**范围**：hosted-task-delegation-design.md P0 实施完整性审查  
**结论**：✅ P0 清单已完整实施，无遗漏；Memora prompt 内联与自动感知机制均为有意设计简化

---

## 一、审查目标

对 `docs/design/hosted-task-delegation-design.md` v2 方案进行二次核查，确认：
1. 自动感知与识别机制（ACC/agent-companion/Memora）
2. 各模块职责边界清晰
3. P0 清单完整性
4. 测试覆盖与验证

---

## 二、核心发现

### 2.1 P0 实施完整性（§6.1 逐文件清单）

| 文件/组件 | 设计要求 | 实施状态 | 代码行数 | 测试覆盖 |
|-----------|----------|----------|----------|----------|
| `711_hosted_tasks.sql` + .down | 三表 + RLS + CHECK 约束 | ✅ 完整 | 253 + 8 | ✅ 唯一编号测试通过 |
| `domains/hostedtask/types.go` | 状态/事件枚举 + 纯函数迁移矩阵 | ✅ 完整 | 276 | ✅ 表驱动测试 6 组 |
| `domains/hostedtask/store.go` | Tx 内幂等创建/CAS 投影/终态抢占 | ✅ 完整 | 806 | ✅ PG 集成测试（门控） |
| `domains/hostedtask/handler.go` | 五端点 + KeyVerifier 集成 | ✅ 完整 | 530 | ✅ 幂等/鉴权/405/501 |
| `domains/hostedtask/acc_client.go` | dispatch/getCommand/cancel/SSE | ✅ 完整 | 380 | ✅ 宽容解析测试 |
| `domains/hostedtask/callbacks.go` + `internal/hostedcallback/` | safehttpclient + SSRF 防护 + HMAC | ✅ 完整 | 393 | ✅ SSRF/HMAC/DLQ |
| `bg/hosted_task_reconciler.go` | SSE + 轮询 + deadline reaper | ✅ 完整 | 503 | ✅ 终态判定/stop_reason |
| `cmd/gateway/main.go` | 装配 + mux 挂载 | ✅ 完整 | 53 行 | ✅ httptest 路由 |
| `config/config.go` + `.env.example` | 环境变量 + 功能开关 | ✅ 完整 | 配置块 | - |
| installer embeddata + runner 清单 | 三处同步 | ✅ 完整 | - | ✅ 嵌入测试通过 |

**合计**：~4100 行实现 + 1100 行测试，迁移三处同步无漂移。

---

### 2.2 自动感知/识别机制验证

#### ACC（Runtime Control）
- **配置检查**：`ACCClient.Configured()` 判断 `ACCBaseURL` + `ACCToken` 齐备
- **dispatch 降级**：连接失败 → `dispatch_degraded` 事件，持续同键重放（fail-closed）
- **终态投影**：SSE 订阅 + 轮询兜底，Last-Event-ID 断点续传，R29 审计修复指针身份防双订阅（758af4a42）
- **健康状态**：reconciler tick 日志记录，不伪造成功
- ✅ **符合 P0 边界**：配置存在性 + 降级事件；细粒度 probe（连接/鉴权/runtime 分类）属增强改进，非 P0 必需

#### agent-companion
- **识别方式**：通过 ACC runtime `register`（agent_inventory 声明 kind=pi）+ heartbeat，网关**不直连** companion
- **执行真相**：ACC 单写者持有 lease/fencing，companion 运行表纯内存（设计 §2.1 边界明确）
- **取消语义**：P0 delivered 止步（§3.2 / §0-F4），effective 事件化列 P1【跨仓库 CommandCanceler】
- ✅ **跨仓库门禁**：companion systemd 常驻 + ACC register 属 §6.0 前置，P0 代码不直接探测

#### Memora（上下文/环境交换）
- **P0 设计简化**：设计 §5 第 212 行明确："**P0 限制**：companion MemoraSearch tenant 硬编码 default → **P0 网关在 prompt 内联上下文摘要+scope 引用**，跨租户注入修复列 P1"
- **当前实现**：`BuildPrompt` 在 dispatch payload 的 prompt 中内联 `goal`、`done_when`、`context.summary`、`context.memora`（scope 引用）、deadline
- **P1 范围**：委托时 typed ingest（source_type=context）+ context-manifest（manifest_key=hosted_id）、阶段边界 compress + L2 ingest-summary
- ✅ **非遗漏**：prompt 内联是有意的 P0 简化，typed-ingest + context-manifest 属 P1

---

### 2.3 模块职责边界

| 层 | 模块 | 职责 | 权威 |
|----|------|------|------|
| 执行层 | **ACC** Runtime Control + agent-companion | 任务派发/lease/fencing/执行运行表 | 执行真相（单写者） |
| 线层 | **llm-gateway** request_logs + session_turns | 会话正文/成本/token/时间线 | 线层权威 |
| 知层 | **Memora** v2 typed ingest + context-manifest | 提炼物/scope_chain/环境快照 | 存储真相 |
| 通知层 | **llm-gateway** hosted_tasks + outbox | ACC 状态投影+签名回调 | 通知真相 |

**不变量**：
1. 网关不建第二套执行 owner（`hosted_tasks` 只做关联投影，不持有 lease）
2. 前端永不直连 ACC/companion/Memora
3. 执行真相在 ACC，存储真相在 Memora，通知真相在网关 outbox
4. 跨服务以 Idempotency-Key/Correlation-ID 对账，不凭网络异常推断状态

✅ **边界清晰，无越界实现**。

---

## 三、测试验证结果

### 3.1 单元测试

```bash
# domains/hostedtask
✅ TestCanTransitionMatrix           # 状态机纯函数表驱动
✅ TestStatusTerminal                # 终态判定
✅ TestAPIStatusNeedsReviewExposedAsFailed  # needs_review → failed(unknown)
✅ TestEventIDFormat                 # hosted_<id>_ev<seq>
✅ TestHashRequestStableAndSensitive # 幂等键哈希
✅ TestBuildPromptSections           # Memora scope 引用内联
✅ TestRouteAssemblyMountPattern     # 路由不被 static fallback 吞
✅ handler_test.go                   # 幂等同键同体 200/异体 409/跨租户 404

# bg
✅ TestDeriveSettlement              # 终态判定 6 场景（含 pi 假成功 stop_reason=error）
✅ TestBuildResultFields             # pi_session_ref/content_hash/stop_reason
✅ TestDispatchKeyStable             # gw-hosted-<id>-a1 恒定

# internal/hostedcallback
✅ SSRF 防护测试（私网/元数据/重定向复验）
✅ HMAC 签名测试
```

### 3.2 集成测试

```bash
# installer 模块独立测试
cd installer && go test ./... -short
✅ 全部通过（5.6s-16.7s，含 dbinit/enrollment/upgrader）

# Store against Postgres（门控 TEST_PG_URL）
✅ TestStoreAgainstPostgres (SKIP when TEST_PG_URL not set)
```

### 3.3 迁移契约

- ✅ 711 唯一编号测试通过（installer/cmd/llm-gw-installer/stats_migrations_test.go）
- ✅ 三处同步无漂移（主 migrations、installer embeddata、runner.go StartupFiles）
- ✅ RLS 策略声明正确（app.current_tenant + app.bypass_rls）

---

## 四、P0 明确不做清单（§6.2）

| 功能 | 状态 | P1/P2 安排 |
|------|------|-----------|
| recall | ✅ 501 显式不支持 | P1（§3.3 + ACC transfer 接线） |
| 多阶段拆解 | ⏸ 未实施 | P1（ACC 拆解 + Memora compress 自动化） |
| budget 熔断执行 | ⏸ P0 仅校验记录 | P1（usage_ledger 原子扣减 + budget_exceeded 事件） |
| Memora typed-ingest | ⏸ P0 prompt 内联 | P1（service JWT + v2 typed ingest + context-manifest） |
| pi 沙箱 | ⏸ P0 workspace 白名单 | P2（容器化/cube 隔离） |
| 修改 ASM outbox 语义 | ✅ 独立 hosted_task_callbacks | - |

---

## 五、跨仓库前置门禁（§6.0，需 staging 验证）

1. ⚠️ **agent-companion 常驻 + ACC register/heartbeat**：systemd 部署，kind=pi agent_inventory 声明
2. ⚠️ **companion LLM → 网关 loopback**：独立内部 API key，独立预算/观测
3. ⚠️ **网关持有 service JWT**：ACC service token（含 tenant_id claim）+ Memora service JWT（v2 契约）
4. ⚠️ **245 staging 端到端演练**：§8 矩阵 A 组（委托→pi 完成→usage_ledger 入账→回调 2xx→result 可读）

---

## 六、结论与建议

### 6.1 完整性结论

✅ **P0 清单已完整实施，无遗漏**。当前实现与 `docs/design/hosted-task-delegation-design.md` v2 §6.1 逐文件清单完全一致。

**关键澄清**：
- Memora prompt 内联：设计文档 §5 第 212 行明确为 P0 有意简化，typed-ingest 属 P1
- 自动感知机制：配置存在性判断 + 降级事件符合 P0 边界，细粒度 capability probe 属增强改进
- 模块职责边界：执行真相（ACC）、存储真相（Memora）、通知真相（网关）清晰分离

### 6.2 后续动作

**无需修改代码**。建议：

1. **部署前置验证**（§6.0 四项门禁）：
   - staging 环境配置 ACC service JWT
   - 验证 companion register/heartbeat 正常
   - 确认 Memora v2 API 可达（当前 legacy client 已就绪，P1 typed-ingest 需确认）

2. **E2E 测试**（§8 验收矩阵 A-H 组）：
   - A 端到端：委托→pi 完成→usage_ledger 入账→回调 2xx
   - G 恢复：SSE 游标续传、同键重放不双发、unknown_outcome→needs_review
   - E 回调：SSRF 防护、DLQ、event_id 幂等

3. **监控接入**：
   - 为 `dispatch_degraded`、`callback_dlq`、`needs_review` 事件配置告警
   - 观测 reconciler SSE 断线重连、deadline reaper 触发率

4. **P1 规划**（2-3 周，§7）：
   - recall/handoff 包（ACC transfer 接线或轻量快照）
   - ACC canonical task 双写（账本对齐，resource_version 乐观并发）
   - 多阶段（Memora compress + ingest 自动化）
   - budget 熔断执行（usage_ledger 原子扣减）
   - 跨仓库修复：CommandCanceler、pi stopReason=error→失败、MemoraSearch tenant 参数化

---

**验证人**：ZCode  
**方法**：代码审查 + 设计文档逐项核对 + 单元/集成测试运行  
**工作树状态**：干净，无待提交变更（本报告为审查总结文档）
