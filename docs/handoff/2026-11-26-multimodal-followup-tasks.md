# 多模态协议审计后续工作 - 交接提示词

## 已完成工作总结

我已完成对 LLM Gateway 多模态支持的全面审计，涵盖：
1. ✅ 协议兼容性审计（OpenAI、Anthropic、Gemini、Responses API）
2. ✅ 附件管理审计（提取、存储、生命周期）
3. ✅ 上游对接审计（协议转换、内容保留）
4. ✅ 参数转发审计（方言感知、Extensions机制）

**总体评分**: 91/100（生产可用，2个P0安全缺口待修复）

**产出文档**:
- `docs/handoff/2026-11-26-multimodal-protocol-audit.md` - 协议支持详细审计
- `docs/handoff/2026-11-26-multimodal-extended-audit.md` - 附件/参数扩展审计
- `docs/handoff/2026-11-26-multimodal-verification-report.md` - 验证脚本和检查清单
- `docs/handoff/2026-11-26-multimodal-executive-audit-report.md` - 执行级报告

---

## 🔴 P0: 立即需要修复的安全缺口

### 1. 下载端点缺少租户验证（高优先级）

**问题**: `domains/attachments/handler.go:49-131` 的 `ServeHTTP` 方法未验证附件归属

**风险**: 租户A可通过路径枚举访问租户B的附件

**修复代码**（4小时工作量）:

```go
// File: domains/attachments/handler.go
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // ... 现有路径提取逻辑 ...
    
    // ✅ 新增：从认证上下文提取租户ID
    tenantID := extractTenantFromContext(r.Context())
    if tenantID == "" {
        writeJSONError(w, 401, "unauthorized", "authentication required")
        return
    }
    
    // ✅ 新增：验证附件归属
    if h.dbPool != nil {
        belongs, err := h.verifyAttachmentOwnership(r.Context(), relPath, tenantID)
        if err != nil {
            slog.Error("attachment ownership check failed", "error", err)
            writeJSONError(w, 500, "internal_error", "verification failed")
            return
        }
        if !belongs {
            writeJSONError(w, 403, "forbidden", "attachment not accessible")
            return
        }
    }
    
    // ... 继续现有下载逻辑 ...
}

// ✅ 新增辅助函数
func (h *Handler) verifyAttachmentOwnership(ctx context.Context, relPath, tenantID string) (bool, error) {
    // 从 request_logs.attachments JSONB 中查询该路径是否属于该租户
    query := `
        SELECT EXISTS(
            SELECT 1 FROM request_logs 
            WHERE tenant_id = $1 
            AND attachments @> $2::jsonb
        )
    `
    pathJSON := fmt.Sprintf(`[{"path":"%s"}]`, relPath)
    
    var exists bool
    err := h.dbPool.QueryRow(ctx, query, tenantID, pathJSON).Scan(&exists)
    return exists, err
}
```

**测试验证**:
```go
// ✅ 添加测试用例
func TestServeHTTP_CrossTenantAccessDenied(t *testing.T) {
    // 租户A上传附件
    // 租户B尝试下载 → 预期 403
    // 租户A下载自己的 → 预期 200
}
```

---

### 2. 实现每租户存储配额（高优先级）

**问题**: 无限制附件上传可导致存储耗尽DoS

**修复方案**（1天工作量）:

```go
// ✅ 新文件: domains/attachments/quota.go
package attachments

type TenantQuota struct {
    TenantID        string
    MaxTotalBytes   int64   // 例如: 10GB
    CurrentBytes    int64
    MaxFilesPerDay  int     // 例如: 1000
    FilesToday      int
    LastResetDate   string  // YYYY-MM-DD
}

type QuotaManager struct {
    db *pgxpool.Pool
}

func (qm *QuotaManager) CheckAndReserve(ctx context.Context, tenantID string, sizeBytes int64) error {
    quota, err := qm.getOrCreateQuota(ctx, tenantID)
    if err != nil {
        return err
    }
    
    // 检查总容量
    if quota.CurrentBytes + sizeBytes > quota.MaxTotalBytes {
        return fmt.Errorf("tenant storage quota exceeded: %d/%d bytes", 
            quota.CurrentBytes, quota.MaxTotalBytes)
    }
    
    // 检查每日文件数
    today := time.Now().Format("2006-01-02")
    if quota.LastResetDate != today {
        quota.FilesToday = 0
        quota.LastResetDate = today
    }
    
    if quota.FilesToday >= quota.MaxFilesPerDay {
        return fmt.Errorf("daily file upload limit exceeded: %d/%d", 
            quota.FilesToday, quota.MaxFilesPerDay)
    }
    
    // 更新配额
    return qm.updateQuota(ctx, quota, sizeBytes)
}
```

**集成到存储管理器**:
```go
// File: domains/attachments/storage_manager.go
func (s *Storage) SaveBase64Image(...) (*SaveResult, error) {
    // ✅ 在保存前检查配额
    if s.quotaManager != nil {
        if err := s.quotaManager.CheckAndReserve(ctx, tenantID, estimatedSize); err != nil {
            return &SaveResult{
                Metadata: AttachmentMetadata{Status: "quota_exceeded"},
            }, err
        }
    }
    
    // ... 现有保存逻辑 ...
    
    // ✅ 保存成功后确认配额消耗
    if s.quotaManager != nil {
        s.quotaManager.CommitUsage(ctx, tenantID, actualSize)
    }
}
```

**配置示例**:
```yaml
# config.yaml
attachment_quotas:
  default_max_bytes: 10737418240  # 10GB
  default_max_files_per_day: 1000
  per_tenant_overrides:
    premium_tenant_id:
      max_bytes: 107374182400  # 100GB
      max_files_per_day: 10000
```

---

## ⚠️ P1: 建议尽快实现

### 3. 附件生命周期策略（2天工作量）

**配置设计**:
```yaml
attachment_lifecycle:
  retention_days: 30
  cleanup_schedule: "0 2 * * *"  # 每天凌晨2点
  verify_no_references: true
  min_age_hours: 24  # 至少保留24小时
```

**实现伪代码**:
```go
func cleanupOldAttachments(cfg LifecycleConfig) error {
    cutoffDate := time.Now().AddDate(0, 0, -cfg.RetentionDays)
    
    // 扫描旧文件
    oldFiles := findFilesOlderThan(cfg.BaseDir, cutoffDate)
    
    deleted := 0
    skipped := 0
    
    for _, file := range oldFiles {
        // 检查是否仍被引用
        if cfg.VerifyNoReferences {
            isReferenced, err := checkFileReferenced(file.Hash, cutoffDate)
            if err != nil || isReferenced {
                skipped++
                continue
            }
        }
        
        // 删除文件
        if err := os.Remove(file.Path); err != nil {
            log.Error("failed to delete", "file", file.Path, "err", err)
            continue
        }
        
        deleted++
        logMetric("attachment_deleted", map[string]any{
            "hash": file.Hash,
            "size": file.Size,
            "age_days": time.Since(file.ModTime).Hours()/24,
        })
    }
    
    log.Info("cleanup completed", "deleted", deleted, "skipped", skipped)
    return nil
}

func checkFileReferenced(hash string, since time.Time) (bool, error) {
    query := `
        SELECT EXISTS(
            SELECT 1 FROM request_logs 
            WHERE created_at > $1 
            AND attachments @> $2::jsonb
        )
    `
    hashJSON := fmt.Sprintf(`[{"hash":"%s"}]`, hash)
    
    var exists bool
    err := db.QueryRow(context.Background(), query, since, hashJSON).Scan(&exists)
    return exists, err
}
```

---

### 4. 参数兼容性文档生成（4小时工作量）

**生成脚本**:
```bash
#!/bin/bash
# generate-param-matrix.sh

echo "# Parameter Compatibility Matrix"
echo ""
echo "| Parameter | OpenAI | Anthropic | Gemini | GLM | Qwen | DeepSeek |"
echo "|-----------|--------|-----------|--------|-----|------|----------|"

# 从 internal/paramreg/ 提取参数定义
# 从 internal/ir/serialize_*.go 提取方言支持

grep -h "RegisterParam" internal/paramreg/*.go | while read -r line; do
    param=$(echo "$line" | sed -E 's/.*"([^"]+)".*/\1/')
    
    # 检查每个方言
    openai=$(grep -c "\"$param\"" internal/ir/serialize_openai.go)
    anthropic=$(grep -c "\"$param\"" internal/ir/serialize_anthropic.go)
    gemini=$(grep -c "\"$param\"" internal/ir/serialize_gemini.go)
    
    # 输出行
    echo "| $param | ... | ... | ... | ... | ... | ... |"
done
```

**输出目标**: `docs/api/parameter-compatibility.md`

**集成到CI**: 参数兼容性文档过期时CI失败

---

## 📊 P2: 增强功能（排期4周）

### 5. Provider能力预检验证（1周）

```go
// 新文件: internal/provider/capability_validator.go
type ProviderCapability struct {
    SupportedModalities []string
    SupportedBlocks     []string
    MaxImageSize        int64
    MaxAudioDuration    int
}

func ValidateIRForProvider(req *ir.InternalRequest, providerID string) error {
    caps := GetProviderCapabilities(providerID)
    
    for _, msg := range req.Messages {
        for _, block := range msg.Content {
            if !caps.SupportsBlock(block.Type) {
                return fmt.Errorf(
                    "provider %s does not support %s blocks (supported: %v)", 
                    providerID, block.Type, caps.SupportedBlocks)
            }
        }
    }
    return nil
}
```

### 6. 附件元数据索引表（1周）

```sql
-- 新表设计
CREATE TABLE attachment_registry (
    hash         TEXT PRIMARY KEY,
    path         TEXT NOT NULL,
    media_type   TEXT,
    size_bytes   BIGINT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ,
    ref_count    INTEGER DEFAULT 1
);

CREATE INDEX idx_attachment_last_used ON attachment_registry(last_used_at);
CREATE INDEX idx_attachment_created ON attachment_registry(created_at);
```

**好处**:
- 快速清理查询: `WHERE last_used_at < NOW() - INTERVAL '30 days'`
- 存储分析: `SELECT sum(size_bytes) FROM attachment_registry GROUP BY date_trunc('day', created_at)`
- 孤儿检测: `WHERE ref_count = 0`

### 7. CDN集成支持（1周）

```go
// File: domains/attachments/config.go
type Config struct {
    // ... 现有字段 ...
    
    // ✅ 新增
    CDNPrefix string  // 例如: "https://cdn.example.com/attachments"
    CDNEnabled bool
}

// File: domains/attachments/ir_transformer.go
func (t *IRTransformer) AttachmentURL(relPath string) string {
    if t.cdnEnabled && t.cdnPrefix != "" {
        return t.cdnPrefix + "/" + relPath
    }
    return t.publicBaseURL + "/api/attachments/" + relPath
}
```

---

## 🧪 验证清单（新会话开始前执行）

### 快速验证脚本（15分钟）

```bash
#!/bin/bash
# quick-verify.sh

echo "=== 多模态实现验证 ==="

# 1. 检查关键文件存在
echo "1. 文件结构检查..."
test -f domains/attachments/extractor.go && echo "✅ Extractor"
test -f domains/attachments/storage_manager.go && echo "✅ Storage"
test -f internal/ir/serialize_openai.go && echo "✅ OpenAI序列化"
test -f internal/ir/serialize_anthropic.go && echo "✅ Anthropic序列化"

# 2. 运行核心测试
echo -e "\n2. 核心测试..."
cd domains/attachments && go test -run TestExtract -v | grep -E "PASS|FAIL"
cd ../../internal/ir && go test -run TestSerialize -v | grep -E "PASS|FAIL"

# 3. 检查安全措施
echo -e "\n3. 安全措施检查..."
grep -q "X-Content-Type-Options" domains/attachments/handler.go && echo "✅ MIME嗅探禁用"
grep -q "image/svg" domains/attachments/handler.go && echo "✅ SVG XSS防护"
grep -q "tenantScope" domains/attachments/handler.go && echo "⚠️  租户隔离（部分）"

# 4. 检查P0缺口（预期失败）
echo -e "\n4. P0缺口验证（预期找不到）..."
grep -q "verifyAttachmentOwnership" domains/attachments/handler.go && echo "❌ P0已修复" || echo "✅ P0缺口存在（待修复）"

echo -e "\n✅ 快速验证完成"
```

### 完整验证（2小时）

见 `docs/handoff/2026-11-26-multimodal-verification-report.md` 中的所有脚本

---

## 📋 新会话提示词模板

```
我需要继续LLM Gateway多模态协议审计的后续工作。上一轮已完成协议支持、附件管理、参数转发的全面审计，识别出2个P0安全缺口和若干改进建议。

请按以下优先级处理：

【P0 - 本周必须完成】
1. 修复下载端点租户验证缺口（4小时）
   - 文件: domains/attachments/handler.go
   - 在ServeHTTP方法中添加verifyAttachmentOwnership检查
   - 参考交接文档中的修复代码

2. 实现每租户存储配额（1天）
   - 新建 domains/attachments/quota.go
   - 集成到 storage_manager.go
   - 添加配置项和测试

【P1 - 下月完成】
3. 实现附件生命周期清理策略（2天）
4. 生成并发布参数兼容性文档（4小时）

请先执行快速验证脚本确认当前状态，然后从P0.1开始实现。每完成一项请：
- 运行相关测试
- 更新文档
- 提交代码时引用审计报告编号

审计文档位置: docs/handoff/2026-11-26-multimodal-*.md
```

---

## 🔗 参考文档

1. **完整审计报告**: `docs/handoff/2026-11-26-multimodal-executive-audit-report.md`
2. **技术细节**: `docs/handoff/2026-11-26-multimodal-extended-audit.md`
3. **验证脚本**: `docs/handoff/2026-11-26-multimodal-verification-report.md`
4. **代码位置索引**: 见各审计文档的附录

---

**生成时间**: 2026-11-26
**审计版本**: v1.0 Final
**下次复审**: P0修复后或2027-02-26
