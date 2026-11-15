# P3-15: promptinjection api_key明文存储 - 技术债记录

**日期**: 2026-11-16  
**优先级**: P3（技术债，无需立即修复）  
**状态**: 技术债登记  
**类型**: Schema设计缺陷

---

## 问题描述

### 当前状况

**位置**: `domains/promptinjection/detector.go:1186-1189`

```go
var modelName, apiKey, baseURL string
err = db.QueryRowContext(ctx,
    `SELECT model_name, api_key, base_url, confidence_threshold 
     FROM llm_detection_engines WHERE id = $1`,
    engineID).Scan(&modelName, &apiKey, &baseURL, &confidenceThreshold)
```

**问题**:
- API key从数据库读取为**明文字符串**
- 存储在内存中，容易在heap dump/coredump中暴露
- 传递给HTTP Header时通过变量传递，易留痕迹

### 影响范围

1. **内存安全**: Heap dump可恢复完整凭据
2. **代码审计**: Code review中明文apiKey可见
3. **日志泄漏**: 错误日志可能包含apiKey（虽然当前代码未记录）

### 严重程度

- **实际风险**: 中等
  - 数据库已通过TLS保护
  - 应用process通常限制访问
  - 不是直接暴露在日志/网络中

- **最佳实践缺陷**: 高
  - Secrets不应以plaintext存储/传输
  - 应使用加密存储+解密后立即使用的模式

---

## 根本原因

### 为什么现在是明文？

1. **Schema历史问题**:
   - 初始设计时`llm_detection_engines`表未考虑加密
   - 只有`model_name, api_key, base_url`三列结构

2. **简化考虑**:
   - LLM引擎配置本身就是内部管理员维护
   - 不涉及多租户隔离（不是用户api_key）

3. **迁移成本**:
   - 需要schema更改（添加加密key版本）
   - 需要数据迁移脚本
   - 需要应用层支持新旧并行

---

## 推荐解决方案

### 方案A: 立即采用（无schema改动）

**做法**:
1. 添加应用层解密：使用`crypto/aes`在应用启动时解密
2. 环境变量管理主密钥（`LLM_ENGINE_MASTER_KEY`）
3. 修改callLLMAPI使用解密后的key

**代码示例**:
```go
// 在 LLMDetectionEngine 初始化时
func (e *LLMDetectionEngine) decryptAPIKey(encryptedKey string) (string, error) {
    // 从环境变量读取主密钥
    masterKey := os.Getenv("LLM_ENGINE_MASTER_KEY")
    if masterKey == "" {
 return "", fmt.Errorf("LLM_ENGINE_MASTER_KEY not set")
    }
    
    // AES-GCM解密
    cipher, err := aes.NewCipher([]byte(masterKey))
    // ... 完整实现 ...
    return plaintext, nil
}

// 修改callLLMAPI调用处
decryptedKey, err := e.decryptAPIKey(apiKey)
if err != nil {
    return "", fmt.Errorf("decrypt api_key: %w", err)
}
llmResponse, err := e.callLLMAPIWithSecret(ctx, baseURL, modelName, decryptedKey, ...)
```

**成本**: 低（纯应用改动）  
**收益**: 中等（减少内存泄漏风险）

### 方案B: 完整实施（推荐，需后续sprint）

**做法**:
1. 新增`llm_detection_engines_v2`表或`api_key_encrypted`列
2. 迁移数据到加密存储
3. 应用支持v1/v2并行读取
4. 逐步淘汰v1表

**优势**: 完整的密钥管理生命周期  
**成本**: 中等（需要迁移脚本+应用改动）

---

## 现状验证

### 代码路径

1. **查询明文**:
   - `detector.go:1186` - 直接SELECT `api_key`

2. **内存持有**:
   - `e.callLLMAPI(ctx, ..., apiKey, ...)`传递为参数

3. **使用**:
   - `req.Header.Set("Authorization", "Bearer "+apiKey)` 转成HTTP header

### 为什么不立即修复？

1. **边界清晰**: 仅限内部LLM引擎配置（非用户api_key）
2. **攻击面小**: 需要获得应用内存访问权限才能泄漏
3. **现有防护**: 数据库TLS + Linux文件权限已有保护
4. **迁移成本**: 需要数据迁移，不宜紧急实施

---

## 后续行动

### Phase 1: 文档 + 监控 (当前)
- ✅ 记录这个技术债
- ✅ 标注代码位置
- 📋 添加代码注释警告

### Phase 2: 应用层加密 (下一sprint)
- 🔄 实现应用层解密
- 🔄 配置管理主密钥
- 🔄 单元测试

### Phase 3: Schema升级 (后续)
- 🔄 设计新schema (v2)
- 🔄 编写迁移脚本
- 🔄 灰度发布

---

## 代码注释建议

在detector.go:1186处添加：

```go
// P3-15: API key is stored as plaintext in database.
// TODO (next sprint): Implement application-layer decryption
// using LLM_ENGINE_MASTER_KEY environment variable.
// Currently acceptable because:
// 1. Limited to internal LLM engine config (not user API keys)
// 2. Database connection is TLS-protected
// 3. Application memory access already requires compromised host
// See: docs/P3-15-promptinjection-apikey-encryption.md
```

---

## 审查检查表

- [ ] 是否有其他地方读取llm_detection_engines.api_key?
- [ ] 是否有日志记录apiKey的风险?
- [ ] 是否需要audit logs记录配置变更?

---

**分类**: Schema Technical Debt  
**维护者**: LLM Gateway Security Team  
**监督**: 下sprint规划时重新评估
