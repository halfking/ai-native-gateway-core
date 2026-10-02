# R64 → R65 48h 审计 Handoff

**日期**: 2026-09-25
**上轮**: R64（docs/audit/2026-09-25-r64-48h-audit-round.md，窗口 153ad093a..97d8870aa + 死区补审 7489c8e92..153ad093a）
**基线**: 本轮收口提交之后的 main HEAD
**下一可用迁移**: 747（745/746=report_snapshots 系、800=provider_endpoint_protocols 已占用；R65 勘误：9635b9b17 占用 746）

---

## 一、给 R65 的入口清单（按优先级）

1. **[P2] mock-probe 生产接入拍板**（R63 起第三轮登记）：现装配在 cmd/gateway-v2
   （演示入口），生产 cmd/gateway 零装配，README 已如实声明。若拍板接入：复用
   cmd/gateway-v2 的装配四件（config 闸 / 端点注册 / authChain 旁路 / runner——按
   符号定位，勿钉行号），加 settings 热开关评估；若拍板不接入：README 标注裁决。
2. **[P2] P4 selector 接线（r0924 收尾）**：库函数级障碍 R63 已清、本轮验证无回归
   （irToCatalogProtocol 归一、权重 higher=higher 三方一致）。接线步骤按
   docs/handoff/20260924-r0924-supplier-protocol-optimization-audit.md §三；前置：
   serialize 多模态 gate + V800 boot ensure（下条）。
3. **[P2] V800 boot ensure**（§三.5 延续）：db ensure 链零 800 消费者，文件头自证。
   形态对照 745 的 ensureReportSnapshots（db/db.go）。P4 接线时同做。
4. **[P2] auto 专项 P2/P3**（按 AUTO_ROUTING_CLOSED_LOOP_V2_PLAN 自身节奏）：P0/P1
   已落地并经 R64 批判式复审收口；P2=TierSelector 生产接线、P3=V3 影子（≥7 天）。
   R64 修复后的 gate 口径：volume/corrections 均限 classifier='heuristic'，
   keyword_add 三枚举词休眠（structured_features 升级自由 token 后自动恢复）。
5. **[P3] 大专项候选**（用户标准清单差距，择一立项）：①sanitizer 跨进程 offset
   原子预占；②nodestatecache 单模块收编；③bg keyword 去重 LIKE 转义（R64 FixA
   同族发现，feedback_analyzer.go:260；R65 勘误行号）。
6. **[P3] installer 凭据专项收尾**：refresh.token 仍为零消费方休眠凭据（写入点
   auto_activate.go:174-186，全仓无读取方）——接 refresh 流时须同步补读取方；
   容器内 refresh daemon 三件套（compose 挂载+文件名对齐+LICENSE_AUTHORITY_URL
   激活后置写 env）；activation.json license_key 迁独立文件；legacy device_code 清洗。
7. **[P3] PR4 删 python mock**：owner 确认后删 scripts/mocks/llm-mock-upstream/*.py、
   Dockerfile*、llm-mock.conf、docker-compose.yml，验收 grep scripts/mocks = 0。
8. **[P3] R61 §六 1/2/3/4/5 延续**（读面归一 ~7 处 / parse 面 requestID 4 处 /
   hostedtask store 并发形状 / UA SSOT / btrim 漂移）——R64 复核全部仍遗留。
9. 例行部署：R64 收口提交合入后按 R62 模式部署 154/245（本轮零新增迁移）。

## 二、R64 关键纪要（R65 审计时作上下文）

- **窗口死区教训**：R63 窗口终点后、收口提交前落地 5 提交全部逃逸审计（含千行级
  80bb3675a）。R64 以 48h 日志全额补审。**R65 圈窗口时必须同时检查「上轮窗口终点
  ..上轮收口提交」区段**，不能只看收口提交之后。
- **scratch PG 夹具口径**（下轮直接复用）：schema_migrations(version PK,
  description, applied_at)；session_turns 分区表 + DEFAULT 分区（session_turns_hot
  即 DEFAULT 分区即可满足 744 ensure 双臂）；800 需 providers(id, tenant_id,
  protocol, base_url, catalog_code, deleted_at) + provider_catalog(code) 夹具。
- **i18n 门禁已收紧**：web/scripts/i18n-audit.mjs 对 missing keys exit 1（R64 起）。
  前端提交若带新 t() 键，必须 8 语言同步，否则推送门禁前手工跑
  `node scripts/i18n-audit.mjs` 先红先修。
- **testbench 门禁已收紧**：baseline 语义校验（阈值非零+total_cases+suite_files 一致，
  违者 exit 2）、CLI 互斥组合 exit 2、负值舍入修正。改随仓 baseline 必须走
  `scripts/auto-testbench.sh --refresh-baseline`（R64 修后可达）。

## 三、R65 标准审计轮提示词（可直接拷贝）

> 你是 llm-gateway-go 仓库（/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-2）的审计协调者，执行 R65 48h 审计轮。
>
> 步骤：
> 1. git fetch && git status 对齐 origin/main；git log --since="48 hours ago" 圈定窗口；**必须额外检查上轮窗口终点..上轮收口提交区段是否又有逃逸提交**（R64 实锤死区教训）。
> 2. codegraph 增量构建若报 malformed 则 rm -rf .codegraph && codegraph build --no-incremental（R61/R63/R64 三轮同款坑）。
> 3. 读窗口内新方案文档 + docs/audit/2026-09-25-r64-48h-audit-round.md（上轮发现与登记项）。
> 4. 按轨道分 5-6 组并行只读审计子代理（纪律：禁连库、禁改文件、每条发现带 file:line 证据；库级验证收敛协调者 scratch PG17 容器单点：docker run -d --rm --name r65-scratch-pg -e POSTGRES_PASSWORD=scratch -e POSTGRES_DB=gwtest -p 55432:5432 postgres:17-alpine，夹具口径见 handoff §二，用后即焚）。
> 5. R64 遗留项复核（§三 + §二登记不处置项）：逐项判定「仍遗留/已消化/状态漂移」。
> 6. 修复轮：文件集互斥的并行修复子代理 → 协调者合并后全量验证（go build 根+installer、go vet、受影响包 go test -count=1、apply-db-revision-sequence 门禁、scratch 真库契约、web i18n-audit+typecheck）。
> 7. 轮文档 docs/audit/<日期>-r65-48h-audit-round.md + handoff 更新 + 提交推送。
>
> 关键上下文：下一可用迁移 747（745/746/800 已占位，R65 勘误）；selector 权重语义=higher 更高优先级（勿再反转）；installer 凭据三件 R63 已落地 R64 复核健康（验它别重做它）；mock-probe 生产接入仍未拍板，若拍板接入审计其装配面（config 闸三处同源、关闭态五不、auth 旁路、origin 打标已就位）；i18n-audit 与 testbench baseline 门禁 R64 已收紧，声明里别再写旧行为。
