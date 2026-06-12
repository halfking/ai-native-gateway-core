# LLM Pricing — 184 k3s llm_gateway DB Sync

> 自动抓取 + 交叉验证 + 双表写入 (`pricing_plans` 源真值 + `model_offers` 快照)

## 国内模型 CNY 计价规则

> **所有国内厂商模型必须使用人民币(CNY)计价**，不可标 USD。

已修正的厂商（60 个 offer，全部 nvidia-build credential_id=8）：

| 厂商 | 模型前缀 | CNY 来源 | Offers |
|---|---|---|---|
| 智谱 Zhipu | glm-* | open.bigmodel.cn/pricing | 12 |
| DeepSeek | deepseek-* | api-docs.deepseek.com/zh-cn | 6 |
| 通义千问 Qwen | qwen-*, qwq-* | help.aliyun.com/model-pricing | 6 |
| 豆包 Doubao | doubao-* | volcengine.com/docs/82379/1544106 | 12 |
| 月之暗面 Moonshot | moonshot-v1-* | platform.moonshot.ai/docs/pricing | 3 |
| MiniMax | minimax-*, abab-* | platform.minimaxi.com/pricing-paygo | 4 |
| 百川 Baichuan | baichuan3-*, baichuan4 | platform.baichuan-ai.com | 3 |
| 零一万物 Yi | yi-* | platform.lingyiwanwu.com | 3 |
| 阶跃星辰 Step | step-* | platform.stepfun.com/pricing | 3 |
| 商汤 SenseChat | sensechat-* | platform.sensenova.cn | 3 |
| BAAI BGE | bge-* | open-source, USD*7.2 估算 | 1 |
| Xiaomi Mimo | mimo-* | USD*7.2 估算 | 1 |

修正脚本：`2026-06-12-cny-fix.sql`

## 目录

- [2026-06-12-llm-pricing.md](2026-06-12-llm-pricing.md) — 主文档（人读）
- [2026-06-12-llm-pricing.csv](2026-06-12-llm-pricing.csv) — Go `POST /api/pricing/import` 输入
- [2026-06-12-pricing-plans.sql](2026-06-12-pricing-plans.sql) — `psql` 直连执行（pricing_plans 源真值）
- [2026-06-12-offers-baseline.csv](2026-06-12-offers-baseline.csv) — 184 DB 旧价快照（pre-import）
- [2026-06-12-all-paid-offers.csv](2026-06-12-all-paid-offers.csv) — 184 DB 全量付费 offer 列表
- [2026-06-12-credentials-with-plan-type.csv](2026-06-12-credentials-with-plan-type.csv) — 凭据分类结果（带 plan_type 标记）
- [2026-06-12-pricing-matrix.csv](2026-06-12-pricing-matrix.csv) — 完整价格矩阵（73 模型）
- [2026-06-12-diff.md](2026-06-12-diff.md) — 旧价→新价 diff
- [scripts/fetch-pricing.sh](scripts/fetch-pricing.sh) — agent-reach 一键抓取
- [scripts/apply-pricing.sh](scripts/apply-pricing.sh) — 双表写入编排
- [raw/](raw/) — 各厂商原始 markdown 抓取存档

## 数据库结构

- **目标 DB**: 184 k3s PostgreSQL, database=`llm_gateway`, user=`llm_gateway`
- **主表**: `model_offers` (legacy 快照, Go admin 读写), `pricing_plans` (Python 设计, 源真值)
- **凭据表**: `credentials` (`active_plan_id` 反向引用 `pricing_plans`)
- **Schema 迁移**: `services/llm-gateway/sql/2026_06_12_pricing_billing_mode_meta.sql`
  - `ALTER TABLE model_offers ADD COLUMN plan_meta JSONB`
  - 扩展 `billing_mode` CHECK 约束 (加 `token_plan`, `code_plan`)
  - Backfill `per_token` → `token`

## 9 个 Provider 当前状态

| Provider | Code | Credential | Offers | Plan Type |
|---|---|---|---|---|
| 智谱 AI | zhipu | roocode | 10 | token |
| MiniMax | minimax | minimax-prod-1 | 5 | token |
| 小米大模型 | xiaomi | xiaomi-token-plan | 9 | **token_plan** |
| 火山方舟 TokenPlan | volcano-tokenplan | demo-tokenplan | 5 | **token_plan** |
| 火山方舟 普通版 | volcano-normal | hzx-normal | 6 | token |
| EvolAI 聚合 | evol | evol-openclaw-proxy | 25 | token |
| NVIDIA NIM | nvidia | nvidia-build | 123 | token |
| 国家超算 | scnet | scnet-acrbo3aajx | 2 | token |
| Vapeur AI | vapeur | vapeur-main | 4 | token |

## 月度刷新流程

```bash
# 1. 抓取最新价目
bash scripts/fetch-pricing.sh

# 2. 生成新 CSV / SQL
python3 scripts/build-csv.py
python3 scripts/build-sql.py

# 3. Review diff
cat 2026-06-DD-llm-pricing-diff.md

# 4. 应用
bash scripts/apply-pricing.sh  # 登录 admin → POST /api/pricing/import + psql pricing_plans

# 5. 校验
curl -sk -H "Authorization: Bearer $API_KEY" \
  "https://llmgo.kxpms.cn/api/pricing/summary" | jq .
```

## 安全 & 凭据

- **admin password**: 从 k8s secret `llm-gateway-secret` (key `admin-password`) 读
- **SSH**: 火山 184 = `root@14.103.112.184`, 凭据 `K8S_SSH_PASSWORD` 环境变量
- **psql**: 直连 184 postgres 容器，DB=`llm_gateway` user=`llm_gateway`
- **DON'T**: 改 Casdoor admin 密码 / 改任何 secret / 直写 secret_ciphertext

## 历史快照

| Date | 模型数 | 覆盖率 | priced_in | 备注 |
|---|---|---|---|---|
| 2026-06-12 Phase E | 73 | 32% | 61 | Tier 1 首次批量入库 |
| 2026-06-12 Phase G | 149 | 96% | 182 | Tier 2 长尾补全 (NVIDIA NIM 119 + xiaomi 5) |
| 2026-06-12 Audit fix | 149 | 96% | 182 | 修复 billing_mode 不一致 + plan_meta 补全 |
| 2026-06-12 CNY fix | 272 | 96% | 182 | 60 个国内模型 USD→CNY（10 厂商官方价） |

## 月度刷新 (2026-06-12 增补)

**CronJob**: `deploy/k8s/cron/pricing-monthly-refresh.yaml` (1st @ 03:00 UTC)

**Pipeline**:
1. git clone 拉取源码 → `bash scripts/fetch-pricing.sh`
2. `python3 scripts/diff-pricing.py` → `diff.json` (人工审批用)
3. `POST /api/pricing/import` (multipart) + 直 psql 应用 token_plan
4. `INSERT pricing_refresh_log` 审计
5. diff_count > 5 → Feishu 通知

**手动触发**:
```bash
kubectl -n pms-test create job --from=cronjob/pricing-monthly-refresh manual-$(date +%s)
kubectl -n pms-test logs -f job/manual-$(date +%s)
```

**前置资源** (one-time setup):
- `Secret: llm-gateway-pg-pass` (key: `pg-password`)
- `Secret: pricing-refresh-secret` (key: `feishu-webhook`, optional)
- `PVC: pricing-refresh-work` (10Gi)
- SQL migration `2026_06_12_pricing_refresh_log.sql` ✅ 已应用
