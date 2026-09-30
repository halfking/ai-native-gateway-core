# bodies 列存化存储优化轮（R16，2026-09-30/10-01）

任务来源：用户指令"检查 request_logs_bodies_2026_09 是否使用列式存储并有压缩，检查其它大数据量表并优化"。承接 R15 审计（docs/audit/2026-09-30-252-sql-log-audit-round15.md §三 D19 容量赛跑）。

## 一、取证结论（问题一的直接回答）

**`request_logs_bodies_2026_09` 未使用列存、无任何压缩**：

| 事实 | 实测值 |
|---|---|
| 访问方法 | `heap`（pg_am 实查），全库 bodies 家族（分区+hot）均 heap |
| TOAST 压缩 | 列 `attcompression` 为空 = 默认 pglz（`default_toast_compression=pglz`） |
| 体量 | 53GB 总（主叉仅 629MB，~52GB 在 TOAST）；1.39M 行 / 均 370.9KB 行（500 行真样本 octet_length 实测） |
| 索引 | 2026_09 分区 **0 索引**（drawer 冷读本就是全扫） |
| 压缩率现状 | 500 行真样本：raw 79.8MB → heap+pglz 存储 38.1MB = **2.1×**（pglz 对该负载几乎无效） |

对照基准（252 真库 500 行真实 body 样本）：

| 存储 | 体量 | 压缩率 |
|---|---|---|
| raw octets | 79.8MB | 1.0× |
| heap + TOAST pglz（现行） | 38.1MB | 2.1× |
| **citus columnar（zstd-3，默认参数）** | **1.41MB** | **56.6×** |

外推：2026_09 同量数据列存 ≈ **1.9GB**（53GB → 3.6%）。本机 dev 库 bodies 月分区已先行是 columnar（先期存储轨道实践，同 citus 13.3-1）——生产列存化有同版本先例。

## 二、可行性边界（全部真库实证，非假设）

| 验证 | 结果 |
|---|---|
| `CREATE TABLE … PARTITION OF … USING columnar`（citus 13.3/PG17） | ✅ PASS |
| promote 路由兼容 | ✅ promote 函数为裸 `INSERT INTO request_logs_bodies SELECT`（无 ON CONFLICT），父表路由进 columnar 分区 INSERT 0 1 PASS（500 行 scratch + 回滚包裹的生产探针行） |
| 生产读形态 | ✅ 单列点读/父表 count/`request_logs_bodies_with_current_month WHERE request_id`（drawer 精确形态）三臂 PASS |
| TTL 退役路径 | ✅ `DROP TABLE`（columnar 分区）PASS；`drop_old_request_logs_bodies_partitions` 语义不受影响 |
| **session_bodies 不可列存** | ❌ promote（615/626）用 `ON CONFLICT (id, partition_date) DO NOTHING`——columnar 无唯一索引，硬边界；改判 lz4 TOAST |
| 分区继承列压缩 | ✅ 父表 `SET COMPRESSION` 被后续 `PARTITION OF` 继承（scratch 实证 child=[l]） |
| 本构建 TOAST zstd | ❌ `SET COMPRESSION zstd` → `invalid compression method`（构建有 `--with-lz4`；zstd 运行时不可用）→ heap 侧压缩牌=lz4；columnar 的 zstd 为 citus 自带链接不受影响 |
| Go 直写分区名 | 无（全仓 grep 零命中，全部走父表路由） |

lz4 基准（session_bodies 真库 200 行样本）：raw 78.7MB / pglz 35.95MB / **lz4 35.37MB**——尺寸中性，压缩 CPU 显著降低（body 写为会话热路径，pglz 大 payload 压缩是慢路径）。

## 三、其它大数据量表核查

| 表 | 体量 | 判定 |
|---|---|---|
| session_bodies_2026_09 | 20GB heap | lz4 TOAST（新行）；列存被 ON CONFLICT 阻断（§二）；存量分区无重写空间（盘 93%），按自身 TTL 退役 |
| ursm_node_snapshot_min | 20GB / 38.4M 行 / 单表 | **保留机制在位且正常**（SnapshotRetentionWorker 无条件装配，30 天窗 `URSM_SNAPSHOT_RETENTION_DAYS`；表龄 24 天未到首个清理点）。初判"无 TTL"经代码复查**证伪并撤回**。可选优化：cutover 后下调保留窗（env，ops 拍板），未动 |
| request_logs_bodies_hot | 4.3GB / 8h 保留 | lz4 TOAST 新行生效；随 promote 自清，无需处理 |
| request_stage_events | 1.27GB | **3 死索引清除**：`idx_stage_events_request_id`(556MB)/`tenant_ts`(352MB)/`stage_status`(30MB) pss 自 09-11 累计 idx_scan=0 + 非测试代码零读面（全仓 grep 仅 retention 走 pkey/created_at）→ 合计 **938MB** 回收（R1 注册的 F10 关闭；死重较 R1 的 443MB 又增长了 2×） |
| request_logs_2026_09 / session_turns_2026_09 / request_state_transitions 等 | 4.2/2.6/3.4GB | 有活跃读面/索引/保留机制，未动 |

## 四、改动（迁移 765 + 五点同步）

**`sql/migrations/startup/765_bodies_columnar_storage.sql`**（+ `.down.sql`）：

- **A** `ensure_request_logs_bodies_partition` 重定义：`citus_columnar` 在位 → `USING columnar` 建月分区；否则回退 heap（全新环境无扩展可安装）。函数为 DB 侧对象，Go 仅按名调用（partition_manager.go:1233）——**252 预应用后三实例立即生效，无需部署**。
- **B** 存量**空** heap 月分区一次性转 columnar：先空检查 → `LOCK TABLE … ACCESS EXCLUSIVE` → 持锁复核空 → DROP+CREATE USING columnar；非空分区（2026_09 的 53GB）不动，按 TTL 整分区 DROP 退役（10-08）。
- **C** bodies 两族 lz4 TOAST（父表×2 + hot×2 + 既有空 session_bodies_2026_10 子分区，共 5 目标 21 列）；零重写，只影响新 toast 行。
- **D** request_stage_events 三死索引 DROP（普通 DROP INDEX：索引 unlink 亚秒级、ACCESS EXCLUSIVE 窗口可控，764 先例随部署序列执行；无 CONCURRENTLY 即不触发 installer 单事务豁免清单）。
- 台账自登记（695-705 定式）。

**五点同步**：embeddata/startup 双拷（up+down）、`main.go` go:embed var + embeddedSQLFiles map、`dbinit/runner.go` StartupFiles、`stats_migrations_test.go` 注册 map、`apply-db-revision-sequence.sh` 通道清单、`docs/db-changelog.md` 条目。

## 五、测试与真库验证

- `go build ./...` PASS；`go vet ./installer/...` PASS；`go test ./installer/...` 全绿（含 704+ 注册守卫）；`go test ./sql/migrations/startup/` PASS。
- **252 生产真库预应用**（psql 文件通道，`ON_ERROR_STOP=1` 零错误）：后验证 V1 2026_10=columnar（options: compression=zstd, level=3, stripe 150000/chunk 10000）✓；V2 ensure 函数 2026_11 演练建 columnar+回滚零残留 ✓；V3 五目标 lz4 全落位 ✓；V4 三死索引消失、pkey(160MB)/created_at(7.3MB) 守门索引在位 ✓；V5 台账 765 落账 ✓。
- **本机 dev 库第二真库实跑**：2026_11 heap→columnar NOTICE 确认、三索引清除、台账落账 ✓（本地 2026_09/10 先期已列存=同版本先例）。
- **生产回滚包裹探针**：INSERT 经父表路由进 columnar 2026_10 → ROLLBACK 后零残留 ✓。
- **CTID 误报排除**：诊断用 `JOIN pg_class ON c.oid=r.tableoid` 构造触发 columnar 的 CTID-scan 拒绝——生产无此形态（drawer/视图/promote/TTL 四臂全 PASS），属自取证构造（纪律㉕ 剔除）。

## 六、遗留风险与观察项

1. **10-01 起首个生产写入验证**：columnar 2026_10 在 10-01 00:00 后迎来首批 promote 行；下轮对账 `columnar.stripe` 条带增长 + 大小（预期月内 ~2GB 量级 vs 原 ~50GB 轨迹）+ drawer 冷读时延。
2. **非空分区不转换**是数据安全阀的代价：2026_09 的 53GB 占用持续到 10-08 TTL DROP；期间盘 93% 的 D19 赛跑由"10 月增量列存化"缓解（10 月轨迹 50GB→~2GB，预计 10-06/08 满盘窗口解除），但仍建议盘量复核。
3. **columnar 无 UPDATE/DELETE**：bodies 分区生命周期（INSERT+DROP TABLE）与之完全兼容，但若未来有人对分区引入行级 UPDATE/DELETE（如行级 redaction）会在运行时炸（错误信息明确）。已在迁移头注释与本文件登记。
4. **运维磁盘不释放**：DROP 分区即还空间（整分区语义），无需 VACUUM；lz4/hot 不改变该性质。
5. **运维选项**：`URSM_SNAPSHOT_RETENTION_DAYS`（现默认 30 天）在 URSM v2 cutover 完成后可下调；D19 的 ttl 7→3 建议因列存化**解除急迫性**（10 月不再有 ~50GB 增量），是否仍调整归用户。
6. 764 备注"在制 759_session_turn_details 须用 765+"所指文件从未落地（origin 已核实无 765 撞号）；本轮占用 765，push 前再 fetch 复核（纪律㉒）。

## 七、自审计（批判式，A1-A5）

- **A1 初判"ursm 无 TTL"证伪**：先按"表大=可能无清理"假设登记，复查 `domains/ursm/v2/persist/retention.go` 后撤回——**大表≠无保留，先找 worker 再定性**。
- **A2 "R1 登记的 F10=443MB"过期**：pss 实查死重已涨至 938MB；登记值不等于现值，执行前必须重测。
- **A3 zstd TOAST 假设被真库打脸**：`--with-zstd` 出现在 pg_config 不代表运行时可用（SET COMPRESSION zstd 直接报错）——**每个压缩选项都先 SET 验证再写进方案**。
- **A4 诊断构造 CTID 报错差点误判"columnar 生产不可用"**：INSERT 已成功、报错在诊断 JOIN；分离"生产形态"与"诊断形态"再下结论。
- **A5 embeddata 副本漂移防线**：763 轮的教训（embed 缺登记守卫持续红）已由 `stats_migrations_test.go` map 强制——本轮五点一次到位，测试 map 漏条目会直接红。
