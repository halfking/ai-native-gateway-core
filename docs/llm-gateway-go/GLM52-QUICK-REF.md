# GLM-5.2 问题快速参考卡片

## 🎯 一句话总结
**GLM-5.2 格式转换逻辑正确，需要实际测试验证上游行为**

## ✅ 已完成
- [x] 代码审查完成
- [x] 15 个单元测试全部通过
- [x] 诊断工具就绪
- [x] 3 个修复方案准备好

## ⏳ 需要用户做的事

### 1. 提供测试凭据
```bash
export GLM_API_KEY="your-actual-key"
```

### 2. 运行诊断（3 分钟）
```bash
cd __LOCAL_PATH_1__
./scripts/diagnose-glm52.sh -v
```

### 3. 描述具体现象
- 什么时候发生？（每次 / 偶尔）
- 什么样的混乱？（空响应 / 格式错误 / 客户端崩溃）
- 使用什么客户端？（curl / SDK / UI）

## 📊 测试结果

```
✅ 请求转换 (OpenAI → Anthropic)     7/7 通过
✅ 响应转换 (Anthropic → OpenAI)     2/2 通过
✅ 事件检测 (混合格式防护)           5/5 通过
✅ 端到端转换                        1/1 通过
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Total:                              15/15 通过 (100%)
```

## 🔧 如果确认有问题

### 快速修复（推荐）
应用字符串粗筛，部署到 71：

```bash
# 在 relay/anthropic_to_openai_stream.go:292 前添加
if strings.Contains(string(data), `"choices"`) {
    slog.Warn("dropping OpenAI format data")
    continue
}
```

### 监控指标
添加 metrics：
- `dropped_non_anthropic_events_total`
- `empty_choices_warnings_total`

### 验证方法
```bash
# 1. 部署后测试
curl -X POST https://__DOMAIN_2__/v1/chat/completions \
  -H "Authorization: Bearer <key>" \
  -d '{"model":"glm-5.2","messages":[...],"stream":true}'

# 2. 查看日志
docker logs llm-gateway-go | grep -E "dropping|anthropic_to_openai"

# 3. 检查 metrics
curl http://localhost:__PORT_2__/metrics | grep dropped_non_anthropic
```

## 📂 重要文件

| 文件 | 用途 |
|------|------|
| `docs/llm-gateway-go/2026-06-21-glm52-final-analysis.md` | 完整分析报告 |
| `docs/llm-gateway-go/2026-06-21-glm52-format-issue-diagnosis.md` | 详细诊断 + 3 个方案 |
| `scripts/diagnose-glm52.sh` | 一键诊断工具 |
| `relay/glm52_conversion_test.go` | 15 个单元测试 |

## 🎓 技术细节

### GLM-5.2 路由路径
```
客户端 (OpenAI)
    ↓
Gateway 转换为 Anthropic
    ↓
上游 glm-5.2 (Anthropic 端点)
    ↓
Gateway 转回 OpenAI
    ↓
客户端收到响应
```

### 已知问题
代码注释提到 `glm-5.2-oneday at https://api.supxh.xin` 会泄漏 OpenAI 格式块

### 现有防护
✅ 三层防护机制已部署
✅ 事件类型过滤
✅ OpenAI 格式检测
✅ 空 choices 检测

## 🚦 决策树

```
用户报告"混乱"
    ↓
运行 diagnose-glm52.sh
    ↓
    ├─→ 没有复现 → 关闭工单 ✓
    │
    └─→ 复现问题
        ↓
        ├─→ 空 choices → 应用快速修复 → 部署 → 监控
        ├─→ 混合格式 → 应用快速修复 → 部署 → 监控
        └─→ 其他错误 → 深入调查 → 联系上游
```

## 📞 联系方式

- **文档**: `docs/llm-gateway-go/2026-06-21-glm52-*.md`
- **代码**: `relay/chat_to_anthropic.go`, `relay/anthropic_to_openai_stream.go`
- **测试**: `relay/glm52_conversion_test.go`

---

**最后更新**: 2026-06-21  
**状态**: 等待用户验证  
**优先级**: P1
