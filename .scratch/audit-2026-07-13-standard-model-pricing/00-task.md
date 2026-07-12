# Audit: /model-pricing 标准模型定价与计费清单

**状态**: in_progress
**优先级**: P0
**创建日期**: 2026-07-13
**关联页面**: https://llm.kxpms.cn/model-pricing

## 任务目标

产品提出 `/model-pricing` 页面的"定价表"必须满足下列业务约束：

1. **每个标准模型一行**：用户浏览列表时看到的是标准模型（canonical model）清单，
   不应该知道是哪家供应商提供，只关心"用这个模型需要花多少成本"。
2. **必须支持批量操作**：管理员能批量设置 / 重置 / 填充全局定价。
3. **专用模型（gemini nano 等）多模态定价**：图片 / 视频 / 音频的计费与图片大小、分辨率
   相关，需要给管理员一种"按模态定价"的扩展入口；现阶段至少要做到：
   - 模型清单展示该模型支持的模态，让管理员一眼看出哪些需要单独定价
   - 数据库结构可扩展多模态 token 的 credit rate，且 `ChargeRequest`
     可以接受多模态 token 计数并查表计费
   - gemini-2.5-flash-image / 多模态 Doubao 等应保留"按多模态 token"的能力扩展空间
4. **修正已有的明细定价表**：
   - `model_credit_rates` 缺少 multimodal 字段（虽然 IR 已支持，但 billing 一侧未读）
   - `BatchUpsertModelRates` 当前是串行循环，新增 atomic 批量 upsert + 批量 reset
   - `StandardModelPricingView` 显示了 "厂家" 列，对用户没意义
   - `BatchUpsert` 在 `manual_*` 全 false 时只 insert NULL → DB 层面 OK，但 admin
     一键"清空所有自定义"入口缺失

## 改动范围（受 rule 42 控制）

| 模块 | 改动 | 估算行数 |
|---|---|---|
| `deploy/sql/objects/tables/model_credit_rates.sql` | 增加 multimodal 字段（image/audio/video）与 modal_* manual flag | +5 |
| `deploy/sql/docs/pricing/2026_07_13_model_credit_multimodal.sql` | ALTER 脚本 | ~20 |
| `maas/model_rates.go` | `ListAdminModelRates` / `UpsertModelRate` / `BatchUpsertModelRates` 增强 + 异步 benchmark | +80 |
| `maas/rates.go` | `BaseRateSet` + `ModelRateValues` 增加 image/audio/video | +20 |
| `maas/service.go` | `ChargeRequest` 接受 multimodal token 计数 | +40 |
| `admin/maas_handlers.go` | `handleMaasSettings` 增加 multimodal settings 校验 | +20 |
| `maas/model_rates_test.go` | 新增 multimodal + batch 单元测试 | +120 |
| `maas/credits_test.go` | 增加 multimodal CalcCredits 用例 | +60 |
| `web/src/api/maas.ts` | 接口字段增 `image/audio/video` | +20 |
| `web/src/views/StandardModelPricingView.vue` | 隐藏 vendor / 增加批量 reset / 显示 modality tag | +60 |
| `web/src/locales/zh-CN/standardModelPricing.ts` | 文案补充 | +30 |
| `docs/changelogs/2026-07-13-standard-model-pricing.md` | changelog | +80 |
| `CHANGELOG.md` | Unreleased 一段 | +20 |

总计修改 ~600 行（rule 42 上限偏紧），按需分多个 commit：
1. commit-1：DB schema + Go struct + 后端单测
2. commit-2：前端 UI 改造
3. commit-3：CHANGELOG + docs

## 改动清单（待实施）

### A. 数据库扩展
- `model_credit_rates` 表新增
  - `credits_per_1m_image_tokens` (bigint, nullable)
  - `credits_per_1m_audio_tokens` (bigint, nullable)
  - `credits_per_1m_video_tokens` (bigint, nullable)
  - `manual_image`, `manual_audio`, `manual_video` (bool)
- 提供 ALTER 脚本 `2026_07_13_model_credit_multimodal.sql`

### B. Go 后端
- `rates.go::BaseRateSet` / `ModelRateValues` 各加 3 个模态字段
- `model_rates.go::AdminModelRateRow` 加 `Modality` 字段（来自 models_canonical.modality）
- `model_rates.go::UpsertModelRate` 接受 multimodal + manual flags
- `model_rates.go::BatchUpsertModelRates` 接受 multimodal + 提供
  "reset all" 模式（cursor shape 改成 `updates` 或 `reset` 二选一）
- `service.go::ChargeRequest` 接受 multimodal token counts 并查表计费

### C. 前端
- `StandardModelPricingView.vue`：去掉 "厂家" 列；强化空状态；增加
  "批量复制" / "批量恢复全局" / "批量应用全局基准" 三种快捷操作
- 列表新增列 "模态"（text / vision / multimodal / embedding 等）
- 手工定价 modal 增加 modal toggle（image/audio/video），但默认隐藏
- `api/maas.ts` 接口同步新增 `credits_per_1m_image_tokens` 等字段

### D. 验证
- `go test ./maas/... -count=1`
- `go build ./cmd/...`
- `golangci-lint run`
- browser-use 实测 `/model-pricing` 页面
- 截图保留 `ui-verify-standard-model-pricing-*.png`

## 关联 commit / 文档

- 关联 commit：
- 关联文档：

## 下一步

执行 → 验证 → CHANGELOG → commit → push
