# R13 收尾：模型抽屉修复轮部署 + 复验 + 两机分叉集成（2026-09-11）

## 本轮完成

### 1. 本机重新部署（F3/F4 生效）
- 入口：`./deploy-local.sh`（薄封装 → `scripts/deploy-local.sh deploy`，蓝绿 8781→8782）。
- **幂等预检坑**：8782 已有健康网关时直接 "nothing to do" 退出——重新部署必须先 `docker stop llm-gateway-local-8782`。
- **env 坑（关键）**：go-4 checkout 没有 `.env.local`，且 go-2 的 `.env.local` 里 SECRET_KEY / CREDENTIAL_ENCRYPTION_KEY / ADMIN_PASSWORD(`Veritrans&9527`，含 `&`，必须加引号否则 `source` 当后台符)/ADMIN_API_KEY 与活系统不一致。**正确做法：从活系统 `~/kaixuan/llm-gateway-go/bin/current/env` 抽取 LLM_GATEWAY_* + DATABASE_URL 重建 `.env.local`**（DSN/Redis 用宿主形态 127.0.0.1，容器创建层会自动改写为 host.docker.internal）。
- 结果：build 2081 (897067f3e) → 复验中发现第 6 颗问题并修复 → **build 2082 (cf7256f6)** 在跑；两次凭据解密冒烟均 ✓（providers=587 creds=7 failed=0）。

### 2. 浏览器复验（全过）
- 页头版本徽标 = `cf7256f6 · #2082`。
- `/providers/36994?tab=models`：`claude/opus-5`（junk 行）抽屉**可编辑**（F3 ✓）：标准能力、标准化名、关联 canonical、出站名、上下文覆盖、价格、计费模式、保存节点全部可交互。
- 走链：相似度 98% 建议 → 自动填充 claude-opus-5 → 保存节点 → API 确认 standardized_name/canonical_name 持久化（F4 ✓）→ 切回 "—" 保存 → canonical_id=null 持久化 → 用 99% 建议**还原原状**（opus-5↔opus-5，测试不越权改数据态）。
- Credentials→模型 面板：**"Models (37)"** 37 行正常渲染。
- 抽屉"最佳"徽标仍落 junk 行 `opus-5`(99%) —— 与 handoff 预期一致，属数据态。
- 另注：面板可路由性摘要显示 `0 / 37 可路由`，属探活/路由数据态，留给运营者观察。

### 3. 第 6 颗修复（复验中发现，cf7256f66，已在 main）
- 现象：`GET /api/providers/{id}/credentials/{cid}/models` 500：`column mo.provider_modality does not exist (42703)`。
- 根因：**model_offers 视图定义脑裂**——新装 baseline 有该别名；升级环境视图最后一次重建是 678（其视图体没有它）。共享 DTO 无条件引用必炸。
- 修复：沿用同文件 pm.source 探针先例——启动时双 EXISTS 探测 + compat 回退（canonical modality-only）；`listCredentialModels` 抽 `serveListCredentialModels` offerQuerier seam + pgxmock 30 列回归（TestServeListCredentialModels_StaleViewWithoutProviderModality_Returns200）。admin 包全绿。

### 4. 两机分叉集成（integrate-branch-merge-pattern）
- win11 `433454802`（harden 重放）与本地 `5e98bf625` **diff 逐字节等价**（已验证）。
- 流程：`chore(release)` 提交 2082 版本身份（menu-config.json 仅启动时间戳 churn，还原不提交）→ merge origin/main（仅版本三件套冲突，取 2082 侧）→ `go build ./...` + admin/logging 测试绿 → 推 main（d5be9203b→**4eaa4cf59**）。
- 清理：删除 本地+远端 `fix/r13-logging-hygiene`、远端 `fix/model-drawer-verification-fixes`、本地 `r13-push`（prune 掉 win11 的 stale worktree）。**未 force-push 任何共享分支**。当前停在 main。

## 待运营者决策（本轮不越权）
- `govern-junk-canonical` 诊断（dry-run）：**suspects 15 = fixable 1 + review 1 + withheld 13**。
  - fixable：`3.1-pro`(id=3198350, 仅 provider 36994) → gemini-3.1-pro (1.000)，`-apply` 可自动修。
  - review：`free`(id=2664333) → glm-5.2:free (0.990)。
  - withheld 13：opus-5→claude-opus-5、sonnet-5→claude-sonnet-5、v4-flash/v4-pro→deepseek-*、3.8-flash→gemini-3.8-flash、4.6→grok-4.6 等，全部 0.990–1.000 强证据；有外来自定义别名路由其上，**由运营者复核后 `-apply`**。
  - -apply 后复测：模型抽屉"最佳"徽标应从 junk 行移到 claude-opus-5 / grok-4.6。

## 下机注意
- 下次部署前不要还原 VERSION/version.json（main 已带 2082 身份）；`web/public/menu-config.json` 的时间戳 churn 永远不要提交。
- 若 deploy 报 "gateway already healthy"：先 `docker stop llm-gateway-local-8782`。

## 会话审计（收口，2026-09-12 补记）

- **方式**：本仓库无 `.acc-session-policy` / `scripts/session-governance.sh`（非 opt-in）→ 按 session-audit-gate 约定降级为轻量双轴自审（降级原因即此，记录于本节）。
- **Spec 轴**：对照 handoff 四步（部署→浏览器复验→govern 诊断→两机集成）逐条核对——全部完成，无越界（未 force-push、未 -apply、测试行已还原原状、13 条 withheld 未动）。
- **Standards 轴**：`git diff d5be9203b..main` 通读；F6 沿用同文件 pm.source 探针先例与 offerQuerier seam 惯例；gofmt/vet 干净；无重复/坏味道。
- **合并丢合核查**：main 侧（5f1979f9a→d5be9203b）未触碰本轮 4 个 admin 文件；merge 净带入 `admin/routing.go +11`（win11 侧），无丢合无重复。
- **生产探针证据**：2082 容器日志出现 `resolveOfferListSQL: model_offers.provider_modality missing; using compat SQL` —— 探针按设计评估真实 schema 并显式选择 compat，非偶然走通。
- **同类 42703 暴露面扫描**：baseline 视图 33 列 vs 本地 33 列，缺 `provider_modality`（已被 F6 compat 覆盖）与 `created_at`（全仓无 Go 引用，无害）；`mo.priority` 本地存在。**model_offers 视图无其他潜伏 42703**。
- **2082 上的 UI 复验**：页头 `#2082`；`claude/opus-5` 行抽屉复开，12 个可交互控件在位（模态下拉、保存按钮等），行数据为还原后的原状（opus-5 / —）。
- **已知局限（记录不修）**：探针 sync.Once 取首个请求的 pool 且失败即终身 compat（安全侧默认，重启恢复）；compat 模式下无 canonical 关联的行 modality 恒为 'text'（数据降级换取 200，升级环境视图补齐 provider_modality 后自动恢复完整数据）。
