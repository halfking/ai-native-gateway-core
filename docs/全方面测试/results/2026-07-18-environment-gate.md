# 全方面测试环境门禁报告（2026-07-18）

## 结论

状态：`BLOCKED_ENVIRONMENT`

本次未执行 16 场景大型压测。原因是 252 与本地 PG17 的 schema 尚未一致，且本地 vector 扩展无法安全加载。继续压测会把测试台错误误判为 Gateway 行为结果。

## 已完成

- 已从 `origin/main` 拉取，工作区位于测试分支。
- 已通过证书登录 252（SSH 端口 `${PORT_SSH_252}`），确认远端 `pg-252-pg17` 可访问。
- 已备份本地数据库（排除无法读取的 `public.memories` 表）：`/tmp/llm-gateway-local-pre-sync-20260718-no-memories.dump`。
- 已恢复本地 `llm-gateway-pg`，使用原绑定目录和 Citus PG17 镜像，连接稳定。
- 已验证本地扩展 catalog：`citus`、`citus_columnar`、`vector`。
- 已补充错误分类与环境门禁文档，并修复同步脚本的错误码误报。

## 阻塞证据

| 检查项 | 252 | 本地 | 结果 |
|---|---:|---:|---|
| public tables | 273 | 271 | fail |
| public columns | 4448 | 4382 | fail |
| public indexes | 790 | 741 | fail |
| public views | 35 | 35 | pass |
| public functions | 483 | 481 | fail |
| RLS policies | 69 | 64 | fail |
| triggers | 33 | 33 | pass |

本地原镜像缺少 `vector.so`。本地 vector-enabled Citus 镜像包含 `vector.so`，但加载时 PostgreSQL 记录 `signal 4: Illegal instruction`，因此不能用于当前 ARM 数据目录。

## 恢复后执行

1. 准备 CPU/ABI 兼容的 PG17+Citus+pgvector ARM 镜像。
2. 验证 `LOAD 'vector'` 不崩溃。
3. 人工审查并清理本次同步预演留下的不完整表壳，或从同步前备份恢复。
4. 运行 `scripts/pg-table-copy.sh --source configs/env-252.sh --target configs/env-local.sh --schema-only`。
5. 通过 schema/object diff 后，再运行 `docs/全方面测试/scenarios/run_all.sh`。
