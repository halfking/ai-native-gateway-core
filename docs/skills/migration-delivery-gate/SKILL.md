---
name: migration-delivery-gate
description: 为 llm-gateway-go 仓新增或修改 PostgreSQL 迁移时的交付纪律。适用场景：写新迁移号、修改已应用迁移的内容、发现某迁移在部分环境缺失、部署被 checksum/产物探针闸门拦下、种子数据（规则集等）需要随迁移投递。
---

# 迁移投递门禁（七点登记 + 探针不毒丸）

来源：`docs/audit/2026-11-17-PI智能体审计与完善-主档.md` §六/§八点五（迁移 977/978 的实战教训）；54 增量（942-949 五点同步缺两点的教训）。

## 一、新迁移的七点登记（缺任何一点＝某个通道静默少跑）

新迁移文件 `sql/migrations/startup/NNN_name.sql` 必须同时登记七处：

| # | 位置 | 作用 |
|---|---|---|
| 1 | `sql/migrations/startup/NNN_name.sql`（正典） | 内容本体（+ `.down.sql` 如需可回滚） |
| 2 | embeddata 副本（`installer/cmd/llm-gw-installer/embeddata/` 下，经 go:embed 打包进安装器） | 安装器打包通道的字节一致副本 |
| 3 | `go:embed` 指令 + 包级 `var` | Go 侧物化 |
| 4 | `embeddedSQLFiles` map 条目 | 代码侧按文件名索引 |
| 5 | `installer/internal/dbinit/runner.go` 的 `StartupFiles` 清单 | 全新安装链 |
| 6 | `scripts/apply-db-revision-sequence.sh` 的 files 数组 + `ledger_artifact_probe` | 存量升级通道 + 部署闸门探针 |
| 7 | `sql/schema/installed_startup_migrations.tsv` 行 + `docs/db-changelog.md` 台账行 | 机器可读清单 + 人读账 |

验证门：`go test ./internal/sqlguard/ -run 'Migration<NNN>'`（为每个新迁移写 delivery test）。

## 二、探针不毒丸（产物探针只写「产物」）

部署闸门里的 `ledger_artifact_probe` 是「迁移真的产出了对象」的断言。两条已实证的毒丸形态：

1. **缺席型判词被注释污染**：`position('RETURNING' in p.prosrc) = 0` 这类「某词缺席」判据，会被**函数体注释里的同一个词**命中 ⇒ 活库上恒 false ⇒ 部署被自己的探针打死。`pg_get_functiondef()` 同样保留注释，换读法躲不开——**必须先剥注释**：
   ```sql
   position('RETURNING' in regexp_replace(p.prosrc, '--[^\n]*', '', 'g')) = 0
   ```
   并配**正形判据**（要求关键实现句在场，防「空壳也过」）。守卫门：`internal/sqlguard/artifact_probe_comment_strip_gate_test.go`。

2. **断言运行时状态**：探针断「表里有 N 行」这类会随生产变化的状态 ⇒ 从守卫退化成毒丸。探针只写 DDL 产物（对象存在、索引 unique、函数体形状）。

**纪律：新探针写好后，拿目标活库真的求值一遍**（读代码永远看不出注释会命中判词）。

## 三、改已应用迁移的内容 = 永久 checksum 漂移

已应用迁移的字节被改动 ⇒ 部署闸门「recorded sha ≠ local sha」恒拦。合规通道是 `scripts/apply-db-revision-sequence.sh` 的 `legacy_content_replays`（白名单登记现内容 sha + 重放理由）。重放前先量三件事：

1. 差异是否语义等价（只有注释/超时预算）；
2. DDL 是否幂等形态（`IF NOT EXISTS` / `ON CONFLICT DO NOTHING`）；
3. **数据侧会不会变**（backfill 实测 would_insert=0 才放行）。

台账对齐在重放**之后**（台账语义 = 「这串字节真的跑过」）。

## 四、种子数据也是要投递的交付物

「表在」≠「表里有货」。种子数据若只住在历史迁移里：

- 查**全新安装链**：`installed_startup_migrations.tsv` 里有没有它；没有 ⇒ 新装环境恒空表。
- 查**存量升级链**：低于对账地板的旧迁移是否在 `scripts/migration-low-version-delivery.list` 白名单。
- 查它自身可重放性：无 `ON CONFLICT` 的 INSERT 在已有同键行时会 23505 炸掉整条迁移。

正解（978 先例）：把 canonical 种子集做成**新迁移**，`ON CONFLICT DO NOTHING`（幂等、不覆盖既有行、不删行）⇒ 各环境收敛到同一份；为它跑隔离 PG 三形态真跑腿（空表 / 幂等重放 / 存量形态）。

## 五、正则相关迁移：判据要用真实执行引擎

PG 的 `\b` 是**退格符**（PG 词边界是 `\y`），Go RE2 的 `\b` 才是词边界。仓内规则 pattern 只在 Go 侧评估 ⇒ 拿 PG 复核 RE2 语义会得**假阴性**。在迁移注释与 changelog 里写明这一点。
