# 2026-09-22 onnxruntime_go / cgo 构建约束诊断

> 触发原因：执行 `go test ./domains/streaming/...` 时观察到
> `imports github.com/yalue/onnxruntime_go: build constraints exclude all Go files`
> 错误。`domains/streaming` 不直接依赖 ONNX，是经由
> `autoroute → routingopt → onnxruntime_go` 被卷进来。本文是诊断留档。

## 1. 现象

```
$ go test ./domains/streaming/... ./autoroute/...
package github.com/kaixuan/llm-gateway-go/domains/streaming
        imports github.com/kaixuan/llm-gateway-go/autoroute
        imports github.com/kaixuan/llm-gateway-go/routingopt
        imports github.com/yalue/onnxruntime_go: build constraints
        exclude all Go files in C:\...\vendor\github.com\yalue\onnxruntime_go
FAIL    .../domains/streaming [setup failed]
FAIL    .../domains/streaming/executors [setup failed]
FAIL    .../domains/streaming/integrity [setup failed]
FAIL    .../autoroute [setup failed]
```

`CGO_ENABLED=1` 也无法直接解决——本机 `gcc` 缺失：
```
cgo: C compiler "gcc" not found: exec: "gcc": executable file not found in %PATH%
```

## 2. onnxruntime_go 的构建标签与依赖

| 文件 | 约束 | 备注 |
|------|------|------|
| `onnxruntime_go.go` | 无 | 顶层 `import "C"` + `#cgo CFLAGS -O2 -g` + `#include "onnxruntime_wrapper.h"`。**cgo 必备**。 |
| `setup_env.go` | `//go:build !windows` | Linux/macOS 的 dlopen + dlsym；用 cgo。 |
| `setup_env_windows.go` | `//go:build windows` | Windows 的 LoadLibraryW + GetProcAddress；用 cgo。 |
| `tensor_type_constraints.go` | 无 | 纯 Go，类型常量表。 |

- 四个文件里**只有一个纯 Go 文件**（`tensor_type_constraints.go`），但它没有
  独立的初始化代码，单独存在没有实际意义。
- `import "C"` 在 Go 编译时的隐式约束：cgo 关闭 → 文件整体被排除。
- 因此 `CGO_ENABLED=0` 时整个 `onnxruntime_go` 包无任何 `.go` 文件参与编译，
  Go 工具链报 "build constraints exclude all Go files"。

## 3. autoroute 是否需要 ONNX 运行时？

**否。** `autoroute` 只用到 `routingopt` 的非 ML 接口：

| 引用点 | 用途 | 是否触及 ONNX |
|--------|------|---------------|
| `autoroute/decision.go` | `routingopt.WithRequestMeta`、`routingopt.RequestMeta`（上下文传递） | 否 |
| `autoroute/decision_v2.go` | 同上 | 否 |
| `autoroute/optimizer_bridge.go` | `routingopt.ModelCandidate`、`routingopt.RoutingContext`、`routingopt.MLRouteFeatures` 类型定义 | 仅类型，无 MLSelector 实例化 |
| `autoroute/outcome_feedback.go` | `routingopt.RoutingFeedback`（反馈 DTO） | 否 |
| `autoroute/outcome_metrics.go` | 计数器 | 否 |
| `autoroute/decision_optimizer_test.go` | `routingopt.NewDefaultOptimizer()`（无 ML 的占位） | 否 |

`MLSelector`、`MLReranker`、`NewMLSelector`、`Predict` 等真正调用 ORT 的入口
**没有从 autoroute 直接引用**。

## 4. 根因：routingopt 的包面设计

`routingopt/ml_selector.go:26` 有顶层无条件导入：

```go
import (
    ...
    ort "github.com/yalue/onnxruntime_go"
)
```

这意味着任何 import `routingopt` 的包都被拖入 cgo 依赖。这是设计选择：
"ML 选择器是 routingopt 核心能力之一"。

副作用：
- 本地开发机没装 gcc/TDM-GCC → `go test ./...` 大面积失败
- CI 里只要装上 gcc + onnxruntime 共享库就 OK（现网部署走 ROUTING_ML_*
  默认关的路径，库可选；测试时 `ml_selector_test.go` 也走 Skip 而非硬依赖）

## 5. 已验证的非 ONNX 路径

| 命令 | 结果 |
|------|------|
| `go test ./telemetry/` | ok |
| `go test ./internal/clienttype/` | ok |
| `go test ./internal/ir/` | ok |
| `go test ./domains/streaming/executors/webcookie` | ok |
| `go test ./domains/streaming/state` | ok |
| `go test ./domains/streaming` | **FAIL**（autoroute 卷入） |
| `go test ./autoroute` | **FAIL**（routingopt 卷入） |
| `go test ./routingopt` | **FAIL**（onnxruntime_go cgo） |

## 6. 修复路径（建议，下一轮裁决）

| 方案 | 影响面 | 取舍 |
|------|--------|------|
| A. 给 `ml_selector.go` 加 `//go:build onnx` 标签 | 小 | ONNX 变为 opt-in；默认编译关闭；测试走 stub |
| B. 拆 `routingopt/mlselector` 子包，autoroute 只引父包接口 | 中 | 包图改;接口抽象掉一行 |
| C. CI/dev 装 gcc + onnxruntime 共享库 | 小 | 平台差异（windows 需 TDM-GCC/MSYS）；文档+脚本 |
| D. 把 `onnxruntime_go` 替换为纯 Go 实现（待选） | 大 | 工程量大；不在本轮范围 |

**推荐**：方案 A + 方案 C 组合。`ml_selector.go` 走 `//go:build onnx` 默认关闭
（对齐 `ROUTING_ML_ENABLED=false` 默认值），CI 单独开 `-tags onnx` 跑端到端
ONNX 推理用例（`TestMLSelectorEndToEnd`），本地开发/普通测试零 cgo 依赖。

## 7. 状态

- 诊断结论：**autoroute 不直接需要 ONNX**；失败是 routingopt 把 MLSelector 放
  在与核心类型同包内、未加构建标签造成的副作用。
- 修复未在本轮执行（属 P2.5 模块拆分决策，影响面较大，留待 R52 入口时
  与 ML team 同步方案）。
- 本次 zcode/minimax-code/deepseek-code 客户端识别不依赖此路径——相关
  测试均在 `./telemetry/` 与 `./internal/clienttype/`，全绿。