# Handoff: 存储优化 v2 —— S3 波1 合入 main、合并事故审计修正与本机部署验证

**会话**: 2026-09-15（本机，llm-gateway-go-4 workspace）
**日期**: 2026-09-15
**状态**: ✅ S3 波1 已合入 main（2b8eb0023）+ 合并事故已审计修正（83f19f6c4）+ 本机 2119=83f19f6c 部署健康；⚠️ 灰度开关实测与 252 部署未执行
**运行环境**: 本机 llm-gateway-local-8781/8782 均 2.5.4-83f19f6c-20260915-2119（/healthz 核验）

---

## 结论 / 根因

1. **S3 波1 样板已合入 main**：`feat/s3-wave1-admin-logs-native-turns` rebase 到 main 后合入（rebase 中跳过 6114b9bed 部署产物与 c7ece5fe1 文档两个过时提交——审计确认无内容丢失：脚本在 branch tip 与 main 间 diff 为空，文档 main 侧更新）。合并提交 2b8eb0023，远端已推送。
2. **合并事故（已修正）**：rebase 解决 ledger 冲突时只处理了第一个冲突块（E5 行），**遗漏第二块（Round 2/每日观察段）**，致 3 行冲突标记（`<<<<<<< HEAD`/`=======`/`>>>>>>>`）进入 2b8eb0023 提交。审计 grep 全仓定位后以 83f19f6c4 删除标记（保留 HEAD 侧完整记录），复查确认仓库内无存活冲突标记（历史文档中的引用为事故记录非标记）。**根因：冲突解决后未对整文件复查标记残留即 `git add`；教训：rebase 冲突解决必须 `grep -n '<<<<<<<\|=======\|>>>>>>>'` 全文件复查后再 continue。**
3. **本机部署验证（2119=83f19f6c）**：`deploy-local.sh` 蓝绿部署，8781 先行通过（含凭据解密冒烟 providers=587/creds=7/failed=0）；8782 就绪门首轮超时（readyz database:null，ensure 迁移链与蓝绿锁竞争，plan §4.2-8 已知模式），受控重试后自愈——`database status changed degraded→available`，两端口 /healthz 均 2.5.4-83f19f6c-20260915-2119、ready=true。
4. **部署后健康面**：session_mirror_outbox pending=0（GAP-2 重放器消化正常）；PG 容器日志无网关侧严重错误（仅外部探测 401 认证失败与审计中已修正的探查性 SQL 列名错误）；session_turns 近 1h 无新行属预期（无业务流量窗口）。

## 改动文件与关键行为

| 提交 | 文件 | 内容 |
|---|---|---|
| 2b8eb0023 | admin/logs.go | listLogs/getLog FROM 源经 logsSourceFromSQL() 切换（默认视图形态逐字不变），COUNT/聚合/分组/分页共用同一行集 |
| 2b8eb0023 | admin/logs_turns_source.go（新） | `nativeTurnsReadSetting` + `logsSourceFromSQL()`：开关开→`db.SessionFamilyTurnsSourceSQL() rl`，关→视图；113 列逐列同形，外层零改动 |
| 2b8eb0023 | admin/logs_turns_source_test.go（新） | 形态守卫两则（默认视图 / 原生 turns） |
| 2b8eb0023 | db/request_logs_view_schema.go | `SessionFamilyTurnsSourceSQL()` 导出 710 session 分支投影（视图 ensure 链与读端共享同一列映射契约） |
| 2b8eb0023 | settings/spec_storage.go | `storage.admin_logs_native_turns_read`（默认关、Warning、HotReload） |
| 83f19f6c4 | docs/03-design/04-data-design/storage-observation-ledger.md | 删除 3 行残留冲突标记（Round 2/每日观察记录保留完整） |
| 本轮待提交 | docs/03-design/04-data-design/storage-optimization-plan.md | §4-S3 行补波1 合入状态与开关说明 |
| 本轮待提交 | VERSION / version.json / web/public/version.json / web/public/menu-config.json | 部署产物同步：2119=83f19f6c（版本身份入库，menu-config 仅构建时间戳） |
| 本轮待提交 | docs/handoff/20260915-s3-wave1-merge-local-verify/ | 本 handoff |

## 测试命令与结果

```
go test ./admin/ -run TestLogsSourceFromSQL -count=1   # 合并后复跑 PASS（2/2）
go build ./...                                          # PASS
bash scripts/deploy-local.sh deploy                     # 8781 ok（凭据解密冒烟 providers=587 creds=7 failed=0）；8782 首轮就绪门超时后受控重试自愈，/healthz 两端口均 2119=83f19f6c ready=true
curl -s http://127.0.0.1:{8781,8782}/healthz            # version/git_sha/build_seq 三元组与 main HEAD 一致
grep -rn '<<<<<<<|^=======$|>>>>>>>' docs/ admin/ db/ settings/ scripts/audit/   # 无存活标记
psql: session_mirror_outbox pending=0                   # GAP-2 重放器健康
```

## 遗留风险 / 未决

1. **灰度开关未实测**：`storage.admin_logs_native_turns_read` 的 API A/B 对账（native_only=0）与 Web UI 日志页/详情弹窗验证因验证窗口内无可用 API key、无业务流量未执行——下轮部署窗口用 admin UI 建 key → 发测试请求 → PUT 开关 → 复测。
2. **252 生产部署未执行**：本机验证通过即可按 `deploy-to-252` 惯例流程执行；252 侧应用 706-713 前仍须复核 default 分区分布（plan §4 P0 既有约束）与双账本编号。
3. **观察期计数**：连续归零自 2026-09-16 每日轮起算（Day1），达标 earliest 2026-09-22；cron automation-15b858fd 每天 09:00 自动跑 `storage_observation_round.sh`，任一日 FAIL 计数清零。
4. **claim 置位 is_final_success 结构性漏镜像**（~3 行/天，台账 09-15 条目）仍开放：修复候选①claim UPDATE 补发 mirror 触发 ②登记例外类 E6 ③每日回填兜底常态化——S4 停写前须评估定案。
5. **8782 首轮就绪门超时复发面**：与 plan §4.2-8 蓝绿 ensure 锁竞争同根，受控重试可自愈；如再现 2/2 全失败须人工单独重启 standby。

## 下一轮提示词

> 继续 storage-optimization-plan.md §4-S3 波1 灰度实测与 252 部署：
> ① 本机 2119=83f19f6c 已部署（8781/8782 健康）。用 admin UI 登录 8782 建 API key，发 3~5 条测试请求（含多轮会话与无会话头流量各至少 1 条），验证 session_turns/session_bodies 落行；
> ② PUT `storage.admin_logs_native_turns_read=true` 后：API 层近 3h 窗口 113 列全形状 EXCEPT 双向对账（期望 native_only=0、view_only 全为已知类）；Web UI 请求日志列表/详情弹窗实测（browser-use 或手动）；
> ③ 全部通过后按脚本流程部署 252（先复核 default 分区分布与双账本 706-713），生产侧同款 A/B 对账后保持开关灰度；
> ④ 约束：对账一律父表∪hot 双侧；对账前核对 /healthz 构建身份与 main 对应；观察期每日轮照 cron 继续累计，GLOBAL_G2>0 时按台账速查处置（pending 等 60s、dead 列明细、必要时重跑 mirror_outbox_backfill.sql）。
