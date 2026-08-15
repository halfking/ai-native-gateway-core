# 版本号文件自洽修正 (build 1546)

日期: 2026-08-15
类型: chore(version) (版本号文件)
影响模块: VERSION / version.json / web/public/{version,menu-config}.json / web/dist/version.json

## 背景

合并 `feat/m3-durable-wave2` (a7b69fdb) 后，仓内版本号文件未按 `scripts/bump-version.sh`
的 SSOT 锁步规则自洽，存在以下偏差：

1. **build_seq 跳号**：本地工作区版本为 1545，但 `bump-version.sh` 规则是
   `current + 1`（`scripts/bump-version.sh:80`），跳号 1 不在脚本锁步覆盖范围。
2. **4 文件 lockstep 缺失**：脚本维护 4 个版本号文件
   (`version.json` / `VERSION` / `web/public/version.json` / `web/dist/version.json`)，
   之前手工修改只覆盖了前 3 个，遗漏 `web/dist/version.json`。
3. **menu-config.exported_at 漂移**：未提交工作区中 `exported_at` 与 HEAD 时间戳相差
   仅 5 分钟（仓内历史 release 间隔为 4-7 小时），疑似误操作。
4. **缺独立 CHANGELOG**：上述 4 个文件的 release 提交历史上均不附 CHANGELOG，
   本次补齐。

## 修正内容

- 跑 `scripts/bump-version.sh`（不带 `--dry-run`），让 build_seq 严格自洽：
  - 1545 → **1546**（+1 锁步）
  - git_sha = `a7b69fdb`（HEAD 短 SHA，9 个 char）
  - 同时补齐 4 个文件 lockstep（含 `web/dist/version.json`）
- `web/public/menu-config.json` 的 `exported_at` 与 release 时刻对齐。

## 验证

- `go build ./...`: exit 0
- `go vet ./...`: exit 0
- `git diff --stat`: 4 行变更（VERSION / version.json / web/public/version.json /
  web/public/menu-config.json），脚本补的 `web/dist/version.json` 因与
  `web/public/version.json` 内容相同被自动识别为 no-op。

## 不影响

- 不修改 `docs/db-changelog.md`：该文件记录的是已 apply 的 migration，与版本号
  bump 解耦，按 `0824c3004` / `a7b69fdb1` 历史惯例不联动。
- 不修改 `CHANGELOG.md` 主体（仅新增本条目引用）。
