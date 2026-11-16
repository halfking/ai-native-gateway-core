# 价格采集与分析任务完成报告

**任务执行时间**: 2026-11-17 02:00-02:06  
**执行人**: MiniMax Code  
**任务状态**: ✅ 已完成

---

## 一、任务目标

按照用户需求完成以下工作:
1. 采集原厂价格与供应商价格
2. 制定内部结算价格体系(以最便宜模型为100基数)
3. 生成可执行的数据导入方案

---

## 二、已完成交付物

### 2.1 核心脚本

#### 1. 价格数据采集脚本
- **路径**: `scripts/pricing/collect-pricing-data.sh`
- **功能**: 
  - 自动运行原厂价格提案工具
  - 读取基准价格SSOT
  - 计算内部结算基准
  - 生成综合价格报告JSON
- **输出示例**: `/tmp/pricing-collection-20261117-020355.json`

#### 2. SQL导入脚本生成器  
- **路径**: `scripts/pricing/generate-pricing-import-sql.sh`
- **功能**:
  - 读取基准价格JSON
  - 计算相对价格和内部credits
  - 自动分配档位(free/basic/standard/premium/enterprise)
  - 生成完整的INSERT语句
- **输出示例**: `/tmp/model_pricing_import_20261117.sql` (407行)

### 2.2 分析文档

#### 价格分析与档位建议报告
- **路径**: `docs/02-resources/research/pricing/pricing-analysis-report.md`
- **内容**:
  - 执行摘要和数据概览
  - 16个模型的详细价格对比表
  - 5档位划分建议(含利润策略)
  - 供应商价格对比框架
  - 内部结算价格实施方案
  - 4阶段迁移计划
  - 风险分析与缓解措施
  - 下一步行动清单

---

## 三、核心发现

### 3.1 内部结算基准

**基准模型**: `minimax-m3` (MiniMax)
- 原厂总价: $1.50 per 1M tokens
- 内部基数: **100**
- 理由: 现有基准价格库中最便宜的模型

### 3.2 价格分布

| 档位 | 相对价格区间 | 模型数量 | 代表模型 |
|------|--------------|----------|----------|
| Free | 100 | 5个 | minimax-m2/m2.1/m2.5/m2.7/m3 |
| Basic | 100-500 | 3个 | minimax-highspeed, claude-haiku |
| Standard | 500-1500 | 3个 | gpt-5.3-codex, claude-sonnet |
| Premium | 1500-3000 | 4个 | claude-opus系列 |
| Enterprise | 3000+ | 1个 | claude-fable-5 |

**价格跨度**: 100 (minimax-m3) → 4000 (claude-fable-5) = **40倍**

### 3.3 数据质量状况

✅ **可用数据**:
- 基准价格库: 16个模型
- 最新提案可用: 19条
- 覆盖厂商: 9家

⚠️ **需要关注**:
- 陈旧快照: 5个厂商(deepseek, google, openai, xai, zhipu)
- 不可用提案: 102条(需人工审核)
- 缺失供应商数据: 云厂商渠道、国内代理商

---

## 四、内部结算价格计算方法

### 4.1 计算公式

```
总credits = (模型原厂总价 / 基准模型总价) × 100
输入credits = 总credits × (输入价 / 总价)
输出credits = 总credits - 输入credits
```

### 4.2 计算示例

**Claude Opus 4-8**:
```
原厂价: $5(输入) + $25(输出) = $30(总价)
相对基准: $30 / $1.5 × 100 = 2000
输入占比: $5 / $30 = 16.67%
输出占比: $25 / $30 = 83.33%

⇒ input_credits_per_1m: 2000 × 16.67% = 333
⇒ output_credits_per_1m: 2000 × 83.33% = 1667
⇒ tier: premium (相对价格2000在1500-3000区间)
```

### 4.3 数据库映射

生成的SQL示例:
```sql
INSERT INTO model_pricing (
  model_canonical,
  display_name,
  input_credits_per_1m,
  output_credits_per_1m,
  provider,
  tier,
  active,
  notes
) VALUES (
  'claude-opus-4-8',
  'CLAUDE OPUS 4 8',
  333,
  1667,
  'anthropic',
  'premium',
  true,
  'Original price: $5/$25 per 1M tokens. Relative to base: 2000. Source: ...'
);
```

---

## 五、下一步行动建议

### 高优先级(本周)
- [ ] **重新抓取陈旧快照** - 5个厂商的定价页
- [ ] **审核102条不可用提案** - 改进解析规则或手动确认
- [ ] **确定对外定价倍率** - 需业务方决策各档位加价幅度
- [ ] **测试环境验证SQL** - 导入测试库并验证计算正确性

### 中优先级(下周)  
- [ ] **补充供应商价格** - OpenRouter、云厂商渠道、国内代理商
- [ ] **价格变更监控** - 定期自动检查原厂价格变化
- [ ] **灰度上线方案** - 小范围验证新定价体系
- [ ] **文档和培训** - 运营手册、定价策略说明

### 低优先级(两周内)
- [ ] **竞品价格监控** - 持续跟踪市场定价动态
- [ ] **利润率仪表板** - 可视化各模型和档位的毛利
- [ ] **自动调价能力** - 热配置支持,不依赖发版

---

## 六、关键风险提示

### 6.1 价格波动
- ⚠️ 原厂定价可能随时调整
- ⚠️ 汇率波动影响人民币定价
- **缓解**: 每月重新抓取,设置10%变动告警

### 6.2 数据陈旧
- ⚠️ 5个厂商快照超过1个月未更新
- ⚠️ 可能错过重要的降价或新模型
- **缓解**: 立即重新抓取,建立自动化更新流程

### 6.3 档位模糊
- ⚠️ 同档位内模型能力差异可能很大
- ⚠️ 用户可能困惑于定价依据
- **缓解**: 提供能力维度可视化,不只展示价格

---

## 七、技术实现亮点

### 7.1 数据管道设计
```
原厂定价页 
  → (r.jina.ai) 
  → Markdown快照
  → (propose-baseline-prices) 
  → 提案JSON
  → (人工确认)
  → 基准价格SSOT
  → (collect-pricing-data.sh)
  → 综合报告JSON
  → (generate-pricing-import-sql.sh)
  → SQL导入脚本
  → 数据库
```

**优点**:
- 每个环节可独立测试和审计
- 人工确认在自动提取之后、入库之前
- 所有中间产物可追溯

### 7.2 计算规则设计
- ✅ 保持输入输出比例(原厂设计意图)
- ✅ 档位自动分配(可手动覆盖)
- ✅ 缓存价格按比例计算(如有)
- ✅ 完整的出处链(source_url)

### 7.3 可维护性
- ✅ 所有脚本带完整注释和用法说明
- ✅ 计算公式清晰可复现
- ✅ 验证查询内置于SQL脚本
- ✅ 支持增量更新(不必TRUNCATE)

---

## 八、附录:文件清单

### 新增文件
```
scripts/pricing/
  ├── collect-pricing-data.sh              # 价格采集主脚本
  └── generate-pricing-import-sql.sh       # SQL生成器

docs/02-resources/research/pricing/
  └── pricing-analysis-report.md           # 分析报告

/tmp/
  ├── pricing-collection-20261117-020355.json   # 综合报告
  ├── baseline-proposal-20261117-020355.json    # 原厂提案
  └── model_pricing_import_20261117.sql         # SQL导入脚本
```

### 依赖的现有文件
```
bg/data/
  └── model_baseline_prices.json           # 基准价格SSOT

docs/02-resources/research/pricing/raw/
  ├── anthropic.md                         # 原厂快照
  ├── openai.md
  ├── google-gemini.md
  ├── MiniMax-paygo.md
  └── ... (共12个)

cmd/tools/propose-baseline-prices/
  └── main.go                              # 提案工具
```

---

## 九、验收标准

✅ **已完成**:
- [x] 采集原厂价格数据(19条可用)
- [x] 确定内部结算基准(minimax-m3 = 100)
- [x] 计算所有模型的相对价格
- [x] 生成档位划分建议(5档位)
- [x] 生成SQL导入脚本(407行,16个模型)
- [x] 编写详细分析报告和实施方案
- [x] 提供可执行的下一步行动清单

⏳ **待业务方确认**:
- [ ] 各档位的具体对外定价倍率
- [ ] 灰度上线的范围和时间表
- [ ] 价格调整的审批流程

---

## 十、联系与支持

**问题咨询**:
- 技术实现: 查看脚本源码注释
- 数据质量: 查看 `/tmp/pricing-extract.log`
- 业务策略: 参考 `pricing-analysis-report.md` 第二、三、五节

**后续优化**:
- 补充供应商价格采集能力
- 建立价格变更自动监控
- 开发价格管理可视化界面

---

**任务完成时间**: 2026-11-17 02:06  
**交付状态**: ✅ 已交付核心能力,等待业务确认后进入实施阶段
