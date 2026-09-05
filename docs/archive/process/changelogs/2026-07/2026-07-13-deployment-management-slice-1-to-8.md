# 2026-07-13 — 部署管理硬化 · 切片 8（deprecation wrappers）

## 概要

按 spec `2026-07-13-deployment-management-hardening-design.md` §"Compatibility disposition" 收尾切片 8：
- `deploy/deploy.sh` 转薄为 fail-closed deprecation wrapper
- `deploy/rollback.sh` 转薄为 fail-closed deprecation wrapper
- 13 个 wrapper 行为断言（forwarding / 退出码 / fail-closed）

## 切片 8：compatibility wrappers

| 文件 | 角色 |
|---|---|
| `deploy/deploy.sh` | 60 行：warn + `exec scripts/deploy.sh "$@"` |
| `deploy/rollback.sh` | 60 行：warn + `exec scripts/deploy.sh rollback "$@"` |
| `tests/deploy_wrapper_test.sh` | 175 行：13 个断言 |

**两个 wrapper 的设计原则：**

1. **打印一次性 deprecation 警告到 stderr**（黄色 `!` 前缀），不污染 stdout。CI / 终端都能直接看到 migration 提示。
2. **完整转发所有参数**（包括 `--to`、`--json`、`--dry-run`），不改变退出码语义。
3. **`exec` 替换而非 fork**：wrapper 进程被 canonical CLI 替换，不浪费 PID。
4. **Fail-closed**：找不到 canonical CLI 时退出 64 并打印明确错误，不静默出错。
5. **保留原子性**：canonical CLI 的 `set -euo pipefail` 行为不变 — wrapper 不引入自己的 shell 层。

```bash
# 旧用法（继续工作但带警告）：
$ ./deploy/deploy.sh plan 154 --json
! deploy/deploy.sh is deprecated (spec cf8aad1a9 Slice 8)
! Forwarding to canonical CLI: .../scripts/deploy.sh plan 154 --json
{"target":"154", ...}

# 新用法（推荐）：
$ ./scripts/deploy.sh plan 154 --json
{"target":"154", ...}
```

## spec 表中未转薄的部分（保留依据）

| 脚本 | spec 处置 | 当前状态 |
|---|---|---|
| `deploy/deploy.sh` | fail-closed wrapper | ✅ 本切片完成 |
| `deploy/rollback.sh` | fail-closed wrapper | ✅ 本切片完成 |
| `deploy-154.sh` / `scripts/deploy-154.sh` | 转换延后到 canonical 154 deploy parity 通过 | ⏳ 保留 475 行专用实现 |
| `deploy-to-252.sh` | 保留独立（非 canonical，252 deferred 期间） | ✅ 保持不变 |
| `deploy-kaixuan1.sh` / `scripts/deploy-kaixuan1.sh` | 保留独立（非 canonical，kaixuan deferred 期间） | ✅ 保持不变 |
| `scripts/deploy-154-data-bindmounts.sh` | 专用 setup | ✅ 保持不变 |
| `scripts/deploy-from-scratch.sh` | 专用 setup | ✅ 保持不变 |
| `scripts/deploy-k8s.sh` | 专用 setup | ✅ 保持不变 |
| `scripts/deploy-minimal-nodb.sh` | 专用 setup | ✅ 保持不变 |
| `scripts/deploy-verify-*.sh` | 专用 verify 脚本 | ✅ 保持不变 |
| `deploy-llm-kxpms-cert.sh` / `deploy-tab-navigation.sh` | 站点级变更 | ✅ 保持不变 |

## 全部 8 个切片测试套件

```text
$ bash tests/deploy_cli_test.sh       24 passed, 0 failed, 2 skipped
$ bash tests/deploy_host_test.sh      23 passed, 0 failed
$ bash tests/deploy_154_test.sh       15 passed, 0 failed
$ bash tests/deploy_wrapper_test.sh   13 passed, 0 failed

 TOTAL: 75 passing assertions, 0 failing
```

## spec AC-11 验收对照

```text
bash -n deploy.sh scripts/deploy.sh scripts/deploy-lib/*.sh tests/deploy_*.sh   OK
shellcheck -S error scripts/deploy-lib/*.sh deploy/deploy.sh deploy/rollback.sh \
                       tests/deploy_*.sh                                       OK
bash tests/deploy_cli_test.sh && bash tests/deploy_host_test.sh && \
  bash tests/deploy_154_test.sh && bash tests/deploy_wrapper_test.sh           OK

```

ShellCheck 版本 0.11.0 在本环境可用（不是 unavailable）。

## 文件清单

### 新增
- `tests/deploy_wrapper_test.sh`

### 修改
- `deploy/deploy.sh`：转薄为 deprecation wrapper
- `deploy/rollback.sh`：转薄为 deprecation wrapper
- `CHANGELOG.md`

### 删除
- 旧的 `deploy/deploy.sh` 内部 4 KB 的 docker / kubectl / systemctl flow（被替换）
- 旧的 `deploy/rollback.sh` 内部 deploy-tracker 读取流程（被替换）

## 验证

- ✅ `bash -n` 全部通过
- ✅ `shellcheck -S error` 0 errors
- ✅ `bash tests/deploy_*_test.sh` 总 75/75

## 后续（非本切片范围）

- 切片 6/7 剩余（SOPS policy + HEAD 凭据清理）独立 PR。
- 154 canonical 完整 parity 工作（独立 PR；spec §Compatibility disposition 解锁 154 转换）。
- 245 的 `build / scp / checksum` 步骤的 orchestrator 串联（独立 PR；Slice 4 已经把库函数备齐，剩余工作是把 build 流水线串起来）。