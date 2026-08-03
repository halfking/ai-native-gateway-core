# 会话压缩与三层缓存优化方案

> **文档版本**: v1.0
> **创建日期**: 2026-08-02
> **状态**: 🚧 设计阶段

---

## 📋 执行摘要

本文档基于对 OmniRoute 压缩算法的深入分析，重新定义我们的三层缓存架构，并制定完整的测试验证方案。

### 核心目标
1. **架构修正**: 将三层缓存从"存储位置"重新定义为"处理阶段"
2. **算法优化**: 吸收 OmniRoute Lite/Deep Compression 优势
3. **总结增强**: 支持即时/事后总结，多维度标注
4. **完整验证**: 端到端测试 + 性能基准 + 生产验证

---

## 🎯 Part 1: OmniRoute 算法优势分析

### 1.1 Lite Compression (轻量压缩)

**核心优势**: 无 LLM 调用，零成本优化

#### 算法详解

**步骤 1: 空格规范化**
```typescript
// 问题: GPT-4o 将换行符渲染为双空格
normalize(text: string): string {
  return text
    .replace(/\n/g, ' ')     // 换行 → 单空格
    .replace(/\s+/g, ' ')    // 多空格 → 单空格
    .trim();
}
```

**收益**: 节省 5-15% Token（长文档场景）

---

**步骤 2: 系统消息去重**
```typescript
// 问题: 重复系统 Prompt 浪费 Token
dedupeSystemMessages(messages: Message[]): Message[] {
  const seen = new Set<string>();
  return messages.filter(msg => {
    if (msg.role !== 'system') return true;
    const hash = hashContent(msg.content);
    if (seen.has(hash)) return false;
    seen.add(hash);
    return true;
  });
}
```

**收益**: 节省 10-30% Token（多轮对话场景）

---

<!-- PLACEHOLDER_1 -->
