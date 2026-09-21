# 2026-09-04 凭据解密事故修复 + 部署解密冒烟门禁

**环境**：245 预发布（llmgo.kxpms.cn）　**修复版本**：v1921-2e1ccfb5

## 事故概要

245 上 `/providers/587` 页面所有凭据显示「无法解析」，用户 apikey reveal 报
`invalid fernet token`；同一时刻 154（llm.kxpms.cn，与 245 共享 252 PG 同一个库）
对同一批数据解密完全正常。

## 根因

**不是密钥配置问题**——245 的 `/opt/llm-gateway-go/.env` 与 154 的
`/etc/llm-gateway-go/env` 及两边运行进程的 `/proc/<pid>/environ` 指纹完全一致
（`LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY`、`LLM_GATEWAY_SECRET_KEY` 均相同），
keyring 也都正常初始化。

真正差异在 **binary 版本**：245 还在跑 build_seq 1914 的旧构建，其
`admin.Handler.decryptCred` 把所有 `v1:legacy:` 前缀的密文一律送进 Fernet 解密
路径；而 provider 587 的 7 条凭据（id 31/33/51-59）是 kid="legacy" 的 **AES-GCM**
密文（Fernet 密文固定以 `gAAAAA` 开头，仅 credential 17 是真 Fernet），于是全部
失败 → 前端 `key_mask_error=decrypt_failed` → 显示「无法解析」。修复（commit
`8a27d7daa`：`decryptCred` 改走 `secret.DecryptAny`，GCM 优先、兼容
`v1:legacy:` 的 inline/包裹两种 Fernet 形态）当时只在 154（seq 1917）上线。

修复动作：`bash scripts/deploy-245.sh` 蓝绿部署同一构建（seq 1921），凭据 7/7
恢复、reveal 正常、公网链路 200。

## 部署链路防御（本篇重点）

事故暴露的门禁盲区：**DB 就绪 ≠ 密钥链路可用**。旧流程 healthz / readyz /
background-tasks / admin 登录全绿，解密却全挂，没有任何一步能发现。

新增 `deploy_verify_credential_decrypt`（`scripts/deploy-lib/post-deploy-verify.sh`），
并在 `deploy-seamless.sh` 蓝绿切换后插入 **step 9.2**（admin 密码同步之后、
verified 标记之前）：

1. 用目标机 env 的 admin 凭据登录（复用 gateway_ready 的登录通道）；
2. `GET /api/providers` 取 `active_credential_count` 最高的前 3 家（可用远端 env
   `LLM_GATEWAY_DECRYPT_SMOKE_PROVIDER_ID=587,...` 固定抽检对象）；
3. 逐家 `GET /api/providers/{id}/credentials` 统计 `key_mask_error`：
   - **全部失败 = 系统性 keyring/解密回归 → FAIL**，deploy-seamless 走
     `_bluegreen_abort` 自动回滚；
   - 部分失败（疑似历史脏行）或库内无凭据 → WARN 不阻断；
   - 至少一条 `key_masked` 成功 → OK。

245/154 共用该门禁（两个 wrapper `deploy-245.sh` / `deploy-154.sh` 都委托
deploy-seamless）。

## 验证

- 离线契约测试 `tests/deploy_credential_decrypt_verify_test.sh`：OK / 全败回滚 /
  部分败 WARN / 空库 WARN / env 缺失 FAIL，5/5 通过；
- 现网实测：245 `providers=18,1,587 creds=15 failed=0` → OK；154 同样 OK；
- 错误 env 路径注入 → 正确 FAIL（fail-closed）。

## 排查口诀（共库双机解密不一致）

`/proc/<pid>/environ` 密钥指纹对比 → 相同则比 `build_seq` →
`journalctl -u <unit> | grep -iE 'decrypt|fernet'` → 手工
`GET /api/providers/<id>/credentials` 看 `key_mask_error`。
