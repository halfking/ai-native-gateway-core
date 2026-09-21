# v1:legacy Fernet session-ping 修复审计

- **审计日期**：2026-09-01（+08:00）
- **基线**：`ca2b59f227fdc712de06bdaa7ab44fea7c8e2114`
- **修复分支**：`fix/admin-v1-legacy-fernet-session-ping`
- **范围**：admin credential 解密与回归测试
- **证据等级**：代码级 + 本地单元测试；真实 154 provider 验证为 BLOCKED

## 执行摘要

245/252 只读核验显示：`130dao · apiclaude` 为 credential 17，密文是 `v1_legacy_fernet`；`hzx-2 · minimax` 为 credential 42，虽同为 legacy Fernet 但可正常 ping。245 当前加密环境指纹与 `.env` 一致，252 PostgreSQL 正常，credential 17 未在故障窗口被更新。

根因是 `admin.Handler.decryptCred` 对所有 `v1:` 前缀直接调用 AES-GCM 解密，没有复用 `secret.DecryptAny` 已有的 `v1:legacy:` Fernet fallback。因此历史 `v1:legacy:<Fernet token>` 在 admin session-ping 中返回 `decrypt credential failed`，而正常 provider 路径可以解密。

## 变更

1. `admin/handler.go`
   - 在 `decryptCred` 顶部增加对 `v1:legacy:` 的 Fernet 分支，使用同一 `h.encKey` 解码失败时返回原 `decryptFernet` 错误。
   - 普通 AES-GCM 分支保留原 `DecryptAESGCM` 调用，使 AES 错误仍能透出 `secret.ErrDecrypt`。
   - 不修改数据库、密钥、密文、部署或服务状态。
2. `admin/handler_cred_encrypt_test.go`
   - 新增 `TestDecryptCred_V1LegacyFernetEnvelope`：用同时配置 keyring 与 Fernet key 的 Handler，验证 `v1:legacy:` Fernet envelope 可解密且 `isLegacy=true`。
   - 新增 `TestDecryptCred_V1AESGCMRetainsDecryptError`：用错误 keyring 加密、再用正确 kid 但 key 不同的 keyring 解密，断言返回 `errors.Is(err, secret.ErrDecrypt)==true`，避免将 AES 错误降级为 `unknown_format`。

## 验证结果

通过：

- `go test -count=1 ./admin -run 'Test(EncryptDecryptRoundTrip_FernetPath|EncryptDecryptRoundTrip_KeyringPath|DecryptCred_V1LegacyFernetEnvelope|DecryptCred_RejectsRawBinary|EncryptCred_NoKeyringNoEncKey|DecryptCred_NoKeyringNoEncKey_V1Envelope)$'`
- `go test -count=1 ./secret`
- `go vet ./admin ./secret`
- `git diff --check`

阻断/基线失败：

- `go test -count=1 ./admin`：已有 `TestHandleTrigger_ProbeEnqueueError` 断言期望 200，但当前行为返回 503 `db down`；失败与本次两文件改动无关，未修改该测试或其生产逻辑。
- 154 真实实例核验：被本机缺少 `/Users/xutaohuang/config/ssh-wrapper-154.env` 的 SSOT contract 阻断；未绕过保护。
- 245/252 真实修复生效验证：未执行部署或重启，标记为 `manual_required`。

## 安全与回归审查

- 未读取、记录或提交任何 API key、密码、DSN、密文内容或 keyring 明文。
- 当前仓库主工作区存在其他协作者的 staged/unstaged/untracked 修改；本修复在独立 worktree 完成，仅提交白名单文件与本审计报告。
- 选择窄分支修复（仅在 `v1:legacy:` 显式前缀处增加 Fernet fallback），不切换到 `secret.DecryptAny` 通用委托，避免将原 `secret.ErrDecrypt` 错误语义降级为 `secret.ErrUnknownFormat`。
- 不修改 `IsV1Envelope`，避免影响其他 `DecryptAny` 调用者。
- 不执行 credential 重加密；修复仅恢复既有兼容读取能力。

## 未验证项与后续动作

- 合并后需按 245/154 部署流程发布并重启实例，再分别对 credential 17 的 `claude-sonnet-5` 与 credential 42 的 `minimax-m3` 执行真实 ping。
- 245 的本机 nginx `/healthz` 仍曾返回 502，是独立反代问题，需单独处理。
- credential 17 的历史密文应在修复上线后考虑按现行策略做单条 lazy re-encryption；本次不执行写操作。

## 回滚

回滚本修复只需恢复 `admin/handler.go` 与对应测试文件的本次提交；不涉及数据库或凭据数据变更。
