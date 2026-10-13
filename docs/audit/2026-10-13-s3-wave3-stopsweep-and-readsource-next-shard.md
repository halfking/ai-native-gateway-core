# S3 波 3（第二分片）：停写后的读侧收尾 + 最后一处漏网写点

> 轮次：2026-10-13 上午，S3 波 2（第一分片，16 读点）之后的直接后续。
> 前置文档：`2026-10-13-s3-wave2-requestlogs-readsource-migration.md`。
> 范围：①波 2 §5 登记的三项「下片」读债（tenants stats / picker usage 源 /
> autoroute 指标 rollup）；②写路径全面盘点——发现并收口**最后一个未挂
> S4 门的流量写点**（音频计量行）。

## 1. 写路径盘点：全部写点 × 门控状态

大小写不敏感全仓扫描（非测试代码）`INSERT/UPDATE/DELETE … request_logs`：

| 写点 | 门控 | 处置 |
|---|---|---|
| telemetry client（INSERT 主行 + 5×UPDATE + bodies） | S4 门内（:1394/:2197 段） | 不动 |
| admin telemetry ingest（INSERT + bodies upsert） | S4 门内（:391/:454） | 不动 |
| cmd/gateway lite_telemetry_sink | S4 门内（:134） | 不动 |
| internal/trace FlushToPG（trace_events UPDATE） | S4 门内（R52，丢弃+清 Redis） | 不动 |
| **domains/streaming audio_usage_log LogAudioUsage（INSERT hot）** | **无门（本轮发现）** | **本轮挂门** |
| partition_manager promote/archive（hot→月分区→归档） | 无门（存量生命周期搬运，非新流量） | 保留 |
| admin/data_lifecycle blobs/attachments（UPDATE 治理） | 无门（运维语义，登记在案） | 保留 |
| admin/credential_success_rate DELETE reset | 无门（管理员显式清理动作） | 保留 |
| storage/sqlite + lite_retention_worker | lite 独立存储平面（写经 lite sink 门） | 不适用 |

**音频计量行的定性**：不走 telemetry 持久化链，是宽表家族最后一个游离在门外的
流量写点。停写后若继续写入会破坏「v1 冻结」不变式，并让一切以 max(ts) 为冻结
点的读侧判据失真。处置对齐 trace.FlushToPG 的 R52 语义：**停写即丢弃**
（计费走 maas/usage_ledger 不受影响）。已登记边界：音频秒数观测停写后无
会话族落点，与探针行同类（「观测对象无 session 家族归宿」桶，主拍板项）。
判据 `domains/streaming/audio_usage_gate_test.go`（pgxmock 零期望 = 停写行为
断言 + 门开前提在场断言；变异验证反转门红）。

**本地库实证**（翻转时刻 2026-10-13 07:01:04）：翻转后 request_logs_hot /
request_logs_bodies_hot **零新行**，session_turns_hot 持续承接新流量。
⇒ 本地「停掉 request_logs 相关写入」在数据层已成立；本轮把代码层最后一个
例外（音频）也收进门内。

## 2. 冻结点原语：db.V1FreezeCutoffSQL（新）

```sql
(SELECT GREATEST(
  COALESCE((SELECT max(ts) FROM request_logs_hot), '-infinity'::timestamptz),
  COALESCE((SELECT max(ts) FROM request_logs),   '-infinity'::timestamptz)))
```

给「性能裁决禁视图、又不能对 v1 腿挂反连接」的读面一个廉价去重原语：会话腿
只收 `ts > 冻结点` 的行。写门开着时 max(ts)≈now()，会话腿近似空集（回滚安全，
行为与纯 v1 时代一致）；停写后冻结点停在翻转时刻，会话腿接管新流量。

**实测修正（判据纪律再 +1）**：镜像行的 ts 是**落库侧时刻**、不等于 v1 行 ts
（本地实测差 ~15s）。冻结点切分单独使用时，翻转边界存在「v1 双子（ts≤冻结点）
+ 晚落镜像（ts>冻结点）」的双计行（真库实测 1 例：request_id 8fbbc828…）。
⇒ **消费方必须同时在会话腿带 NOT EXISTS v1 双子**（canonical 视图同款双
NOT EXISTS，但只作用在冻结点以上的少数行——不是 R36-A1 禁掉的 v1 全量反连接）。
文档与常量注释已按此事实订正。max 必须同盖 hot∪母表：promote 只搬 8h 前的行，
只看母表会把冻结点定在数小时前。

## 3. 读侧迁移（波 2 §5 登记的三项下片债）

### 3.1 admin/tenants.go logsTable（R36-A1 性能裁决读面）

四臂化：两条会话腿（tenant_id/ts 直映列投影 + 冻结点 + 双子排除；**不联
details**——byModel 首选臂 outbound_model 直映 t.model，次选 client_model 投影
NULL 属已登记覆盖边界）+ 两条 R36-A1 原样 v1 腿（hot∪母表、零反连接、零新谓词
——252-dev 30s 超时裁决保持）。credits/byModel/byApp/daily 四组统计停写后由
会话腿继续供数。租户维度索引 idx_session_turns(_hot)_tenant 已在场，谓词可下推。

**真库验证**：30 天窗 default 租户 旧=35,506 行 / 新=37,605 行，差值 = 会话腿
接管的翻转后流量（2,099）；双子排除加入后全表**零双计**（逐 request_id 分组
复核）。分类表改判 silently_empty → **silently_degraded_aggregate**（残余降级
三条已写进 Note：client_model 次选臂 NULL / 音频行不入统计 / 探针不再计入）。

### 3.2 admin/routing.go popularModelsHotSQL（picker usage 源）

listTopModels（波 2）同款改法：FROM 换 `db.SessionFamilyTurnsSourceSQL()`
（hot∪parent + details），模型列回退链 client→outbound→canonical、alias JOIN
键同步加三臂。R50 探针排除谓词保留（会话族本就不镜像探针，退化为防御性 no-op）。
2026-08-26 的视图禁令继续有效（钉桩双向禁：禁视图、禁 v1 hot）。**文件整体
脱离 v1 族**：read inventory / 停写分类 / retirement 分档域（114→113）/
exposure 四登记表同步除名，静默档 61→59（审计文档已带订正批注）。

### 3.3 bg/auto_index_refresher.go（autoroute 候选池核心指标源）

**前置实测（audit 波 2 留的核对项）**：3 天双写对里 v1 名匹配 offers 96.7%，
会话侧 raw_model_name 精确等值仅 **54%**——session_turns.raw_model_name 是剥
provider 前缀的归一化名（`nvidia/nemotron-…` vs `nemotron-…`），**精确 JOIN
此路不通**。改走 alias 链 `lower(t.model) → model_aliases → canonical_id →
model_offers.canonical_id`：恢复到 **96.5%**（与 v1 持平）。t.canonical_id 在
这批行上全 NULL（另一条死路）。

**双源化（half-1 与 half-2 必须同迁）**：
- half-1（真实指标）FROM 改双源：v1 腿原样 + 会话腿（冻结点守卫 + alias 链
  offers 匹配，输出名用 offers 自己的 raw_model_name，与基线键/peak_1m 同名空间）；
- half-2 反连接加同源会话抑制腿——**没有它的话停写后 cold-start 基线会每 5 分钟
  经 ON CONFLICT 覆盖 half-1 刚写入的真实指标**（DELETE+INSERT 两段式下则是
  直接多写基线行）。
- 真库验证：整句 INSERT 事务内执行成功；纯 SELECT 视角确认会话腿产出
  offers 名形态行（`nvidia/nemotron-3-super-120b-a12b`）携带真实 p95，
  无流量对回落基线。
- 5 分钟窗 ≪ hot 保留窗（8h），会话腿只读 session_turns_hot。

**rollupModelTaskIndexSQL 登记不迁**：auto 请求（sys:% 会话）按设计**不镜像进
session 族**（实测 3 天 v1 3,420 行 vs 会话 0 行）——没有会话数据源，与探针
面族同桶（主拍板项）。代码注释 + 钉桩双重登记「不迁」决策，防止后人把这张表
「迁移」成静默空表。

## 4. 判据与变异验证

- `admin/s3_wave3_read_sources_test.go`（tenants 四臂形状 + v1 腿裸性 +
  列契约）：M1 去会话腿红 / M2 去双子排除红 / M3 去冻结点（单腿删除）红——
  M3 第一版判据只查在场被「删一条留一条」骗过（0 红），已加强为 count==2。
- `bg/auto_index_refresher_s3_wave3_test.go`（双源 + alias 链 + 冻结点×2 +
  model_task 不迁登记）：M1 去 half-2 抑制腿红（第一版同样被只查在场骗过，
  加强为 count==2）/ M2 去冻结点红。
- `admin/routing_popular_models_test.go` 重钉（波 2 listTopModels 钉桩同款双向禁）。
- `domains/streaming/audio_usage_gate_test.go`：反转门红。

**判据纪律增量**：形状钉桩对「两条同构腿」必须 count==N 而非 Contains——
只查在场时，删一条腿的变异仍绿，且那正是最危险的静默回退形态
（half-2 抑制腿被删 = 基线覆盖真实指标）。

## 5. 门状态

- `go build ./...` / `go vet`（admin/bg/db/streaming）rc=0；gofmt 改动文件零输出。
- `CGO_ENABLED=0 go test ./admin/ ./bg/ ./db/ ./settings/ ./domains/streaming/
  ./domains/streaming/executors/`：admin 唯一红 = `TestV1BodiesReadersAreAssessed`
  （维护者故意负向闸门，pristine 同红）；bg 唯一红 =
  `TestDirectProbeBodyUsesAnthropicMessagesShape`（**pristine HEAD 同红，既存**，
  与本轮无关——工作区另有并行会话 r1023 的缩进级遗留）。
- 审计网络全绿：read inventory（106 文件）/ 停写分类证据 / tiering 域
  （105/24/16/113）/ exposure / 静默档计数门（59）。

## 6. 登记不动项（本轮新增/维持）

| 项 | 状态 |
|---|---|
| tenants attachTenantUsage7d / getTenant 的视图腿 | 本就有 session 臂（波 2 已核），不动 |
| auto_index rollupModelTaskIndexSQL | 不迁（无会话数据源，主拍板） |
| 音频秒数观测 | 停写后无落点（与探针行同桶，主拍板） |
| promote/archive/data_lifecycle/credential reset | 存量生命周期/运维语义，保留 |
| 波 2 其余登记项（探针面族 / trace_events / expireStaleModels / popularity_tracker / data_lifecycle blob 治理） | 维持 |

## 7. 部署与回滚

全部为读侧 SQL 拼接与一个写点门，无 DDL、无 settings 变更。回滚 = revert 本
提交（写门开着时所有新会话腿近似空集，行为自动回到 v1 单源形态）。
