# 安全审计发现记录

**生成时间**: 2026-07-12  
**审计工具**: `tests/security/code_review.sh`

---

## 🔴 HIGH 级别问题汇总

### 1. SQL 注入风险（86 个）

**描述**: 发现 86 处使用 `fmt.Sprintf` 构造 SQL 查询的代码，可能存在 SQL 注入风险。

**示例位置**:
- `admin/dashboardapi/session_active.go:77`
- `admin/session_analytics_tasks.go:117`
- `admin/data_lifecycle_storage.go:625`
- `admin/pricing.go:336, 496, 645, 834`
- `admin/routing.go:1383`
- 等等...

**分析**:

经过进一步检查，这些 SQL 查询大部分是 **动态构造 WHERE 子句**，但使用了 **参数化传递** 实际值：

```go
// 示例：admin/dashboardapi/session_active.go:77
countQuery := fmt.Sprintf("SELECT COUNT(*) FROM session_summaries %s", whereClause)
// whereClause 是字符串拼接，但实际值通过 args... 参数传递
if err := h.db.QueryRow(ctx, countQuery, args...).Scan(&totalActive); err != nil {
```

**风险评估**:
- ✅ **实际参数化传递**：值通过 `args...` 传递，使用占位符 `$1, $2` 等
- ⚠️ **WHERE 子句拼接**：WHERE 子句本身是字符串拼接（如 `"WHERE status = $1 AND ..."`）
- ⚠️ **表名/列名动态**：部分代码中表名或列名可能来自用户输入（需进一步审查）

**修复优先级**: 🟡 MEDIUM
- 大部分代码已使用参数化查询
- 需要人工审查表名/列名是否来自可信源
- 建议重构为查询构建器模式（如 `squirrel`）

**建议修复**:
1. 添加表名/列名白名单验证
2. 使用查询构建器（如 `github.com/Masterminds/squirrel`）
3. 添加单元测试覆盖 SQL 注入场景

---

## 🟡 MEDIUM 级别问题汇总

### 1. 路径穿越防护（15 处）

**描述**: `autoupdate/` 和 `installer/` 中的文件操作缺少路径验证。

**位置**:
- `autoupdate/downloader.go`
- `autoupdate/installer.go`
- `autoupdate/rollback.go`

**当前防护**:
- ✅ 使用 `filepath.Join` 限制在指定目录
- ✅ SHA256 校验和验证
- ❌ 缺少 `filepath.Clean` 和 `../` 检测

**修复建议**:
```go
// 在文件操作前添加路径验证
func validatePath(baseDir, targetPath string) error {
    cleanPath := filepath.Clean(targetPath)
    if strings.Contains(cleanPath, "..") {
        return fmt.Errorf("path traversal detected")
    }
    absBase, _ := filepath.Abs(baseDir)
    absTarget, _ := filepath.Abs(cleanPath)
    if !strings.HasPrefix(absTarget, absBase) {
        return fmt.Errorf("path outside base directory")
    }
    return nil
}
```

**修复优先级**: 🟡 MEDIUM

---

### 2. 敏感信息泄露（100+ 处）

**描述**: 日志中可能包含敏感字段（token/password/secret）。

**风险评估**:
- ✅ 大部分日志只打印字段名，不打印值
- ⚠️ 部分 `fmt.Printf` 需要人工审查

**修复建议**:
- 使用结构化日志（`slog`）
- 敏感字段使用占位符（如 `token: ***`）
- 添加日志脱敏中间件

**修复优先级**: 🟢 LOW

---

## 🟢 LOW 级别问题汇总

### 1. 并发安全（53 处）

**描述**: 部分 map 操作可能缺少 mutex 保护。

**当前状态**:
- 已使用 `sync.Map` 或 `sync.RWMutex` 的模块较多
- 需要人工审查剩余 map 操作

**修复优先级**: 🟢 LOW（需审查但非紧急）

---

## ✅ 通过的检查

1. **JWT 认证**: ✅ 使用 Ed25519 签名，7 天 TTL
2. **Ed25519 签名**: ✅ 防伪造、防重放、时钟偏移检查
3. **Nonce 防重放**: ✅ 5 分钟 TTL，Redis 分布式存储
4. **加密算法**: ✅ 未发现 MD5/SHA1 弱加密
5. **随机数生成**: ✅ 安全相关随机数使用 `crypto/rand`
6. **命令注入**: ✅ 未发现通过 shell 执行命令

---

## 📋 修复优先级

| 优先级 | 类别 | 数量 | 截止日期 |
|--------|------|------|---------|
| 🔴 P0 | 无 | 0 | - |
| 🟡 P1 | SQL 表名/列名验证 | 86 | 2 周内 |
| 🟡 P2 | 路径穿越防护 | 15 | 2 周内 |
| 🟢 P3 | 敏感信息脱敏 | 100+ | 1 月内 |
| 🟢 P4 | 并发安全审查 | 53 | 2 月内 |

---

## 🔧 下一步行动

### 立即行动（本周）
1. ✅ 运行 `tests/security/audit.sh` 验证运行时安全
2. ✅ 运行 `tests/security/code_review.sh` 生成报告
3. ⚠️ 安装 `gosec` 进行静态分析
   ```bash
   go install github.com/securego/gosec/v2/cmd/gosec@latest
   gosec -fmt json -out tests/security/gosec-report.json ./...
   ```

### 短期修复（2 周内）
1. 在 `admin/` 中添加 SQL 表名/列名白名单验证
2. 在 `autoupdate/` 中添加路径穿越防护函数
3. 编写单元测试覆盖安全场景

### 长期优化（1-2 月）
1. 重构 SQL 查询构建为查询构建器模式
2. 添加日志脱敏中间件
3. 增加并发安全测试覆盖

---

## 📚 参考

- OWASP Top 10: https://owasp.org/www-project-top-ten/
- Go Security Checklist: https://github.com/OWASP/Go-SCP
- 代码审查报告: `tests/security/code_review_report.md`
