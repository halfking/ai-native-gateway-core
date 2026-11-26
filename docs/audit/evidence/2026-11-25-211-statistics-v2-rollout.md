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

## Maestro 线上复验

Maestro 2.11.0、Chrome 158.0.8127.88、Node 22.23.3，测试目标均为槽 a 的构建 3274。现役产品提交保持 `6f099525`，以下修正只更新本地测试脚本。

- 运营统计 flow 18 通过。看板探针匹配实际 V2 路径并对新请求核对范围，修正后主探针 26 条、自定义及七个预设 5 条、列表深链 5 条均通过。
- 会话分析 flow 16 通过。原测试截图有效高度 3457，Top5 区块底部 3701。采用 1440×4800 测试视口后，原可见性断言通过。探针复用浏览器页面，按实际总览重定向路径断言，最终 65 条通过。排序筛选探针 20 条通过。
- 两套 flow 和各层探针的通过读数来自逐层重跑。首次带旧 API 路径和旧总览路径的失败结果不作为产品异常。

```text
flow 18  flow rc=0 FAILED=0
stats range  RESULT: PASS（26 条）
custom range and presets  RESULT: PASS（5 条）
deep links  RESULT: PASS（5 条）
flow 16  flow rc=0 FAILED=0
session analytics  RESULT: PASS（65 条）
ranking filter  RESULT: PASS（20 条）
SELFTEST: PASS
```

## 30 分钟稳定窗口

观察从 22:46:07 到 23:16:07（Asia/Shanghai）。31 次采样中，公网和槽 a 的版本均为 `6f099525`、构建号 3274。数据库和 Redis 就绪检查、投影检查均为 HTTP 200，期间槽 a 无重启，投影待处理最早记录的最大年龄为 13.927 秒。nginx 日志中，本域名的网关 upstream 没有连接拒绝、超时、重置、提前断开或无可用槽位。

正式日对账、统计导出及就绪探针没有新增 5xx。访问日志里的另两笔会话总览 500 和一笔热门模型 500 均对应查询上下文取消的服务端记录。对同一区间的 V2 会话总览单独复测三次，均返回 200、来源为 `request_metrics`。模型 API 另有 297 / 930 笔 5xx，发布前的同长对照窗为 485 / 1515。

初次监测脚本的本机 Python 3.9 解析不了部分可变精度时间戳。下列数据来自改用可变精度解析后完整重跑的 30 分钟窗口。

```text
{"at": "2026-11-25T14:46:07.565563+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 7.705333, "projection_updated_at": "2026-11-25T22:46:00.606392+08:00", "counters": {"requests": 1739, "http_5xx": 389, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T14:47:07.567116+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 4.698778, "projection_updated_at": "2026-11-25T22:47:01.011061+08:00", "counters": {"requests": 2012, "http_5xx": 393, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T14:48:07.574308+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 5.916045, "projection_updated_at": "2026-11-25T22:48:01.60709+08:00", "counters": {"requests": 2227, "http_5xx": 394, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T14:49:07.568664+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 6.236033, "projection_updated_at": "2026-11-25T22:49:01.870909+08:00", "counters": {"requests": 2576, "http_5xx": 406, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T14:50:07.567660+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 6.051964, "projection_updated_at": "2026-11-25T22:50:02.2303+08:00", "counters": {"requests": 2904, "http_5xx": 441, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T14:51:07.582446+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 2.865205, "projection_updated_at": "2026-11-25T22:51:02.454521+08:00", "counters": {"requests": 3397, "http_5xx": 443, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T14:52:07.569267+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 1.045166, "projection_updated_at": "2026-11-25T22:52:02.785545+08:00", "counters": {"requests": 3611, "http_5xx": 451, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T14:53:07.571467+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 2.01073, "projection_updated_at": "2026-11-25T22:53:03.061022+08:00", "counters": {"requests": 3781, "http_5xx": 462, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T14:54:07.572543+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 5.102861, "projection_updated_at": "2026-11-25T22:54:03.303656+08:00", "counters": {"requests": 3927, "http_5xx": 474, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T14:55:07.578331+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 3.792443, "projection_updated_at": "2026-11-25T22:55:03.564557+08:00", "counters": {"requests": 4118, "http_5xx": 481, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T14:56:07.573899+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 3.445058, "projection_updated_at": "2026-11-25T22:56:03.830079+08:00", "counters": {"requests": 4291, "http_5xx": 491, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T14:57:07.578955+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 2.931901, "projection_updated_at": "2026-11-25T22:57:04.013699+08:00", "counters": {"requests": 4584, "http_5xx": 492, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T14:58:07.576202+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 0.132305, "projection_updated_at": "2026-11-25T22:58:04.219522+08:00", "counters": {"requests": 4844, "http_5xx": 496, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T14:59:07.582744+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 1.750882, "projection_updated_at": "2026-11-25T22:59:04.438369+08:00", "counters": {"requests": 5145, "http_5xx": 497, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T15:00:07.582779+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 1.930915, "projection_updated_at": "2026-11-25T23:00:04.66336+08:00", "counters": {"requests": 5456, "http_5xx": 501, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T15:01:07.582868+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": null, "projection_updated_at": "2026-11-25T23:01:04.940562+08:00", "counters": {"requests": 5830, "http_5xx": 517, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T15:02:07.576325+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 1.230509, "projection_updated_at": "2026-11-25T23:02:05.473782+08:00", "counters": {"requests": 6197, "http_5xx": 537, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T15:03:07.578697+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 1.770732, "projection_updated_at": "2026-11-25T23:03:05.946103+08:00", "counters": {"requests": 6497, "http_5xx": 540, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T15:04:07.577412+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 0.685285, "projection_updated_at": "2026-11-25T23:04:06.335896+08:00", "counters": {"requests": 6597, "http_5xx": 551, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T15:05:07.583680+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": null, "projection_updated_at": "2026-11-25T23:05:06.659997+08:00", "counters": {"requests": 6694, "http_5xx": 558, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T15:06:07.582720+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": null, "projection_updated_at": "2026-11-25T23:06:07.181622+08:00", "counters": {"requests": 6788, "http_5xx": 564, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T15:07:07.584455+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": null, "projection_updated_at": "2026-11-25T23:07:07.488693+08:00", "counters": {"requests": 6895, "http_5xx": 588, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T15:08:07.585386+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": null, "projection_updated_at": "2026-11-25T23:08:07.833244+08:00", "counters": {"requests": 6995, "http_5xx": 596, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T15:09:07.579062+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 10.3192, "projection_updated_at": "2026-11-25T23:08:52.995926+08:00", "counters": {"requests": 7096, "http_5xx": 603, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T15:10:07.586427+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": null, "projection_updated_at": "2026-11-25T23:10:08.239833+08:00", "counters": {"requests": 7182, "http_5xx": 611, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T15:11:07.583161+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 8.158891, "projection_updated_at": "2026-11-25T23:10:53.497418+08:00", "counters": {"requests": 7271, "http_5xx": 614, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T15:12:07.584904+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 12.137073, "projection_updated_at": "2026-11-25T23:11:53.72091+08:00", "counters": {"requests": 7365, "http_5xx": 621, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T15:13:07.583314+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 13.927318, "projection_updated_at": "2026-11-25T23:12:53.922145+08:00", "counters": {"requests": 7462, "http_5xx": 633, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T15:14:07.587919+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 7.149699, "projection_updated_at": "2026-11-25T23:13:54.157733+08:00", "counters": {"requests": 7555, "http_5xx": 645, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T15:15:07.584962+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 13.108632, "projection_updated_at": "2026-11-25T23:14:54.405641+08:00", "counters": {"requests": 7653, "http_5xx": 662, "report_5xx": 295}, "restart_count": 0}
{"at": "2026-11-25T15:16:07.582838+00:00", "sha": "6f099525", "build_seq": 3274, "slot": "a", "ok": true, "readyz": 200, "projection_http": 200, "pending_age_secs": 12.924945, "projection_updated_at": "2026-11-25T23:15:54.760447+08:00", "counters": {"requests": 7760, "http_5xx": 689, "report_5xx": 295}, "restart_count": 0}
{"samples": 31, "window_s": 1800, "counter_delta": {"requests": 6021, "http_5xx": 300, "report_5xx": 0}, "stable_restart_count": true}
refused 0
timeout 0
reset 0
early_close 0
no_backend 0
{"5xx": {"model_api:502": 5, "model_api:503": 480}, "end": "2026-11-25T14:40:43Z", "other_admin_routes": {}, "slot": "b", "start": "2026-11-25T14:10:43Z", "totals": {"model_api": 1515, "other": 956, "readiness": 272}}
{"5xx": {"model_api:502": 13, "model_api:503": 284, "other:500": 1, "reports_and_analytics:500": 2}, "end": "2026-11-25T15:16:07.582838+00:00", "other_admin_routes": {}, "slot": "a", "start": "2026-11-25T14:46:07.565563+00:00", "totals": {"model_api": 930, "other": 3771, "readiness": 482, "reports_and_analytics": 209}}
{"timestamp": "2026-11-25T14:52:50.37405658Z", "event": "session statistics unavailable", "causes": ["context canceled"]}
{"timestamp": "2026-11-25T15:01:25.25309193Z", "event": "session statistics unavailable", "causes": ["context canceled"]}
{"timestamp": "2026-11-25T14:52:50.374234473Z", "route": "/api/admin/dashboard/v2/session-overview", "status": 500, "duration_ms": 4265}
{"timestamp": "2026-11-25T15:01:15.394016898Z", "route": "/api/logs/top-models", "status": 500, "duration_ms": 1587}
{"timestamp": "2026-11-25T15:01:25.253174945Z", "route": "/api/admin/dashboard/v2/session-overview", "status": 500, "duration_ms": 4313}
200 request_metrics 5.518
200 request_metrics 5.505
200 request_metrics 5.428
```
