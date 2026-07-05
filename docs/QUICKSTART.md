# 🚀 快速开始 - 架构重构下一步

## 📍 当前状态
✅ Phase 1 完成（审计 + 会话状态机 + 文档）  
⏳ Phase 2-6 待执行（16 个任务）

---

## 🎯 立即开始（复制下面的提示词到新会话）

### 任务 2.1: 实现 IRTransformer 接口基础设施

```
我正在重构 llm-gateway-go 的客户端适配器架构。请帮我完成以下任务：

1. 查看 ARCHITECTURE_REFACTOR_GUIDE.md 了解整体方案
2. 基于 domains/streaming/client_adapter.go，扩展以下接口：
   - IRTransformer 接口（TransformRequestIR, TransformResponseIR）
   - SessionAwareAdapter 接口（OnSessionStart, OnSessionEnd）
   - StreamAwareAdapter 接口（OnStreamStart, OnStreamChunk, OnStreamEnd）

3. 实现一个 BaseIRAdapter 基类，提供默认实现（空操作）

4. 为 CursorAdapter 实现完整的 IRTransformer：
   - TransformRequestIR: 确保所有 tool_call 有 ID，检测长上下文
   - TransformResponseIR: 格式化输出
   - 添加单元测试

工作目录: __LOCAL_PATH_1__

要求：
- 参考 domains/session/state_machine.go 和 context.go 的设计风格
- 每个接口都要有详细的注释说明使用场景
- 单元测试覆盖率 >85%
- 完成后运行 go test ./domains/streaming/... 验证

完成后创建 git commit 并推送。
```

**预计时间**: 2-3 小时

---

## 📚 完整任务列表

查看 **`ARCHITECTURE_REFACTOR_GUIDE.md`** 获取所有 16 个任务的详细提示词。

---

## 📊 进度

- **Phase 1**: ✅ 完成
- **Phase 2**: ⏳ 3 个任务（7-10h）← **你在这里**
- **Phase 3**: ⏳ 3 个任务（8-11h）
- **Phase 4**: ⏳ 3 个任务（6-9h）
- **Phase 5**: ⏳ 4 个任务（11-15h）
- **Phase 6**: ⏳ 3 个任务（4-6h）

**总进度**: 6.25% | **总估算**: 36-51 小时

---

## 📖 关键文档

| 文档 | 用途 |
|------|------|
| `HANDOFF.md` | 交接总览 |
| `ARCHITECTURE_REFACTOR_GUIDE.md` | 30+ 页详细重构指南（含全部任务提示词 ⭐） |
| `ARCHITECTURE_REFACTOR_GUIDE.md` | 详细技术指南 |

---

**开始重构**: 复制上面的提示词 → 新建 AI 会话 → 粘贴执行 🎉
