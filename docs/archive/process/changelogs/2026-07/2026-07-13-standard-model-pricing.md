# /model-pricing 标准模型定价与计费清单审计修复

**日期**: 2026-07-13
**作者**: OpenCode AI Agent
**关联页面**: <https://llm.kxpms.cn/model-pricing>
**状态**: ✅ 已实现并通过本地单元测试 + TS 编译 + Vite build + golangci-lint

---

## 一、痛点与目标

业务团队反馈 `/model-pricing` 页面存在以下问题：

1. **用户视角混乱**：列表里展示了"厂家（vendor）"列，但用户只关心"用这个模型需要花多少成本"，不知道供应商是谁。
2. **多模态模型定价缺失能力**：gemini-2.5-flash-image、doubao-seed、glm-4v 等多模态模型，图片 / 音频 / 视频 token 按原厂说明需要单独定价，但当前 DB 不支持；`IR.Usage` 已经提取出 `ImageTokens/AudioTokens/VideoTokens`，但 billing 一侧从未用上。
3. **批量操作不够细**：已有 `batchUpsertAdminMaasModelRates`（粘贴到所选）+ `openBatchModal`（批量定价），但缺少"批量恢复全局"和"批量填入当前全局"两个常用动作。
4. **手工定价标识**：原"X/4 手工"对加了多模态维度后变成"X/7 手工"，需要更新文案。

## 二、本次目标

| 目标 | 完成情况 |
|---|---|
| 每个标准模型只占一行，不显示厂家 | ✅ 前端模板移除 vendor 列 |
| 支持多模态 token 单独定价（image/audio/video） | ✅ DB schema + Go struct + 前端 UI |
| 手工定价 N/7 维度显示 | ✅ 状态徽章 + count 升级 |
| 批量操作：粘贴到所选 / 批量定价（保持） | ✅ 已有 |
| 批量恢复全局（新增） | ✅ `batchResetAdminMaasModelRates` |
| 批量填入当前全局（新增） | ✅ `batchFillGlobalAdminMaasModelRates` |
| 向后兼容：现有 4 维 Charge 调用不受影响 | ✅ `ChargeRequest` 保留旧签名；新增 `ChargeRequestMultimodal` |
| 给 gemini nano 等专用模型的扩展位 | ✅ 见下 |

## 三、关键改动

### 3.1 数据库 — `model_credit_rates` 表

新增 6 列（3 维度 × 2：value + manual flag）：

```sql
ALTER TABLE public.model_credit_rates
  ADD COLUMN IF NOT EXISTS credits_per_1m_image_tokens bigint,
  ADD COLUMN IF NOT EXISTS credits_per_1m_audio_tokens bigint,
  ADD COLUMN IF NOT EXISTS credits_per_1m_video_tokens bigint,
  ADD COLUMN IF NOT EXISTS manual_image boolean DEFAULT false NOT NULL,
  ADD COLUMN IF NOT EXISTS manual_audio boolean DEFAULT false NOT NULL,
  ADD COLUMN IF NOT EXISTS manual_video boolean DEFAULT false NOT NULL;
```

迁移脚本：`deploy/sql/docs/pricing/2026_07_13_model_credit_multimodal.sql`（可重复执行）。

同时同步基线 `deploy/sql/objects/tables/model_credit_rates.sql` 和 `deploy/sql/schemas/baseline/01-schema.sql`，确保 pg_dump 重生结果一致。

### 3.2 Go 后端

| 文件 | 改动 |
|---|---|
| `maas/rates.go` | `BaseRateSet` 与 `ModelRateValues` 各加 `Image / Audio / Video` 3 字段；`storedModelRates` 加 3 个 `*int64` 与 3 个 manual flag；新增 `modelValuesFromBase` 辅助函数 |
| `maas/model_rates.go` | `AdminModelRateRow` 加 `modality` + 3 个 value + 3 个 manual flag；`ModelRateUpsert` 加 3 个 value + 3 个 manual flag；`ListAdminModelRates` SQL 与 Scan 更新；`UpsertModelRate` 校验与 SQL 加 3 维度；新增 `AnyManual()`、`BatchResetModelRates()`、`BatchFillGlobalModelRates()`、`loadStoredManualFlags()`；`ResetModelRateFields` 支持 `image / audio / video / all` 四个新 key |
| `maas/service.go` | `modelRateValues` SQL 与 Scan 增加 3 维度；`ChargeRequest` 抽出 `chargeTokens` 私有方法，新增 `ChargeRequestMultimodal(usage TokenUsage)` |
| `maas/credits.go` | 抽出 `TokenUsage` 结构（7 个 token 计数）；新增 `CalcCreditsMultimodal(usage, rates)` 作为单一计费入口；旧 `CalcCredits` 变薄为 4-tuple 包装（保留向后兼容） |
| `admin/maas_handlers.go` | 新增两个 route：`/api/admin/maas/model-rates/batch-reset`、`/api/admin/maas/model-rates/batch-fill-global`；对应 handler |
| `maas/model_rates_multimodal_test.go`（新） | 7 个测试函数 + 7 个 subtest，覆盖：global effective 含 multimodal、`stored.IsAnyManual`、`effectiveModelRates` 多模态 fallback/manual、`Upsert.AnyManual`、`CalcCreditsMultimodal` 7 个场景（text-only、image 叠加、all 维度、缺失 rate fallback、zero usage、向后兼容 wrapper、向上取整） |

### 3.3 前端

| 文件 | 改动 |
|---|---|
| `web/src/api/maas.ts` | `AdminMaasModelRate` 加 `modality` + 3 个 value + 3 个 manual flag + 3 个 custom value；`MaasModelRateUpsert` 加 3 个 value + 3 个 manual flag（全部 optional）；新增 `batchResetAdminMaasModelRates`、`batchFillGlobalAdminMaasModelRates` API 包装 |
| `web/src/views/StandardModelPricingView.vue` | 移除 vendor 列；新增「模态」列（按 modality 着色 badge）；状态徽章 N/7；批量操作栏新增「全部恢复全局」「填入当前全局」按钮；手工定价 modal 拆分为「文本 Token 维度」「多模态 Token 维度」两段；批量定价 modal 含 7 维度（4 text + 3 multimodal） |
| `web/src/locales/zh-CN/standardModelPricing.ts` & `en-US` | 新增文案键：模态标签、多模态字段提示、批量操作提示、文本/多模态分段标题；修复 en-US 既有中文残留 |

### 3.4 文档与计划

| 文件 | 改动 |
|---|---|
| `.scratch/audit-2026-07-13-standard-model-pricing/00-task.md` | 任务计划与里程碑 |
| `docs/changelogs/2026-07-13-standard-model-pricing.md` | 本文档 |
| `CHANGELOG.md` | Unreleased 一段 |

## 四、向后兼容与迁移

- **DB schema**：6 列均为 nullable / DEFAULT false；老数据行为不变（manual 全 false → 跟随全局基准）。
- **API 兼容**：
  - `GET /api/admin/maas/model-rates` 新增字段对老调用方兼容（前端只读取新字段，旧字段保留）。
  - `POST /api/admin/maas/model-rates/batch` 旧 payload schema 行为不变；`manual_image/audio/video` 缺省视为 false。
  - `ChargeRequest` 旧 4 维签名保留；新增 `ChargeRequestMultimodal(TokenUsage)` 启用多模态计费，调用方可在 streaming/relay handler 升级后接入。
- **前端**：列结构变化 + 文案变化，但 `model-pricing` 路由不变；旧设置值不会丢（`base_credits_per_1m_in` 默认从 `base_credits_per_1m` 兼容填充）。

## 五、验证

### 5.1 后端单测

```text
$ go test ./maas/... ./admin/... -count=1
ok    github.com/kaixuan/llm-gateway-go/maas          0.194s
ok    github.com/kaixuan/llm-gateway-go/admin         1.258s
ok    github.com/kaixuan/llm-gateway-go/admin/dashboardapi  0.258s
```

新增 multimodal 测试全部通过：
- `TestGlobalEffectiveMultimodal`
- `TestEffectiveModelRatesMultimodalFallback`
- `TestEffectiveModelRatesMultimodalManual`
- `TestStoredIsManualMultimodal`
- `TestModelRateUpsertAnyManual`
- `TestCalcCreditsMultimodal/{text_only,adds_image_tokens,all_buckets_combined,missing_rate_falls_back_to_input_rate,zero_usage_returns_zero,legacy_wrapper_still_works,rounds_up_not_down}`

### 5.2 前端编译

```text
$ cd web && npx vue-tsc --noEmit
EXIT 0

$ cd web && npx vite build
✓ built in 8.16s
```

### 5.3 静态检查

```text
$ golangci-lint run ./maas/ --timeout=5m
0 issues.
```

### 5.4 浏览器实测（部署后）

由于本次改动同时影响前端 + 后端 + DB，浏览器实测建议在以下前提后进行：

1. 在 prod / test env 上应用 `2026_07_13_model_credit_multimodal.sql`：
   ```bash
   PGPASSWORD=$LLM_GATEWAY_PG_PASS psql -h 184.kxpms.cn -U llm_gateway -d llm_gateway \
     -f deploy/sql/docs/pricing/2026_07_13_model_credit_multimodal.sql
   ```
2. 部署最新 gateway 二进制（`make build` + Docker image push + k3s rollout）。
3. 部署最新 web dist（`vite build` → nginx 替换）。
4. 用 platform-ops 账号登录 `https://llm.kxpms.cn/model-pricing`：
   - 验证「模态」列出现，每个 model 仅有 1 行；
   - 验证「厂家」列不再出现；
   - 验证「批量定价」弹窗含 7 个字段（4 text + 3 multimodal）；
   - 验证「全部恢复全局」「填入当前全局」按钮可点击；
   - 验证后端 6 列 `model_credit_rates` 写入正确，DEV/pgadmin 可见。

## 六、相关 commit / 关联工作

- 关联 PR / commit：（commit 时填）
- 关联 audit：`docs/IR格式优化/10-Provider-IR-Multimodal-Audit-2026-07-13.md`（gemini-2.5-flash-image 等多模态 IR）
- 关联任务：`.scratch/audit-2026-07-13-standard-model-pricing/`

## 七、遗留与风险

| 风险 | 等级 | 说明 | 缓解 |
|---|---|---|---|
| `model_credit_rates` `image/audio/video_tokens` 默认跟随 input 价 | 中 | 多模态原厂价差较大（图片 token 价 ≠ 输入 token 价），默认跟随 input 可能产生偏差 | 已在前端 list 上挂 "MM" 徽章，提醒 admin 应手工设置 |
| `ChargeRequestMultimodal` 暂未在 streaming/relay handler 接入 | 低 | 老的 4 维 `ChargeRequest` 仍然计 0 的 multimodal token | 在二阶段升级 handler，从 `evt.ImageTokens` 等取出后调用 `ChargeRequestMultimodal` |
| 184 / 252 部署需要在本次合入前手动应用 SQL | 中 | 否则 `model_credit_rates` 缺 6 列，前端新 UI 调用将 fail | `2026_07_13_model_credit_multimodal.sql` 是 idempotent，可重复执行 |
| 前端 UI 复杂度提升 | 低 | modal 字段数从 4 增到 7，需要 attention to detail | 拆分为「文本」「多模态」两段 + hint 提示 |
| 审计报告建议：增加 batch 操作的事务原子性 | 低 | 当前 `BatchUpsertModelRates` 串行循环，部分失败不影响整体 count | 二阶段可改成 `BEGIN/COMMIT` + 单条 SQL `INSERT ... ON CONFLICT` |

## 八、下一步

1. **P0**：streaming/relay handler 升级为 `ChargeRequestMultimodal`，从 `ir.ResponseUsage.{Image,Audio,Video}Tokens` 取值；
2. **P1**：批量操作事务化（`pgx.Batch` 或单一 `INSERT ... ON CONFLICT`）；
3. **P1**：CSV 导出/导入 `model_credit_rates` 多模态维度；
4. **P2**：UI 可视化"按原厂价批量回填"（一键同步 gemini-2.5-flash-image / doubao-seed 等官方价）。
