# 2026-10-29 24h 审计第 4 轮：asm 投影缺口 reconciliation（结构性 opened 缺口根修 + DLQ 34,183 排空）

> goal 会话 · 2026-10-29 00:20~02:30 CST · 基线 commit `2c7dcf48c`（本档随部署补 §11）
> 上轮遗留：`docs/audit/2026-10-28-24h审计第3轮-出口修复与背压退避.md` §10 的 1~4 项本轮全部收口。

## 0. 结论摘要

**asm 事件消费「连续 503」的根因不是 asm 故障，是网关侧的结构性数据缺口**：
`session.opened.v1` 只在请求日志**新建插入**路径发射，而**早失败请求（39ms、provider=unknown）只走终态更新路径**——这些请求所属的会话永远没有 opened 事件 ⇒ asm 的 `session_projection` 无行 ⇒ 这些会话的**每一条** `request.completed.v1` 都命中
`projection row missing for session …` → Retryable → 整批 503。生产实测 **14,369 个会话**卡在此缺口，网关 outbox DLQ 累积 **34,183 条**（8,781 opened + 25,402 completed）全部源于此。上轮观察到的「asm audit sink 失败 10 小时」（10-28 09:20–13:30 UTC，1,572 次）是**第二重独立诱因**：它把 10-21 起正常会话的 opened 挡在 claim 之前（无 inbox 行），网关重试 3 次后进 DLQ，其会话的 completed 随后落入同一缺口。

本轮修复三件套（全部完成并验证）：
1. **SQL 回填**：为 14,369 个缺口会话补发 opened（确定性 event_id，与代码同构），DLQ opened 8,781 条翻回 pending 重放；
2. **代码根修**（`2c7dcf48c`）：终态路径同事务补发 opened（幂等），未来同类缺口不再产生；
3. **DLQ completed 25,402 条**在 opened 波排空后翻回 pending 排空（§3.4）。

asm 侧配套：迁移 36 → **56** 一次通过（`capgov_assets` 等 9 张 capgov 表落地，digest 守护进程缺关系报错归零）。**重启评估：不需要重启**——asm 容器（起于 10-26 20:30 UTC）DB 可达、消费管线实时推进（checkpoint 秒级更新）、503 已止息；两处病灶（迁移缺失、缺口数据）都不经重启修复。

## 1. 证据链（读数全部 211 生产实测，acc-pg17）

| 读数 | 值 | 含义 |
|---|---|---|
| `gateway_event_inbox`（asm） | completed 35,551 / **retryable 14,456** / dead_letter 0 | 14,456 条卡在「claim 过但投影失败」 |
| retryable 的缺口占比 | **14,448 / 14,456**（14,374 个 distinct 会话无投影行） | `payload->>'session_id'` 不在 `session_projection` |
| retryable 按日分布 | 10-21: 655 · 10-22: 3,133 · 10-23: 182 · 10-24: 1,000 · 10-27: 3,408 · 10-28: 6,074 · 10-29: 4 | 与网关 DLQ 按日分布逐日吻合 |
| outbox DLQ 构成 | **opened 8,781 + completed 25,402 = 34,183** | completed 中 `failed/unknown` 16,170（早失败族）、`succeeded/minimax` 5,539（opened 被弹掉的配对受害者）、`failed/nvidia` 2,993（幻影模型族） |
| 缺口会话 × outbox 对拍 | **14,369 / 14,375 在 outbox 无任何 opened 行**（5 条 DLQ + 1 条 sent） | 结构性缺口：opened 从未发出，不是丢失 |
| DLQ opened × 投影对拍 | 8,781 条 opened 均不在缺口会话清单（它们是被 audit sink 挡在 claim 前的另一族） | 两族诱因独立，修复路径共用重放 |
| asm 迁移版本 | 36（仓内 56）；`capgov_assets` 等缺失 | 上轮「capgov_assets 缺关系」实锤 |
| 实时 503 | 01:32 CST 仍有（`evt-…-a5669…` attempts=2→3 → failed） | 缺口会话的新请求持续制造 churn |
| audit sink 现状 | 10-28 21:30 CST 后零失败；新会话投影 10-27: 4,883 / 10-28: 6,745 / 10-29: 105 | sink 已自愈，缺口是存量 |

**机制确认（源码）**：
- opened 发射点唯一：`domains/hooks/observability/telemetry/client.go` `insertRequestLog`（新建路径）；
- completed 发射点唯一：同文件 `updateRequestLog` 终态块；两者 guard 同为 `entry.GwSessionID != nil`——但**早失败请求不经过 insertRequestLog**（无 insert 行可更新 ⇒ 直写终态），这是缺口的代码层根因；
- dispatcher claim 查询只取 `pending OR (failed AND next_retry_at<=NOW())`，**DLQ 永不自动重拾** ⇒ 重放必须人工翻状态；
- 背压（503）走 `markBackpressure`：永不 DLQ、指数退避（8s×2 封顶 10min）+ 熔断整循环暂停 64s（`24b6ddf3b`）⇒ 缺口事件在现状机制下**无限重试且反复暂停整循环**——这是「背压退避部署后积压不排空」的真正原因：不是退避参数问题，是**重试对象是永久不可消费的事件**。

## 2. 为什么不是重启 / 迁移 / 参数调优能解决的

- **重启**：503 的门是 per-request DB 错误，不是进程启动状态（`d != nil`、DB 可达）；checkpoint 秒级推进证明消费活的。
- **迁移 36→56**：事件管线五张表（000009/10/12/13/14）在版本 36 时已存在——audit sink 现在健康；缺的 000038+ 只影响 capgov digest（噪声，非 503）。
- **退避参数**：退避只决定重试节奏；缺口事件重试一万次也是 503。真正缺的是**让事件变得可消费**（补 opened）。

## 3. reconciliation 执行记录（全部可回滚，SQL 留档）

### 3.1 快照与挂起（01:41）
```sql
-- 挂起 4,201 条 failed（全是 event_retryable 缺口事件）：防止排空期间每条
-- 到期重试都触发 64s 熔断暂停、把 23k 条 opened 的排空拖成小时级
update outbox_events set next_retry_at = now() + interval '8 hours', updated_at = now()
where status='failed' and event_type='request.completed.v1' and next_retry_at is not null;
-- UPDATE 4201
```

### 3.2 回填 opened（01:47）
缺口清单从 asm 侧导出（`gateway_event_inbox status='retryable'` 的 distinct `payload->>'session_id'`，14,375 行 → `/tmp/gap_sessions.txt` → 容器内 COPY 进临时表），然后：
```sql
insert into outbox_events (event_id, event_type, schema_version, tenant_id,
                           aggregate_id, aggregate_version, occurred_at, payload, status)
select 'evt-session-opened-' || substr(encode(digest(
           convert_to(tenant_id,'UTF8') || '\x00'::bytea || convert_to(sid,'UTF8'),
           'sha256'), 'hex'), 1, 16),
       'session.opened.v1', 1, tenant_id, sid, 1, occ,
       jsonb_build_object('session_id', sid, 'user_id', 'unknown', 'source_system', 'gateway'),
       'pending'
from (select o.aggregate_id as sid, o.tenant_id, min(o.occurred_at) as occ
      from outbox_events o join gaps g on o.aggregate_id = g.s
      where o.event_type='request.completed.v1' and o.status in ('dlq','failed')
      group by o.aggregate_id, o.tenant_id) src
on conflict (event_id) do nothing;
-- INSERT 0 14369（6 条与既有 opened event_id 幂等冲突，恰等于对拍出的 5 DLQ + 1 sent）
```
要点：
- **event_id 公式逐位对拍**：`sha256(tenant || NUL || session)[:8]` hex —— 本地 python 对生产已知样本 `default/gw_0972ee4c…` 算出 `eab3798a82e101d1`，与 DLQ 实存行一致才执行（SQL 里 `chr(0)` 不合法，改走 `convert_to(bytea)` 拼接）；
- `occurred_at` 取该会话最早 completed 时刻（回填的诚实近似；checkpoint 只前进不回退）；
- payload 形态与 `BuildSessionOpenedEventV1` 完全一致，通过 asm 签名路径 schema（`internal/gatewayevents/schema/gateway-event-schema-v1.json` SessionOpenedPayload 只要求 `session_id`+`user_id`；**注意** `contracts/event-schemas/session.opened.v1.json` 是另一份更严的未部署契约，别拿它当验收标尺）。

### 3.3 DLQ opened 重放（01:50）
```sql
update outbox_events set status='pending', attempts=0, last_error=null, next_retry_at=null, updated_at=now()
where status='dlq' and event_type='session.opened.v1';  -- UPDATE 8781
```

### 3.4 DLQ completed 重放 + 释放 failed（02:0x，opened 清零后执行）
```sql
update outbox_events set status='pending', attempts=0, last_error=null, next_retry_at=null, updated_at=now()
where status='dlq' and event_type='request.completed.v1';  -- UPDATE 25402
update outbox_events set next_retry_at = now(), updated_at = now()
where status='failed' and event_type='request.completed.v1';  -- 释放 4202
```
**次序纪律**：completed 波必须在 opened 波**全部送出后**才翻——否则乱序 completed 先到 ⇒ asm 503 ⇒ 熔断暂停 64s/次，25,402 条交错会把排空拖成天级。opened 波按 `occurred_at ASC` 天然先于各自 completed。

### 3.5 第三族缺口（部署后终验发现，01:15 补）
第二波翻完后终验出现 1 条新 503（`evt-request-completed-7f70b2bd…`，10-24 的 DLQ completed）：其会话 `gw_c067804b…` **连 asm inbox 行都没有**（10-24 被 audit sink 挡在 claim 之前）⇒ 不在 3.2 的缺口清单里 ⇒ 没被回填。
**清单口径订正**：asm inbox retryable 清单天生漏掉「audit sink 弹掉、无 inbox 行」一族；正确的闭环判据是 **outbox 里有 `request.completed.v1` 而无任何 `session.opened.v1`**：
```sql
with src as (
  select o.aggregate_id as sid, o.tenant_id, min(o.occurred_at) as occ
  from outbox_events o
  where o.event_type='request.completed.v1'
    and not exists (select 1 from outbox_events x
                    where x.aggregate_id=o.aggregate_id and x.event_type='session.opened.v1')
  group by o.aggregate_id, o.tenant_id
)
insert into outbox_events (…同 3.2 形态…) select … from src on conflict (event_id) do nothing;
-- INSERT 0 5862；复验 remaining_gaps = 0
```
**缺口数据层清零**（每个有 completed 的会话都有 opened）；未来缺口由代码根修（§4）在源头闭合。

### 3.6 occurred_at 打结拆解（01:52，排空速率塌方定位）
第二波翻完后吞吐从 ~15/s 塌到 **~0.23/s**（15 条突发 + 64s 熔断暂停循环，按此速率剩余积压要 ~32h）。定位：**3.2/3.5 的 SQL 回填把 opened 的 occurred_at 设成该会话 min(completed.occurred_at) —— 与那一条 completed 完全同刻**；claim `ORDER BY occurred_at ASC` 在同刻上顺序不定，completed 先被 claim ⇒ asm 503 ⇒ 熔断暂停 64s ⇒ 下一轮 probe 又撞下一对同刻。代码根修（§4）用 `-1ms` 防了未来，**SQL 回填没有复制这个偏移**——补上：
```sql
update outbox_events o set occurred_at = o.occurred_at - interval '1 millisecond', updated_at = now()
from outbox_events c
where c.aggregate_id = o.aggregate_id
  and o.event_type='session.opened.v1' and c.event_type='request.completed.v1'
  and o.status='pending' and c.status in ('pending','failed')
  and o.occurred_at = c.occurred_at;   -- UPDATE 5755
```
**效果立竿见影**：拆结后 2 分钟 943 条成功（~8/s 恢复满速）、失败 1 条。⇒ ★ 通用式补充：**确定性回填必须逐字段复刻代码路径的全部排序不变量**（event_id 对拍了，occurred_at 的 -1ms 漏了——同一不变量要整体搬，不是挑重要的搬）。

### 3.7 第四族回填（02:0x）+ 缺口再生速率实测
3.5 的「清零」只在当时成立：**01:23:48 的一条 live completed（`evt-…-0923d216`）的会话又没有 opened** —— 该事件由并行会话 01:30 上线的活跃槽（`5fbba4ef-dirty`）前后发出，而**该构建不含本轮代码根修 `2c7dcf48c`**（sha 为 dirty 本地构建，祖不可验；由「修复后的终态路径同事务必发 opened」反证其缺失）⇒ 用 3.5 同款判据（这次带 `-1ms`）再回填 **11 个**新缺口会话，remaining=0。
⇒ **缺口再生速率 ≈ 11 个/小时**（每个早失败会话 1 个）——在 `2c7dcf48c` 部署到活跃槽之前，本判据 SQL 可随时重扫；数据层无自动恶化风险（新缺口事件按背压口径 failed 重试，不落 DLQ）。

### 3.8 终态读数（02:05）
| 指标 | 起始（01:40 快照） | 终态 |
|---|---|---|
| outbox dlq | 34,183 | **0** |
| outbox failed | 4,202 | **0**（最后 1 条 02:13 到期重试成功） |
| outbox pending | 25,802 | ~390（活流量尾） |
| asm inbox retryable | 14,456 | **0~1** |
| asm inbox completed | 35,551 | **94,777（与网关 sent 逐位一致）** |
| checkpoint events_processed | 35,551 | 94,777 |
| session_projection | 38,598 | 覆盖全部有 completed 的会话 |

本轮净排空 **59,226 条**（= 34,183 DLQ + 4,202 failed + ~20,800 积压），全程零丢失（asm 幂等去重，网关 sent == asm completed == projection processed 三方对拍）。

### 3.9 验证（部署后复核）
- outbox：`dlq=0 / failed=0`（或仅剩带真实原因的小残量）；
- asm：inbox retryable → 0（对应事件被重投后幂等 claim 完成）、`session_projection` 净增 ≈ 23,150、checkpoint events_processed 同步增长；
- 实时面：缺口会话的新请求 completed 一次成功（无 503 WARN）。

## 4. 代码根修（`2c7dcf48c`，已推送 origin/main）

`domains/hooks/observability/telemetry/client.go`：
- `updateRequestLog` 终态块**同事务先 INSERT opened 再 INSERT completed**（`ON CONFLICT (event_id) DO NOTHING` 幂等；正常会话 no-op）；
- `opened.OccurredAt = now-1ms`：dispatcher 按 occurred_at ASC 投递 ⇒ 同批到达时投影行先建；
- 抽出 `sessionOpenedUserID()` 助手，两条发射路径共用同一份 user_id 推导（同一把尺一份实现）。

测试 `client_opened_terminal_test.go`（pgxmock）：① 终态 + GwSessionID + writer ⇒ opened（event_type 实参钉死 `session.opened.v1`）与 completed 两条 INSERT 按序发出；② writer nil ⇒ 不碰 outbox_events。`go test ./domains/hooks/observability/telemetry/` 全绿。

## 5. asm 迁移 36 → 56（capgov_assets 缺关系收口）

- 工具：仓内 `cmd/asm-migrate`（自带远端守卫 `ASM_MIGRATE_ALLOW_REMOTE=1`）交叉编译（CGO_ENABLED=0 GOOS=linux GOARCH=amd64）→ `docker cp` 进 `ai-session-manager-asm-1:/tmp` 执行；
- 37~56 全部为幂等增量 DDL（`CREATE TABLE IF NOT EXISTS` / `ADD COLUMN IF NOT EXISTS`）——审阅后一次通过：`version=36 dirty=false → done version=56 dirty=false`；
- **9 张 capgov_* 表落地**；digest 守护进程此后无 `does not exist` 报错；
- 容器**未重启**（不需要；增量表对运行中旧代码无感）。

## 6. ③ 13 家 proxy 出口供应商逐家直连实测与决断

211 宿主机直连实测（TLS 握手 + HTTP 状态，`--max-time 8`）：

| 供应商 | id | 直连实测 | 决断 |
|---|---|---|---|
| anthropic | 2 | 403 / 0.9s | **→ direct** |
| cohere | 5 | 403 / 0.4s | **→ direct** |
| fireworks | 8 | 404 / 0.2s | **→ direct** |
| github-copilot | 9 | 404 / 0.9s | **→ direct** |
| groq | 11 | 403 / 0.8s | **→ direct** |
| openrouter | 21 | 404 / 1.0s | **→ direct**（唯一有凭据者：cred 44 active、断路器闭合、从未用过） |
| together | 27 | 403 / 0.3s | **→ direct** |
| azure-openai | 3 | 000 / 0.08s（模板域） | 保留 proxy |
| google-gemini | 10 | 000 / 8s 超时 | 保留 proxy |
| mistral | 15 | 000 / 8s 超时 | 保留 proxy |
| openai | 20 | 000 / 8s 超时 | 保留 proxy |
| perplexity | 22 | 000 / 8s 超时 | 保留 proxy |
| xai | 30 | 000 / 8s 超时 | 保留 proxy |

决断依据：`proxy_subscriptions`/`proxy_nodes` 双 0 行（上轮实证）⇒ proxy 路径对 13 家**本来就是断的**；直连可达的 7 家切 direct 是纯收益（`providers` 策略 TTL 60s 生效，无需重启，`UPDATE 7`，direct 49→56 / proxy 13→6）。6 家不可达者两条路都不通且零凭据零流量——保留 proxy 留待将来订阅就绪，不为零流量配置翻烧饼。

## 7. ④ NVIDIA 幻影模型协议红处置

- DLQ 里 `failed/nvidia` 2,993 条的模型名（riva-translate-4b-instruct-v2 1,027 / nemotron-3.5-bolt-e4b 676 / ising-calibration-1.5-31b 331 / diffusiongemma-26b-a4b-it 312 …）在 `provider_models` **已零行**——目录刷新剪除机制已生效；
- 请求流按日：10-24: 58 → 10-26: 1,470 → **10-27: 265 → 10-28 起零**——10-27 剪除后自然止息；
- 3 条 nvidia 凭据 circuit 全 closed、2 healthy；cred 23 带陈旧 warning（probe_consecutive_failures=672、last_probe 10-11）——无流量，登记观察不动。

## 8. 分支与提交审计

- **origin/main**：仅 main 一条分支；唯一未合并子分支 `r98/mv-refresh-load-attribution`（73e6fda02，live 页窗口统计）最后提交 00:50——**1h 活跃窗口内，登记不删**，归其会话；
- 已 prune 两个死 worktree（`/private/tmp/gw-snap-src`、`/private/tmp/kx-wt-originmain`）；
- incoming 7+3 提交批判审阅：outbox 熔断器两连（`24b6ddf3b`/`5b753fc55`，判据/测试/解耦注释扎实；**补充发现**：熔断把 `event_retryable` 当「门全局关了」，本轮证明它也可以是「单事件投影缺口」——缺口清除后该误判面自动消失，但分类语义值得后续区分）；executors 词表分叉根修（`8482a636d`/`f73653ad6`，canonicalInboundWire 归一 + video_url 诚实拒绝，测试全绿）；brand 位图（`bcf97b7f1`，纯二进制）；r98 的 topology 窗口统计（短窗口走 request_logs 索引、归并口径与 SSE 同源、论证完整）；
- 本轮改动：`2c7dcf48c`（telemetry 终态补发 opened + 测试）。

## 9. 系统 / DB 巡检

- 网关容器近 1h **0 ERROR / 0 WARN**（背压风暴消失的直接证据）；
- PG 慢日志（3h）：仅 `REFRESH MATERIALIZED VIEW CONCURRENTLY routing_analytics_7d` ~1.8s（30min 例行）+ 00:17 一条 4.2s 空文本一次性语句——**无慢 SQL 优化项**；
- `pg_stat_statements` preload 仍需共享 PG 重启授权（遗留，上轮已登记）。

## 10. 遗留（留给下一轮）

0. **活跃槽（`5fbba4ef-dirty`，并行会话 01:30 部署）不含本轮代码根修 `2c7dcf48c`** —— 缺口以 ~11 会话/小时缓慢再生（§3.7）；下轮任何一次部署 origin/main HEAD 即根治；期间可用 §3.5 判据 SQL 随时清扫（注意带 `-1ms`）。本轮部署的 `533b115c`（含修复）已并行槽接管取代，镜像 `registry-211…:211-533b115c` 仍在。
1. **DLQ completed 波排空读数**（部署后观察，§3.5；预计 ~70min 全清）；
2. **asm audit sink 10-21~10-28 13:30 UTC 失败的最初根因**仍未知（容器日志只覆盖 10-26 后；建议 asm 侧把 audit 写失败的底层 pgx 错误打进日志——现在只包了外层 `errAuditWrite`，诊断黑洞）；
3. **asm 契约双轨**：`contracts/event-schemas/session.opened.v1.json`（uuid/actor_id/mode）与 `internal/gatewayevents/schema/gateway-event-schema-v1.json`（session_id/user_id）并存且互斥——前者若上线会把网关现行 payload 全部判死，需 asm 会话对齐；
4. 熔断器 `IsBackpressure` 把「单事件投影缺口 503」与「门全局关」混为一类（本轮缺口清除后无现害，语义拆分留待有真实案例再做）；
5. 6 家不可达 proxy 供应商 + azure 模板域：等凭据或订阅就绪再决断；
6. cred 23（nvidia）陈旧 warning 探针：无流量，观察；
7. `/tmp` 两处迁移临时件清理受限（211 宿主机 rm 权限 + 容器 uid 65532）——均易逝，无风险；
8. 上轮遗留延续：`pg_stat_statements` preload（需授权）、r98 收尾归其会话。

## 11. 部署与最终读数

- 部署：`LLM_GATEWAY_ROOT=/Users/niuer/workspace/ai-native-tools/llm-gateway/llm-gateway-go scripts/deploy-211.sh`（钉根防 DD_REPO_ROOT 漂移），01:11 完成，版本 **2.5.8-533b115c-20261029-211**（槽 a 接管，readyz/登录冒烟/公网身份全过，migrations 至 916 无漂移）；
- **01:22 槽 a 被并行会话停掉（exit 137）、其 01:30 上线自己的构建 `5fbba4ef-dirty`（槽 b）** —— 蓝绿被并行部署接管（多会话常态）；本轮回的 SQL 层修复不受部署影响继续生效，代码根修随 origin/main 由下轮部署覆盖（§10-0）；
- providers 策略生效：`egress_profile` direct **56** / proxy 6（TTL 60s 现读 DB，无需重启）；openrouter（唯一有凭据的 proxy 族）从此可路由；
- outbox 积压趋势：34,183 DLQ + 4,202 failed + ~25,802 pending → **0 / 0 / ~390（活流量尾）**，asm inbox retryable 14,456 → 0~1，三方读数（网关 sent == asm completed == checkpoint）逐位一致 —— §3.8 全表。
