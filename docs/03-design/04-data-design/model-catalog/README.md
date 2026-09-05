# Model Catalog · 标准模型目录

> 本目录是 `models_canonical` 表的"single source of truth 镜像"，
> 用于在 DB 损坏时快速恢复，也用于运维审计（哪些行来自 seed、
> 哪些由运营手填、哪些由 discovery 补充）。

## 目录结构

```
model-catalog/
├── README.md                       # 本文件（导航）
├── standard-models-canonical.md    # 人读：原厂配置表（165 条）
├── standard-models-canonical.csv   # 机器可读：CSV 快照
├── standard-models-canonical.json  # 机器可读：JSON 快照（含 modality caps 等数组）
└── standard-models-canonical.sql   # 可执行：INSERT ... ON CONFLICT 恢复脚本
```

## 文件用途

| 文件 | 用途 | 大小（2026-08-29 快照） |
|---|---|---|
| `standard-models-canonical.md` | 人读、审计、复核原厂 URL | ~35 KB |
| `standard-models-canonical.csv` | Excel/Sheets 直接打开 | ~13 KB |
| `standard-models-canonical.json` | 程序解析（含数组字段） | ~65 KB |
| `standard-models-canonical.sql` | `psql -f ...` 即可恢复 | ~210 KB |

## 何时使用本目录

1. **DB 重建 / 跨环境同步**：把 `standard-models-canonical.sql` 灌入目标库，
   比导入 `02-seed.sql` 更精准（覆盖了 611 修正后的所有真值）。
2. **审计复核**：用 `diff` 对比 `CSV` 与 DB 导出，判断哪些行被运营热改过。
3. **新模型接入**：先在 markdown 表格中标注原厂 URL 与权威值，再去 DB 写。
4. **季度复核**：对照原厂文档，刷新所有 `context_window` / `modality` 字段。

## 重新生成快照

```bash
# 默认：从 LLM_GATEWAY_DATABASE_URL / DATABASE_URL / PG* 读取连接信息
bash sql/scripts/dump-standard-models.sh

# 本地 Docker 容器模式（最常用）：
PGPASSWORD='...' bash sql/scripts/dump-standard-models.sh \
  --label "$(date -u +%Y%m%d)" \
  --docker llm-gateway-pg \
  --target docs/03-design/04-data-design/model-catalog/
```

输出：
- 同时写到 `--target` 目录（带 `_<label>` 后缀）
- 自动镜像到 `docs/03-design/04-data-design/model-catalog/standard-models-canonical.{csv,json,sql}`

## 关联

| 路径 | 说明 |
|---|---|
| `sql/migrations/startup/611_*.sql` | 本次 611 修正迁移（80 行变更） |
| `sql/schema/02-seed.sql` | 基础种子（已被 611 修正） |
| `modelname/modality_defaults.go` | Go 端 modality 兜底（与本目录保持一致） |
| `modeliqdata/data/standard_iq.json` | AA Intelligence Index IQ 分数（独立维度） |
| `CONTEXT_WINDOW_OVERRIDE_GUIDE.md` | 凭据级覆盖链说明 |

## 维护 SOP

1. **季度复核**：对照 `standard-models-canonical.md` 的厂商 URL，逐行校对
   `context_window` / `modality` / `multimodal_caps`。
2. **新模型发布**：厂商 GA 后 7 个工作日内，按以下顺序更新：
   - 在 `standard-models-canonical.md` 表格中新增一行（含原厂 URL）
   - 在 `models_canonical` 写入新行（source='seed' / 'manual'）
   - 跑 `dump-standard-models.sh` 重生成 CSV/JSON/SQL
   - 提交 PR：标题写 `[catalog] add <vendor>-<model>`，自动同步 4 个文件
3. **弃用 / 下线**：把 `models_canonical.status` 改为 `deprecated` / `hidden`，
   `dump-standard-models.sh` 默认 filter `status='active'` 会自动剔除。

## 历史

| 日期 | 变更 |
|---|---|
| 2026-08-29 | 初版：覆盖 165 条 `source IN ('seed','seed-standard-rollout','db','manual','standard')` 标准模型，611 迁移同步应用 |
