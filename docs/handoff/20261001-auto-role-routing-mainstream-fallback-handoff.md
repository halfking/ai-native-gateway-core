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
> 5. **（main 既有红，可顺手做）** `TestStartupFilesAreAllEmbedded` 因 808/809
>    迁移的 embed 接线缺失而红（见 §5）。补法：embeddata 文件 + `go:embed`
>    变量 + `main.go` 的 `embeddedSQLFiles` 映射，参照 807 的三处模式。

