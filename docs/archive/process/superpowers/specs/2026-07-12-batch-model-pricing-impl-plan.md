# 实施计划

## 步骤（按依赖顺序）

### Step 1: 后端 — 新增 batch upsert API
**文件：** `maas/model_rates.go` + `admin/maas_handlers.go`
- 新增 `BatchUpsertModelRates(ctx, updates []ModelRateUpsertWithID)` 方法
- 新增 `POST /api/admin/maas/model-rates/batch` handler
- 前端 `web/src/api/maas.ts` 新增 `batchUpsertAdminMaasModelRates()`

### Step 2: 前端 — /model-pricing 增强
**文件：** `web/src/views/StandardModelPricingView.vue`
- 添加 checkbox 列 + 全选
- 每行加「复制」按钮 → clipboard
- 底部批量操作栏：「粘贴到所选」「批量定价」
- 批量定价弹窗
- 编辑弹窗加「复制」「粘贴」

### Step 3: 前端 — /pricing bug 修复 + 功能增强
**文件：** `web/src/views/PricingManagementView.vue`
- 修复 `pasteToSelected`（排查字段对齐、Content-Type、分页丢失等）
- 新增「按模型批量定价」按钮 + 两步式弹窗

## 验证方式
1. /model-pricing: 多选模型 → 批量定价 → 确认表格刷新
2. /model-pricing: 复制一行 → 多选其他行 → 粘贴到所选
3. /pricing: 复制价格 → 多选行 → 粘贴到所选 → 确认更新
4. /pricing: 按模型批量定价 → 选择模型 → 多选 offer → 定价
