# 安装器/迁移链/测试设施组子代理报告（窗口 e3406f9e2..77837b013）

基线 HEAD=77837b013，全部结论基于只读命令（cmp/shasum/grep/sed）。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 | 处置 |
|---|---|---|---|---|
| 1 | P2 | listener 残留原样存在：4 测试走 testschema 私有 schema 夹具但 9 处 DML public. 限定打门禁库生产表且不清理；触发的是门禁库 baseline 生产触发器，私有夹具触发器从未参与测量 | dispatch_postgres_helper.go:109 / listener test 9 处 | 本轮已收口（DispatchPostgresDatabase 双模式+4 调用点切换） |
| 2 | P2 | 25 条注册迁移无 parity 守卫（478,534,541,572,606,612,637-639,644-646,651-654,742,743,747,750-752,760-762），含生产阻断修复 534/612 | stats_migrations_test.go:14 map | 本轮已收口（反转为 embeddedSQLFiles 全清单驱动） |
| 3 | P3 | 612 down 三重不一致：embeddata 缺副本/头错号 611/534 双侧无 down | 612 down:1 | 612 本轮已修；534 down 留 Owner |
| 4 | P3 | workflow 触发 paths 漏 installer//sql/schema//scripts/** | workflow:5-39 | 本轮已补 |
| 5 | P3 | fork-PR secrets 恒红（fail-loud 设计明示但外部贡献者信号失真） | workflow:172-184 | 登记（内部仓影响小） |
| 6 | P3 | 计数注释三处失真（198/173，actual=200） | TSV:8 / manifest_test:33,63 / gate.sh:333,342 / gate_test:657 | 本轮已修 |
| 7 | P3(信息) | 534 显式 BEGIN/COMMIT 与 --single-transaction 并存（实测无害，deploy 线收编文件） | 534:13,325 | 记录豁免理由 |
| 8 | 信息 | TSV 无 SHA 列（序号+文件名两列，manifest 测试派生守序守量）；verify-migration-checksums 未接 CI | TSV:72 | 归入遗留#2 后续 |
| 9 | 信息 | db-changelog SHA 实算核对通过（612/730 双登记）；534/808/809 按 R26 豁免无登记行 | db-changelog:103,615,802 | 维持 |

## 二、canonical↔embeddata 全量盘点结果
200 条注册迁移：字节漂移 0 处；600 在 up/ 子目录（IDENTICAL，有特判）；session_turns_hot_bootstrap 为 installer-only（有 marker 守卫）；embeddata *.sql 集合与 TSV 完全相等；down 副本 808/809/730 IDENTICAL、612 仅 canonical（已修）、534 双侧无。

## 三、核实为健康的面
- 808 分区迁移符合 hot+columnar 范式（DEFAULT 兜底/职责边界注释/幂等/down 文档化破坏性回退）
- 809 最小 ALTER+外键保留论证完整
- internal/testdb：CREATEDB 缺失 fatal 指路/并发安全命名/WITH (FORCE)/mustLandInScratch 反证
- internal/testschema：RuntimeParams 注入池内每连接/Cleanup LIFO/public. 边界头注明示
- startup_manifest_test 双向守卫真实有效（加文件不登记/登记不重生成/缺 go:embed 均红）
- run-integration-gate.sh：零解析双地板/形态分档/已知缺口棘轮/租户角色实测/14 env 反向对账
- derive-gate-packages.sh：tag 差集定义正确/XTest 覆盖/空差集大声死
- integration-gate job 接回：三雷已排+健康等待+per-package 一次性库+失败聚合+状态行互锁

## 四、未覆盖项与原因
未跑任何集成测试（纪律）；CI 首跑全绿不可本地判定（registry 凭据）；534 仅结构级审查；gomapper/injection-test 两个独立 module 未评估；db-changelog 全量 SHA 对账属 verify 脚本职责。
