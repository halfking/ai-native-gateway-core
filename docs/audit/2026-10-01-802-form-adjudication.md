# 802 形态裁决与生产 healing 记录（2026-10-01）

> 关联：907d67b85（802 回填形态裁决改回递归）、docs/audit/2026-09-30-session-request-data-re-audit.md（S4 会话族读路径迁移主档）。
> 本文档是 907d67b85 之后的批判式复审轮产物：修正该提交里的失实表述、补齐缺失证据、并处置复审挖出的生产级问题。

## 一、复审结论（对 907d67b85 的批判）

907d67b85 的 DDL 修复本身正确（真库 A/B 实证 + 门禁全绿），但存在三处不严谨，本轮全部修正：

1. **「现网真库索引健康」表述失实（最重要）**：该提交的 commit message 与迁移文件头注写
   "现网真库母表索引 indisvalid=true、attached=2（递归形态所建，健康）"，实际只测量了
   **本机** llm_gateway 库。批判式复审实测 252 生产集群：**indisvalid=false、attached=0**——
   生产上真实执行过回填 ON ONLY 形态的 DDL（手工 psql 应用、台账无登记），权限门母表腿
   一直在退化扫描。交接文档链条（上一会话 handoff → 907d67b85）把这个错误一路带了下来，
   属于"复述上游结论未独立核实"。
2. **规划器证据只到语义层面**：复审补了 5 万行 scratch 表的 EXPLAIN 演示——无效索引下
   Seq Scan，递归形态下 Index Only Scan，把"规划器不可用"从声明变成演示。
3. **.down 配对口径不一致**：沿「800 先例」撤回 802 .down，但同族 801 有 .down（800 没有）。
   更近的先例是 801，故本轮恢复 802 .down（内容为 S4 原稿轮已写好的备份，DROP hot 先于
   DROP 母表）。`.down.sql` 在全部守卫测试中显式豁免（embeddata ⊆ StartupFiles、
   ≥704 注册、offline_apply 均跳过），无连带影响。

## 二、证据链（全部实测）

### 2.1 两种 DDL 形态的语义（本机真库事务内，scratch 表，跑完回滚）

| 形态 | indisvalid | attached 子索引 | 5 万行查询计划 |
|---|---|---|---|
| `ON ONLY`（回填版，无 ATTACH） | **false** | 0 | **Seq Scan**（索引被规划器忽略） |
| 递归（修复版，对齐 525） | **true** | 既有分区自动创建并挂载 | **Index Only Scan** |

### 2.2 真实文件 fresh 库 CREATE 路径（本机 scratch 库，最小忠实重建 733 家族形态）

- 修复版 802 文件：应用成功 → `indisvalid=true, attached=2`；
- 旧回填版 802 文件（c54ab8dc1 内容）：应用成功 → `indisvalid=false, attached=0`。

### 2.3 生产集群（252）实测与 healing

三台网关共用 252 的 PG17 集群（pg_stat_activity 实证：210/252=27 连接、
241/245=21 连接、209/154=17 连接），一次 heal 覆盖全部生产。

| 时点 | 母表索引 idx_session_turn_details_tenant_gw_task_id |
|---|---|
| heal 前 | indisvalid=false、attached=0、indexdef 为 ON ONLY 形态（手工应用回填 DDL 的产物）；2026_09 分区 72.2 万行（reltuples 估算）、2026_10 在写 |
| heal 后 | indisvalid=true、attached=2；EXPLAIN（assertTaskInTenant 查询形态探针）两分区均走 Index Only Scan |

heal 动作（2026-10-01 凌晨，psql 单事务，`SET LOCAL statement_timeout='10min'` +
`lock_timeout='30s'` 防 rolconfig 30s 击杀留 INVALID 残留——纪律⑫）：
`DROP INDEX IF EXISTS` + 按修复版文件逐字 `CREATE INDEX`（递归）+ COMMENT ON 覆盖。
构建秒级完成；SHARE 锁窗口仅影响 promote 批量写，hot 表与在线读写不受影响。

### 2.4 pg_indexes 渲染陷阱（系统性，必须入档）

`pg_indexes.indexdef` 对分区父索引**恒定**渲染为 `ON ONLY 母表`，分叶子索引以独立行出现——
**与创建方式无关**（heal 后的健康索引同样渲染为 ON ONLY）。因此：

- R35-N1 按 pg_indexes 逐字回填必然复现该缺陷，这不是一次性手误而是机制陷阱；
- 健康判据只能是 `pg_index.indisvalid` + `pg_inherits` attached 计数，**不能用 indexdef
  文本比对**；
- 任何未来按 pg_indexes 回填分区索引 DDL 的人都会重蹈覆辙，本文档即为对冲。

### 2.5 台账重放语义与残余风险

`gateway_db_revision_sequences` 记账 content_sha256（本机已有该列；252 尚无——脚本版本
随下次部署升级）。同名文件内容变更 → 下次 `apply-db-revision-sequence.sh` 自动重放：

- 对索引本就健康/不存在的环境：重放 = IF NOT EXISTS skip + COMMENT 覆盖 + 刷新指纹，安全；
- **对执行过回填 DDL 且留 下无效索引的环境：重放不自愈**（IF NOT EXISTS 命中同名无效索引
  直接跳过）——必须一次性 `DROP INDEX` + 递归重建（即 252 已执行的操作）。

已知环境状态：本机=递归健康（台账行带旧指纹，下次脚本运行自动刷新）；252=已 heal；
154/245=无独立 PG（共用 252）。若存在未入档的灾备/新装环境，部署前按 2.3 判据核查。

## 三、本轮改动

- `sql/migrations/startup/802_session_turn_details_gw_task_id_index.down.sql`：恢复
  （S4 原稿轮备份内容，DROP hot → DROP 母表，仅删索引不动数据）。
- `docs/audit/2026-10-01-802-form-adjudication.md`：本文档。

## 四、测试命令与结果

- `go test ./sql/schema/ -count=1` → ok（六门）；
- `go test ./cmd/llm-gw-installer/ ./internal/dbinit/ -count=1`（installer 模块）→ ok
  （含 802 canonical↔embeddata bytes.Equal parity、≥704 注册守卫 .down 豁免路径）；
- 真库（本机）事务内 A/B 形态 + 5 万行 EXPLAIN 演示 + fresh 库真实文件 CREATE 路径，
  全部跑完回滚/删库零残留（残留检查 count=0）；
- 252 生产 heal 前后 indisvalid/attached/EXPLAIN 三点实测（见 2.3）。
