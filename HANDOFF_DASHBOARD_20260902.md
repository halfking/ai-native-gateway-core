# Handoff · Dashboard 模型节点负载均衡与请求记录缺失

- **handoff_id**: smm_v1_733fd64ad8c769c2
- **branch**: `fix/gateway-provider-survival-20260901`
- **HEAD**: `2aa1f5b1f` (`fix(gateway): close provider recovery and compression gaps`)
- **module**: `llm-gateway-go` (version 2.4.7-2aa1f5b1-20260901-1882)
- **working dir**: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go`
- **today**: 2026-09-02
- **ui entry**: `http://localhost:8782/dashboard`

## 1. 用户报告的现象

1. 在 `/dashboard` 页面点击 `minimax-m3` 模型，展开节点列表与请求记录面板时，**每一行请求记录缺少请求 ID 与请求标题**，看上去"看不清"。
2. 同一模型层下的多个节点中，**实际承担请求的几乎全是 `MiniMax/minimax-prod-v2`**，其他节点没有被均衡命中，看起来负载均衡失效。

## 2. 假设与定位方向（必须在动手前证实/证伪）

### 2.1 请求记录缺少 ID/标题
- **猜想 A**：`/dashboard` 的请求记录表格列定义中就没有 `id` 和 `title`/`subject` 字段，前端从未渲染，只是 UI 缺字段。
- **猜想 B**：后端接口（dashboard 拉取的请求列表 API）确实返回了 `id` / `title`，但被前端在表格列映射时丢弃。
- **猜想 C**：数据库 `request_records`（或等价表）根本没写入 `id` 和 `title`，后端拿不到。前端不动就会显示为空。

> 落地原则：先在代码里找出"dashboard 请求记录"组件 + 后端 list 接口，对照 SQL/ORM 字段，逐项确认是 A/B/C 哪一种。

### 2.2 节点负载均衡失效（全是 minimax-prod-v2）
- **猜想 D**：在 gateway 的 provider/model 选择策略里，`minimax-prod-v2` 总是被首选（同分、加权、sticky session 之一），其他 sibling 节点永远进不了候选。
- **猜想 E**：上游 provider registry 里其他节点的健康状态被错误置为 unavailable / 权重为 0 / `recover_at` 设为 null，`640_fix_null_unavailable_recover_at.sql` 这条迁移就是为这个场景打的补丁，但**还没在当前数据库执行**。
- **猜想 F**：路由 key（例如 model id 或 tenant）只匹配到了 `minimax-prod-v2` 这一条规则，其他 sibling 没有匹配规则。

> 落地原则：先看 `settings/spec_gateway.go` / `settings/spec_compression.go`（最近 3 次提交改了它俩）以及 `640_fix_null_unavailable_recover_at.sql` 的迁移内容，再确认数据库中 sibling 节点的 `available` / `unavailable_recover_at` / 权重实际值，才能区分 D/E/F。

## 3. 子代理任务分工（4 个，全部并行）

> 提示词原文在 §2。每个子代理必须各自从仓库代码静态分析出发，**不要依赖其它子代理**。

### 子代理 1：负载均衡修正（Node Balancing Fix）
- **目标**：定位"为何同层节点只有 `minimax-prod-v2` 被命中"，并产出代码修复（设置/路由层）。
- **必读**：
  - `settings/spec_gateway.go`、`settings/spec_compression.go`（最近 3 次提交刚改）
  - `settings/spec_gateway_test.go`、`settings/spec_compression_test.go`
  - `sql/migrations/domain/640_fix_null_unavailable_recover_at.sql`（看是不是已经在数据库生效）
  - `sql/schema/01-schema.sql` 中 provider/model 相关表结构
- **动手原则**：
  - 若 640 这条迁移在 `sql/schema/01-schema.sql` 还没合入→补齐 schema 镜像
  - 若 schema 已合但数据库未执行→在交接里明确"必须手动跑迁移"，不要只改 SQL 文件
  - 路由层若发现 `minimax-prod-v2` 硬编码→修成按权重/可用性选
- **产出**：修改的文件列表 + 关键 diff + 一段中文结论，说明"之前为何只有它"+"现在为什么其他节点也能命中"。

### 子代理 2：请求记录 ID/标题修复（Dashboard Request Record）
- **目标**：让 `/dashboard` 的请求记录表格显示 `request_id` 和 `title`。
- **必读**：
  - `web/src/components/QueuePerspectivePanel.vue`（未提交修改里有它，先看现有列定义）
  - `web/src/views/DashboardView.vue` 或等价 dashboard 入口
  - 后端 dashboard list handler：grep `dashboard` / `request_records` / `requests` 找 handler 文件
  - 对照 ORM/SQL 看是否 select 了 `id` / `title`
- **分类处置**：
  - 纯前端缺列 → 改前端
  - 后端没 select → 改 handler 字段 + 加测试
  - DB 没写 → 在交接里记为"数据侧问题，待 PM 确认是否回填"
- **产出**：修改的文件 + 关键 diff + 截图占位（修复后从浏览器再截一张放到 `data/attachments/2026/09/`）。

### 子代理 3：构建/部署/自测
- **目标**：在本地重新构建 gateway 并在浏览器复现/验证修复。
- **动作**：
  - 执行仓库内现成的本地构建脚本（不动生产，只走 `localhost:8782`）
  - 用 curl 或浏览器打开 `/dashboard`，展开 `minimax-m3`，**截图至少两张**：
    1. 修复后的请求记录（验证 ID/标题列出现且非空）
    2. 同层节点分桶/计数（验证不再 100% 落到 `minimax-prod-v2`）
  - 截图存到 `data/attachments/2026/09/dashboard-*.png`，并在交接文档里用相对路径引用
- **产出**：截图文件 + 一段中文说明（含时间戳 / 请求样本数 / 命中分布）。

### 子代理 4：审计与文档
- **目标**：等 1/2/3 完成后，整理 diff、跑过的命令、截图、剩余 TODO，落到一份审计文档。
- **产出**：`HANDOFF_AUDIT_20260902.md`（与本文件同目录），内容包含：
  - 现象 / 根因 / 修复点（一行一条）
  - 全部修改文件清单 + 提交建议（commit message 草案）
  - 截图引用
  - **未解决项**（如数据库迁移未执行、其它节点数据库权重未调整等）

## 4. 串行依赖

- 1、2、3 可以并行启动。
- 4 **必须等 1/2/3 完成**后才能写。
- 1 与 3 共享"修改的 settings 文件需要重启 gateway 才能生效"，子代理 3 在自测时要主动 reload。

## 5. 提交与推送策略

- **不要**直接在 `fix/gateway-provider-survival-20260901` 上积压多个语义无关的 commit。
- 由子代理 4 给出建议分支名（例如 `fix/dashboard-record-fields-20260902` + `fix/gateway-node-balancing-20260902`），按语义拆 commit。
- 用户明确要求"提交并推送到主分支"，但推送前必须确认 CI 通过；先合到 feature 分支，提 PR，主分支由 reviewer 合入。

## 6. 当前未提交修改的处置建议

下列 10 个 modified 文件与本 handoff 主题（负载均衡 + dashboard 记录）**无直接关系**，由子代理 4 决定：
- 与本次任务强相关 → 一并提交
- 无关 → 拆到独立 commit 或保留 stash

清单（来自 §1.6）：
VERSION、scripts/deploy-local.sh、scripts/local-host-layout-helper.sh、
sql/migrations/domain/363_featured_models_standard.sql、sql/migrations/domain/640_fix_null_unavailable_recover_at.sql、
sql/schema/01-schema.sql、version.json、web/public/menu-config.json、web/public/version.json、
web/src/components/QueuePerspectivePanel.vue

> 关注点：`QueuePerspectivePanel.vue` **可能**就是 dashboard 请求记录组件，子代理 2 必须先确认；`640_fix_null_unavailable_recover_at.sql` 是本次任务的核心补丁，子代理 1 必须核对是否已经在 schema 镜像里。
