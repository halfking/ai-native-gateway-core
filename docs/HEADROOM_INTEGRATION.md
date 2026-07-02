# Headroom 压缩算法集成

## 概述

将 Headroom 的 token 压缩优化算法以**纯 Go 插件**形式集成到 LLM Gateway Go 的会话压缩流程中，完全复用现有的 Hook Pipeline / SessionCompressor 架构，不引入微服务或 Python sidecar。

## 核心算法

| 组件 | 文件 | 作用 |
|------|------|------|
| HeadroomCompressor | `headroom_compressor.go` | 编排器：保留规则 + 日志 + 拼接 |
| SmartCrusher | `smart_crusher.go` | 去冗余：移除填充词、压缩空白、合并重复块 |
| AdaptiveSizer | `adaptive_sizer.go` | 自适应：按目标 token 数智能截断（保持句子边界） |

### 关键设计

- **字段保留**：压缩只改写 `content` 字段，`tool_calls` / `tool_call_id` / `name` 等字段原样回写，不破坏工具调用链。
- **顺序保留**：输出消息顺序与输入一致（按 index 重组，而非简单拼接）。
- **保留规则**：system 消息始终保留；最后 N 条消息默认保留（可配置）。
- **配置真正生效**：`loadHeadroomConfig` 通过 `settings.Global.EffectiveValue` 读取 DB/env 配置，非硬编码 nil。
- **降级**：SmartCrusher / AdaptiveSizer 任一失败自动跳过；整体失败时 Prepare 回退到 `delta_append`。

## 集成点

1. `compressor.go`：新增 `ModeHeadroom` (=6)、`StrategyHeadroom`，`envMode`/`LoadMode`/`String` 同步支持。
2. `session_compressor.go`：`Prepare` Phase 4 新增 `ModeHeadroom` 分支，调用 `tryHeadroomCompression` → `NewHeadroomCompressor` → `Compress`。
3. `session_cache.go`：`SessionState` 新增 `HeadroomApplied` / `HeadroomRatio` / `TokensSaved` 字段（`hr_*` JSON tag）。
4. `settings/spec_compression.go`：`compression.mode` 增加 `headroom` 选项；新增 3 个 headroom 专属配置。

## 配置

| 配置项 | 默认 | 说明 |
|--------|------|------|
| `compression.mode` | `smart` | 设为 `headroom` 启用 |
| `compression.headroom.target_ratio` | `0.5` | 目标压缩率 (0.1-0.9) |
| `compression.headroom.enable_smart_crusher` | `true` | 启用智能去冗余 |
| `compression.headroom.enable_adaptive_sizer` | `true` | 启用自适应调整 |

环境变量：`LLM_GATEWAY_COMPRESSION_MODE=6` 等价于 `headroom`。

## 编程接口

```go
config := HeadroomConfig{
    TargetRatio:         0.5,
    MaxTokens:           2000,
    EnableSmartCrusher:  true,
    EnableAdaptiveSizer: true,
    PreserveSystem:      true,
    PreserveLastN:       2,
}
compressor, _ := NewHeadroomCompressor(config)
compressed, _ := compressor.Compress(ctx, messages, targetTokens)

log := compressor.GetCompressionLog()          // 压缩前后状态
stitched, _ := compressor.StitchSession(sid)   // 元数据 + 压缩消息
```

## 测试

```bash
go test ./domains/hooks/compression/ -run "TestHeadroom|TestSmartCrusher|TestAdaptiveSizer" -v
```

覆盖：基础压缩、去冗余、自适应截断、会话拼接（含不变性校验）、非 content 字段保留与顺序保持。

## 与现有模式对比

| 模式 | 延迟 | 信息保留 | 场景 |
|------|------|----------|------|
| `smart` | 5-20ms | 90-95% | 通用 |
| `aggressive` | 10-30ms | 85-90% | 长会话 |
| `headroom` | < 5ms | 87-95% | 口语化内容、低延迟需求 |
| LLM 摘要 | 100-500ms | 80-85% | 高质量摘要 |

Headroom 模式延迟远低于 LLM 摘要，适合对延迟敏感且内容含较多冗余的场景。
