# 探测流量与生产请求物理隔离 — 操作方案

> 日期：2026-11-02  
> 状态：**代码已合入 main；211 待本轮批判审计后重部署开 flag**（2026-11-02 08:56 批判审计）。  
>   **事实（08:56 实测）**：211 现役 `2.5.9-157a5777`（build_seq 2936）——  
>   **不含** `feat(probe-lane)`（`12f34ff30` 非祖先）⇒ env 无
>   `LLM_GATEWAY_PROBE_LANE_PHYSICAL`；近 6h 业务 hot 探测行 382；
>   `session_turns_probe_hot` 仍仅 1 行（01:53 临时启用遗留）。  
>   **代码面（origin/main）**：P1 DDL928 + P2 写分叉 + P3 读侧三端点 +
>   泳道默认 business + `/api/logs` 默认排除探测 + 12h 审计修复
>   （markers 口径 / rollup 真接线 / 死代码门）已在树内。  
>   **本轮补丁**：启动期 `AssertLaneTablesReady`；`MinuteAccumulator` 死路径门；
>   changelog 928 改为 applied。  
>   **仍属主/下一轮**：U1 TTL 清理；U2/U3 SSE 订阅侧；U4 lane=probe 语义改名；
>   MinuteAccumulator 是否删除 vs 复活（产品裁决）。  
> 范围：会话族请求流水、分钟统计、实时流 Redis、请求列表/实时拓扑/泳道前端、usage_ledger、api_keys  
> 修订：07:40 订正状态失真；08:56 批判审计对齐「代码已合 ≠ 生产已开」

---

## 0. 一句话结论

**代码目标态**（main）：写路径可按 `LLM_GATEWAY_PROBE_LANE_PHYSICAL` 分叉到探测族表/缓存；读侧默认业务；探测有独立读端点。  
**生产现状（211 @ 08:56）**：仍跑旧树 + flag 关 ⇒ **行为上仍是「同表打标 + 读侧排除」**（近 6h 业务 hot 仍有大量探测行）。  
开启 flag 且部署含 `12f34ff30+` 的树之后，才进入物理隔离运行态。

---

## 1. 现状（事实）

### 1.1 已有能力（逻辑隔离）

| 面 | 机制 | 证据 |
|---|---|---|
| 判定 SSOT | `internal/probekind`（stage / actor / task_type / quality_flags） | `probekind.go` 包头；2026-10-23 四处口径收敛 |
| 侧表标记 | `request_context_attrs.is_probe` | `domains/streaming/context_attrs.go` |
| 看板计数 | `IsProbeTraffic` → **不写入** `request_stats_*_minute` | `domains/stats/minute_entry.go:134-136` |
| SQL 排除 | `ProbeTrafficExclusionPredicateView` / Session 臂 | `bg/probe_policy.go:198-277` |
| 会话镜像 | 无会话头且 kind 含 `probe` 的合成会话 **不进** mirror | `internal/sessionv2mirror/hook.go:86-94`；**不含** `self_check`（无会话头仍进 `session_turns`） |
| 实时拓扑 UI | `showProbe` 默认 false，前端过滤 `is_probe` | `web/src/types/liveTopology.ts`；`liveTopologyFilter.ts` |
| 探测专表（健康态） | `node_probe_runs` / `node_probe_state` / `model_probe_*` / `system_probe_runs` | schema 已有；**≠ 请求流水表** |
| 专用 SSE | `ProbeSSEHub` / system-monitor（`probe_triggered`）与 live-stream **物理隔离** | `cmd/gateway/main.go` 探测 SSE 挂载 |

### 1.2 仍共用的生产面（问题所在）

| 面 | 共用对象 | 后果 |
|---|---|---|
| 请求流水 | `session_turns` / `session_turns_hot` | 有会话头的探测仍镜像；**`self_check` 无会话头仍进**；R175 routing 族 turns 腿约 **62.5% 探针行** |
| 计费账本 | `usage_ledger_hot` | telemetry 写入**无探测门** → 探测混入账本 |
| API Key 累计 | `api_keys` totals | 成功探测也会 `+1` |
| 侧表 | `request_context_attrs`（`is_probe` 列） | 同表打标，隔离靠读侧 |
| 实时流 Redis | 同一租户 ZSET（tile 带 `is_probe`） | 后端 SSE **不过滤**；挤占队列 |
| 泳道 UI | LiveRequestStreamV2 / RequestTile | **默认 business+probe 全显**（与拓扑默认隐藏**相反**） |
| 请求列表 API | 会话族读面 | 未见默认服务端排除探测 |
| 分钟统计 | 业务表已排除写入；**无独立探测计数表** | 探测 QPS/成功率要另查流水或 probe_runs |
| analytics 过滤 | `businessRequestFilterStages` **手写** stage 列表 | 与 `probekind` 内容对齐但**未引用 SSOT** → 漂移风险 |

### 1.3 已登记、未落地的属主项

R175 审计遗留（`docs/audit/2026-10-29-R175-…md` §7）：

> 治法候选：(a) 部分索引按 `origin_stage` 排除；(b) **探针与业务分表/分分区（写路径语义变更）** —— 需属主授权。

本方案采纳 **(b) 为主路径**；(a) 仅作过渡或历史表加速。

**2026-11-02 12h 审计轮补登记**（实现落地后新出现的未落地项，**不是**原方案遗漏）：

| # | 未落地项 | 现状 | 后果 | 归属 |
|---|---|---|---|---|
| U1 | **探测族 TTL / 清理作业** | `session_turns_probe_hot`（928 建）**无 TTL、无分区、无清理作业**；全仓无对它的 DELETE/TRUNCATE | 该表**单调增长**。方案 §0 原写「热表 + 短 TTL 为终态」，`sql/objects/tables/session_turns_probe_hot.sql` 注释亦称「TTL 由后续清理作业管」——**该清理作业尚未存在** | 属主待定（建议随存量清理作业一并落地） |
| U2 | **SSE 频道分离（P3 剩余）** | 探测 tile 已分流到 `llmgw:live:probe:*` 键空间（写侧完成），但 SSE **订阅侧**未分离；读侧已补独立端点 `/api/admin/probe/lane/live` | 探测 tile 不会进业务 SSE 频道（写侧已断），但前端「订阅探测频道」这条路径未做 | P3 剩余项 |
| U3 | **前端探测订阅切换（P3 剩余）** | `liveStreamPreferences` 默认已从 `['business','probe']` 收敛为 `['business']`（**默认隐藏**已达成）；但「显式打开才订探测频道」的网络层行为未验证 | 前端只能「不显示」，不能「订阅探测流」 | P3 剩余项 |
| U4 | **`/api/logs?lane=probe` 的语义分层** | 该参数读的是**业务表**上的历史探测行（`admin/analytics.go` 的 `probeRequestFilter` 注释自陈「物理分表开启前读业务表上的历史探测行」） | 车道开启后，**新**探测只能从 `/api/admin/probe/lane/requests` 读；`lane=probe` 读到的永远是隔离开启**前**的历史行 ⇒ UI 上必须区分「历史」与「当前」 | 本轮已补独立端点，UI 分层待做 |

★ U1 的诚实边界：本轮落地的 `bg/stats_minute_probe_rollup.go` 的 `retireProbeMinutes`
只处理「桶已无源数据」的**退役**，**不处理「源数据本身过期」** ⇒ 探测流量停止后，
`request_stats_probe_minute` 会停在最后一次有数据的桶上，不再增长（**不增长**≠**会清理**）。

---

## 2. 目标态（验收口径）

1. **新写入**：探测请求 **不进入** 业务 `session_turns(_hot)`（含 `self_check` / 有会话头探测）；进入探测族表。  
2. **计数**：业务分钟表/看板缓存 **零探测行**；探测有独立计数面。  
3. **账本 / Key**：探测 **不写入** 业务 `usage_ledger_hot`、**不** 给 `api_keys` totals `+1`（或写入探测专用账本，属主二选一）。  
4. **实时流**：业务 SSE/ZSET 与探测 SSE/ZSET **键空间分离**；默认订阅业务面。  
5. **前端**：拓扑 **与泳道默认策略对齐**（均默认只见业务）；探测需显式开关或订探测频道。  
6. **健康探测专表**（`node_probe_runs` 等）职责不变。  
7. **历史数据**：不强行搬迁；业务读默认谓词排除旧探测行。  
8. **口径同源**：`admin/analytics.businessRequestFilterStages` 等手写名单改读 `probekind`。

---

## 3. 推荐架构（分叉点）

```
HTTP/网关入口 / telemetry 直写
    │
    ├─ probekind.IsProbe*(…) = true
    │       ├─ 流水 → session_turns_probe(_hot)（含原会进 session_turns 的 self_check）
    │       ├─ 账本 → 跳过业务 usage_ledger_hot / api_keys（或探测专用账本）
    │       ├─ 计数 → request_stats_probe_minute / Redis probe:stats:*
    │       └─ 实时 → Redis …:probe:… → 可选 SSE（或沿用既有 ProbeSSEHub）
    │
    └─ business
            ├─ 写 → session_turns(_hot) / usage_ledger_hot / api_keys（现状）
            ├─ 计数 → request_stats_*_minute（现状）
            └─ 实时 → 现有 live ZSET（禁止再写入探测 tile）
```

判定仍以 **`internal/probekind` 为唯一真值**；mirror 跳过谓词也应对齐 `IsProbe`（今日 `Contains("probe")` ≠ SSOT）。

---

## 4. 分阶段操作

### P0 — 冻结与量具（1 短迭代，无行为变更）

- [ ] 全仓写/读面影响矩阵定稿（见 §5），与 `request_logs` 读面台账交叉核对。  
- [ ] 生产只读抽样：24h 内 `session_turns_hot` 中 `origin_stage ∈ ProbeStages` 占比、实时 ZSET 中 `is_probe=true` 占比。  
- [ ] 门：任何新写路径必须声明 `lane=business|probe`（测试桩）。  
- [ ] **属主确认**：是否保留「带会话头的探测」进探测族（建议：**全部探测进探测族**，避免半隔离）。

### P1 — DDL：探测族表（写路径开关默认关）

新建（命名可微调，原则 = 与业务对称、独立 TTL）：

| 新对象 | 镜像自 | 备注 |
|---|---|---|
| `session_turns_probe_hot` | `session_turns_hot` 列子集（请求流水必要列） | 可先窄于全列，避免复制 100+ 列债务 |
| `session_turns_probe`（分区） | 同上 | TTL 可短于业务（建议 7–14d） |
| `request_stats_probe_minute` | `request_stats_minute` | 探测 QPS/成功率专用 |
| （可选）`request_context_attrs_probe` | `request_context_attrs` | 或探测行不再写侧表 |

迁移登记：startup 链 + `docs/db-changelog.md` + embed/runner/tsv（仓内六点纪律）。  
Feature flag：`LLM_GATEWAY_PROBE_LANE_PHYSICAL=0|1`（默认 0）。

### P2 — 写路径分叉（双写窗口可选）

改动锚点（实施时以源码为准再钉行号）：

1. `domains/hooks/observability/telemetry/client.go` — `usage_ledger_hot` INSERT / `api_keys` UPDATE：**探测跳过业务面**（或写探测账本）。  
2. `internal/sessionv2mirror` — 跳过谓词对齐 `probekind.IsProbe`（覆盖 `self_check`）；有会话头探测也不再进业务 `session_turns`。  
3. `domains/streaming/request_log_pipeline.go` / 侧表 — 探测行改写探测族（或停写业务侧表）。  
4. `domains/stats/minute_entry.go` — `IsProbeTraffic` 为真时写入 **probe 分钟表**（今日是丢弃；改为分流）。  
5. `admin/live_stream_redis_store.go` / SSE hub — 探测 tile 只进 `…:probe:…`，**禁止** ZADD 业务 `main`。  
6. `bg/active_probe_emitter.go` 等 telemetry 直写者同步。  
7. `admin/analytics.go` `businessRequestFilterStages` → 改读 `probekind`（P0/P2 可并行的低风险项）。

过渡：**flag=1 时仅写探测族；业务表/账本零新探测行**。不建议长期双写。

### P3 — 读面与前端

| 入口 | 行为 |
|---|---|
| 看板 / board / settle cohort | 继续读业务表；谓词保留作历史兜底 |
| `/api/admin/...` 请求列表、logs | 默认 `lane=business`；`lane=probe` 才读探测族 |
| Live SSE | 默认业务频道；探测需 `?probe=1` 或沿用 ProbeSSEHub |
| `LiveTopologyView` | 保持默认隐藏；改为订探测频道而非同池滤 |
| `LiveRequestStreamV2` / 泳道 | **默认改为与拓扑一致（只 business）**；显式打开才订探测 |
| `RequestLogsView` | 默认业务 |
| 探测运维页 | `node_probe_*` + 探测族流水 |

### P4 — 测试门

- 单测：probekind 判定 → 写路径选表（mock DB / 真库腿）。  
- 契约：业务表插入探针 stage **在 flag=1 时失败或 0 行**（负例）。  
- 实时流：同租户并发业务+探测 → 业务 ZSET 成员 `is_probe` 恒 false。  
- 前端：`showProbe=false` 时网络层不订阅探测频道（不只是客户端滤）。  
- 变异臂：删分叉 / 写错表 / Redis 键拼错 → 门红。  
- `make test-short` + 相关真库腿；部署闸门对比基线。

### P5 — 部署顺序

1. 合并 → 211 蓝绿（`LLM_GATEWAY_ROOT` 钉根）。  
2. 先发 **DDL + flag=0**（表就位、行为不变）。  
3. 冒烟后 **flag=1**（或按节点滚动）。  
4. 验收 SQL：新窗口业务 hot 表 `origin_stage ∈ ProbeStages` 新行 = 0；探测 hot 表有增量。  
5. 245 / 154 按现网拓扑跟进（若本轮仅 211 承载探测，如实记）。  
6. 历史清理：**属主拍板**后再做（DELETE/归档探测旧行属 rule 19 三阶）。

---

## 5. 影响矩阵（实施清单草稿）

| 角色 | 组件 | 动作 |
|---|---|---|
| Writer | `telemetry/client.go` → `usage_ledger_hot` | 探测跳过业务 INSERT（或探测账本） |
| Writer | `telemetry/client.go` → `api_keys` | 探测不 `+1` |
| Writer | `sessionv2mirror.PersistHook` | 对齐 `probekind.IsProbe`；业务表零新探测 |
| Writer | streaming request_log_pipeline / 侧表 | 分表或停写业务 |
| Writer | active_probe_emitter / node_probe HTTP | 自然受益于 telemetry 分叉 |
| Writer | stats minute accumulator | 分流探测分钟表 |
| Writer | live_stream redis store | 键空间分离 |
| Reader | board / dashboard_board_fallback | 保持；历史谓词 |
| Reader | credential monitor / settle / shared_pick | 保持 Session 排除谓词至历史清空 |
| Reader | `admin/analytics.businessRequestFilter*` | 改读 `probekind` |
| Reader | admin logs / session_list / request detail | 默认业务；探测显式 |
| Reader | LiveTopology / RequestLogs / **泳道** | 频道分离 + **默认策略对齐** |
| Cache | Redis live ZSET / boardcache | `…:main` vs `…:probe:…`；boardcache 已排除保持 |
| DDL | migrations + changelog + 登记点 | 新表族 |
| Ops | probe_runs / probe_state / ProbeSSEHub | **不改职责** |
| 文档债 | hook 注释「探针仍在 v1」 | 890 后退役，实施时订正 |

---

## 6. 风险与非目标

**风险**

- 漏改一个写点 → 探测仍进业务表（用「业务表探针新行=0」棘轮门兜住）。  
- 列集过宽复制 session_turns → 迁移/维护成本爆炸（探测表用**窄列**）。  
- 前端只改过滤不改订阅 → 假隔离（P3 验收看网络）。  
- flag 全开前未灌 DDL → 写失败（部署分两步）。

**非目标（本方案不做）**

- 回填搬迁历史探针行（除非属主另开任务）。  
- 合并/替代 `node_probe_runs`（职责不同）。  
- 改变探测调度、退避、路由候选逻辑。  
- 用「部分索引」代替分表作为终态（仅过渡）。

---

## 7. 决策请属主确认（实施前）

1. **物理分表**是否批准为终态（相对「只加部分索引」）？ → 方案默认 **是**。  
2. **全部探测**（含 `self_check`、有会话头探测）是否进探测族、且 **不进** 业务 `session_turns`？ → 建议 **是**。  
3. 探测对 **`usage_ledger_hot` / `api_keys`**：跳过业务面，还是写探测专用账本？ → 建议 **跳过业务面**（探测不计费、不抬 Key 计数）。  
4. 泳道默认是否与拓扑对齐为 **隐藏探测**？ → 建议 **是**。  
5. 探测流水 TTL（建议 7–14 天）与是否要产品级「探测实时流」（或仅保留 ProbeSSEHub）？  
6. 首发环境：仅 211 还是 211+245+154？

确认后按 P0→P5 自动推进：DDL → 写分叉+测试 → flag 灰度 → 验收 SQL → 前端频道分离。

---

## 8. 证据索引

- `internal/probekind/probekind.go`  
- `domains/stats/minute_entry.go`（IsProbeTraffic）  
- `domains/hooks/observability/telemetry/client.go`（`usage_ledger_hot` / `api_keys` 无探测门）  
- `bg/probe_policy.go`（排除谓词三臂）  
- `admin/live_stream_redis_store.go`（同池 `IsProbe`）  
- `internal/sessionv2mirror/hook.go` + `synthetic_session.go`（self_check 仍进 turns）  
- `web/src/composables/useLiveStreamFilters.ts`（泳道默认含探测）  
- `docs/audit/2026-10-23-live-topology-probe-tagging-audit.md`  
- `docs/audit/2026-10-29-R175-…md` §7（分表属主项）  
- AGENTS.md「2026-10-23 实时拓扑探测打标四处源真值漂移轮」
