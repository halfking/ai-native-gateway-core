# R50 审计轮 Handoff（2026-09-21）

> 完整留档：docs/audit/2026-09-21-r50-48h-audit-round.md（§一 交付明细、
> §二 测试与验证、§三 遗留台账、§四 纪律注记）。

## 本轮收口

- **F14** role winner 进 failover 恢复链：decision_v2 `withRoleFailoverHead`
  （rolePrefs∩候选池按 role 顺序置顶，tier 计划保序去重），handler 消费面零改动；
  修复前必红钉桩在案。
- **F15** sessions 三列写入方七处布线（采集→telemetry Mirror-only 传输→镜像桥→
  writer→outbox 编解码→upsert）；agent_role 冲突臂"默认可精化"语义
  （真库演练抓到归一默认 'main' 覆盖首写角色的 P1 后修正）；parent 双列
  NULLIF 归一保部分索引 NULL 语义。730 两条索引结构性豁免（真库实证
  已存在且 valid，重建纯风险，理由留档）。
- **F17** 验证态收口（并行轮 53bc87952 已交付，真库表形一致）。
- **F19+F13** modelname 包收敛完成：foldedNameExpr 单一手写点 +
  JunkSeedGuardSQL 字节级钉 + DedupCanonicalNameSQL + createModel 23505 兜底；
  行为级集成测试（含 disabled 占名、glm5.3 契约边界）。
- **F20** INV-2 ×4 行形态 + INV-4 ×3 凭据形态真库行为钉桩
  （testcontainers PG16 + 本地 PG17 演练双证）。
- **门禁收口**：installer 五点补课（728 豁免名单 / 733+734 五点同步）。

## R51 入口（优先级顺延）

1. **归一化数据对账**：6 组折叠重复对（双侧活跃引用，引用数清单在审计文档
   §一.4）→ cleanup 脚本（备份+重定向+V 校验，F8 先例）→
   `uq_models_canonical_active_folded_name` 表达式唯一索引。索引表达式与
   modelname.DedupCanonicalNameSQL 的 run-collapse 臂逐字一致。
2. **admin pin 翻盘后 failover 头部错位**（F14 同型存量，先审计）。
3. **schema_migrations 缺 730/733 注册行**（本地实查；幂等自愈，252 侧核对）。
4. **运维**：252 侧跑 approval pending 查询 + probe 占比 SQL（审计文档 §三.4
   已留）；**70/71/72 × glm-4-flash 三条 auth 失败绑定建议裁决停用**。
5. F18 / F21 / F22 P3 / 727+730 契约测试按 726 模板——R49 §三顺延不变。

## 共享工作区提示（给下一轮）

本轮窗口内并行会话在同一 worktree 高频活动（discovery 包起落、match.go、
canonical follow-up）。提交前：`git status` 逐文件核对归属；共享门禁红
（sqlguard/installer/契约）先归因再动手，他会在途文件不越权修改；
sqlguard 当前红 = discovery/discovery.go 的 Go `//` 注释落在 SQL 字面量内，
归并行会话。
