# 2026-10-14 252 pg-252-pg17 SQL 日志审计轮（三分片缺索引根修轮）

> 窗口：2026-10-12 23:47 → 10-14 06:07（快照归档两份 + 活尾巴，30.3h）。
> 取证：`/tmp/pg252-r1014-audit/`（252 侧，含 extract.py 锚定提取器与原始切片；
> 本地镜像 `/tmp/pg252-r1014/`）。快照层 10-13/10-14 归档在位、逐小时推进。
> 主修复：**迁移 862**（session_turns / _request_logs 两分区父表无谓词 `(ts)` 前导索引），
> 252 已按 736 先例预应用同名索引（等价语句、现名），EXPLAIN/稳态双重验证。

## 一、总量与三轴对账

| 轴 | 值 |
|---|---|
| 原始行 / 容器前缀帧 / P 帧片段 | 2,043,454 / 2,043,452 / 35,144 |
| 锚定 ERROR+FATAL 记录 | 9,335（ERROR 6,229 + FATAL 3,106） |
| duration ≥1s 完成条目 | 16,695（总墙钟 ~31.5h，其中 request_logs 视图族 ~8.4h） |
| 锁等待行（still waiting/lock timeout/deadlock） | 1,260 |

开轮 load=45：两个孤儿 `grep -rl` 全盘扫描（父进程 PPID=1 死 SSH 会话遗留，
一个跑了 7h 烧 4.9h CPU）+ 一个挂了 2 天的 `redis-cli MONITOR` 调试孤儿，
全部清除（R-10-13 轮同族先例），load 45→17.6。宿主另有常驻外部负载：
**pms-redis 50% CPU × 3 天**（23.5M commands）、**smm fundamentals 回填
62% CPU × 8h**——外部租户，登记升级不越界。

## 二、根修：两族分区缺「无谓词 (ts) 前导索引」（迁移 862）

### 症状（指纹普查，纪律㉘ 精确形状计数）

request_logs/canonical 视图族慢查 7,480 条/30.5h（总墙钟 8.4h），
10-14 00:00-06:00 每小时 121→190 条持续爬坡；错误侧 user-cancel ×4,104 +
statement-timeout ×1,552 与之同源同相（30s rolconfig 击杀 = 同一查询的
另一侧写法）。大族（≥1s）：cfl 双面读 640、PMM rollup 558、latest_bucket 474、
探针资格 449、checkAutoCool 视图形态 372、CMI_HOT rollup 368（mean 7.4s）、
client_model 探针选择 351（mean 6.5s）、collector TPS 93、requestStatus 释放闸门
166（mean 18.3s）……

### 根因（EXPLAIN 实证，252 真库）

两族分区（session_turns_2026_07..11/default、request_logs_2026_08..11/default）
的全部 ts 索引要么复合前导（`(tenant_id, ts)`、`(CASE credential, ts DESC)`），
要么部分索引（`(ts DESC) WHERE discard_events IS NOT NULL`、
`(ts, id) WHERE digest IS NULL`）。**没有一条能服务「只按 ts 窗」的形态**：

- `db.V1FreezeCutoffSQL` 母表臂 `max(ts) FROM request_logs`：planner 选
  `(tenant_id, ts)` 整条索引扫聚合，2026_10 一臂 cost 8,442 / 334,757 条目
  （InitPlan cost 11,108，占 rollup 总 cost 31,018 的 36%）。该冻结点被内联进
  auto_index_refresher rollup（活抓：154/245 两实例同 tick 6-29.5s，贴
  30s rolconfig 击杀线）与 admin/tenants.go 四臂统计。
- canonical 视图窗口读臂：`session_turns_2026_09`（79.8 万行）cost 11,917、
  `request_logs_2026_10` cost 4,747——collector 三连、admin 面板、
  探针 client_model、所有视图窗查询全部陪绑。

### 修复（本提交）

`sql/migrations/startup/862_partition_family_leading_ts_idx.sql`（+down +判据
`migration_862_test.go` + 五点 installer 同步 + 通道腿登记 + TSV 再生）：

- `idx_session_turns_ts ON public.session_turns (ts)`、
  `idx_request_logs_ts ON public._request_logs (ts)`（860 后名，862 序号在
  860 之后；预应用环境按**索引名**幂等跳过）。
- 判据两条反向钉（858 教义）：**索引必须无谓词**（现存部分索引正是缺陷成因，
  加 WHERE「优化」会经 IF NOT EXISTS 静默退化）、**request_logs 腿必须用
  `_` 前缀名**。无 CONCURRENTLY（分区父表被拒，842/846/858 先例）。

### 252 预应用与验证（736 先例：手动治效、不占台账、canonical 862 部署时幂等自记账）

- `idx_session_turns_ts` 建 94.2s（2026_09 79.8 万行，宿主负载下）；
  `idx_request_logs_ts` 0.68s（冻结族）。双索引 indisvalid=true、
  附着 6/5 个分区子索引。
- **94s ShareLock 锁窗零损伤**：turns_hot 持续推进、aggregate outbox pending=4
  （正常涓流）、mirror outbox pending=0。
- EXPLAIN 前后：冻结点 InitPlan 11,108→**1.21**（每分区倒扫 top-1）；
  collector TPS 总 cost **19,575→309**（~63×）；rollup **31,018→6,775**。
  实测（准空闲态）：冻结点 55ms、TPS 84ms、session_turns_2026_09 臂
  cost 11,917→**2.40**（Index Only）。
- 稳态切片（07:00-07:05 活日志）：**rollup 与 TPS 两族从 ≥1s 慢查榜消失**
  （修复前 30.5h 分居 368/93 条）。残余榜 = 既有登记族（见 §四）。

## 三、错误族对账（9,335 锚定记录）

| 族 | 量 | 归因 / 处置 |
|---|---|---|
| user-request cancel + conn lost | 4,104 + 2,522 | 流式代理取消形态 + §二根因放大；根修后预期回落，下轮对账 |
| statement timeout | 1,552 | 同上；§二修复直接覆盖 request_logs 族 1,068 条 |
| parallel worker terminated | 571 | 上述 cancel 的并行 worker 陪葬 |
| **pocket `scheduled_tasks` $4 inconsistent types** | 350（~11.5/h） | **E6 外部族第 6 次登记**，节奏不变，催办升级 |
| lock timeout ×71 = `usage_ledger_hot` CREATE INDEX | 03:41-03:44 | 并行 MAAS 轨 DDL——**已对上仓内 856 迁移**（预应用尝试），锁超时后由迁移链收敛，销案 |
| **`column "tablename" does not exist`** | 56（00:33-05:09） | 新形状：`information_schema.tables` 上用 `tablename`（pg_tables 列名混用）+提示语。非网关语句，来源未知巡检/迁移 lint 脚本，登记 |
| DSM could not attach/map | 37 | 与上轮 1.2/h 基线持平，宿主内存压症状（swap 3,958/4,095） |
| deadlock ×2 | 10-13 06:38 | credentials UPDATE 双向等锁（探针×可用性更新器），2/30h 低频，登记 |
| `analyze_llm_gateway_table_stats(text)` 不存在 ×1 | 05:53 | 手工 psql 传 `'2'::text` 的一次性会话噪音 |
| 判据失败/阶梯判据失败/model_probe DDL 错 | 16:41-17:03 (10-13) | 并行会话对生产库跑探针阶梯真库实验的 RAISE 噪音，非生产缺陷 |
| ursm_node_snapshot_min INSERT timeout ×16 + cancel ×3 | ~1/h 贴点 | ursm 轨（830 待 owner），INSERT 现为慢而非 42P10——表已带 tenant_id（830 形态在位），**升级 ursm 轨** |
| 密码失败 asm_app ×6 / role acc ×3 / protocol 0.0 ×2 等 | 零星 | 外部/扫描器噪音 |

## 四、登记不修（对基线）

- **252-dev（llmgo-252-dev，:8780，10-01 构建 cc0e977f）仍在跑旧视图形态
  checkAutoCool**：`WITH win...FROM request_logs_with_current_month` 5min 窗
  ×372 慢查 ≈ 12/h = 单实例 5min tick。10-13 已推 hot 化修复（514a273e0），
  154/245 新构建不受影响——**该实例是视图形态唯一残存源头，重部署/退役
  裁决自 R21 拖至今，本轮给出量化：372 慢查 + ~200 stmout/30.5h，owner 拍板**。
- **ra7d REFRESH 212×37.5s（总墙钟 2.2h/30.5h，单族最大）**：matview 全量
  聚合语义（源=hot∪全分区 UNION），列存转堆后仍无索引可用形态——并行轨
  移交项维持，指数恶化暂停（本轮 37.5s < 上轮 65.2s 峰，随负载回落）。
- **WITH targets 回填 194×17.3s**：514a273e0 生效中（23.1→17.3s、DSM 需求侧
  下降），仍非根本治法（增量水位），维持登记。
- requestStatus 释放闸门 gauge 166×18.3s：79 万 NULL 历史行 heap 探测，
  随会话侧回填推进收敛；闸门口径（无窗）是 R30 刻意裁决，不动。
- probe 资格检查（`cmb.available...node_probe_runs DISTINCT ON`）449+158 条：
  r1026 已登记的 reconciler 盲区族，候选逐条 DISTINCT ON 成本，ursm 轨。
- smm 股票族第四+五次登记：`daily_kline MAX(date)` 168×13.7s、
  **`ALTER TABLE daily_kline ADD COLUMN IF NOT EXISTS` 165×14.8s**（回填进程
  在跑，CPU 62%×8h）——外部轨，随 fundamentals 回填结束自停，观察。
- `neg-cost` 健康检查 179×8.6s：`_request_logs` 30 天窗列存转堆后仍全扫，
  Optional+warning 语义，伴随 862 的 request_logs 臂改善部分收益，维持。
- `auto_route_selections` 统计 193×5.8s：对 request_logs 母表的 NOT EXISTS
  反连接（自动路由轨），停写后该反连接恒命中历史行，登记 auto-route 轨。
- `analyze_llm_gateway_table_stats('2')` 568s 一次（07:00 夜窗，与索引构建
  同窗竞争）+ `session_client_stats` REFRESH 124s：下轮对账确认是否常态。

## 五、量具坑（本轮新增）

1. **子串族分类器会混桶**：`count(*)...view` 这类 regex 把 TPS 仪表、
   request_id ANY、policy 候选全塞进一桶（报出 2,243 条假 TPS）。精确形状
   计数后 TPS 只有 93 条——纪律㉘ 的第三次实证，**粗分类只能用于分母，
   定罪必须指纹级**。
2. **`(ts)` LIKE 探测索引会漏**：`indexdef LIKE '%(ts)%'` 匹配不到
   `(ts DESC) WHERE ...`（括号后跟空格），一度误判「零前导 ts 索引」；
   改查 `pg_index`+`indisvalid`+定义全文后真相是「有 partial、无全量」。
   **判形态必须取 indexdef 全文，不能子串猜**。
3. **并行 S4 轨在我审计期间合入 860/861**（本 checkout 快进落后 4 提交）：
   HEAD 代码已引用 `_request_logs*` 新名而 252 库未跑 860——EXPLAIN 夹具
   必须按「被测部署版本」的表名渲染（_前缀替换后重跑），否则 42P01 假障。
   862 的表名选择（860 后名）与预应用幂等键（索引名）以此为基础。

## 六、修复物与门

- `sql/migrations/startup/862_partition_family_leading_ts_idx.{sql,.down.sql}`
- `sql/migrations/startup/migration_862_test.go`（形状钉 + 两条反向钉）
- 五点同步：installer embeddata 副本 / main.go embed+map / dbinit StartupFiles /
  `scripts/apply-db-revision-sequence.sh` 通道腿 / TSV +1 行（257 号）
- 门：apply-db-revision-sequence_test rc=0；installer `go test ./...` 13 包全绿；
  startup 包测试 ok；installer vet rc=0；root `go build ./...` rc=0；gofmt 净。

## 七、下一轮

1. 862 随 154/245 下次部署生效后：cancel/stmout 风暴（§三前四族）与
   rollup/TPS/冻结点族在 24h 窗对账（预期：视图族慢查 7,480→三位数）。
2. 252-dev 处置拍板后的对账（视图形态 auto-cool 应清零）。
3. `analyze('2')` 568s 与 session_client_stats 124s 是否常态。
4. swap 3,958/4,095 与 pms-redis 50% CPU：宿主内存治理（ops 升级项）。
