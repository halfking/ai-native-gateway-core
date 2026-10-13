# S3 波 2（第一分片）：request_logs 停写后的读侧读源迁移

> 轮次：2026-10-13，S4 停写切换（07:01 翻转 `storage.request_logs_write_enabled=false`）
> 之后的第一个读侧改造波次。生产 154/245/252 **未翻转**，本轮全部改动随代码发布，
> 行为由写门在运行时决定（写门开 = 旧行为/回滚路径，写门关 = 会话族源）。

## 1. 背景与判据

S4 停写后新流量只落 session_turns 家族；canonical 视图
`request_logs_with_current_month`（118 列契约）= session_turns(_hot) 投影
UNION v1 冻结体 × 反连接。直读 v1 底表（request_logs/_hot/_bodies*）的读面
随热窗 promote 排空逐窗变空——AGENTS.md 登记的「约 35 个直读面」即本轮范围。

迁移判据沿用守卫规则（`admin/session_view_dependency_risk_test.go`），
按**谓词形态**分派目标源：

| 形态 | 目标源 | 本轮读方 |
|---|---|---|
| A：gw_session_id/session_id 会话内谓词 | `SessionFamilyTurnsForSessionSQL()`（谓词内推，idx_session_turns_session） | summarystore 两门、request_summary、goal/history_store、LastOutboundForSession、lookupTurnNumber |
| B：request_id 反查 / 时间窗聚合 | canonical 视图（唯一完整源：会话臂新流量 + v1 臂历史 + 反连接去重） | waterfall×2、diagnostics、providers、swim_lane、work_types、request_trace(summary/origin)、provider_models(count)、forensics ListRecentSessions、minute_aggregator、db.routingAnalyticsMVSQL |
| 性能裁决在案（禁视图） | 会话族原生源拼接（`SessionFamilyTurnsSourceSQL`，无列式臂、无反连接） | logs.listTopModels、model_alternatives.usage_7d |
| 热表点查（窗口 ≤ 热表保留期） | 双热腿 UNION（单次往返，回滚期两腿都供数） | telemetry.FindRecentGatewaySession、db.recent_success_rate |

## 2. 修复清单（16 个生产读点）

**热路径/核心功能：**

1. `telemetry.FindRecentGatewaySession`（会话粘滞，请求热路径）——双热腿
   UNION；session_turns_hot 的 api_key_id/identity_hash 由 707 S1a 供列、
   idx_session_turns_hot_ts 供 5 分钟窗。
2. `telemetry.lookupTurnNumber`（outbox turn_no）——会话族 MAX(turn_no)
   权威读，0 回退 v1 母表 30d 计数；S4 后新会话序号不再恒 1。
3. `internal/summarystore` CountNewTurns/CountTotalTurns（自动摘要两个门槛）——
   会话族优先、0 行回退视图；停写后摘要不再停摆。
4. `domains/hooks/goal/history_store.FetchBySession`（审计钩子历史重建）——
   turns×bodies 两源拼装（request_id 单键 JOIN），0 行回退 v1。
5. `domains/analysis/request_summary.loadRequestLogs`——ForSession 源；
   顺带修复「只读母表、hot 行今天就漏」的既有缺口。
6. `cmd/gateway/main_v3_wiring.LastOutboundForSession`（压缩 L3 冷启动）——
   turns 元数据 + session bodies 的 outbound_body 两源拼装，v1 回退。
7. `cmd/gateway/waterfall_by_request / waterfall_db`（调度瀑布）——视图化；
   t0..t9 会话侧直映（513 起）。
8. `db/db.go recent_success_rate`（凭据健康核心函数，运行时 SSOT）——双热腿
   取样；修复 `BelowThreshold` 恢复机制停写后全体失明（停写分类表登记的
   主失效源）。
9. `db/db.go routing_analytics_source`（运行时 SSOT）——重建为 canonical
   视图单源；matrix/flow/funnel 端点与 7d 物化视图不再随热窗归零。

**仪表盘/诊断族（视图改指）：**

10. `admin/swim_lane_init`（看板泳道）、11. `admin/work_types`（×4 统计 +
    「指定模型」判据扩 client→outbound→canonical 回退链）、12. `admin/providers`
    （error_rate_24h）、13. `admin/diagnostics_credential`（近期失败样本）、
    14. `admin/request_trace`（summary + probe origin 两腿；trace_events 腿
    保留 v1，见 §4）、15. `admin/provider_models`（count 腿从裸母表改视图，
    修复既有的 total 低估）、16. `domains/sessionforensics.ListRecentSessions`、
    `internal/quality/minute_aggregator`（分钟质量聚合，写目标不受停写门管）、
    `domains/streaming/model_alternatives.usage_7d`（失败路径热度，会话族原生源，
    顺带修复 7 天窗只读 hot 的既有缺口）。

## 3. 单元测试与变异验证

新增 8 个形状钉桩（判据同层：断言驱动包级 SQL 常量/变量——它们就是发给
PostgreSQL 的文本）：

- `db/db_recent_success_rate_body_test.go`（双族取样/守卫 CAST/探针排除×2 腿/
  取样口径）+ `TestRoutingAnalyticsSourceServesSessionFamily`（15 列契约）
- `domains/hooks/observability/telemetry/s3_wave2_read_sources_test.go`
- `internal/summarystore/s3_wave2_turn_count_sources_test.go`
- `domains/hooks/goal/s3_wave2_history_sources_test.go`
- `domains/analysis/s3_wave2_summary_source_test.go`
- `internal/quality/s3_wave2_minute_metrics_source_test.go`
- `domains/streaming/s3_wave2_model_alternatives_source_test.go`
- `cmd/gateway/s3_wave2_waterfall_sources_test.go`

**变异验证（判据有牙）**：① 删除 recent_success_rate 会话腿 → 判据红 ✓；
② waterfall 点查退回 request_logs_hot → 判据红 ✓。③（摘要腿替换式变异因
类型不合法作废，未计入。）

**门状态**：`admin` 全包唯一红 = `TestV1BodiesReadersAreAssessed`（维护者
故意红的负向闸门，pristine HEAD 同红，不修不加白名单——维护者策略）；
`db / telemetry / summarystore / goal / analysis / quality / sessionforensics /
streaming(152s 全包) / cmd/gateway(形状钉桩)` 全绿。cmd/gateway 的
LiteRequestLogSink 系列在 CGO_ENABLED=0 下不可运行（SQLite 驱动限制，文档
载明的环境边界，与改动无关）；CGO 开启时该环境触发 go-m1cpu cgo SIGSEGV
（同为已知环境问题）。

## 4. 审计网络同步（全部门复绿）

清单 108→**106**（request_summary / model_alternatives 整体脱离 v1 族，除名）、
分档域 116→**114**、间接总体 24 不变、未判定基线 48→49（+candidate_failure_monitor，
并行会话引入的 Tier ③ 读方成员补记；+db/db.go 回落未判定态）、盲区桶 37→**46**
（9 个视图化文件迁入）、控制面表/停写分类表/breaker/cutover/补位列豁免逐条
改判并留痕（受影响条目的 Note 均带 2026-10-13 批注）。停写分类的静默档口径
69→**61**（四条改判 unaffected、两条除名、两条改判 degraded_aggregate），
`2026-09-30-session-request-data-re-audit.md` §静默档行已带订正批注，
`TestAuditDocSilentClaimMatchesRegistry` 钉住。

## 5. 登记不动项（本轮审计确认，不改）

| 项 | 理由 |
|---|---|
| `admin/tenants.go` logsTable（裸 hot∪母表） | R36-A1 实测裁决：视图反连接 + 租户 COUNT 252 上 30s 超时。停写后 stats 对新流量渐进全盲——**已知债务**，需专门分片（会话臂物化或租户维度索引）而非盲翻 |
| 探针面族（model_probe / credential_selfcheck / credential_recovery / today_success_probe / model_tier / providerprofile.adapters / anomaly_harvester / recommend_v2 / quality_correlations / provider_diagnose / integrity_fingerprint_probe / ledger_reconciliation / candidate_failure_monitor） | 观测对象（探针/自检流量行）按设计不进会话族，S4 后宽表族无处落行——改读视图救不回观测对象；正确出路是逐个改读探针专属表（node_probe_runs 等）或随 v1 退役，**属主拍板项** |
| `admin/routing.go` popularModelsHotSQL | 同 listTopModels 病（会排空），但有 Redis live 热度腿兜底；改写涉及 model_aliases 精确匹配键的会话侧语义，留待下片（钉桩 TestPopularModelsHotSQL_TargetsHotTable 保持原状未动） |
| `internal/trace`（FlushToPG 写门内 / LoadFromPG trace_events） | trace_events 不在 118 列契约（镜像从不写、视图刻意不投影），结构性缺口；新请求链路事件以 Redis 为唯一来源，trace 家族迁移是独立工作项 |
| `discovery.expireStaleModels` | 已有 S4 护栏（staleExpiryMayRun），停写期安全；长期需 raw_model_name 回填（独立事项） |
| `domains/credentialstate/popularity_tracker` | 包已声明被 URSM v2 取代（仅 legacy/off/canary 模式存活）；created_at 不在契约，随包退役 |
| `admin/data_lifecycle_*` 清理/体量面 | metrics 是 v1 存储体量语义（定义如此）；bodies/attachments 的 blob 治理对停写后新正文是静默 no-op（容量治理需另行设计），execute 的清理写属运维语义不受停写门管 |
| `bg/auto_index_refresher` | 5min rollup + 模型名精确等值 JOIN；会话臂 outbound=t.model 归一化口径会失配（降级到 cold-start 基线，非崩溃）——改指前需真库核对名字匹配率，留待下片 |
| `admin/credential_success_rate` 的 DELETE reset | 写/清理路径，不受停写门管；oldest_request_time 子查询已在视图上 |
| 已验证正确的既有形态 | unified_detail / body_resolver / session_sanitize_matches / credential_monitor（长短窗+快路径）/ heatmap / session_detail_v2 / session_tenant / auto_route_affinity_worker 三腿 / 全部写路径（写门齐全） |

## 6. 已知语义边界（登记，非回归）

- **734 details 特征层依赖**：会话臂的 client_model/provider_id/attachments/
  quality_flags 等 30 列由 details 层供值；缺 details 行的会话轮在这些列上
  为 NULL。真库实测覆盖 99.9996%（model_status 条），残余缺口已逐点登记。
- **work_type 会话侧近乎恒空**：work_types 的 by_work_type 桶对停写后流量
  退化（task_type 桶不受影响）；「指定模型」判据已扩回退链兜住非 auto 流量。
- **会话族口径不含探针/内部回环**：热度/摘要/统计类读面的总体因此更干净；
  对「必须含全部流量」的 B 形读面一律走视图（v1 臂服务停写前历史）。
- **瀑布/诊断类视图点查的成本**：ORDER BY + LIMIT 由面板 LIMIT 兜住；
  252 规模若成瓶颈，参照 credential_monitor 的 ≤120min 双热腿快路径分波补。

## 7. 部署与回滚

- 全部读源切换都受 `storage.request_logs_write_enabled` 直接或间接约束：
  写门开（回滚）时旧行为保持（hot 快路径/v1 腿自动恢复供数）；写门关即
  本轮新行为。无 DDL 迁移（routing_analytics_source 与 recent_success_rate
  由 boot 时 CREATE OR REPLACE 自愈到新形状）。
- 回滚 = 翻回写门 + revert 本提交；无 schema 变更需要回滚。
