# 20261001-auto-role-routing-mainstream-fallback-handoff.md

> 接力文档。上一轮：auto 路由「模型选择逐层回退」修复 + 批判式审计。
> 仓库：llm-gateway-go-5　分支：见文末「落地状态」

---

## 1. 结论 / 根因

### 用户诉求
「在 auto 进行任务类型分析时，模型的选择需要逐层进行回退，当低价的模型不可用时，
也要选择当前的主流模型。」

### 定位到的真实缺陷（R48 role 路由）
`autoroute/role_llm_router.go` 的 `builtinRoleLLMPreference` 每个 TaskKind 只有
**2 个偏好且同池**：

```go
KindSearch:    {"minimax-m3", "glm-5.3-flash"}     // 都是轻量池
KindAnalysis:  {"glm-5.3", "deepseek-v4-pro"}      // 都是重量池
```

而 `SelectLLM` 的原契约（role_llm_router.go:13-14 原文）是：

> 全不存在则维持原候选（role 路由静默让位，**绝不因偏好模型不可用而失败**）

**根因：轻量池偏好整层不可用时，role 路由没有任何通往重量池的路径** ——
`promoteFirstPresent` 返回空串，决策权交回评分，审计标签不记 role_route。
低价模型可用性成了单点：轻量池 4 个模型任一整体掉线（欠费/下线/tier 滤除），
搜索/总结/git/运维类子代理请求就**静默**改用评分结果，运维从 `routing_source`
上看不出任何异常。

### 真库证据（本地 llm-gateway-pg，2026-10-01 实测）
| 核对项 | 结果 |
|---|---|
| `role_task_llm_mapping` 行数 | 48 行 / 24 组平台级，**与内存表逐条一致** |
| 生效路径 | **走 DB 行**（`SelectLLM` 优先读快照，内存表仅无行时兜底） |
| 5 个主流模型在 `models_canonical` | **9/9 精确命中**（含 4 个轻量池名字） |
| 主流层活凭证 | deepseek-v4-pro 21,045 / claude-opus-5 7,775 / glm-5.3 6,455 / gpt-5.6-sol 6,116 / grok-4.6 1,153 —— **全部在跑** |

结论：缺陷在生产是活的；主流层名单不是空转。

---

## 2. 改动文件与关键行为

### 新增能力：逐层回退
| 层 | 内容 | 何时胜出 |
|---|---|---|
| 第 1 层 `kind` | `SelectLLM` 的 kind 偏好（轻量池/重量池，DB 行优先） | 任一在候选池内 |
| 第 2 层 `mainstream` | `builtinMainstreamFallback`（重量池 5 个） | **整层缺席时** |
| — | 评分结果 | 两层皆缺席（= 旧行为） |

| 文件 | 改动 |
|---|---|
| `autoroute/role_llm_router.go` | 新增 `builtinMainstreamFallback` 重量池表 + 口径注释 |
| `autoroute/helpers.go` | 新增 `withMainstreamFallback`：**只追加不插队**、跨层去重、不改入参 |
| `autoroute/decision.go` | 新增 `resolveRolePrefs`（**V1/V2 共用单点**）、`rolePrefPlan.layerOf`、`layerName`、`roleMainstreamFallbackActive` |
| `autoroute/decision_v2.go` | 改走 `resolveRolePrefs`；记录命中层 |
| `autoroute/feature_flags.go` | `AutoRoleMainstreamFallback` 默认 **true**，env `AUTO_ROLE_ROUTING_MAINSTREAM_FALLBACK` |
| `autoroute/session_intent_cache.go` | `CachedIntent.RoleFallbackLayer`（随缓存跨轮复用） |
| `domains/streaming/auto_route.go` | `autoRouteDecision.RoleFallbackLayer`（omitempty）→ 落 `request_logs.auto_decision` |
| `envs.samples/02-gateway-features.env.sample` | 新 env 说明 |
| `sql/migrations/startup/730_session_role_hierarchy.sql` + `installer/.../embeddata/.../730_*.sql` | **纯注释**修订记录（两副本必须一致，有门） |

### 关键行为
- **父开关 `AUTO_ROLE_ROUTING_ENABLED` 仍默认 false**（R48 既有约定，未动）。
  修复在该开关打开前**不影响任何线上行为**。
- **tier 豁免副作用（刻意）**：`rolePrefs` 会作为 `pinned` 传入
  `ApplyTierPolicyWithWorkType`，兜底层因此对 role 路由请求**同样免疫 tier 硬过滤**。
  取舍理由：不豁免则运维一条 tier 配置就能把兜底层彻底否掉，修复名存实亡。
  需要严格 tier 排他的部署关掉本层。
- 主流层**刻意不播种进 `role_task_llm_mapping`**：播种 = 3 角色 × 9 kind × 5 模型
  = 135 行冗余，且让「删掉某行」这个唯一逃生口失效。

### 顺带修的既有缺陷（非本次引入）
**V1 缓存命中从不置 `CacheReused`**。V1/V2 在同一 commit `0662c2ba9`（2026-06-29）
分叉，V2 置了、V1 没置，V1 侧至今未补。后果不是「少个字段」：`CacheReused` 无
`omitempty`，经 `autoRouteDecision` 落进 `X-Gw-Auto-Decision` 与
`request_logs.auto_decision`，**每一次 V1 缓存命中都被记成 `cache_reused=false`**，
运维与 `cmd/autoroute-e2e-audit` 会得出「会话缓存从未命中」的相反结论。已补齐。

---

## 3. 测试命令与结果

```bash
go build ./...
go vet ./autoroute/ ./domains/streaming/ ./cmd/gateway/
go test ./autoroute/ ./domains/streaming/ ./cmd/gateway/ -count=1
go test ./admin/ ./bg/ ./domains/streaming/executors/ \
        ./domains/hooks/goal/ ./domains/hooks/handoff/ -count=1
cd installer && go test ./cmd/llm-gw-installer/ -count=1
```

**新增门 12 条**（`autoroute/decision_role_fallback_test.go` 11 条 +
`domains/streaming/auto_route_r52_layer_test.go` 1 条）。分两类：

- **鉴别门**（回退修复必红）：`...MainstreamFallbackWhenLightLayerAbsent`（V1 + V2）、
  `...MainstreamFallbackOnDBRowPath`。
- **护栏门**（防新缺陷，修复回退不会让它红）：`...MainstreamLayerDoesNotOutrankLightLayer`、
  `...MainstreamFallbackSurvivesSessionCache`、`TestDecisionToWire_...RoundTrip`。

**三次反控已跑**（每条都确认门承重，不是恒绿）：
| 变异 | 预期红 | 实际 |
|---|---|---|
| `resolveRolePrefs` 不追加兜底层 | 2 条鉴别门 | 红（`got kimi-k3`） |
| 兜底层插队到首位 | 护栏门 | 红（选了 `claude-opus-5`） |
| 删掉 `decisionToWire` 映射 | wire 门 | 红 |

### 改过的既有门（都不是改断言就完事）
| 门 | 处置 | 原因 |
|---|---|---|
| `TestDecide_RoleRouting_PreferenceFallback` | 显式钉 opt-out 口径 | **原默认无鉴别力**：池是 `{glm-5.3(90), deepseek-v4-flash(35)}`，glm-5.3 既是主流层首项又是天然 winner，promote 命中索引 0 空转——加不加兜底层都绿 |
| `TestDecideV2_RoleRoute_CreditStaysWithTier` | **改复现条件**（池头名换 `qwen3-max`） | 主流层经豁免复活 glm-5.3 会留在 postTierWinner 首位，promote 不再 i==0 空转，R49 P2 前提消失。**没有改断言** |
| `TestDecideV2_RoleWinner_HeadsTierFailoverChain` | 换模链加 `glm-5.3` | 主流层进了 role 块，与 winner 口径一致 |

---

## 4. 遗留风险

1. **父开关默认关，修复当前不生效。** 这是 R48 的既有灰度约定。若期望「现在就生效」
   需单独决策：开 `AUTO_ROLE_ROUTING_ENABLED`，或把兜底层提到不依赖 role 路由的路径。
   两者影响面差很多，本轮**没有替你选**。
2. **成本面变化**：轻量层整层掉线时，搜索/总结类请求会跳到重量模型。这是诉求本身要的行为，
   但它是**静默的升档**——`role_fallback_layer=mainstream` 是唯一观测点。
   建议接入监控：`auto_decision->>'role_fallback_layer' = 'mainstream'` 的比例骤升
   = 轻量池出事了。
3. **主流层名单是硬编码快照**（取自 730 种子与 2026-09-19 handoff 口径原文）。
   已在真库核对存在且有活凭证，但**新增/下线模型不会自动跟随**——这是 R48 就存在的
   快照债，本轮沿用未扩大。730 自己留的「canonical_name 实测核对」待办，
   本轮已代为核对 5/5。
4. **worker 会话撞车风险**：本地工作区当时被另一会话并行使用（见 §5）。

---

## 5. 落地状态（已完成）

- 修复提交：`7f24fb5af`
- 分支：`fix/auto-role-routing-mainstream-fallback`（已推送）
- 合并：`8bbc1ed5d` merge → `main`（已推送）
- ⚠️ **本轮开工时工作区分支是 `fix/xcode-sse-finish-reason-sensenova`**
  （含另一会话的 streaming 提交 e84d574d8 / e469c0234）。已确认**文件无重叠**，
  且为不干扰对方会话，改动是在独立 worktree 里从 `origin/main` 重建后合并的，
  **没有把那两个提交带进 main**。
- 合并期间 main 又前进到 `6b3d89c3c`（别人：sensitive 门改法），已确认与本修复
  14 个文件无重叠，合并后重跑三包全绿才推。

### ⚠️ main 上存在一处与本修复无关的既有红
`installer/cmd/llm-gw-installer` 的 `TestStartupFilesAreAllEmbedded` 在**干净的
`origin/main` 上就已经红**（非本修复引入，证据：干净 worktree 复跑同一失败集，
逐行 diff 只差耗时数字）：

```
StartupFiles entry "808_request_logs_default_partition.sql" is not provided by setupSQLDir
StartupFiles entry "809_instance_release_status_nullable_release_id.sql" is not provided by setupSQLDir
```

来源：`ca4c828f4`（2026-10-01，halfking，R44 收口）。`installer/internal/dbinit/runner.go`
的 apply 列表已列入 808/809，但 `go:embed` 变量与 `main.go` 的 `embeddedSQLFiles`
映射未补齐。

**本轮刻意未修**：它属于别人当天在做的迁移接线，合并进来会与其在途工作冲突。
需要单独一轮补（参照 807 的三处接线模式：embeddata 文件 + `go:embed` 变量 +
`embeddedSQLFiles` 映射）。


---

## 6. 下一轮提示词

> 接着 `docs/handoff/20261001-auto-role-routing-mainstream-fallback-handoff.md` 往下做。
> 先跑 `go test ./autoroute/ ./domains/streaming/ -count=1` 确认基线绿，再动下面任一条：
>
> 1. **（最该先做）接监控**：`request_logs.auto_decision->>'role_fallback_layer' = 'mainstream'`
>    的量。轻量池整层掉线时这个值会跳升，而现状**没有任何告警**——诉求本身
>    「低价不可用要退主流」在发生时是静默的。
> 2. **灰度决策**：`AUTO_ROLE_ROUTING_ENABLED` 要不要开、什么时候开。父开关关闭期间
>    整套 role 路由（含本次修复）都不生效，这是产品决定不是技术决定。
> 3. **名单漂移**：把 `builtinMainstreamFallback` / `builtinRoleLLMPreference` 改成从
>    `models_canonical` 动态解析（例如按 `standard_iq` 阈值取重量层），消除硬编码快照债。
>    注意这会改变 tier 豁免名单长度，进而影响 `withRoleFailoverHead` 的换模链顺序。
> 4. **门**：若要改 `TestDecideV2_RoleRoute_CreditStaysWithTier` 的池，**不要改成
>    主流层里的模型**——见 handoff §3，会让 R49 P2 复现条件失效。

---

## 7. R53 轮（2026-10-02）已落地

本节记录本轮实际完成的两件事，§6 的四条状态见 §7.3。

### 7.1 优先级 1：接监控（R53）

诉求「低价不可用退主流」在发生时是静默的——轻量池整层掉线时请求照常成功，
只是静默升档到重量模型。本轮把它变成可被告警的事件。

**关键前提（先于实现核实）**：`role_fallback_layer` 在父开关关闭时**恒为 NULL**。
`Decision.RoleFallbackLayer` 带 `omitempty`，flag-off 时 `rolePrefs` 为空、
字段根本不进序列化。所以一条纯 SQL 比例告警在 flag-off 部署上返回 NULL，
与「开关开着但从没回退过」在面板上**完全一样**。这是 handoff §6 第 1 条
没写出来的前提，也是本轮设计的出发点。

**因此没有只做 SQL 告警**，而是加了 Go 侧 counter：

| 指标 | 作用 |
|---|---|
| `llmgw_autoroute_role_fallback_layer_total{layer="kind"\|"mainstream"}` | 两个 label 值在构造时 `Add(0)` **预置** |
| `llmgw_autoroute_role_routing_active` | 0/1，区分「没回退」与「没开」 |

预置是关键：`CounterVec` 只有被 `WithLabelValues` 过才出现在 `/metrics`。
不预置则 flag-off 部署上 `increase(...[15m])` 返回**空向量**，告警拿到
`no data` 而非 `0`——而「no data」不会触发任何通知，会静默吞掉一次真实的
整层掉线。预置后读数为 0，告警语义正确。

**埋点 4 处**（V1/V2 × fresh/cache），全部在 `roleRoutingActive()` 门内，
所以「flag-off」与「介入但未命中」天然可分。缓存轮刻意埋点：缓存轮确实在用
重量模型服务该请求，排除它会低报真实成本；SQL 侧同一字段同一路径，不漏不多。

**告警 3 条**（`deploy/prometheus/rules/role-fallback-mainstream.yml`）：
| 告警 | severity | 回答的问题 |
|---|---|---|
| `AutoRouteRoleFallbackMainstreamSpike` | warning | 轻量池是不是整层掉线了 |
| `AutoRouteRoleRoutingDisabled` | info | 兜底机制到底装载了没有（灰度决策依据） |
| `AutoRouteRoleFallbackNeverExercised` | info | 兜底路径有没有被真实流量验证过 |

**✅ PromQL 语义已用 promtool 验证**（2026-10-02 补，见 `deploy/prometheus/rule_tests/`）。

原提交信息里写的「未经 promtool 验证」已过时。补验证时（prom/prometheus
官方镜像）做了一件本该一开始做的事：**先证伪自己的担心，再决定要不要改**。

四变体对照（输入带 `job`/`instance` 抓取标签，占比 83%、开关开）：

| 写法 | 实测 |
|---|---|
| 裸 `and` + 未聚合 gauge | **不触发** ← 唯一真正静默失效的形态 |
| 裸 `and` + `max()` 折叠 | 触发 |
| `and on()` + 未聚合 gauge | 触发 |
| `and on()` + `max()`（本轮采用） | 触发 |

结论修正了两处认知：
1. 危险形态是**「裸 and」与「未聚合 gauge」同时存在**，不是 `and on()` 单独
   的问题。`max()` 与 `and on()` **各自都能**独立解决——本轮同时采用是冗余
   但安全的，任何一个被"顺手简化"掉仍然正确。
2. 最初提交信息与代码注释把功劳全记在 `and on()` 上，**这是不准确的**，
   已订正（yml 注释 + Go 门注释）。留着错误注释比没有注释更危险：它会让
   下一个人以为 `max()` 可以随便删。

6 个 promtool 场景全绿（占比高触发 / 开关关不触发 / 占比低不触发 /
样本量不足不触发 / 6h 兜底未验证触发 / 零流量三条都不触发）。

**夹具侧的一个坑（已写入 yml 注释）**：`for: 30m` + `increase([6h])` 的场景
**必须用 1m 采样**，不能用 30m——稀疏样本下 `increase()` 外推会让条件在
pending 窗口内反复翻转，告警永远等不满（实测：同表达式去掉 `for` 就正常，
加 `for` 就不响）。生产中 Prometheus 按 15~60s 抓取，6h 窗口有 360~1440 个
样本，不存在该问题——这是**夹具**约束，不是规则约束。

运行方式（不装 promtool，用官方镜像）：
```bash
docker run --rm -v "$PWD/deploy/prometheus:/p" --entrypoint promtool \
  prom/prometheus check rules /p/rules/role-fallback-mainstream.yml
docker run --rm -v "$PWD/deploy/prometheus:/p" --entrypoint promtool \
  prom/prometheus test rules /p/rule_tests/role-fallback-mainstream_test.yml
```

仍需灰度环境确认一次真实触发（promtool 只验求值语义，不验端到端抓取与
Alertmanager 投递链路）。

### 7.2 优先级 2：修 main 既有红（808/809 embed 接线）

`ca4c828f4`（R44 收口）把 808/809 加进 `StartupFiles`，但 `main.go` 三处都没接线
（`//go:embed` 缺指令、`var` 缺声明、`embeddedSQLFiles` map 缺条目），
`TestStartupFilesAreAllEmbedded` 在 main 上一直红。已按 807 三处模式补齐，
分支 `fix/installer-808-809-embed-wiring`（**未推**）。
在途核查：halfking 在 `ca4c828f4` 后无相关提交，确认不在途。

### 7.3 剩余项状态

| 项 | 状态 |
|---|---|
| 灰度决策（父开关开不开） | **未做，产品决定**。监控侧已就位：`AutoRouteRoleRoutingDisabled` 提供了决策依据 |
| 名单动态解析（standard_iq） | **未做**。建议放在 252 部署实证轮之后——名单漂移会改 tier 豁免长度，进而影响 `withRoleFailoverHead` 换模链顺序 |
| `CreditStaysWithTier` 池头 | **未碰**（本轮只动 installer/autoroute 埋点与告警，与该门无关） |

### 7.4 门与反控

新增门 8 条（Go 5 + 规则 3），5 次变异全部按预期红、control 全绿、零残留：

| 变异 | 预期红 |
|---|---|
| 删 `Add(0)` 预置 | 预置门（flag-off 可见性） |
| `and on()` 退化成裸 `and` | set operator 门 |
| 删样本量下界 `>= 5` | 假阳性门 |
| `layerName` 与常量分叉 | 审计/指标同源门 |
| 不注册 gauge | 指标名对齐门 |

**过程中被变异抓住的一次自身缺陷**：`and on()` 门最初写成 `Contains(expr,"and on()")`，
而该表达式有**两个** set operator——只退化其中一个时门仍然绿（实测 M2 变异存活）。
已改为「剥离全部 `and on()` 后不得再有裸 `and`」的口径。这条与
「逐个承重的门不能用 Contains」是同一条纪律。

> 5. **（main 既有红，可顺手做）** `TestStartupFilesAreAllEmbedded` 因 808/809
>    迁移的 embed 接线缺失而红（见 §5）。补法：embeddata 文件 + `go:embed`
>    变量 + `main.go` 的 `embeddedSQLFiles` 映射，参照 807 的三处模式。

