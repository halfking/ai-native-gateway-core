# llm-gateway-go 文档归档

归档基线：2026-08-17
归档分支：`chore/docs-archive-2026-08`
归档范围：仓库根目录 87 个过期过程文档

## 目的

仓库根目录历史累积了大量 bugfix / audit / fix / phase / handoff / deploy-report 类过程文档，
多数距今 24 天以上未维护，与 `docs/` 子目录下的归档结构重复。本批归档仅做位置搬迁，
文件内容保持不变，可通过 `git revert <commit>` 100% 还原。

## 目录划分

| 桶 | 数量 | 取自 | 说明 |
|---|---|---|---|
| `2026-07/` | 50 | 根目录 mtime ∈ 2026-07-19~07-31 的过程文档 | 7 月 bugfix/audit/phase/handoff 风暴 |
| `2026-08/` | 37 | 根目录 mtime ∈ 2026-08-02~08-13 的过程文档 | 8 月 routing/audit/credential 风暴 |

## 保留不动的活跃文档（白名单）

- `README.md`、`CHANGELOG.md`、`SECURITY.md`、`CONTRIBUTING.md`、`PROJECT_CONFIG.md`、`LICENSE`
- `QUICK_REFERENCE.txt`（8-13 维护）、`SUMMARY.md`（8-13 维护）、`AUDIT_24H_20260817.md`（今天 8-17 新增活跃）
- Go 元数据 `go.mod`、`go.sum`、`VERSION`、`version.json`、`build_seq`
- 部署/构建配置 `.env.example`、`config.example.yaml`、`Dockerfile`、`Makefile`
- `docs/` 子目录、`_to_be_deleted/`、deploy 脚本

## 检索

```bash
# 按月份定位
ls docs/archive/2026-07/ | grep -i routing
ls docs/archive/2026-08/ | grep -i audit

# 按主题全量搜
grep -RE "swimlane" docs/archive/

# 按文件名
find docs/archive/ -name "*ROUTING*"
```

## 回滚

本次提交全部使用 `git mv`（或 `mv` + `git add`，历史可追溯），整批操作是单 commit。
若需回滚：

```bash
git revert <commit>
# 或
git checkout HEAD~1 -- <file>
```

## 相关索引

- `../INDEX.md` —— 文档主索引（已追加 archive 章节）
- `../../CHANGELOG.md` —— 项目变更日志
- `../../docs/runbooks/` —— 当前活跃 Runbook 主题
- `../../docs/changelogs/` —— 按日期归档的变更记录
