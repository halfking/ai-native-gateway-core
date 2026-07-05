# Phase 1: llm-gateway-go 插件化基础设施

> **工期**: 2周  
> **优先级**: P0 (最高)  
> **依赖**: Phase 0完成  
> **并行**: 可与Phase 2、Phase 4并行

---

## 📋 Phase概述

本阶段建立llm-gateway-go的插件化Hook基础设施，并实现5个核心插件。所有模块可以并行开发。

---

## 🎯 目标

1. **Hook框架**: 统一的插件注册、配置、执行机制
2. **输出脱敏**: 完整的PII检测和流式脱敏能力
3. **Memora自动沉淀**: 会话自动推送到kxmemory
4. **会话编辑器**: 会话操作能力（切片、分支、合并等）
5. **Vibe Coding**: 代码质量评估和自循环

---

## 📦 模块清单

| 模块ID | 模块名称 | 负责人 | 工期 | 依赖 | 状态 |
|--------|---------|--------|------|------|------|
| 1.1 | Hook框架增强 | 开发者A | 1周 | 无 | ⏳ |
| 1.2 | 输出脱敏插件 | 开发者B | 1周 | 1.1 | ⏳ |
| 1.3 | Memora自动沉淀 | 开发者C | 1.5周 | 1.1, Phase2.1 | ⏳ |
| 1.4 | 会话编辑器 | 开发者D | 1周 | 无 | ⏳ |
| 1.5 | Vibe Coding评估 | 开发者E | 1.5周 | 无 | ⏳ |

---

## 🔗 模块链接

- [Module 1.1: Hook框架增强](./module-1.1-hook-framework.md)
- [Module 1.2: 输出脱敏插件](./module-1.2-output-sanitizer.md)
- [Module 1.3: Memora自动沉淀插件](./module-1.3-memora-auto.md)
- [Module 1.4: 会话编辑器插件](./module-1.4-session-editor.md)
- [Module 1.5: Vibe Coding评估插件](./module-1.5-vibe-coding.md)

---

## 📊 整体进度追踪

```
Module 1.1 ████████░░ 80%
Module 1.2 ██░░░░░░░░ 20%
Module 1.3 ░░░░░░░░░░  0%
Module 1.4 ██░░░░░░░░ 20%
Module 1.5 ░░░░░░░░░░  0%

总体进度: 24%
```

---

## ✅ 验收标准（Phase级别）

### 功能验收
- [ ] 所有5个模块单元测试通过（覆盖率>85%）
- [ ] 所有插件可通过配置文件启用/禁用
- [ ] 插件热加载成功（配置变更<3秒生效）
- [ ] 集成测试通过

### 性能验收
- [ ] Hook链执行延迟 < 50ms (P95)
- [ ] 内存占用增加 < 100MB
- [ ] CPU增加 < 10%

### 文档验收
- [ ] 所有模块有完整设计文档
- [ ] API文档完整
- [ ] 部署文档更新

---

## 🚀 快速开始

### 1. 环境准备

```bash
# 安装依赖
go mod download

# 运行测试
go test ./domains/hooks/...

# 启动服务
./gateway --config=configs/local.yaml
```

### 2. 开发新插件

```bash
# 1. 创建目录
mkdir -p domains/hooks/myplugin

# 2. 实现Hook接口
cat > domains/hooks/myplugin/hook.go <<EOF
package myplugin

import (
    "context"
    "__REPO_URL_3__/domains/hooks"
)

type MyHook struct {
    enabled bool
}

func (h *MyHook) Name() string { return "my-plugin" }
func (h *MyHook) Priority() int { return 50 }
func (h *MyHook) Enabled() bool { return h.enabled }
func (h *MyHook) Phase() hooks.Phase { return hooks.PhasePreRouting }

func (h *MyHook) Execute(ctx context.Context, env *hooks.Environment) error {
    // 实现逻辑
    return nil
}
EOF

# 3. 注册插件
# 在 cmd/gateway/main.go 中添加
# hookRegistry.Register(myplugin.NewMyHook(config))

# 4. 添加配置
# 在 config/hooks.yaml 中添加配置项
```

---

## 📚 相关资源

- [Hook接口规范](../00-总体架构设计.md#31-hook插件系统)
- [配置文件Schema](./hook-config-schema.yaml)
- [开发提示词](../prompts/phase1/)
- [样例数据](../samples/phase1/)

---

## 🐛 常见问题

### Q: Hook执行顺序如何控制？
A: 通过Priority字段控制，数值越小越先执行。同阶段内按Priority排序。

### Q: 如何禁用某个插件？
A: 在config/hooks.yaml中设置`enabled: false`，或在环境变量中设置`HOOK_{NAME}_ENABLED=false`。

### Q: 插件失败如何处理？
A: 根据配置可选择：1) 返回错误中断请求 2) 记录日志继续执行 3) 降级到默认行为。

---

**文档维护**: 开发者A  
**最后更新**: 2026-07-03
