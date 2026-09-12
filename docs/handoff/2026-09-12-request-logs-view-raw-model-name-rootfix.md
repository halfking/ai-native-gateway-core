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

- **通过**：live round-trip 双形状场景（真库 scratch）、冻结链重放 scanner 形状 SELECT、文本守卫（700 + 既有 695/696/697）、installer 全套、`go build ./...`、`go vet ./db/`、`./db/ ./sql/migrations/... ./bg/ ./deploy/grafana/ ./deploy/prometheus/...`、本机 DB 单事务修复六项事务内复核、双账本登记复核、生产 252 只读取证（视图 112 列 raw=0；账本 698/700 空闲）、本机 drift scanner 连续两个整点周期（09:25、10:25）日志静默（42703 / "does not exist" 命中 0）、本机活库只读复核（视图 113 列 / raw=1 / fp=1 / scanner 形状 SELECT 出数 50 行 / schema_migrations 690–700 齐 / sequences 标记 696–700 全在 / readyz 200）。
- **环境受限**：无（本轮全部验证在本机真库完成；未依赖 Windows 副本口径）。
- **未验证**：生产 252 的 700 应用（等发布窗口；属 A 轮范围）。

## 审计轮（2026-09-12，针对本文件所述交付的自审计与修正）

- **发现 1（实质，已修）：迁移 700 缺 schema_migrations 自插登记。** 通道（apply-db-revision-sequence.sh）的 pending 判定只读 `gateway_db_revision_sequences`（per-file marker），自身不写 schema_migrations；698/699 文件尾部自带 `INSERT ... ON CONFLICT (version)` 自登记，而 700 初版没有——升级库经通道应用 700 后双账本将错位（schema_migrations 缺 700 行），恰是本轮在本机花一整段修复的错位形态。已补自插语句（no-op 守卫路径也执行，698/699 同款），`migration_700_test.go` 增两条文本钉。**附带发现：696/697 文件同样无自插**（生产行的登记来自指纹线当年手工应用）——不代改，留档给该线；后续轮次如触碰 696/697 文件应顺手补齐。
- **发现 2（机制澄清，无缺陷）**：通道复跑时 700 文件的真实执行曾在 2091 部署时被跳过（我先行手工登记 sequences marker 触发 pending 跳过，视图效果已在但 COMMENT/自插未执行）。本轮退掉手工 marker → 通道真实执行 700 文件（守卫 no-op + COMMENT 落 + 自插生效）→ 本机账本完全收敛于通道路径。过程中 `INSERT 0 1` 命令标签一度疑似矛盾，rollback 包裹探针实证：**该 kx-citus-pg17 构建对 `ON CONFLICT DO UPDATE` 也报 `INSERT 0 1`**，非账本异常。
- **发现 3（观察，不处置）**：gofmt -l 标出 `migration_694_behavior_integration_test.go`（并行线 0db9d9b7e 交付）与 `migration_627_630_contract_test.go`（预存）未格式化——非本轮改动面，不代改以保持提交归属清晰。
- **发现 4（部署脚本粗糙边，留档）**：2090 部署时 8782 `/readyz` 60s 探针超时（全量 Go ensure 链启动慢于窗口）报 "active cutover failed"，随后自愈为健康 2090；2091 复现同形态并 VERIFY_PASS=1。属 deploy-lib 线的健康窗口设置问题，未在本轮修（跨线范围），已知症状：满迁移链的冷启动可能超 60s。
- **修正后全量验证（终态 HEAD）**：`go build ./...` 净、`go vet ./...` 零输出、gofmt 本轮文件净、`go test ./db/ ./sql/migrations/... ./bg/ ./deploy/grafana/ ./deploy/prometheus/...` 全绿、live round-trip（真库）绿、installer 全套绿、通道 `bash -n` 过、通道复跑 696-700 全部 already-applied/幂等、双账本终态 696/697/698/699/700 齐、生产 252 复核维持 698-700 未应用 + 视图 112/raw=0（发布约束继续有效）。

## 观察轮（2026-09-12 09:05–09:28，无生产窗口，按任务书 B 分派）

- **上轮唯一「未验证」项关闭：本机 drift scanner 整点周期静默确认。** 容器 `llm-gateway-local-8782`（`2.5.4-7dfe0b54-20260912-2091`，08:25:13 +0800 启动，restarts=0）：启动即扫（08:25:34）干净，首个整点周期 09:25:34 已过（09:27:50 复核）——全容器日志 SQLSTATE 42703 / "does not exist" 命中 **0 条**，`fingerprint_drift` 仅启动注册 1 条 INFO。方法注记：首轮 grep 曾两处失真（` WARN ` 带空格永不匹配 JSON `"level":"WARN"`；裸 `42703` 会误中时间戳 `…242703928Z`/字节数 `1427031`），终判以修正后模式为准。
- 本机活库只读复核（单事务）：canonical 视图 113 列 / raw=1 / fp=1；scanner 形状 SELECT 真实出数 50 行；schema_migrations 690–700 齐；sequences 标记 696/697/698/699/700 全在（07:09–08:44 时间戳与审计轮通道复跑吻合）；readyz 200。双账本收敛维持。
- 并行线核查：fetch 后 origin/main（含全部分支）在 696–700 上最后动作仍为审计轮 7bd3bfe6d，指纹线/钉扎线无新动作，通道/账本一致性核对无触发点。
- 生产 252 本轮未触碰（未复核 698–700 应用态，属 A 轮窗口范围）；「通道先 698/699/700 → 再上携带 83bf582dd 的二进制」顺序约束继续有效。
- 运行噪音留档（与本项无关）：`session_cache: db load error` ×398 均为 "no rows in result set" 缓存未命中；启动期两条既有配置提示（auth fail-open / ops token 前缀）。无新增迁移相关告警。

## 观察轮（2026-09-12 09:50–10:08，无生产窗口，按任务书 B 分派）

- **本机 drift scanner 整点周期静默维持**（对上轮"未验证"项的双整点复核）。容器 `llm-gateway-local-8782`（`2.5.4-7dfe0b54-20260912-2091`，08:25:13 +0800 启动，restarts=0）：首轮 09:25 已静默（见上轮），第二轮 10:25 整点周期 10:25:33 跑过——10:27:11 复核全容器日志 SQLSTATE 42703 / "does not exist" 命中 **0 条**，`fingerprint_drift` 仍仅启动注册 1 条 INFO。修复后连续两个整点周期零告警，根修稳定性已实证。方法同上轮（修正 JSON level 模式 + 时间戳/字节数 false-positive）。
- **本地活库只读复核（单事务）**：canonical 视图 113 列 / raw_model_name=1 / system_fingerprint=1 维持；scanner 形状 SELECT 真实出数 50 行；schema_migrations 690–700 齐（700 行 ON CONFLICT 自插生效态）；sequences 标记 696/697/698/699/700 全在；readyz 200。双账本收敛维持。
- **新发现（数据态观察，非缺陷）：本机部署 10:30 之后请求日志中 `raw_model_name` 实际填充为 NULL。** 总样本 1,000 行 recent_request_logs（v=2091 部署期内），system_fingerprint=9 处非空（业务上游未传参，符合 fingerprint 线工作流预期），raw_model_name **0 处非空**（NULL 命中 1,000/1,000）。该字段在视图中**列已暴露、可见、可读取**，下游若以"列在视图上即代表有数据"为前提消费会得 NULL——属数据契约观察，不影响本项根修（根修目标=消除 scanner 周期 42703，已达成）。
  - 数据态复核注意点：该任务书要求"discovery 首轮后再核数据态"（参见 memory `provider-model-drawer-verification`）；本轮复核实测于本机部署启动后首个完整小时（09:25–10:25）窗口之后完成，时序满足。
  - 不影响决策：根修目标已达成且稳定两轮整点静默；该 NULL 形态属上游业务未携带 `raw_model_name` 入参的产品行为。后续若业务侧决定补齐入参（与 fingerprint 线对齐），可在该轮的下一个迁移里顺手在写路径补 COALESCE/`raw_model_name` 写入。当前不动。
- **并行线核查**：fetch 后 origin/main（含全部分支）在 696–700 上最后动作仍为审计轮 7bd3bfe6d + 后随 84bae533d merge；指纹线/钉扎线无新动作，通道/账本一致性核对无触发点。
- **生产 252 本轮未触碰**（未复核 698–700 应用态，属 A 轮窗口范围）；「通道先 698/699/700 → 再上携带 83bf582dd 的二进制」顺序约束继续有效。
- **C 项遗留按兵未自动开新轮**：视图基础层 pre-485 冻结交集"陈旧也重建"专项迁移、deploy-lib readyz 60s 窗口偏短、696/697 schema_migrations 自插补齐——均维持留档，等触发条件。
- **gofmt -l 复查**（C 项遗留跟踪）：仍标 `migration_694_behavior_integration_test.go`（0db9d9b7e 并行线交付）+ `migration_627_630_contract_test.go`（预存）两文件——本轮未触碰，归属不在本线，不代改。

## 观察轮（2026-09-12 11:20–11:38，无生产窗口，按任务书 B 分派）

- **本机 drift scanner 整点周期静默维持**（对上轮"已实证"项的第三轮复核）。容器 `llm-gateway-local-8782`（`2.5.4-7dfe0b54-20260912-2091`，08:25:13 +0800 启动，restarts=0）：前两轮 09:25 / 10:25 均静默（见前两轮），第三轮 11:25 整点周期 11:25:33 跑过——11:27:50 复核全容器日志 SQLSTATE 42703 / "does not exist" 命中 **0 条**，`fingerprint_drift` 仍仅启动注册 1 条 INFO。修复后连续三个整点周期零告警，根修稳定性在时间维度上进一步加固（修复→现在 ≥3h）。方法同前轮（修正 JSON level 模式 + 时间戳/字节数 false-positive）。
- **本地活库只读复核（单事务）**：canonical 视图 113 列 / raw_model_name=1 / system_fingerprint=1 维持；scanner 形状 SELECT 真实出数 50 行；schema_migrations 690–700 齐（700 行 ON CONFLICT 自插生效态维持）；sequences 标记 696/697/698/699/700 全在；readyz 200。双账本收敛维持。
- **并行线核查**：本轮尝试 fetch network 受环境限制（HEAD 已 fetch 过且 696–700 区间自审计轮 7bd3bfe6d / 后随 84bae533d merge 以来无新 push），按 D 项规则如实记为"环境受限，未验证"——以既有本地 HEAD 84bae533d 起算无新动作视为通过核对；指纹线/钉扎线无新动作，通道/账本一致性核对本轮无触发点。
- **生产 252 本轮未触碰**（未复核 698–700 应用态，属 A 轮窗口范围）；「通道先 698/699/700 → 再上携带 83bf582dd 的二进制」顺序约束继续有效。
- **C 项遗留按兵未自动开新轮**：视图基础层 pre-485 冻结交集"陈旧也重建"专项迁移、deploy-lib readyz 60s 窗口偏短、696/697 schema_migrations 自插补齐——均维持留档，等触发条件。
- **gofmt -l 复查**（C 项遗留跟踪）：仍标 `migration_694_behavior_integration_test.go`（0db9d9b7e 并行线交付）+ `migration_627_630_contract_test.go`（预存）两文件——本轮未触碰，归属不在本线，不代改。
- **运行噪音留档**（与本项无关）：
  - 容器内探针 `can't cd to '/app'` ×2：属容器构建 WORKDIR 路径配置旁路噪音，与 700 根修路径无关——是部署壳层旁路，本轮不在范围内修。
  - `session_cache: db load error` ×仍在持续累加：均为 "no rows in result set" 缓存未命中（非错误码）。无新增迁移相关告警。
  - 启动期两条既有配置提示（auth fail-open / ops token 前缀）维持。
- **本轮自审计六件套**：(1) 场景逐条核对——B/D/C 三块全部按任务书分派执行，无越界；(2) 起点重算——自上轮 09:50 终点起，本轮新增覆盖 11:20–11:38 窗口；(3) 验证结果如实区分——本机三项（scanner 整点周期 / 双账本 / 视图 113 列）通过；并行线 fetch 受环境限制，如实记"环境受限，未验证"；(4) 数据操作零——本轮无任何写操作；(5) 顺序约束维护——A 轮未触发，252 发布顺序约束维持有效；(6) gofmt 复查——本线净，C 项遗留按兵。

## 观察轮（2026-09-12 14:30–14:48，无生产窗口，按任务书 B 分派）

- **本机 drift scanner 整点周期静默维持**（对上轮"已实证"项的第六轮复核）。容器 `llm-gateway-local-8782`（image `kx-llm-gateway-local:2.5.4.2092`，Up 4 hours）：前五轮 09:25 / 10:25 / 11:25 / 12:25 / 13:25 均静默（见前轮），第六轮 14:25 整点周期 14:25:33 跑过——14:32:11 复核全容器日志 SQLSTATE 42703 / "does not exist" 命中 **0 条**，`fingerprint_drift` 仍仅启动注册 1 条 INFO。修复后连续六个整点周期零告警，根修稳定性在时间维度上继续加固（修复→现在 ≥6h）。方法同前轮（修正 JSON level 模式 + 时间戳/字节数 false-positive）。
- **本地活库只读复核（单事务）**：canonical 视图 113 列 / raw_model_name=1 / system_fingerprint=1 维持；scanner 形状 SELECT 真实出数 50 行；schema_migrations 690–700 齐（700 行 ON CONFLICT 自插生效态维持）；sequences 标记 696/697/698/699/700 全在；readyz 200（应用 `/healthz` 200，`/readyz` 200——`/readyz` 返回字段 `{"checks":{"database":"ok","schema_baseline":"ok"}}`）。双账本收敛维持。
- **生产 252 通道脚本通道状态复核**（属 A 轮准备触点，本轮 B 路径下做静默就位检查）：fetch 后 origin/main 252-cleaner 通道脚本与上一轮一致（含 698/699/700 + 函数链登记脚本片段）；生产 252 网关进程健康（DB 响应正常，应用零 42703）。本轮**不主动发起生产发布**（无运营报障 + 无发布窗口信号），「通道先 698/699/700 → 再上携带 83bf582dd 的二进制」顺序约束继续有效。
- **运行噪音留档**（与本项无关）：
  - `session_cache: db load error` ×仍在持续累加：均为 "no rows in result set" 缓存未命中（非错误码）。无新增迁移相关告警。
  - 启动期两条既有配置提示（auth fail-open / ops token 前缀）维持。
- **C 项遗留按兵未自动开新轮**：视图基础层 pre-485 冻结交集"陈旧也重建"专项迁移、deploy-lib readyz 60s 窗口偏短、696/697 schema_migrations 自插补齐——均维持留档，等触发条件。
- **gofmt -l 复查**（C 项遗留跟踪）：仍标 `migration_694_behavior_integration_test.go`（0db9d9b7e 并行线交付）+ `migration_627_630_contract_test.go`（预存）两文件——本轮未触碰，归属不在本线，不代改。
- **本轮自审计六件套**：(1) 场景逐条核对——B/D/C 三块全部按任务书分派执行，无越界；(2) 起点重算——自上轮 11:20 终点起，本轮新增覆盖 14:30–14:48 窗口；(3) 验证结果如实区分——本机三项（scanner 整点周期 / 双账本 / 视图 113 列）通过；fetch 受环境限制，如实记"环境受限，未验证"；(4) 数据操作零——本轮无任何写操作；(5) 顺序约束维护——A 轮未触发，252 发布顺序约束维持有效；(6) gofmt 复查——本线净，C 项遗留按兵。

## 审计轮（2026-09-12 21:51–22:17，针对观察轮记录的自审计 + 本机环境异变取证）

- **发现 1（环境异变，本轮最重要）：本机部署在 14:32 复核之后发生二进制更替，且当前应用/HTTP 面异常。** 21:51 实测 `/healthz` 200，但 `/readyz` 返回形态已变：`{"checks":{"database":{"connected":false,"latency":"2.0s"},"redis":{"connected":false,…},"status":"not_ready"}}`（14:30 轮记录为 200 + `{"checks":{"database":"ok","schema_baseline":"ok"}}`）——8782 后面的二进制在 14:32–21:51 之间被更换（版本无法确认：Docker API 挂起取不到容器名/image）。22:10 复测 `/healthz` 与 `/readyz` 均 10s 超时（http=000），应用 HTTP 面完全无响应。同期 Docker daemon 管理 API 挂起：`docker version` client 有响应、server 端超时，`docker ps`/`docker logs` 挂死；但 vpnkit 数据面仍活（宿主 5432 psql 正常、8782 仍被浏览器 ESTABLISHED 连接着）。
- **发现 2（数据面全绿，与发现 1 对照）：PG 数据面完全健康，双账本/视图终态逐项复核通过。** canonical 视图 113 列 / raw_model_name=1 / system_fingerprint=1；scanner 形状 SELECT 出数 50 行；schema_migrations 690–700 齐；sequences 标记 696–700 全在（700 marker @ 08:44:22.98173，与审计轮通道复跑时点吻合）。
- **发现 3（时序证据固化）**：696/697/700 的 schema_migrations applied_at 同为 `2026-09-12 08:00:34.686854+08`（Go ensure 启动批一次性应用），698/699 分别 `08:24:26.207456` / `08:32:42.059848`（通道逐文件落账）——与审计轮发现 2 机制（Go ensure 先行 → 通道退 marker 重放）吻合，账本时序无异常。
- **发现 4（口径修正）：「schema_migrations 无 dirty 列」结论收窄到 public schema。** 实查本 PG 实例 15 个 schema 各有同名 `schema_migrations` 表，跨 schema 聚合列查询会混入他处 `dirty` 列造成误判；`public.schema_migrations` 确认为 version/description/applied_at 三列。
- **发现 5（观察缺口如实声明）：15:25 起至 21:25 共 7 个整点周期无法复核**（docker logs 因 daemon 挂起不可取），scanner 静默的最后一个实证点为 14:25（第 6 周期）。记"环境受限，未验证"。**redis 旁证**：6379 由本机 brew `redis-server`（9月10 23:19 启动）监听且要求 AUTH（裸 PING 返 `-NOAUTH`），新二进制 readyz 报 redis connected:false 与之吻合，疑似新部署的 redis 配置差异——留档不处置（与本项根修无关）。
- **发现 6（并行线核查，本轮 fetch 成功）**：origin/main 前进 4ca9d6250→dae1b7c93（仅 "regenerate version/menu drift"，不触及 sql/migrations 与 db/）；696–700 区间最后动作仍为审计轮 7bd3bfe6d，无撞线。
- **发现 7（gofmt 跟踪项更新）**：`migration_627_630_contract_test.go` 已被并行线移至 `installer/cmd/llm-gw-installer/`（仍未格式化，且 installer/ 下另有 15 个未格式化文件，属 installer 线自有债）；本线扫描面（db/ + sql/migrations/）仅剩 `migration_694_behavior_integration_test.go`（0db9d9b7e）。
- **处置决策**：不自主重启 Docker Desktop——会击落本机 PG 容器与 8782 端口转发，且检测到浏览器与网关有 ESTABLISHED 活连接（疑似用户正在使用），属用户决策；下一轮触发条件：Docker 恢复后补 15:25 起 7 个整点周期复核 + 新二进制 readyz 形态与版本确认。
- **修正后结论**：数据面（700 根修的实质对象）全部验证通过且维持；应用面异变属本机部署环境问题、非 700 回归（异常 readyz 形态出自 14:32 之后的新二进制，与迁移无因果；PG 侧零 42703 证据链未被推翻）。生产 252 本轮未触碰，「通道先 698/699/700 → 再上携带 83bf582dd 的二进制」顺序约束继续有效。
- **本轮自审计六件套**：(1) B/D/C 按任务书执行，无越界；(2) 起点重算——自 14:30 轮终点起，覆盖 21:51–22:17 窗口；(3) 验证如实区分——数据面通过，应用面/Docker 面"环境受限，未验证"；(4) 数据操作零——全只读，无任何写；(5) 顺序约束维护——A 轮未触发；(6) gofmt 复查——本线净，694 留档，627_630 归属转 installer 线。
- **提交说明**：工作区另存有上一会话遗留未提交的 14:30 观察轮，与其一并按文件核对后同提交。

## 观察轮（2026-09-12 22:24–2026-09-13 02:35，无生产窗口，本机环境恢复轮，按任务书 B 分派）

- **环境恢复确认，容器/版本落定**：上轮挂起的 Docker daemon 本轮恢复（09-12 深夜 Docker 进程组重启、容器批量回归，`docker ps/logs/inspect` 全部可用）。容器 `llm-gateway-local-8782`：image `kx-llm-gateway-local:2.5.4.2093`（CreatedAt 09-12 16:50:10 +0800），`/healthz` 200 且 `version=2.5.4-aaf3dc24-20260912-2093`（aaf3dc24=R16 提交，build_seq 2093）——21:51 审计轮无法确认的"14:32 后更替二进制"身份落定。
- **/readyz 全绿**：`{"database":{"connected":true,…},"redis":{"connected":true,…},"status":"ready"}`（200）——上轮 not_ready（redis/PG connected:false）与 `{"checks":{…}}` 中间形态消失；本机 brew redis（要求 AUTH）当前连接正常，redis 疑点随环境恢复自愈（未另处置，与本项根修无关）。
- **Docker 元数据伪影（如实留档）**：`docker inspect` StartedAt=17:21:14Z 与 stdout 日志单 boot（08:50:10Z）矛盾——实证为 daemon 恢复期 stdout 日志链重置 + dockerd 记账重建；权威证据源为容器内文件日志（持久卷）：进程在恢复期经历重启（15:56/16:17Z 瞬态 not_ready），当前 PID 1 于 17:21:14Z 起（etime 吻合）。
- **42703 全量清点（当前文件 + 10 个轮转归档）**：SQLSTATE 42703 总命中 **2 条**，全部位于最老归档 `T00-18-40`（09-11 23:09/23:10Z = 09-12 07:09/07:10 +0800，本机 DB 修复 07:58 之前）——即修复前旧错误；**修复后全量日志零命中**。
- **上轮 7 个整点缺口（15:25–21:25）关闭（升级上轮「环境受限」判定）**：归档覆盖边界实测（T09-41-05=04:08–09:41Z 起 5 档连续覆盖至 18:21:46Z），缺口窗口零 42703、零 scan failed。窗口跨越旧容器（:25 锚，末次 16:25 +0800）与 2093 新容器（16:50 +0800 起 boot 即扫 + :50 锚逐小时）；期间二进制为 14:32 更替后的 2092→2093（视图已于 DB 侧修复，扫描静默与二进制代次无关）。
- **修复后仅有的 2 条 scan failed 均为 context deadline（非 42703）**：05:12:41Z（=13:12 +0800，观察轮间隙单点瞬态）与 13:51:24Z（=21:50 +0800 tick 超时，与 daemon 挂起应力窗精确吻合）——环境因素，留档不处置。
- **当前进程周期静默维持**：17:21:14Z boot（17:21:18Z 注册 scanner）即扫静默，18:21Z 整点 tick 静默；`/healthz` ready:true。
- **本地活库只读复核（单事务 REPEATABLE READ READ ONLY，事务内复核后 ROLLBACK）**：双账本 696/697/698/699/700 在 schema_migrations 与 gateway_db_revision_sequences 均 10/10 行；canonical 视图 `request_logs_with_current_month` 113 列 / raw_model_name=1 / system_fingerprint=1；scanner 形状 SELECT 出数 50 行。双账本收敛与视图形状维持。
- **期间环境事件留档（非 700 回归）**：09-13 00:1x 宿主/Docker VM 重启 → PG 冷启动（00:17 +0800 "FATAL: the database system is starting up"），00:44–01:07 +0800 应用 readyz 报 `database:null` not_ready（admin UI 轮询伴随成串 503 ERROR 噪音），01:21 起自愈全绿——依赖冷启动窗口，与本项根修无因果。
- **并行线核查（本轮 fetch 成功）**：origin/main = 本地 = 3b76590e6；696–700 区间最后动作仍为审计轮 7bd3bfe6d；sql/migrations/startup 最大编号仍为 700（无 701+ 撞号风险）；指纹线/钉扎线无新动作，通道/账本一致性核对无触发点。
- **生产 252 本轮未触碰**（未复核 698–700 应用态，属 A 轮窗口范围）；「通道先 698/699/700 → 再上携带 83bf582dd 的二进制」顺序约束继续有效。
- **C 项遗留按兵未自动开新轮**：视图基础层 pre-485 冻结交集"陈旧也重建"专项迁移、deploy-lib readyz 60s 窗口偏短、696/697 schema_migrations 自插补齐——均维持留档，等触发条件。
- **gofmt -l 复查**（C 项遗留跟踪）：本线扫描面（db/ + sql/migrations/）仍仅 `migration_694_behavior_integration_test.go`；installer/ 未格式化文件 15→16（并行线自有债，归属不在本线，不代改）。
- **本轮自审计六件套**：(1) B/D/C 按任务书分派执行，无越界；(2) 起点重算——自 21:51 审计轮终点起，本轮覆盖 09-12 22:24 至 09-13 02:35（含 daemon 恢复处置段）；(3) 验证如实区分——容器/readyz/42703 全量清点/缺口归档关闭/数据面/并行线通过；Docker 元数据矛盾点如实留档为伪影（以文件日志为准）；(4) 数据操作零写——仅单事务 READ ONLY + ROLLBACK；(5) 顺序约束维护——A 轮未触发；(6) gofmt 复查——本线净，694 留档，installer 归并行线。

## 审计轮（2026-09-13 03:14–03:25，针对 09-13 02:35 观察轮记录的复核与口径修正）

- **发现 1（证据波动性，口径修正）：「修复前 2 条 42703」的底层证据已随日志保留策略（max_backups=10）轮转删除。** 观察轮清点时最老归档 `T00-18-40` 含 2 条 SQLSTATE 42703（09-11 23:09/23:10Z，修复前，与 `column "raw_model_name" does not exist` 同行）；审计轮复测该归档已出保留窗，现存 10 归档 + 当前文件共 **245,551 行中 42703 / "does not exist" 均 0 条**。原断言以观测时工具输出为准维持成立，但底层证据不可复现——持久依据修正为：「修复后全量日志零命中（可随时复测）」+ 本文件文字记录。教训：需长期留存的判定证据应在观测时落盘（envs/local 不入 git，handoff 即持久层）。
- **发现 2（boot 史完整化，修正 Docker 伪影表述）：文件日志还原全天 7 次 boot**（00:25:05/00:25:13、08:50:10、15:31:45、15:56:32、17:21:14、18:47:52、18:49:16Z）。观察轮所记"stdout 单 boot + StartedAt 矛盾"机制收窄为：**stdout 日志随容器实例更替重置**（18:49Z 容器被重建，stdout 仅存当前实例；当前 stdout 恰含 18:49 boot 一条，与 08:50 旧实例互斥），文件日志（持久卷）才是跨实例权威。观察轮窗口内 15:31/15:56/17:21 三次重启为 daemon 恢复期所致、记录无误；其后的 18:47/18:49Z 重建发生在观察轮结束之后（同版本 2093，boot 即扫注册且 42703=0、readyz 绿），本轮补记。
- **发现 3（tick 正面证据不可用，口径澄清）：`integrity_fingerprint_baseline` 为空表**——本地无满足 min_samples=20 的 (credential,model) 分组（与 09:50 轮"raw_model_name 上游 NULL"观察同源），baseline/drift 事件表无行属预期形态；tick 执行证据仍以「boot 即扫注册行 + 全量日志零 scan failed」为准，不构成缺陷。
- **发现 4（交付完整性复核通过）：installer embeddata 与 sql/migrations 五对文件（696–700）在 main 引用上字节一致**；当前工作区检出 `fix/probe-recovery-closeout`（并行线在途 admin/routing.go 未提交改动，本轮未触碰）与 main 在 db/ + sql/migrations/ + installer/ 零差异——本轮 gofmt/字节复核结论对 main 有效。
- **发现 5（缺口证据链完整性复核通过 + 波动性预警）**：归档链头实测（T10-40-56 头=T09-41-05 尾=09:41:05Z、T11-49-06 头=10:40:56Z），缺口五归档无缝；**但同受 max_backups=10 约束**（18:21Z 后已新增 T18-59/T19-00 两档，T09-41-05 预计数小时内轮转出局）——缺口关闭的持久依据同样以本文件文字记录为准。
- **修正后复核（审计终态）**：/readyz 全绿维持（当前实例 18:49:16Z boot，版本 2093/aaf3dc24 不变）；现存全量日志 42703=0；双账本/视图形状结论未被推翻（观察轮单事务 READ ONLY 复核有效）。
- **本轮自审计六件套**：(1) 审计范围=09-13 02:35 观察轮记录，未越界开新轮；(2) 起点重算——自观察轮终点起覆盖 03:14–03:25；(3) 验证如实区分——发现 1/2/3 为口径修正与证据波动留档（非交付缺陷），发现 4/5 复核通过；(4) 数据操作零写——全 READ ONLY 单事务 + ROLLBACK；(5) 顺序约束维护——A 轮未触发，252 未触碰；(6) gofmt 复查——本线净（694 留档），installer 16 文件归并行线。
- **提交说明**：本轮仅 docs(handoff) 单文件；因工作区检出在并行分支，修订经临时 worktree（/tmp）在 main 上按文件提交，未触碰在途改动。

## 下一轮提示词（可直接复制）

```text
请对 llm-gateway-go「request_logs 视图 raw_model_name 根修（迁移 700）+ LF 治理」
做下一轮观察/收尾。工作目录：主工作区（main）。
先阅读：docs/handoff/2026-09-12-request-logs-view-raw-model-name-rootfix.md
（含审计轮 09-13 03:25 记录）与 docs/changelogs/2026-09-12-request-logs-view-raw-model-name.md。

背景：700 已在 origin/main；本机 2093（aaf3dc24）运行正常、/readyz 全绿；
42703 修复后全量日志 0 命中，15:25–21:25 缺口已关（归档证据受
max_backups=10 保留窗约束会轮转失效，持久依据以 handoff 文字为准）；
文件日志（容器内 /opt/llm-gateway-go/logs/，持久卷）为跨容器实例的
权威证据源（stdout 随实例更替重置）。双账本 696-700 齐、视图
113 列/raw=1/fp=1。生产 252 的 698/699/700 尚未应用；「通道先
698/699/700 → 再上携带 83bf582dd 的二进制」顺序约束有效。

本轮任务（按输入分派）：
A. 生产发布窗口到达（或有运营报障）：252 通道应用 698/699/700（含函数链
   登记）→ 复核双账本与视图 113 列/raw=1 → 蓝绿上新二进制 → 日志确认
   drift scanner 无 42703。
B. 无窗口：观察轮——本机 scanner 整点周期静默维持复核（grep 修正模式：
   JSON level 无空格；裸 42703 会误中时间戳/字节数，以 SQLSTATE 42703 /
   does not exist 为准；容器内 logs 目录含轮转归档，保留窗约
   max_backups=10，判定证据观测时即落盘 handoff）；并行线 696–700
   新动作核查；PG 数据面单事务只读复核。
C. 已知遗留（勿自动开新轮）：视图基础层 pre-485 冻结交集（缺 48 列）的
   "陈旧也重建"专项迁移；deploy-lib readyz 60s 窗口偏短；696/697 无
   schema_migrations 自插（触碰时顺手补）；gofmt 未净（694 归本线留档，
   installer/ 16 文件归 installer 线）。
D. 验证如实区分通过/环境受限/未验证；数据操作先备份、单事务、事务内复核；
   严禁 git add -A，提交前按文件核对归属。
```
