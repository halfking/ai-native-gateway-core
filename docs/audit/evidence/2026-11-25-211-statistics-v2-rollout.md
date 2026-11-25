# 211 请求与会话统计 V2 发布验收

## 候选与迁移

- 候选提交 `6f0995251933e72fdbae55fb11f9b9ff8523906a`。发布前 211 为 `c5120d3b`，构建号 `3273`，nginx upstream 指向槽 b 的 `8783`。
- 只读获取的生产原始台账共 544 条，其中 `gateway_db_revision_sequences` 375 条、`schema_migrations` 169 条。冻结候选与 211 现役提交的迁移 SQL 差异只含 973 的新资产。375 条部署指纹中，372 条在当前源码路径找到相同字节，另有 1 条匹配历史 Git 资产，余下 2 个历史文件名在现役提交与候选中均无交付路径。
- 973 三份上线资产逐字一致，SHA-256 为 `287647400087da6ec2512da3acc0c51d2f1e7f4d8b43a3c7d51ceb4f76e92adf`。最终 SQL 在 211 同源 PostgreSQL 17.10 库耗时 0.688 秒，源 `request_metrics` 68,140 行。三项新索引已建立，544 条原台账记录逐条不变，直执行只追加 `schema_migrations.973`。
- 同源库安装器直升解决 520 策略冲突、562 退役表重放和 565 回填前置条件。部署链交接与同通道重跑核对全部原记录，重跑前后 376 条部署台账与 205 条逻辑台账完全一致。冻结版部署通道独立首跑在原库上只追加 973 逻辑登记及其文件指纹。最后两次调整未上线的 973 预算与实测注释，执行语义沿用已验证的投影和迁移。
- 隔离空库按安装器清单走完 408 件启动迁移，全部成功。新鲜对象底账 `fresh=2876` 包含 973 三项新索引。投影真库集成测试通过。
- 隔离库投影处理 68,469 条事实，就绪由 HTTP 503 转为 200，`pending_since=null`。同一只读、可重复读快照 `8655:8655:` 的两个窗口得到下表。缺少真实账务样本的指标状态为 `no_observations`。

| Asia/Shanghai 日期 | 财务状态 | 客户端收件报表 / WAL | 确认发送观测 | 请求过账观测 | 精确日账单 |
| --- | --- | ---: | ---: | ---: | ---: |
| 2026-11-23 | closed | 38,967 / 38,967 | 0 | 0 | 0 |
| 2026-11-24 | open | 29,580 / 29,580 | 0 | 0 | 0 |

## 发布门禁原始读数

```text
ok  	github.com/kaixuan/llm-gateway-go/admin	209.155s
ok  	github.com/kaixuan/llm-gateway-go/admin/dashboardapi	2.540s
ok  	github.com/kaixuan/llm-gateway-go/bg	38.236s
ok  	github.com/kaixuan/llm-gateway-go/cmd/gateway	30.531s
ok  	github.com/kaixuan/llm-gateway-go/domains/metrics	1.968s
ok  	github.com/kaixuan/llm-gateway-go/domains/reconciliation	3.771s
ok  	github.com/kaixuan/llm-gateway-go/domains/reportrollup	4.387s
ok  	github.com/kaixuan/llm-gateway-go/internal/metricsstore	5.209s
ok  	github.com/kaixuan/llm-gateway-go/internal/sqlguard	29.747s
ok  	github.com/kaixuan/llm-gateway-go/middleware	6.114s
ok  	github.com/kaixuan/llm-gateway-go/scripts/deploygate	8.485s
ok  	github.com/kaixuan/llm-gateway-go/scripts/ursmcheck	539.511s
ok  	github.com/kaixuan/llm-gateway-go/settings	4.527s
ok  	github.com/kaixuan/llm-gateway-go/sql/migrations/startup	21.877s
ok  	github.com/kaixuan/llm-gateway-go/sql/schema	143.873s
ok  	github.com/kaixuan/llm-gateway-go/installer/cmd/llm-gw-installer	6.414s
ok  	github.com/kaixuan/llm-gateway-go/installer/internal/dbinit	7.490s
[test-gate] ★ 闸门通过：./admin ./admin/dashboardapi ./bg ./cmd/gateway ./domains/metrics ./domains/reconciliation ./domains/reportrollup ./internal/metricsstore ./internal/sqlguard ./middleware ./scripts/deploygate ./scripts/ursmcheck ./settings ./sql/migrations/startup ./sql/schema 全绿 ★
```

## 211 蓝绿结果

标准入口 `scripts/deploy-211.sh` 的测试门通过。桌面和移动端构建完成。部署序列应用 973，并通过产物探针。候选槽在旧槽继续服务时完成历史回填，初次探针要求追到 22:30:54.405711 水位，22:40:43.110414 达标。nginx 从槽 b 的 8783 切到槽 a 的 8781。公网版本和入口 200 验证通过，标准登录冒烟通过。线上构建号从 3273 增至 3274。

用同一查询在切流前后读回两本台账。原 375 条部署台账与 169 条逻辑台账逐行相等，仅有 973 的逻辑版本及部署指纹新增。旧槽 b 已停止，容器 ID 与镜像 ID 和发布前相同。旧槽的兼容写入及失败退回在隔离验收完成。

## 正式日报和只读影子对拍

线上通过设置临时用上海财务时区关账至 2026-11-23。两个日期从同一只读可重复读快照 `118969741:118969741:` 采集，读点 `2026-11-25T14:42:45.177814Z`。

| Asia/Shanghai 日期 | 财务状态 | 客户端收件报表 / WAL | 终态投影 | 供应商确认发送 / 请求过账 / 日账单 |
| --- | --- | ---: | ---: | ---: |
| 2026-11-23 | closed | 38,967 / 38,967 | 38,901 | 0 / 0 / 0 |
| 2026-11-24 | open | 29,580 / 29,580 | 29,568 | 0 / 0 / 0 |

- 关账日的覆盖、请求、尝试、过账、会话五节合计 96 个 JSON 与 CSV 汇总字段均相符，与快照数值相符。开放日的五节 JSON 和 CSV 均返回 HTTP 400。
- 桌面和移动浏览器默认打开 11 月 23 日，实际请求 V2 覆盖接口，显示相同的 38,967 笔收件。移动端改查 11 月 24 日时显示错误并清除上一次覆盖数据。
- 两天各导出一份四页 Excel，与 V2 统计汇总 API 对上 38,901 和 29,568 笔终态请求。工作簿记录 `request_metrics` 来源。
- 已通过设置恢复 `automatic`，随后按该次写入时间和值清除测试创建的覆盖项。读回设置库覆盖项 0 条，API 返回 `automatic`、`source=default`，自动入口恢复到 11 月 24 日。平台设置写入留痕保留。
- 线上抽检没有覆盖两个日窗的供应商账单和合同费率，确认发送和请求级过账均标记 `no_observations`。内部实扣及供应商应付金额等待对应的真实凭据。

## 离线投递资产

标准升级包的 SQL 收集阶段复制了 497 件。下列六件与在役候选源码的字节一致。旧离线执行器仍按数字编号将 973 排在创建 `request_metrics` 的 983 前。未具备投影表的旧库需要另行验证离线执行。

```text
925_supplier_contract_rates.sql 4efa2552c4de4635efc98a9ef1c4e0648acfc7028384e6f06259ca45a059e34e
973_request_metrics_projection.sql 287647400087da6ec2512da3acc0c51d2f1e7f4d8b43a3c7d51ceb4f76e92adf
985_request_reconciliation_links.sql fc4430f8aba297fa449cf4f321fb5b4ee9753a766b656d413e42fb7a810c28a9
986_provider_attempt_media_usage.sql 2e3be29b1a5102def9d6b459664897fa1bcc43e90465bd55f5a2e8c1a5dfecc9
987_provider_invoice_evidence.sql 6bb0dc4760c805fedf3774d797203f41a448db4b45c69d74c629b0f302ffa5c8
988_provider_invoice_usage_buckets.sql 3a46a04e819cc540c8590d6a02ab6c7bf33c6888860421948341959877186086
```
