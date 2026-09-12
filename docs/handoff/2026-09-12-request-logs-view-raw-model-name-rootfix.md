# handoff：request_logs 视图 raw_model_name 根修（迁移 700）与共享工作区 LF 治理

日期：2026-09-12
基线：2089/dcd4ea46f 审计轮之后的独立项轮；合并 origin/main 至 7606f7a33 后实施。
主题：① 696 drift scanner 本机 42703 WARN 根修（升级为仓库级缺陷修复）；③ .gitattributes 强制 LF；② 生产发布窗口维持 deferred。

## ① 根修：700 视图补 raw_model_name

### 与并行指纹工作流的 handoff 对齐（接手前核查，按任务书要求）

- 指纹线（83bf582dd 696 视图 / d03f0ada4+8af09c799 697 写路径）交付状态：696/697 已于 2026-09-12 05:33/05:58 经通道应用于生产 252 PG（双账本对齐）；installer 接线缺口已由该线在 origin 上自修（1f17dc2ac）；后续 promote 时区钉定挂 698（cf6eb457f）。**该线 696/697 轮已收尾，无在途声明与本项冲突。**
- 但其交付存在一个未被发现的双缺陷（本轮实查取证）：
  1. **83bf582dd 把 scanner 读面切到视图，而视图从未暴露 raw_model_name**——scanner SELECT `raw_model_name` 每周期 42703。696 只补了 fp 一列。基础交集包装停在 pre-485（485 才给父表加 raw_model_name），577/610/696 的 lateral 从不重建基础层，680/db.go 自愈只救"canonical 缺失"不救"陈旧"。
  2. **live round-trip 契约测试自 696 起对真库即失败**（scratch 表无 fp 列，自愈 DDL 引用 h.system_fingerprint 42703；测试 gated on DSN、CI 离线跳过，故一直未暴露）。
- 影响面实查（只读）：本机 2089 部署 active WARN（启动 + 每小时）；**生产 252 视图 112 列、raw_model_name=0、fp=1，与本机同构——潜伏缺陷**，生产现跑 2086/2087 二进制未携带切视图 scanner，下一个携带 83bf582dd 的生产二进制上线即复现。

### 交付（详见 changelog 同名文档）

- 迁移 700（696 同款双形状幂等，同一 lateral 保留 fp 不回退 696）+ 文本守卫测试（含通道钉）+ db.go 自愈镜像条件化（修上述缺陷 2）+ installer 四点接线（embeddata 字节一致副本）+ 通道脚本条目。**bg/ 零改动**（读面守卫继续钉视图）。
- 编号核查与撞号改判：07:52 按当时生产双账本实查取 699（698/699 均空闲）；提交后与并行线撞号——该线在会话窗口内推送 2ad8d64ad（**698 promote 时区钉扎**，即 cf6eb457f 预留项落地）/ 531ea1a86（**699 supplier_errors ensure 钉扎**），先行入 origin。本迁移**重编号 699→700**，重编号前复核生产双账本 698-702 均 0 命中（两线均尚未应用生产，撞号发生在仓库存号层）；后续新迁移编号核查须在 fetch 后加 `git show origin/main:sql/migrations/startup/` 仓库存号复核。
- 本机 DB 修复：备份（envs/local/backup-llmgateway-viewfix-20260912-075826.sql）→ 单事务（696 预期 no-op + 697 本机补课 + 700 + 双账本登记）→ 事务内六项复核（113 列 / raw×1 / fp×1 / scanner SELECT 解析 / promote fp×3 且 695 自愈保留 / 样例可读）→ COMMIT。本机 sequences 账本原有 696/697 标记而 schema_migrations 无行的错位一并归齐。
- 运行中 2089 二进制零重启受益；WARN 停止以本机日志整点周期复核为准（见下）。

### 给并行线/下一轮的交接点

- **生产约束**：700 进 252 之前，**不得向 245/154 发布携带 83bf582dd 的二进制**（scanner 在生产必 42703）。700 与 ②（生产发布窗口）绑定：下次生产发布 = 通道先 700、再上二进制。
- 698/699 已归并行时区钉定线（2ad8d64ad/531ea1a86，promote 9 函数 + supplier_errors ensure；其 698 体 = 697 体加钉扎）。**本轮发现并补齐其 699 的通道接线缺口**：531ea1a86 只接了 installer embeddata/dbinit 路径，漏了 apply-db-revision-sequence.sh——升级型数据库（共享 252 生产 PG 即是）经通道升级永远轮不到它，恰是其自身测试注释所述 693 类缺口。已补通道条目 + intentional_function_chains 登记（`ensure_supplier_errors_partition|V371...|699...|`，守卫按设计拦截后按提示登记），本机通道复跑已应用并验证（函数体带钉、直接调用返回正确分区名、双账本齐全）。生产 252 的 698/699/700 均待下次发布窗口经通道应用。
- 视图基础层仍是 pre-485 冻结交集（父表 48 列不在视图上），同类"读者需要基础列"缺陷可能再发；治本选项 = 680 形态的"陈旧也重建"变体迁移，未在本轮实施（风险面大，需专项轮）。

## ③ 共享工作区 LF 治理

- 本轮开工实查：工作区 33 文件整文件 CRLF 重写（86681 插入/86681 删除对称），`git diff --ignore-cr-at-eol` 全空——**零内容变更的纯行尾污染**，即 48fb5ba81 记录的"merge blocked by in-flight edits"的真实成分。已验证零信息损失后恢复。
- 新增 `.gitattributes`：文本统一 LF（add 时自动归一，杜绝整文件重写提交）；既有 CRLF 内容（pricing CSV、vendor/）豁免保字节；.bat 检出 CRLF；.lnk 二进制。合入后 status 干净，无 renormalize 副作用。
- 本地 main（2089 轮五提交）已与 origin/main（7606f7a33）合并并推送——48fb5ba81 的 pending push 解除。提交归属按文件核对：33 个污染文件恢复属零内容操作；本轮自身提交逐文件列出。

## ② 生产 245/154 发布窗口

维持 deferred（上轮决策；无运营报障输入不自动执行）。就绪条件：本修复合入 origin/main 后，发布流程 = 252 通道应用 700 → 蓝绿上携带 696/697/700 全套与 2089 审计轮的二进制。触发条件见任务书（运营报障则提前）。

## 验证记录（如实区分）

- **通过**：live round-trip 双形状场景（真库 scratch）、冻结链重放 scanner 形状 SELECT、文本守卫（700 + 既有 695/696/697）、installer 全套、`go build ./...`、`go vet ./db/`、`./db/ ./sql/migrations/... ./bg/ ./deploy/grafana/ ./deploy/prometheus/...`、本机 DB 单事务修复六项事务内复核、双账本登记复核、生产 252 只读取证（视图 112 列 raw=0；账本 698/700 空闲）。
- **环境受限**：无（本轮全部验证在本机真库完成；未依赖 Windows 副本口径）。
- **未验证**：生产 252 的 700 应用（等发布窗口）；本机 WARN 停止的整点日志复核（扫描周期 1h，修复后首个整点待观察；修复事务内已验证 scanner 形状 SQL 可解析执行）。

## 审计轮（2026-09-12，针对本文件所述交付的自审计与修正）

- **发现 1（实质，已修）：迁移 700 缺 schema_migrations 自插登记。** 通道（apply-db-revision-sequence.sh）的 pending 判定只读 `gateway_db_revision_sequences`（per-file marker），自身不写 schema_migrations；698/699 文件尾部自带 `INSERT ... ON CONFLICT (version)` 自登记，而 700 初版没有——升级库经通道应用 700 后双账本将错位（schema_migrations 缺 700 行），恰是本轮在本机花一整段修复的错位形态。已补自插语句（no-op 守卫路径也执行，698/699 同款），`migration_700_test.go` 增两条文本钉。**附带发现：696/697 文件同样无自插**（生产行的登记来自指纹线当年手工应用）——不代改，留档给该线；后续轮次如触碰 696/697 文件应顺手补齐。
- **发现 2（机制澄清，无缺陷）**：通道复跑时 700 文件的真实执行曾在 2091 部署时被跳过（我先行手工登记 sequences marker 触发 pending 跳过，视图效果已在但 COMMENT/自插未执行）。本轮退掉手工 marker → 通道真实执行 700 文件（守卫 no-op + COMMENT 落 + 自插生效）→ 本机账本完全收敛于通道路径。过程中 `INSERT 0 1` 命令标签一度疑似矛盾，rollback 包裹探针实证：**该 kx-citus-pg17 构建对 `ON CONFLICT DO UPDATE` 也报 `INSERT 0 1`**，非账本异常。
- **发现 3（观察，不处置）**：gofmt -l 标出 `migration_694_behavior_integration_test.go`（并行线 0db9d9b7e 交付）与 `migration_627_630_contract_test.go`（预存）未格式化——非本轮改动面，不代改以保持提交归属清晰。
- **发现 4（部署脚本粗糙边，留档）**：2090 部署时 8782 `/readyz` 60s 探针超时（全量 Go ensure 链启动慢于窗口）报 "active cutover failed"，随后自愈为健康 2090；2091 复现同形态并 VERIFY_PASS=1。属 deploy-lib 线的健康窗口设置问题，未在本轮修（跨线范围），已知症状：满迁移链的冷启动可能超 60s。
- **修正后全量验证（终态 HEAD）**：`go build ./...` 净、`go vet ./...` 零输出、gofmt 本轮文件净、`go test ./db/ ./sql/migrations/... ./bg/ ./deploy/grafana/ ./deploy/prometheus/...` 全绿、live round-trip（真库）绿、installer 全套绿、通道 `bash -n` 过、通道复跑 696-700 全部 already-applied/幂等、双账本终态 696/697/698/699/700 齐、生产 252 复核维持 698-700 未应用 + 视图 112/raw=0（发布约束继续有效）。

## 下一轮提示词（可直接复制）

```text
请对 llm-gateway-go「request_logs 视图 raw_model_name 根修（迁移 700）+ LF 治理」
做下一轮观察/收尾。工作目录：主工作区（main）。
先阅读：docs/handoff/2026-09-12-request-logs-view-raw-model-name-rootfix.md
（含审计轮记录）与 docs/changelogs/2026-09-12-request-logs-view-raw-model-name.md。

背景：700 已在 origin/main（84bae533d 起），本机 2091 部署运行正常（scanner
零 42703）；生产 252 的 698/699/700 尚未应用（视图 112 列、raw=0 实查）。
生产 245/154 发布维持"通道先 698/699/700 → 再上携带 83bf582dd 的二进制"
顺序约束；无运营报障不主动发起生产发布。

本轮任务（按输入分派）：
A. 生产发布窗口到达（或有运营报障）：252 通道应用 698/699/700（含函数链
   登记，登记已在通道脚本内）→ 复核双账本与视图 113 列/raw=1 → 蓝绿上
   新二进制 → api/v1 或日志确认 drift scanner 无 42703。
B. 无窗口：只做观察轮——本机 drift scanner 整点周期日志复核（应为静默）；
   如指纹线或钉扎线对 696/697/698/699 有新动作，核对通道/账本一致性。
C. 已知遗留（勿自动开新轮）：视图基础层 pre-485 冻结交集（缺 48 列）的
   "陈旧也重建"专项迁移；deploy-lib readyz 60s 窗口偏短；696/697 无
   schema_migrations 自插（触碰时顺手补）；并行线 gofmt 未净两文件。
D. 验证如实区分通过/环境受限/未验证；数据操作先备份、单事务、事务内复核；
   严禁 git add -A，提交前按文件核对归属。
```
