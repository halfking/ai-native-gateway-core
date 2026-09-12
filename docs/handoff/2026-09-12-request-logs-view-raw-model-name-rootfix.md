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
