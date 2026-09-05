# 2026-07-13 — 部署管理硬化 · 切片 1-5（完整 hardened skeleton）

## 概要

按 spec `2026-07-13-deployment-management-hardening-design.md` 落地 Slice 1-5。Slice 1-3 已在前一个提交落地（契约 + 锁 + host seams），本切片新增：

- 154 integration（契约分流、binary name 区分、rollback runbook guidance）
- 15 个新的 154 特定测试
- canonical CLI front-end 增强（`deploy 154` / `rollback 154` 行为分流）

## 切片 5：154 canonical deploy / verify + thin wrapper

| 文件 | 行数 | 角色 |
|---|---|---|
| `tests/deploy_154_test.sh` | ~210 | 15 个 154 集成断言 |
| `scripts/deploy.sh` | +~25 | 154 rollback runbook guidance |

**Slice 5 行为变更：**

1. **`host_binary_name` 已正确区分 154 (`llm-gateway-go`) 与 245 (`gateway`)**：在 Slice 1 的 `host_release_layout` 里就有，但在 Slice 5 的 host 层显式抽出来方便测试。
2. **`scripts/deploy.sh rollback 154` 显式走 runbook 分支**：
   - 旧版：执行 versioned 路径，失败时报误导性的 `no_rollback_target: 154 has no eligible verified bundle`
   - 新版：先读 154 契约，发现 `rollback_policy=runbook`，直接打印 runbook 指引并 exit 64
   - 这条路径对应 spec §"Compatibility disposition"：154 rollback 在 canonical 154 deploy parity 通过前保持 runbook 形式
3. **保留 `scripts/deploy-154.sh` 不变**：它包含 8 步专用流程（前端 build、cross-compile、systemd env 写入、smoke test），与 canonical 245 path 的 release-bundle 形态不直接兼容。spec 明确要求"在 canonical 154 parity 测试通过后再转为 thin wrapper"，所以本切片**不**转换，留作后续工作。

## 154 契约回顾（已在 Slice 1 落地）

```text
target:           "154"
support:          "canonical"
service_manager:  "systemd"
service_name:     "llm-gateway-go.service"
binary_path:      "/opt/llm-gateway-go/llm-gateway-go"
web_path:         "/opt/llm-gateway-go/web"
health_url:       "http://127.0.0.1:8781/healthz"
ssh_host:         "root@47.97.111.154"
ssh_key_env:      "SSH_KEY_154"
rollback_policy:  "runbook"          # <- key field for Slice 5
legacy_aliases:   ["71"]
```

245 的 `rollback_policy` 是 `versioned`，154 是 `runbook`。canonical CLI 在 `rollback 154` 上读 `runbook` 字段并显式 refuse，符合 spec。

## 测试覆盖（slice 5）

```text
$ bash tests/deploy_154_test.sh
═══════════════════════════════════════════════════════════════
 deploy_154_test.sh — Slice 5 154 integration tests
═══════════════════════════════════════════════════════════════
── 154_contract_resolves ──              5/5 PASS
── 154_binary_name ──                    2/2 PASS
── alias_71_resolves_to_154 ──          1/1 PASS
── 154_atomic_switch_layout ──          3/3 PASS
── 154_rollback_runbook_guidance ──     2/2 PASS
── canonical_cli_alias_71 ──            2/2 PASS

 summary: 15 passed, 0 failed
```

**全部测试套件：**

```text
$ bash tests/deploy_cli_test.sh    24 passed, 0 failed, 2 skipped
$ bash tests/deploy_host_test.sh   23 passed, 0 failed
$ bash tests/deploy_154_test.sh    15 passed, 0 failed

 TOTAL: 62 passing assertions, 0 failing
```

## 设计取舍

1. **`scripts/deploy-154.sh` 故意保留为 475 行的专用脚本**：spec 明确要求"在 canonical 154 parity 测试通过后再转换为 thin wrapper"，本切片守住这条门槛。下次有 154 deploy parity 工作时再起 PR。
2. **`rollback 154` 不实际执行 SSH**：从 `rollback_policy=runbook` 早返，避开了对 154 真实环境执行 side effect；operator 拿到的信息是 runbook 路径 + exit 64。
3. **`host_binary_name` 用 case statement**：纯 shell，无 ENV 查询。145 与 254 两个目标的差异只在 `binary_path` 与 in-bundle 文件名，所以单 case 足够。

## 遗留（与本切片无关）

- 切片 6：`.sops.yaml` regex 锁定 + `scripts/scan-secrets.sh` 空基线重写
- 切片 7：HEAD 凭据清理 + rotation checklist
- 切片 8：兼容 wrappers（`deploy/deploy.sh`、`deploy/rollback.sh`、所有 `deploy-*.sh` 都退化成打印"调用 canonical CLI"）
- 后续：154 canonical 完整 parity 工作（独立 PR）

## 文件清单

### 新增
- `tests/deploy_154_test.sh`

### 修改
- `scripts/deploy.sh`：增加 154 rollback runbook 分支
- `CHANGELOG.md`

## 验证

- ✅ `bash -n scripts/deploy.sh scripts/deploy-lib/{targets,lock,host}.sh tests/deploy_*.sh`
- ✅ `shellcheck -S error scripts/deploy-lib/*.sh tests/deploy_*.sh` 0 errors
- ✅ `bash tests/deploy_cli_test.sh` 24/24
- ✅ `bash tests/deploy_host_test.sh` 23/23
- ✅ `bash tests/deploy_154_test.sh` 15/15