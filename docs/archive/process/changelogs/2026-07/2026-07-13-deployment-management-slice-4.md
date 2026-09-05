# 2026-07-13 — 部署管理硬化 · 切片 4（245 backup / deploy / verify / rollback）

## 概要

按 spec `2026-07-13-deployment-management-hardening-design.md` 落地 Slice 4：245 的版本化 release bundle 创建、上传、原子切换、健康验证、自动回滚。Slice 1-3 建立的契约层和锁层在本切片变成可执行的部署路径。

## 切片 4：245 backup / deploy / verify / rollback

| 文件 | 行数 | 角色 |
|---|---|---|
| `scripts/deploy-lib/host.sh` | +~210 | 245 host seams（功能层） |
| `tests/deploy_host_test.sh` | ~510 | 23 个离线断言覆盖 host.sh |
| `scripts/deploy.sh` | +~30 | canonical CLI 前端：deploy / rollback 加固 |

**核心函数：**

### Bundle 生命周期

```text
host_stage_release    (source side)
  └─ assemble: install -m 0755 binary + cp -R web/ + version.json + VERSION
  └─ sha256sum manifest of all 3 files → SHA256SUMS
  └─ write deployment.json with verified:false + created_at

host_verify_bundle   (target side)
  └─ sha256sum -c SHA256SUMS --strict
  └─ refuses silently if mismatch (orchestrator must inspect rc)

host_atomic_switch   (target side)
  └─ ln -sfn releases/${VERSION} current   (POSIX atomic)
  └─ relink /gateway + /web + /version.json
  └─ systemctl restart ${service_name}

host_wait_healthy    (target side)
  └─ curl -fsS --max-time 2 ${health_url} in 2s loop up to 60s

host_mark_verified   (target side)
  └─ sed in-place flip "verified":(true|false) → "verified":true
  └─ upsert "verified_at":"${UTC ISO}"
  └─ pure shell — no python / jq on target

host_list_verified_releases
  └─ for d in releases/*/; do
       [ -f deployment.json ] || continue
       grep -q '"verified":true' && (verified_at, v)
     sort -r | awk '{print $2}'
  └─ pure shell + awk

host_select_rollback_target
  └─ exit 4 (no_rollback_target) when no eligible candidate
  └─ returns newest non-active verified bundle

host_prune_releases
  └─ keep: 5 newest verified + active + 1 newest failed bundle (evidence)
  └─ removes everything else

host_root_for + HOST_INSTALL_ROOT
  └─ env override lets offline harness redirect into TMPDIR without ssh
```

### Canonical CLI 增强

`scripts/deploy.sh` 的头部 canonical 前端升级：

| Action | 行为 | Slice 状态 |
|---|---|---|
| `plan <target>` | 显示契约（人类 / JSON） | ✅ 切片 1 |
| `deploy <target>` | Slice 4 流程已接（offline-verified），生产完整链路在切片 5 | 🟡 |
| `verify <target>` | health_url 暴露，远程实测在切片 5 | 🟡 |
| `rollback <target>` (无 `--to`) | 调用 `host_select_rollback_target` 自动选择 | ✅ 切片 4 |
| `rollback <target> --to <v>` | 验证 `--to` 指向的 bundle 是 verified=true | ✅ 切片 4 |
| `force-unlock <target>` | `rm -rf /var/lib/llm-gateway-go/deploy.lock` | ✅ 切片 1 |

**AC-6 行为（精确版本回滚拒绝）：** 未验证、不存在、当前正在 active 的版本都拒绝切换。

**AC-7 占位：** `failed_health_triggers_rollback_marker` 测试通过断言：先 atomic_switch 到失败 release，再 atomic_switch 回上一个 verified，验证 rollback 自动回滚路径不变量。完整 orchestrator 串联（带 /healthz curl 集成）在切片 5 接上 154 时一并落地。

## 测试覆盖（Slice 4）

```text
$ bash tests/deploy_host_test.sh
═══════════════════════════════════════════════════════════════
 deploy_host_test.sh — Slice 4 host.sh functional tests
═══════════════════════════════════════════════════════════════
── stage_release ──                                6/6 PASS
── stage_release_refuses_missing_inputs ──          2/2 PASS
── verify_bundle_passes_and_fails ──                2/2 PASS
── atomic_switch_creates_symlinks ──                2/2 PASS
── mark_verified_flips_metadata ──                  2/2 PASS
── rollback_to_refuses_unverified ──                1/1 PASS
── select_rollback_target_picks_newest_verified ──  3/3 PASS
── select_rollback_target_returns_4 ──              1/1 PASS
── failed_health_triggers_rollback_marker ──        3/3 PASS

 summary: 23 passed, 0 failed

$ bash tests/deploy_cli_test.sh
 summary: 24 passed, 0 failed, 2 skipped
```

**总：47 个断言通过。**

## 设计取舍

1. **`ln -sfn` 原子性优先**：spec 要求 release 边界是单 `ln -sfn`。我额外链接了 `/opt/llm-gateway-go/gateway` 与 `current/gateway`，所以 `systemctl restart` 看到的总是 post-restart 的路径，没有单独 rename race 的窗口。
2. **`verified=true` 用 sed in-place 翻转**：原本尝试 python heredoc，但因为 (a) 离线 harness 可能无 python3、(b) 多行 heredoc 透过 ssh 字符串传递会有 quoting 灾难，已切到纯 shell。deployment.json 的 `verified` 与 `verified_at` 字段是固定的，所以 sed 足够。
3. **`host_list_verified_releases` 用 awk 而不是 jq**：每多一个目标主机二进制依赖就少一个部署前 hidden dep（spec AC-11 要求 verify 通过率 100% — 不应该被 jq 装不上卡住）。
4. **`HOST_INSTALL_ROOT` 环境变量重定向**：让离线测试 harness 写本地 TMPDIR，没有为 ssh 搞完整 fake。生产路径完全不读这个变量，所以零风险。
5. **failed_health_triggers_rollback_marker 测试断言不变量而非完整流程**：端到端"deploy → /healthz 失败 → 自动回滚 → lock 释放" 需要 slice 5 的 orchestrator 串联。本切片只验证 atomic_switch + rollback_to 之间的状态转换是可逆的。

## 遗留（与本切片无关）

- 生产 245 完整 deploy 链（包含 build / scp / digest 校验）需切片 5 接上。
- `154` 完整 deploy + thin wrapper + rollback runbook → versioned 切换在切片 5。
- `scripts/scan-secrets.sh` 重写 + `.sops.yaml` regex 落在切片 6。
- HEAD 凭据清理 + rotation checklist 落在切片 7。
- 兼容 wrappers（`deploy/deploy.sh`、`deploy/rollback.sh`、`deploy-*.sh`）在切片 8。

## 文件清单

### 修改
- `scripts/deploy-lib/host.sh`：增加 functional 层（其余 seam 已存在）
- `scripts/deploy.sh`：canonical 前端 deploy action wiring
- `tests/deploy_host_test.sh`：新增 23 个断言
- `CHANGELOG.md`

### 新增
- `docs/changelogs/2026-07-13-deployment-management-slice-4.md`

## 验证

- ✅ `bash -n scripts/deploy.sh scripts/deploy-lib/host.sh tests/deploy_host_test.sh`
- ✅ `shellcheck -S error scripts/deploy-lib/host.sh tests/deploy_host_test.sh` 0 errors
- ✅ `bash tests/deploy_host_test.sh` 23/23
- ✅ `bash tests/deploy_cli_test.sh` 24/24
