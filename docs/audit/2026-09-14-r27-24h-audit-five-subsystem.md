# R27 · 24h 修正审计轮（五子系统并行 + 修复落地）

- 日期：2026-09-14
- 审计范围：dafcbc873..41b529de5（24h 内 45+ 提交，含一次 origin/main 同步合并 af7a41248 与并行会话部署修复 490e8e989）
- 方法：包级代码图谱（go list 依赖投影，~119 domains 子包）→ 主代理 + 6 个只读子代理并行审计（bg 探针调度 / streaming+IR 协议转换 / 供应商错误链+分区存储 / admin+凭据写入 / web 前端 / installer+SQL 同步）→ 汇总分级 → 按逻辑单元修复、测试、提交、推送。
- 修复落点：89ec7f8f8（streaming）、381504e4e（web）、9ad8bf28a（admin）、5ab158f76（bg）、41b529de5（errorsx/sql）。

## 一、合并轮（会话前半）

- 起点为进行中的 merge（MERGE_HEAD=8e127fa24，89 文件已解决，剩 1 个 UU handoff 文档）。
- handoff 文档冲突取远端超集版本（含 870fac658 惰性 plan 下限补记）。
- 合并门禁 6 项全绿后提交 af7a41248 推送；期间发现 pre-commit token 合规真实拦截了
  AnnotationStatsView 两处 var(--token,#hex) 兜底（修复后入合并提交）。
- 并行会话同期落 367601764（merge）与 490e8e989（703 clobber 链补登 + readyz 诊断），
  后者恰好是子代理 F 独立发现的 P0（详见 §三 F-P0-1），已随之推送。

## 二、已修复（按严重度）

### P0（部署阻断，F 轮发现）
- **703 未登记 clobber chain → 升级通道预检必然 exit 5**：所有部署卡 pre-flight。
  并行会话 490e8e989 已补 6 行注册；本轮补齐根因——shell 契约测试此前仅 `bash -n` +
  grep 抽查，现提取部署脚本真实守卫段 eval 执行（阴性测试复现 703 事故 exit 5），
  并新增目录驱动不变量：最大编号 startup 迁移必须在 files=() 中（防 704 漏登复发）。

### P1
- **writeHealth 硬配额守卫吞失败簿记（A-P1-1）**：硬配额行（permanently/balance_exhausted）
  探测失败时主 UPDATE 被 WHERE 拒成 0 行，last_probe_at/失败计数不前进 → BalanceQuotaProbe
  指数到期闸失效，2min tick 每 tick 重打死上游（720 次/天/凭据，f8322dc04 R4 对该类行不成立）。
  修法：0-rows 分支补簿记 UPDATE（仅梯子+可用性列，quota_state/lifecycle/auto_* 不动），
  源码钉桩测试锁定。
- **Responses 桥 arg-first 悬空 delta（B-P1-1）**：name 未到达的 argument 片段被序列化为
  function_call_arguments.delta，引用永不开项的 item_id，且终态守卫再剔除 → 客户端工具调用
  静默丢失。修法：responsesToolHold（平衡缓冲 + name 到达重放，1MiB 有界），两桥接入，
  anthropic_stream.go pendingArgs hold 同型。
- **color:check 兜底盲区（E-P1-1）**：扫描器把 var(--token,#hex) 兜底剥除后再扫，被禁模式
  对门禁不可见；42 处 hex 兜底 + 12 处 rgba 兜底存量全部漏报。修法：stripVarFallbacks 纯函数
  （平衡括号，支持嵌套 var/十进制三元组）提取兜底并上报 hex/rgba 字面量；54 处存量剥为裸 var
  （行为保持）；color:check 接入 verify.sh --web（此前无自动执行载体）。

### P2
- force_enable/reset-state 部分失败审计缺口（D-P2-1）：tx.Commit 后 URSM 失败 → DB 已落地
  却 500 无审计。db_committed 分界标记 + 错误分支补写 audit_outcome=partial_failed。
- 假成功空响应（B-P2-3）：writeFinalEvents 全部工具项被空名守卫丢弃且无文本 → 终态降级
  incomplete（桥路径经空流门走 failover，scaffold 级守卫为纵深防御）。
- stop_reason 语义矛盾（B-P2-4）：仅剩被丢弃 nameless 调用时不再报 tool_calls/tool_use。
- BalanceQuotaProbe 无重入守卫 + 两探针 tick 无 panic guard（A-P2-2/A-P2-3）：lifecycleMu +
  顶层/per-tick recover。
- 间隔解析溢出（A-P3-5）：balance_floor_guard/periodic_quota_probe 分钟数路径大数溢出为负
  duration → NewTicker panic → guard 静默死亡；加 parsed>0 复检。
- scan_scheduler 每轮无预算/首轮失败 6h 盲区（D-P2-2）：60s cycleTimeout + 30s/60s 有界重试；
  Stop 先于 Start 不再触发首轮扫描（A-P3-1）；Status 增 started 字段（A-P3-2/D-P3-7 日志去重）。
- model_probe recoveringSweeper per-tick recover（A-P3-3，该循环是 recovering 行唯一驱动）。
- supplier_errors months CTE 补 ORDER BY 1（C-F-2，六副本同步 + 契约测试）。
- scan-scheduler status 收敛 superAdmin（D-P3-3，last_error 跨租户信息面）。

### P3
- 701 补 schema_migrations 双账本自登记（F-P3-1）；escapeTenantID 死代码删除（D-P3-6）；
  errors.Is(pgx.ErrNoRows) 替代字符串匹配（A-P3-4）；planTypes 六死键 8 locale 清理 +
  floorPercent 八语文案语义修正（E-P3-7/E-P3-9）；HTTPStatusForKind godoc 归位与表述修正
  （C-F-1/C-F-4）；pre-commit token 检查豁免 *.test.ts 断言夹具。

## 三、登记遗留（下一轮候选，均有证据未修）

| # | 级别 | 内容 | 证据 |
|---|------|------|------|
| 1 | P2 | transformation/anthropic 第三条死桥未退役（commit 97aa179ab 声明与事实不符）：anthropic_stream.go(StreamOpenAIToAnthropicSSE 7 参版)+split test+pendingCapturer 副本，缺 nameless lazy-open 修复 | domains/transformation/anthropic/anthropic_stream.go:30 |
| 2 | P2 | survival 模式 committed breach 双终态：response.completed(incomplete) 后再 response.failed | responses_bridge.go:801-806 + survival_wiring.go:257-260 |
| 3 | P2 | 三协议 integrity-breach 终态矩阵不对称（仅 Responses 格补齐；chat/anthropic 桥仍硬截断无终态） | anthropic_bridge.go:1034、stream.go:978、anthropic_stream.go:802 |
| 4 | P2 | plan 厂商"币下限已清+plan 下限在+plan 探测持续失败"三维全堵永久卡死；建议 plan_quota_checked_at 落后 N 小时逃生门 | balance_floor_guard.go:557-564,874-879,695 |
| 5 | P2 | probe writeHealth 与 writer RestoreOnSuccess last-write-wins，可静默回退 P2 closeout 效果一次；建议 state_updated_at 条件写 | credential_probe_v2.go:1152-1266 vs writer.go:73-105 |
| 6 | P2 | supplier_errors FORCE RLS 依赖 SUPERUSER 角色兜底；NOBYPASSRLS 部署下写入/promote 静默失效，建议 set_config('app.bypass_rls') | V371:90-97 vs supplier_error_logger.go:44-101 |
| 7 | P2 | supplier_error_stats 聚合器只读 hot 表，聚合器停摆 >8h 产生永久统计洞 | supplier_error_stats_aggregator.go:59 |
| 8 | P2 | embeddings 400/402/408/422 原样转发单供应商错误体且不 failover，与 classify.go 契约声明相悖 | embeddings.go:196-227 |
| 9 | P2 | color:check 不覆盖 .vue template/script 段与跨行 rgba；基线 44 条无 reason 字段且 update 会抹手工字段 | color-token-audit.mjs:94 |
| 10 | P2 | pendingArgs 无上限累积（anthropic_stream.go:716），违反本仓有界 accumulator 纪律 | streaming |
| 11 | P3 | 非流式 nameless 丢弃无旗标/finish_reason 不同步；两侧 incomplete 终态形状不一致（缺 incomplete_details.reason）；embeddings Retry-After 与终态家族可错配 | response.go:540 等 |
| 12 | P3 | sweepPlanQuotas 失败凭据不 bump checked_at 可饿死健康凭据；ScanScheduler 无 Prometheus 指标；quota floor 对无探测能力供应商静默空转（UI 文案过度承诺）；floor 输入无客户端预校验；主题缺系统偏好变更/跨标签页同步路径；701 installer 侧无 StartupFiles 条目（fresh install 不盖账） | 各子代理报告 |

## 四、闭环验证结论（对照审计维度）

1. **流程/数据闭环**：probe v2 摘出/回池对称、credential_recovery 各恢复块链路完整、
   balance_floor_guard pull/restore 带 ownership 校验（A 轮确认）；supplier_errors
   写入→promote→分区→凭据详情→web 展示全链闭环（C 轮确认）；本轮修复补齐硬配额行
   簿记断点。
2. **IR 定义与传输转换**：Response IR 覆盖 content/tool_calls/reasoning/usage/unknown-blocks；
   三协议×流式/非流式矩阵本轮补齐 Responses 桥 arg-first 格与退化终态，其余格登记 §三.3。
3. **创建/赋值/解析/存储/序列化**：桥侧有界 accumulator 纪律维持；pendingArgs 无界为唯一
   例外（§三.10）。
4. **多轮与轮次摘要**：requestjourney/sessionmeta/sessionaudit 链路本轮零改动；
   tool_name_never_arrived 旗标经 audit.go→reqLog.QualityFlags 落 request_logs 验证通畅。
5. **队列/并发/安全**：bg 三 worker（balance/periodic/floor-guard）panic 守卫补齐；
   scan_scheduler 重入/预算/退避闭环；无锁内阻塞调用（A 轮全量核对）。
6. **分区存储**：hot 8h + columnar promote 原子 CTE（FOR UPDATE SKIP LOCKED→DELETE
   RETURNING→INSERT）幂等不丢不重；703 时区钉扎必要性成立；promote_supplier_errors
   与 ensure 分区边界时区一致（C 轮确认）。
7. **供应商错误处理**：错误表（supplier_errors 双账本）+ 凭据详情呈现 + 流式
   FailoverNotices 不中断通道均闭环；无备用时的错误返回按协议族映射（chat 429/503、
   anthropic 503 overloaded、embeddings 四族）。
8. **双存储架构/auto 模型**：本轮范围无改动，未审计（登记为后续轮次范围）。
9. **门禁/可观测性**：六门禁本轮全部真实执行过（含两次真实拦截）；color:check 补入
   verify.sh 后七门禁有自动载体。

## 五、验证记录

- 每修复单元独立提交，pre-commit 六门禁逐次全绿（多次真实拦截/放行记录见提交历史）。
- 全量 `go test ./...`：270 包 ok，0 失败（41b529de5）。
- web：vue-tsc、vitest（color-audit 11 用例、i18n parity 6 用例）、color:check strict PASS。
- installer 独立 module go build/test 通过；deploy_readiness_contract_test 11 项 PASS。
- 推送：490e8e989..41b529de5 → origin/main。
