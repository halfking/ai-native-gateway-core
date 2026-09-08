# Multi-DB Sync Plan (Local `llm-gateway-pg` ⇄ 252 `pg17`)

- **Date**: 2026-09-07
- **Direction**: 单向 252 → 本地；以本地结构为冲突仲裁基准。
- **Owner**: ZCode 会话 `smm_v1:733fd64ad8c769c2`
- **Goal**: 5 库结构 100% 一致；除 `llm_gateway` 与分区表外的数据行数完全一致。

---

## 1. Scope & 策略矩阵

| 库                | 结构同步 | 数据同步 | 备注                                 |
| ----------------- | -------- | -------- | ------------------------------------ |
| `llm_gateway`     | ✅       | ❌       | 表多、用户量级大，只同步结构         |
| `kb_builder`      | ✅       | ✅ 普通表 | 分区表只结构                         |
| `memora`          | ✅       | ✅ 普通表 | 分区表只结构                         |
| `prompt_center`   | ✅       | ✅ 普通表 | 分区表只结构                         |
| `kxmemory`        | ✅       | ✅ 普通表 | 分区表只结构                         |

**统一规则**

- 同步方向：252 → 本地；不删本地独有对象；不动 252 服务器任何状态。
- 分区表识别：`pg_partitioned_table` 或父表 DDL 含 `PARTITION BY`；视为「结构-only」。
- `llm_gateway` 全库结构-only；`memora.tenant_*` 等含分区父表的子集按分区表处理。
- 备份：每个阶段执行前 `pg_dump -Fc` 落 `/tmp/multi-db-sync-252/`；保留 30 天。
- 失败回滚：`pg_restore --clean --if-exists` 即可恢复到本阶段开始前状态。
- Dry-run：阶段 2/4 强制 `--dry-run`；阶段 3/5 默认应用，但任何破坏性语句前会再次打印确认。
- 用户确认门槛：阶段 1→2、阶段 2→3、阶段 3→4、阶段 4→5、阶段 5→6 各设一次确认点。

---

## 2. 6 阶段流水线

| #  | 阶段                  | 关键动作                                                                      | 输出                                                            | 用户确认 |
| -- | --------------------- | ----------------------------------------------------------------------------- | --------------------------------------------------------------- | -------- |
| 1  | Discovery             | 建 SSH 隧道；枚举两边库/表/列/索引/约束/分区；列差异报告                      | `discovery.json` `discovery.md`                                 | ✅       |
| 2  | DDL Dry-run           | 对每个库生成结构差异 DDL 草案；标记分区表 skip-data                           | `ddl/<db>.dryrun.sql` `ddl/diff-summary.md`                     | ✅       |
| 3  | DDL 应用 + 结构审计   | 应用结构 DDL；调用 `verify-multi-db-consistency.sh` 多库结构审计              | `structure-audit.md`                                            | ✅       |
| 4  | Data Dry-run          | 逐表行数差异统计；分区表 & `llm_gateway` 跳过数据                            | `data-dryrun.md`                                                | ✅       |
| 5  | Data TRUNCATE + COPY  | 普通表 TRUNCATE+COPY；分区表 skip；多库数据审计                              | `data-audit.md`                                                 | ✅       |
| 6  | 沉淀                  | 生成同步报告 `multi-db-sync-report-20260907.md`；更新 `SKILL.md`；提交代码    | report + skill + git commits                                    | ✅       |

---

## 3. 入口脚本

`scripts/multi-db-sync-252.sh` 6 个子命令：

```bash
./scripts/multi-db-sync-252.sh discovery   # 阶段 1
./scripts/multi-db-sync-252.sh ddl-dryrun  # 阶段 2
./scripts/multi-db-sync-252.sh ddl-apply   # 阶段 3
./scripts/multi-db-sync-252.sh data-dryrun # 阶段 4
./scripts/multi-db-sync-252.sh data-sync   # 阶段 5
./scripts/multi-db-sync-252.sh finalize    # 阶段 6
```

- 工作目录：`/tmp/multi-db-sync-252/{discovery,ddl,structure-audit,data-dryrun,data-audit}/`
- 备份目录：`/tmp/multi-db-sync-252/backups/<db>_<stage>_<timestamp>.dump`
- 配置来源：`configs/multi-db-sync.policy`（JSON）
- 通用性：5 库列表、连接串、partition 识别、dry-run 开关全部来自 policy。

---

## 4. 复用性设计

1. **配置驱动**：`multi-db-sync.policy` 描述「库集合 + 每库策略 + 跳过集合」。复用只需替换此文件。
2. **脚本分层**：`multi-db-sync-252.sh`（编排）+ `verify-multi-db-*.sh`（审计）+ `pg_dump`/`pg_restore` 原语。
3. **策略与机制分离**：连接方式、差异算法是「机制」，policy 是「策略」。同步到别的目标环境（不止 252）只换 SSH 入口与连接串。
4. **审计双轨**：结构审计 (`verify-multi-db-consistency.sh`) + 数据审计 (`verify-multi-db-data-consistency.sh`) 是独立的、可重入的小工具。
5. **可重入**：每个阶段幂等；阶段 5 的 TRUNCATE+COPY 设计上可重跑。
6. **报告骨架固定**：所有产物路径、命名遵循同一规范，下次复用只看配置即可。

---

## 5. 风险与回滚

- **风险**：阶段 3 应用 DDL 时若 252 视图/函数依赖本地不存在对象会失败 → 先应用 DDL，再补依赖；失败立即停止。
- **风险**：阶段 5 TRUNCATE 后 COPY 中断 → TRUNCATE 已提交的库会丢数据 → 阶段 3 之前的全量备份 + 阶段 5 每库 COPY 前备份双保险。
- **回滚**：任意阶段失败 = 用阶段开始前 `.dump` `pg_restore --clean --if-exists -d <db>`。

---

## 6. 沉淀交付物

- `docs/db-sync/multi-db-sync-plan-20260907.md`（本文件）
- `docs/db-sync/multi-db-sync-report-20260907.md`（阶段 6 生成）
- `configs/multi-db-sync.policy`
- `scripts/multi-db-sync-252.sh`
- `scripts/local-dev/verify-multi-db-consistency.sh`
- `scripts/local-dev/verify-multi-db-data-consistency.sh`
- `~/.agents/skills/pg-sync-252-to-env/SKILL.md`（技能沉淀）

---

## 7. 启动条件

- ✅ `configs/multi-db-sync.policy` 已落盘并通过 JSON 校验
- ✅ `scripts/multi-db-sync-252.sh` 已实现 6 阶段 stub + `discovery`/`ddl-dryrun` 完整逻辑
- ✅ `scripts/local-dev/verify-multi-db-consistency.sh` 已落盘
- ✅ `scripts/local-dev/verify-multi-db-data-consistency.sh` 已落盘
- ⏳ 用户确认：「按默认执行」/「调整后再执行」
- ⏳ 252 服务器 SSH 凭据可达（本机 `deploy-to-245` 模式可参考）

确认后将自动进入阶段 1（Discovery）。
