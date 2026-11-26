# 日对账财务日历与凭据规范

## 设置关账日和开放日

平台管理员在「设置 → 通用」搜索 `reports.reconciliation.financial_calendar`，由有写权限的超级管理员修改。设置使用字符串，立即生效并进入设置历史。

| 设置值 | 效果 |
| --- | --- |
| `automatic` | 默认兼容模式。自然日结束后可查看 V2 日对账，适合未启用财务日历的实例。 |
| `open@Asia/Shanghai` | 启用上海财务日历，所有日期保留为开放。 |
| `2026-11-23@Asia/Shanghai` | 从最早保留日关账至 11 月 23 日，包含当天。11 月 24 日及以后为开放。仅已结束的日期可设为截止日。 |

同一实例用一套平台财务日历。V2 日对账须使用设置中的时区，报告和 CSV 返回采集到的财务状态、时区与关账截止日。开放日可用只读影子命令核对，输出同时保留自然日 `window_state` 和财务日历 `financial_period.status`。把截止日往回调会重新开放边界之后的日期。在对账记录中登记操作者、原因、影响窗口和前一批次的凭据。设置历史按现行清理周期保留，结账记录需归入对应批次。设置控制 V2 日对账的正式读取。金额、扣费流水和已出具的供应商账单各自以原始凭据为准。已出账金额的更正使用冲正或调整，并保留对原账的引用。

启用 `LLM_GATEWAY_RECONCILIATION_DAILY_DEFAULT_ENABLED=true` 时，桌面和移动端进入报表会打开最后一个已关账日。财务日历设为全部开放时，报表保留历史快照入口。兼容模式继续默认打开上一个有效自然日。

在授权的环境可通过同一设置入口写入。

```http
PUT /api/admin/settings/reports.reconciliation.financial_calendar
Content-Type: application/json

{"value":"2026-11-23@Asia/Shanghai"}
```

## 供应商合同费率凭据

每个生效版本由供应商、计费账号、绑定凭据、实际发出的模型名和币种确定。对账保留脱敏账号别名，导入时使用网关内部的 `provider_id` 和 `credential_id` 数字映射。浓缩后的费率文件只需保留计价条款，合同原件由经授权的人员保管。

| 字段 | 采集和核对方式 |
| --- | --- |
| 合同版本和来源 | `reference` 用合同号、报价单版本或补充协议编号。登记条款页、校验和、核对人及确认时间。本地样本使用脱敏编号。 |
| 有效期 | `effective_from` 包含，`effective_to` 排除。保留原文时区并转换为 UTC 时刻。以逐次发送时刻选用并冻结当时的费率版本。 |
| 币种和舍入 | 正式结算币种使用 ISO 大写代码。价格输入十进制字符串，记录合同要求的小数位、最小计费单位及舍入发生层级。不同币种各自核对。 |
| 计费项目 | 每项填写计量名、每份计价数量和单价。输入、输出、缓存读写、确认发送、音视频时长及图片张数按合同区分。单次用量阶梯写累积上界，末档无上界。 |
| 金额依据 | 合同测算、供应商返回金额、账单请求项、折扣、抵扣、税与固定费分列。最终应付来自原账单。无法确认量、有效期或币种的行记为待核对。 |

使用现有 `POST /api/admin/provider-cost-reconciliation/rates` 导入已确认版本。下列为结构示例，金额均为合成值。

```json
{
  "provider_id": 101,
  "credential_id": 201,
  "model": "fixture-model",
  "reference": "fixture-rate-v1",
  "currency": "USD",
  "effective_from": "2026-11-01T00:00:00Z",
  "terms": {
    "components": [
      {"meter": "input_tokens", "unit": 1000000, "tiers": [{"price": "0.25"}]},
      {"meter": "output_tokens", "unit": 1000000, "tiers": [{"price": "1.00"}]}
    ]
  }
}
```

逐次调用的 `rate_snapshot.supplier_contract` 保留计价时选用的 `rate_id`、合同引用、条款、有效期和规范化内容的 SHA-256。对拍时核对发送时刻、实发模型、供应商凭据及币种。后补合同按实际导入时间登记，历史调用缺价时仍留待核对。

## 供应商账单凭据

每个交付批次保留供应商原账单、脱敏账单号、计费账号、账期及原时区、币种、凭据 ID 映射、原文件 SHA-256。原文件的摘要与导入后按 ID 读取的规范化内容摘要各自留档。请求收费使用供应商请求 ID，`request_total` 明细和 `usage_bucket` 使用供应商已明确的不重复口径。遇到无法可靠拆分的重叠项，保留原账单并登记待核对。固定费、税、折扣、抵扣和退款分别保留行号及符号。

使用 `POST /api/admin/provider-cost-reconciliation/invoices` 导入已确认的账单。下例使用月账期，金额为合成值。供应商只出日报时，使用原账单的 `day` 字段。每份账单使用一种币种，逐行带符号求和须等于 `stated_total`。

```json
{
  "provider_id": 101,
  "account_ref": "fixture-account",
  "invoice_ref": "fixture-2026-11-usd",
  "currency": "USD",
  "month": "2026-11",
  "timezone": "Asia/Shanghai",
  "credential_ids": [201],
  "stated_total": "0.014",
  "lines": [
    {"line_ref": "1", "kind": "request_total", "provider_request_id": "fixture-upstream-request", "amount": "0.017"},
    {"line_ref": "2", "kind": "credit", "amount": "-0.003"}
  ]
}
```

首次导入返回 `invoice_id`。按账单 ID 读取对账和导出 `csv` 或 `csv-summary`。相同内容可重复导入。供应商更正以新凭据入账，在验收批次保留原账单引用及调整原因。

## 内部过账与冲正凭据

内部积分使用 `credit_ledger_with_current_month` 的实际流水，冷热重复按租户和流水 ID 去重。按实际入账时刻归窗，用租户和服务器请求 ID 关联，对内部拆分请求保留父请求 ID。小时使用桶用于核对毛扣费。扣 12 积分、退 2 积分的批次应分别记为毛扣 12、冲正 2、净扣 10。

| 字段 | 采集和核对方式 |
| --- | --- |
| 身份 | 租户、流水 ID、服务器请求 ID、内部父请求 ID、脱敏用户或费用归属。应用或客户端可从同一请求的留存维度补齐。 |
| 流水 | 过账时刻、类型、积分池和原始有符号积分。消费、退款、冲正及调整逐笔列出。充值、赠送、预扣、未入账任务单列。 |
| 冲正关联 | 保留原流水 ID、冲正或调整的依据编号、入账时间和操作者。网关数据缺原流水引用时，从核准的内部凭证补齐。缺少原凭证时标明待核对。 |
| 货币与换算 | 只有实际记账货币和金额凭据时才填正式内部金额。若使用冻结倍率给出参考换算，单列规则版本、有效期、币种及参考金额。只留存积分的行保留货币金额空值。 |

内部凭据可按下面的 CSV 列交付。示例保留原始流水符号，日对账的 `net_credits` 将扣费表现为正、退回为负。这样原流水和日报均可逐笔对拍。

```csv
tenant_ref,posting_id,request_id,parent_request_id,posted_at,entry_type,pool,owner_ref,ledger_credits,reverses_posting_id,evidence_ref,internal_currency,internal_amount
tenant-fixture,1001,req-fixture,,2026-11-23T01:05:00Z,consume,paid,user-fixture,-12,,fixture-debit,,
tenant-fixture,1002,req-fixture,,2026-11-23T01:12:00Z,reversal,paid,user-fixture,2,1001,fixture-reversal,,
```

## 首次验收材料

1. 用同一平台时区选两个相邻的已结束自然日。将较早日设为关账截止日，后一日保留开放。这样可以检验管理设置的实际控制，即使两个日窗都已过午夜。
2. 对两个日期采集同一只读快照。每个批次保留截止设置、快照读点、供应商账号和币种、原账单与费率的脱敏引用、内部流水及冲正凭证。开放日报告保留暂计、缺数和迟到项。
3. 按请求收件、逐次发送、终态及实际过账各自的时间字段对拍，逐项核对平台流量和无会话归属。账单汇总按账期和币种核对，同一请求的多次发送逐条匹配。用同批次数据核对桌面、移动、API、CSV 和 Excel。

在已授权的证据数据库上，将导入返回的供应商账单 ID 传给只读影子采集。同一个批次的 `postgres_snapshot` 和 `read_at` 覆盖两个日窗与选中账单。可以重复指定不同账单 ID。同一 ID 只输出一次，账期至少与一个所选日窗重叠。

```sh
go run ./cmd/request-reconciliation-shadow -projected \
  -timezone Asia/Shanghai -day 2026-11-23 -day 2026-11-24 \
  -evidence-rows 50 -invoice-id "$SUPPLIER_INVOICE_ID"
```

`selected_supplier_invoices` 保留每份账单的原始账期与全量金额汇总。`-evidence-rows` 只限制各明细样本的行数。每日 `exact_day_supplier_bills` 继续只包含同日窗账单，月账单总额按原账期核对。单日查询省略 `-invoice-id` 时保持原输出格式，指定账单时返回带 `days` 的批次。

核对单日 Excel 时，报表请求的 `start` 和 `end` 用同一个本地日期，`end` 包含当日。WAL、逐次调用和过账的 SQL 时间窗口使用当日零点（含）到下一日零点（不含）。隔离复验曾因把次日传给 Excel 的 `end` 多算一个已结束日，按真实请求口径改正后逐项闭合。 [隔离复验记录](../audit/2026-11-24-请求会话统计与内外对账复验总结.md)
