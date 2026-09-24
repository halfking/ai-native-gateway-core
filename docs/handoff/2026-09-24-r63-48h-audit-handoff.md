# R63 → R64 48h 审计 Handoff

**日期**: 2026-09-24
**上轮**: R63（docs/audit/2026-09-24-r63-48h-audit-round.md，窗口 8ce9da13c..7489c8e92）
**基线**: 本轮收口提交之后的 main HEAD
**下一可用迁移**: 746（745=report_snapshots、800=provider_endpoint_protocols 已占用）

---

## 一、给 R64 的入口清单（按优先级）

1. **[P2] mock-probe 生产接入拍板**：现装配在 cmd/gateway-v2（演示入口），生产
   cmd/gateway 未装配。若拍板接入：复用 cmd/gateway-v2/main.go:59-107 的装配四件
   （config 闸 / 端点注册 / authChain 旁路 / runner），加 settings 热开关评估；若拍板
   不接入：在 README 状态块标注「生产接入已裁决：不/延期」。
2. **[P2] P4 selector 接线（r0924 收尾）**：库函数级障碍 R63 已全清（累计文本契约、
   done+content 分块、StreamError 路由、/v1 URL、命名空间归一、权重方向）。接线步骤
   按 docs/handoff/20260924-r0924-supplier-protocol-optimization-audit.md §三 执行，
   注意：①Candidate.NativeEndpoints + LATERAL 查询要有 ORDER BY（族内确定性，R63 F6）；
   ②weight 语义已统一为 higher=higher priority（R63 F6），LATERAL 直接透传勿再反转；
   ③serialize 多模态 gate（§三.6）与 V800 boot ensure（§三.5）是接线前置。
3. **[P2] PR4 删 python mock**：owner 确认后删 scripts/mocks/llm-mock-upstream/*.py、
   Dockerfile*、llm-mock.conf、docker-compose.yml（保留 tests/local/mocks、
   tests/stress/mocks），验收 grep scripts/mocks = 0。
4. **[P3] 大专项候选**（用户标准清单差距，择一立项）：①sanitizer 跨进程 offset 原子
   预占（smart_sani_guard fail-open 锁根修，INCRBY/分段式）；②nodestatecache 单模块
   收编（S7-4，13 文件空壳→接线或删除裁决）；③auto tier_selector 生产消费
   （AUTO_ROUTING_CLOSED_LOOP_V2_PLAN P0-P4）。
5. **[P3] installer 凭据专项收尾**：容器内 refresh daemon 使能三件套（compose 挂载
   state/→~/.kx-gateway + 文件名对齐 refresh.token→refresh_token + LICENSE_AUTHORITY_URL
   激活后置写 env）；activation.json license_key 迁独立文件；legacy device_code=JWT 清洗。
6. **[P3] R61 §六 1/2/3/4/5 延续**（读面归一 ~7 处 / parse 面 requestID 4 处 /
   hostedtask store 并发形状（多 worker 化前必修）/ UA SSOT / btrim 漂移）。
7. 例行部署：R63 收口提交合入后按 R62 模式部署 154/245（迁移 745/800 将随
   sequence 通道与 boot ensure 落共享 PG——745 有 Go ensure，800 仅 sequence 通道）。

## 二、R64 标准审计轮提示词（可直接拷贝）

> 你是 llm-gateway-go 仓库（/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-2）的审计协调者，执行 R64 48h 审计轮。
>
> 步骤：
> 1. `git fetch && git status` 对齐 origin/main；`git log --since="48 hours ago"` 圈定窗口（R63 收口提交之后的全部提交）。
> 2. codegraph 增量构建若报 malformed 则 `rm -rf .codegraph && codegraph build --no-incremental`（R61/R63 两轮同款坑）。
> 3. 读窗口内新方案文档 + docs/audit/2026-09-24-r63-48h-audit-round.md（上轮发现与登记项）。
> 4. 按轨道分 5-6 组并行只读审计子代理（纪律：禁连库、禁改文件、每条发现带 file:line 证据；库级验证收敛协调者 scratch PG17 容器单点：`docker run -d --rm --name r64-scratch-pg -e POSTGRES_PASSWORD=scratch -e POSTGRES_DB=gwtest -p 55432:5432 postgres:17-alpine`，先建 schema_migrations + 相关表夹具再跑 *RealDB 契约，用后即焚）。
> 5. R63 遗留项复核（§三 1-14）：逐项判定「仍遗留/已消化/状态漂移」。
> 6. 修复轮：文件集互斥的并行修复子代理 → 协调者合并后全量验证（go build 根+installer、go vet、受影响包 go test -count=1、apply-db-revision-sequence 门禁、scratch 真库契约）。
> 7. 轮文档 docs/audit/<日期>-r64-48h-audit-round.md + handoff 更新 + 提交推送。
>
> 关键上下文：下一可用迁移 746；745/800 已占位；selector 权重语义=higher 更高优先级；
> installer 凭据三件（state/refresh.token、~/.kx-gateway/instance.token、enrollment.
> ReadInstanceToken 三路回退）R63 已落地，验它别重做它；mock-probe 生产接入若已拍板，
> 审计其装配面（config 闸三处同源、关闭态五不、auth 旁路）。

## 三、给 owner 的两个待拍板问题

1. mock-probe 是否接入生产 cmd/gateway（行为变更：新增 4 端点 + mock_probe_history 表写入）。
2. scripts/mocks python mock 资产是否确认删除（PR4）。
