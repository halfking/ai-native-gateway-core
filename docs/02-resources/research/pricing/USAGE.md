# 模型定价采集与分析工具使用指南

**创建时间**: 2026-11-17  
**工具版本**: v1.0  
**适用范围**: 原厂价格采集、内部结算价格计算、数据库导入

---

## 📋 目录

1. [快速开始](#快速开始)
2. [工具说明](#工具说明)
3. [完整工作流程](#完整工作流程)
4. [核心概念](#核心概念)
5. [故障排查](#故障排查)
6. [文档索引](#文档索引)

---

## 🚀 快速开始

### 一键采集价格数据

```bash
cd /path/to/llm-gateway-go-cursor

# 运行综合采集脚本
./scripts/pricing/collect-pricing-data.sh

# 查看输出
cat /tmp/pricing-collection-*.json | jq .
```

### 生成数据库导入SQL

```bash
# 生成SQL
./scripts/pricing/generate-pricing-import-sql.sh > /tmp/import.sql

# 预览前50行
head -50 /tmp/import.sql
```

### 查看可视化报告

```bash
# 在浏览器中打开
open docs/02-resources/research/pricing/pricing-visualization.html
```

---

## 🛠️ 工具说明

### 1. collect-pricing-data.sh

**位置**: `scripts/pricing/collect-pricing-data.sh`

**功能**:
- 运行原厂价格提案工具 (`cmd/tools/propose-baseline-prices`)
- 读取基准价格SSOT (`bg/data/model_baseline_prices.json`)
- 计算内部结算基准(最便宜模型为100)
- 生成综合价格报告JSON

**用法**:
```bash
./scripts/pricing/collect-pricing-data.sh [-out OUTPUT_FILE] [-raw RAW_DIR]
```

**参数**:
- `-out`: 输出JSON文件路径 (默认: `/tmp/pricing-collection-YYYYMMDD-HHMMSS.json`)
- `-raw`: 原厂快照目录 (默认: `docs/02-resources/research/pricing/raw`)

**输出示例**:
```json
{
  "generated_at": "2026-11-17T02:01:33Z",
  "summary": {
    "baseline_models": 16,
    "proposal_ready": 19,
    "proposal_unusable": 102
  },
  "internal_settlement_base": {
    "model": "minimax-m3",
    "vendor": "minimax",
    "original_price": {
      "input_per_1m": 0.3,
      "output_per_1m": 1.2,
      "total_per_1m": 1.5,
      "currency": "USD"
    },
    "internal_base_value": 100
  },
  "baseline_prices": { ... },
  "vendor_proposals": [ ... ],
  "pricing_tiers": [ ... ]
}
```

### 2. generate-pricing-import-sql.sh

**位置**: `scripts/pricing/generate-pricing-import-sql.sh`

**功能**:
- 读取基准价格JSON
- 计算每个模型的相对价格
- 自动分配档位(free/basic/standard/premium/enterprise)
- 生成完整的SQL INSERT语句

**用法**:
```bash
./scripts/pricing/generate-pricing-import-sql.sh [OUTPUT_FILE]
```

**参数**:
- `OUTPUT_FILE`: 可选,输出SQL文件路径 (默认输出到stdout)

**生成的SQL结构**:
```sql
BEGIN;

INSERT INTO model_pricing (
  model_canonical,
  display_name,
  input_credits_per_1m,
  output_credits_per_1m,
  cache_write_credits_per_1m,
  cache_read_credits_per_1m,
  provider,
  tier,
  active,
  notes
) VALUES (...);

-- 更多INSERT语句 ...

COMMIT;

-- 验证查询
SELECT tier, COUNT(*) as model_count, ...
FROM model_pricing
WHERE active = true
GROUP BY tier;
```

---

## 🔄 完整工作流程

### 阶段1: 抓取原厂定价(每月执行)

```bash
cd docs/02-resources/research/pricing

# 使用既有的抓取脚本
./scripts/fetch-pricing.sh

# 检查快照新鲜度
ls -lh raw/
```

### 阶段2: 生成价格提案

```bash
# 运行提案工具(由 collect-pricing-data.sh 自动调用)
go run ./cmd/tools/propose-baseline-prices \
  -raw docs/02-resources/research/pricing/raw \
  -out /tmp/proposal.json

# 查看统计
jq '.counts_by_confidence' /tmp/proposal.json
```

**输出解读**:
- `table_row`: 可用的价格条目(从表格行提取)
- `unusable`: 需人工审核的条目

### 阶段3: 人工确认(关键步骤)

⚠️ **重要**: 未经人工确认的价格不得进入生产系统!

```bash
# 查看待审核的价格
jq '.ready_to_review[] | {model, vendor, input: .input_per_1m, output: .output_per_1m}' /tmp/proposal.json

# 对照原厂页面逐条确认
# 确认后更新 bg/data/model_baseline_prices.json
```

### 阶段4: 综合采集

```bash
# 运行综合采集脚本
./scripts/pricing/collect-pricing-data.sh

# 查看结果
cat /tmp/pricing-collection-*.json | jq .summary
```

### 阶段5: 生成SQL导入

```bash
# 生成SQL
./scripts/pricing/generate-pricing-import-sql.sh > /tmp/import.sql

# 验证SQL语法
psql --dry-run < /tmp/import.sql  # 如果你的psql支持
# 或者
cat /tmp/import.sql | grep "INSERT INTO" | wc -l  # 统计INSERT数量
```

### 阶段6: 测试环境验证

```bash
# 连接测试库
export TEST_DSN="postgresql://user:pass@testhost:5432/testdb"

# 执行导入
psql "$TEST_DSN" < /tmp/import.sql

# 验证结果
psql "$TEST_DSN" -c "
  SELECT tier, COUNT(*) as count,
         MIN(input_credits_per_1m + output_credits_per_1m) as min_total,
         MAX(input_credits_per_1m + output_credits_per_1m) as max_total
  FROM model_pricing
  WHERE active = true
  GROUP BY tier
  ORDER BY min_total;
"
```

**预期输出**:
```
   tier     | count | min_total | max_total
------------+-------+-----------+-----------
 free       |     5 |       100 |       100
 basic      |     3 |       200 |       400
 standard   |     3 |      1050 |      1200
 premium    |     4 |      2000 |      2000
 enterprise |     1 |      4000 |      4000
```

### 阶段7: 生产环境部署

```bash
# 备份现有数据
pg_dump "$PROD_DSN" -t model_pricing > /tmp/model_pricing_backup_$(date +%Y%m%d).sql

# 执行导入(谨慎!)
psql "$PROD_DSN" < /tmp/import.sql

# 验证
psql "$PROD_DSN" -c "SELECT COUNT(*) FROM model_pricing WHERE active = true;"
```

---

## 📚 核心概念

### 内部结算基数

**设计原则**: 以最便宜模型的原厂价格作为100基数,其他模型按倍率计算。

**当前基准**:
- **模型**: minimax-m3 (MiniMax)
- **原厂价格**: $0.30 (输入) / $1.20 (输出) = $1.50 total per 1M tokens
- **内部基数**: 100 credits

**计算公式**:
```
总credits = (模型原厂总价 / 基准总价) × 100
输入credits = 总credits × (输入价 / 总价)
输出credits = 总credits - 输入credits
```

### 档位划分规则

| 档位 | 相对价格区间 | 建议毛利率 | 典型模型 |
|------|------------|-----------|---------|
| **Free** | = 100 | 0% | minimax-m2系列 |
| **Basic** | 100-500 | 20-30% | minimax-highspeed, claude-haiku |
| **Standard** | 500-1500 | 30-50% | gpt-5.3-codex, claude-sonnet |
| **Premium** | 1500-3000 | 50-80% | claude-opus系列 |
| **Enterprise** | 3000+ | 定制 | claude-fable-5 |

### 计算示例

**示例1: Claude Opus 4-8**

```
原厂价格:
  输入: $5.00 per 1M tokens
  输出: $25.00 per 1M tokens
  总价: $30.00 per 1M tokens

计算过程:
  相对基准 = $30.00 / $1.50 × 100 = 2000
  输入占比 = $5.00 / $30.00 = 16.67%
  输出占比 = $25.00 / $30.00 = 83.33%

结果:
  input_credits_per_1m: 2000 × 16.67% = 333
  output_credits_per_1m: 2000 × 83.33% = 1667
  tier: premium (2000在1500-3000区间)
```

**示例2: Claude Haiku 4-5**

```
原厂价格:
  输入: $1.00 per 1M tokens
  输出: $5.00 per 1M tokens
  总价: $6.00 per 1M tokens

计算过程:
  相对基准 = $6.00 / $1.50 × 100 = 400
  输入占比 = $1.00 / $6.00 = 16.67%
  输出占比 = $5.00 / $6.00 = 83.33%

结果:
  input_credits_per_1m: 400 × 16.67% = 67
  output_credits_per_1m: 400 × 83.33% = 333
  tier: basic (400在100-500区间)
```

---

## 🐛 故障排查

### 问题1: 提案工具报"stale"快照

**现象**:
```
[stale] openai.md: SKIPPED — this snapshot's content is 0 day(s) older...
```

**原因**: 快照内容的发布时间早于抓取时间

**解决**:
```bash
# 重新抓取陈旧的快照
cd docs/02-resources/research/pricing
./scripts/fetch-pricing.sh

# 或者使用 -max-snapshot-age-days 参数强制使用
go run ./cmd/tools/propose-baseline-prices \
  -raw raw \
  -out /tmp/proposal.json \
  -max-snapshot-age-days 60  # 允许60天内的快照
```

### 问题2: SQL导入失败

**现象**: psql报错,数据未导入

**可能原因1 - 主键冲突**:
```
ERROR: duplicate key value violates unique constraint "model_pricing_pkey"
```

**解决**:
```sql
-- 查看已存在的模型
SELECT model_canonical FROM model_pricing;

-- 改用 UPDATE 或 ON CONFLICT
INSERT INTO model_pricing (...)
VALUES (...)
ON CONFLICT (model_canonical) DO UPDATE SET
  input_credits_per_1m = EXCLUDED.input_credits_per_1m,
  output_credits_per_1m = EXCLUDED.output_credits_per_1m,
  ...;
```

**可能原因2 - 外键约束**:
```
ERROR: insert or update on table "model_pricing" violates foreign key constraint
```

**解决**:
```sql
-- 检查 provider 是否存在
SELECT provider_name FROM providers;

-- 先插入缺失的 provider
INSERT INTO providers (provider_name, ...) VALUES (...);
```

### 问题3: 可视化页面无数据

**现象**: HTML页面打开但图表为空

**原因**: 浏览器CORS限制(直接用file://打开)

**解决**:
```bash
# 方案1: 使用本地HTTP服务器
cd docs/02-resources/research/pricing
python3 -m http.server 8000
# 访问 http://localhost:8000/pricing-visualization.html

# 方案2: 使用PHP内置服务器
php -S localhost:8000
```

### 问题4: jq命令不存在

**现象**:
```
bash: jq: command not found
```

**解决**:
```bash
# macOS
brew install jq

# Ubuntu/Debian
sudo apt-get install jq

# 或者手动查看JSON
cat /tmp/pricing-collection-*.json | python3 -m json.tool
```

---

## 📖 文档索引

### 主要文档

1. **pricing-analysis-report.md**
   - 详细的价格分析和档位建议
   - 供应商价格对比框架
   - 4阶段迁移计划
   - 风险分析与缓解措施
   - 适合: 产品经理、运营、定价决策者

2. **pricing-collection-completion-report.md**
   - 任务执行总结
   - 已完成交付物清单
   - 核心发现和数据质量状况
   - 下一步行动建议
   - 适合: 技术团队、项目管理

3. **pricing-visualization.html**
   - 交互式价格可视化
   - 模型相对价格柱状图
   - 档位分布饼图
   - 完整价格清单表格
   - 适合: 所有相关方

4. **本文档 (USAGE.md)**
   - 工具使用指南
   - 完整工作流程
   - 故障排查
   - 适合: 开发者、运维

### 相关代码

- `cmd/tools/propose-baseline-prices/main.go` - 原厂价格提案工具
- `bg/data/model_baseline_prices.json` - 基准价格SSOT
- `sql/objects/tables/model_pricing.sql` - 数据库表定义

---

## ⚠️ 重要提示

### 价格确认流程

**必须遵守的流程**:
1. ✅ 自动提取 → 生成提案
2. 👤 **人工确认** → 逐条核对原厂页
3. ✅ 更新SSOT → 进入基准价格库
4. 🔄 生成SQL → 导入数据库

⚠️ **绝对不要跳过第2步**! 未经人工确认的价格不得进入生产系统。

### SQL执行安全

- ⚠️ 脚本中的 `TRUNCATE` 默认注释掉
- ⚠️ 生产环境执行前务必备份
- ✅ 支持增量导入(但会遇到主键冲突)
- 🔍 导入后务必执行验证查询

### 数据新鲜度

- 📅 建议每月重新抓取一次原厂价格
- 🚨 原厂价格变动>10%应立即告警
- ⏰ 当前有5个厂商快照标记为"陈旧"

---

## 📞 获取帮助

**技术问题**:
- 查看脚本源码注释
- 检查 `/tmp/pricing-extract.log`

**数据问题**:
- 参考 `pricing-analysis-report.md`
- 对照原厂定价页人工验证

**业务问题**:
- 查看档位划分建议
- 咨询产品/运营团队

---

**文档版本**: 1.0  
**最后更新**: 2026-11-17  
**维护者**: MiniMax Code
