# 安全审计工具集

本目录包含 glowing-tiger 项目的安全审计工具和报告。

---

## 📋 工具清单

### 1. `audit.sh` - 运行时安全测试

**功能**：测试运行中服务的安全特性

**测试内容**：
- Ed25519 签名验证（伪造签名、过期 timestamp、重放 nonce）
- JWT 认证（无 token、伪造 token、过期 token）
- SQL 注入防护
- 路径穿越防护
- 速率限制（可选）
- 敏感信息泄露

**使用方法**：
```bash
# 确保服务已启动
cd cmd/license-authority && go run . &
cd cmd/gateway && go run . &

# 运行测试
./tests/security/audit.sh
```

**输出**：
- 控制台：彩色测试结果
- `findings.md`：发现的问题记录

---

### 2. `code_review.sh` - 静态代码安全审查

**功能**：扫描源代码中的安全问题

**检查内容**：
- SQL 注入风险（字符串拼接 SQL）
- 路径穿越风险（缺少路径验证）
- 敏感信息泄露（日志中的 token/password）
- 加密算法（弱加密 MD5/SHA1）
- 命令注入（exec.Command 使用 shell）
- 随机数生成（math/rand vs crypto/rand）
- 并发安全（无锁 map 操作）

**使用方法**：
```bash
./tests/security/code_review.sh
```

**输出**：
- 控制台：问题统计和详细信息
- `code_review_report.md`：完整的审查报告

---

## 📊 报告文件

### `findings.md`
审计发现汇总，包含：
- 问题分类（HIGH/MEDIUM/LOW）
- 修复优先级
- 行动计划

### `code_review_report.md`
代码审查详细报告，包含：
- 问题统计
- 每个问题的文件位置和行号
- 问题描述和代码片段

---

## 🚀 快速开始

```bash
# 1. 运行代码审查（不需要启动服务）
cd /path/to/glowing-tiger
./tests/security/code_review.sh

# 2. 查看报告
cat tests/security/code_review_report.md

# 3. 运行时测试（需要服务运行）
./tests/security/audit.sh

# 4. 查看发现
cat tests/security/findings.md
```

---

## 🔧 可选工具

### gosec - Go 安全静态分析

**安装**：
```bash
go install github.com/securego/gosec/v2/cmd/gosec@latest
```

**使用**：
```bash
# 扫描整个项目
gosec -fmt json -out tests/security/gosec-report.json ./...

# 只扫描特定目录
gosec ./cmd/... ./middleware/...

# 查看报告
cat tests/security/gosec-report.json | jq
```

---

## 📚 相关文档

- [安全特性与威胁模型](../../docs/SECURITY_FEATURES.md) - 详细的安全特性说明
- [安全政策](../../SECURITY.md) - 漏洞报告流程
- [审计实施总结](../../SECURITY_AUDIT_IMPLEMENTATION_SUMMARY.md) - 本次审计的完整报告

---

## ⚠️ 注意事项

1. **运行环境**：
   - `audit.sh` 需要服务运行
   - `code_review.sh` 可以离线运行

2. **性能影响**：
   - 代码审查可能需要 1-2 分钟
   - 运行时测试不会影响生产环境

3. **定期执行**：
   - 建议每次发版前运行
   - 每月执行一次完整审计

4. **CI/CD 集成**：
   ```yaml
   # GitHub Actions 示例
   - name: Security Audit
     run: |
       ./tests/security/code_review.sh
       gosec -fmt json ./...
   ```

---

## 🐛 问题反馈

如发现安全问题，请发送邮件至：security@internal.example.com

⚠️ **请勿在公开 Issue 中披露安全漏洞**
