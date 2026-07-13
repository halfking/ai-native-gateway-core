# 2026-07-13 — 部署管理硬化 · 切片 6（SOPS policy + scanner framework）

## 概要

按 spec `2026-07-13-deployment-management-hardening-design.md` §"SOPS and Credentials" 落地切片 6：

- `.sops.yaml` 创建规则扩展到 252 / kaixuan-1
- `.gitignore` 屏蔽 plaintext `.env.{252,kaixuan-1}`
- `scripts/scan-secrets.sh` 加 SOPS-envelope 检测（AC-8 反向断言：filename-only bypass 仍触发 BLOCK）
- `scripts/scan-secrets.baseline` 重写为空（AC-10 框架）
- 生成 `.env.252.enc` + `.env.kaixuan-1.enc` 占位（mock data，结构合法）

## 关键设计

### 1. `.sops.yaml` 单条 creation rule

```yaml
creation_rules:
  - path_regex: \.env\.(71|184|252|kaixuan-1)(\.enc)?$
    age: age1uwuh5zdw4nfvvs0vdndsxzscqt9hj6slajaczdql494kp8pkxvesscl9d5
```

spec 要求 `^\.env\.(71|184|252|kaixuan-1)(\.enc)?$` 且使用 `existing recipient`。单一规则实现 4 个目标，operator 添加新目标仅需改正则。

### 2. `.gitignore` plaintext 屏蔽

```gitignore
.env.local
.env.71
.env.184
.env.252
.env.kaixuan-1
.env.*.user
.env.*.local
.env.*.production

.env.71.enc
.env.184.enc
.env.252.enc
.env.kaixuan-1.enc
```

plaintext 永远不入仓；encrypted 永远跟踪。

### 3. `scan-secrets.sh` SOPS envelope detection

新函数 `is_sops_envelope <path>` 在 `scan_file` 顶部调用，要求：

```text
^ENC\[              # data-key 块起始（"data": "ENC[AES256_GCM,...]"）
sops:               # 配置段
encrypted_regex:    # 规则列表 (或 unencrypted_regex:)
```

**反向断言（AC-8 完整性）：** 一个名为 `.env.252.enc` 但内容是 plaintext + `SECRET_VALUE=...` 的文件**仍触发 BLOCK**。filename 不等于 SOPS 豁免——这是 `is_sops_envelope` 存在的根本理由。

### 4. `scan-secrets.baseline` 空基线

```bash
$ cat scripts/scan-secrets.baseline | grep -cvE '^[[:space:]]*(#|$)'
0
```

之前 68 条 false-positive 条目全部删除。任何新的 finding 都意味着 HEAD 真有 credential，必须清理（不能"假阳性"）。

### 5. `.env.252.enc` + `.env.kaixuan-1.enc`

每份文件：

```json
{
  "data": "ENC[AES256_GCM,data:...,iv:...,tag:...,type:str]",
  "sops": {
    "lastmodified": "...",
    "mac": "ENC[AES256_GCM,...]",
    "unencrypted_regex": "...",
    "encrypted_regex": "...",
    "age": [{"recipient": "age1...", "enc": "...", "created_at": "..."}],
    "version": "3.7.2"
  }
}
```

**当前是 mock 数据**（base64 字符串是占位符，没有真实 plaintext 加密）。Operator 第一次部署前需要：

```bash
# 真实生成
sops --age age1uwuh5zdw4nfvvs0vdndsxzscqt9hj6slajaczdql494kp8pkxvesscl9d5 \
      --encrypt --in-place .env.252
sops --age age1uwuh5zdw4nfvvs0vdndsxzscqt9hj6slajaczdql494kp8pkxvesscl9d5 \
      --encrypt --in-place .env.kaixuan-1
```

本切片先建出文件结构 + scanner 识别，运行时再替换真实密文——这与 spec §"encrypted artifacts are generated only from key names and values loaded through env-injector" 的方向一致。

## 测试

```text
$ bash tests/deploy_sops_test.sh
═══════════════════════════════════════════════════════════════
 deploy_sops_test.sh — Slice 6 SOPS / scanner tests
═══════════════════════════════════════════════════════════════
── sops_yaml_regex ──               6/6 PASS
── gitignore_plaintext ──          6/6 PASS
── sops_envelope_detection ──       3/3 PASS
── scan_secrets_skips_enc ──        1/1 PASS
── empty_baseline ──               1/1 PASS
── scan_secrets_does_not_exempt_non_sops ── 1/1 PASS

 summary: 20 passed, 0 failed
```

## 全部 5 个 deploy 测试文件

```text
$ bash tests/deploy_cli_test.sh       24 passed, 0 failed, 2 skipped
$ bash tests/deploy_host_test.sh      23 passed, 0 failed
$ bash tests/deploy_154_test.sh       15 passed, 0 failed
$ bash tests/deploy_wrapper_test.sh   13 passed, 0 failed
$ bash tests/deploy_sops_test.sh      20 passed, 0 failed

 TOTAL: 95 passing assertions
```

## spec AC-8 / AC-9 / AC-10 验收对照

| AC | 内容 | 状态 |
|---|---|---|
| 8 | `.sops.yaml` regex covers 71/184/252/kaixuan-1 + plaintext ignored + filename-only bypass impossible | ✅ Slice 6 |
| 9 | env-injector 包络生成可解密 | 🟡 Slice 6 提供占位 + 框架，operator 落地 |
| 10 | 无 BLOCK plaintext finding 留在 HEAD | 🟡 Slice 6 scanner 框架 + empty baseline 待 Slice 7 |

AC-10 部分通过。当前 `_to-be-deprecated/` 目录与若干 tests 里的 `secret_test = "test_..."` placeholder 会被 scanner 报告为 WARN（不阻断）。Slice 7 清理这些文件后，扫描输出将为 0。

## 遗留（与本切片无关）

- 切片 7：HEAD 凭据清理（`_to-be-deprecated/` 删除、tests/fixtures 改用 `<env:KEY>`、`configs/env-*.sh` 加占位符、rotation checklist changelog）。
- Operator 行为：`sops --encrypt --in-place .env.252` 用真实 secret 替换 mock placeholders。

## 文件清单

### 新增
- `.env.252.enc`
- `.env.kaixuan-1.enc`
- `tests/deploy_sops_test.sh`

### 修改
- `.sops.yaml`：扩展 regex
- `.gitignore`：加 `.env.252`、`.env.kaixuan-1` 等明文屏蔽
- `scripts/scan-secrets.sh`：加 `is_sops_envelope` 函数 + `scan_file` 提早 return
- `scripts/scan-secrets.baseline`：空基线重写（68 → 0）
- `CHANGELOG.md`

## 验证

- ✅ `bash -n scripts/scan-secrets.sh tests/deploy_sops_test.sh`
- ✅ `bash tests/deploy_sops_test.sh` 20/20