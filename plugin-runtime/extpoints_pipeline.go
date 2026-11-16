package pluginruntime

import (
	"github.com/kaixuan/llm-gateway-go/extpoints"
)

// ★ 2026-11-17（12h 审计）：16 阶段扩展点框架此前**只有接口**
//   （包 github.com/kaixuan/llm-gateway-go/extpoints），
//   没有具体注册表/执行引擎 ⇒ 同窗口新增的 demo（plugin-runtime/demo）
//   引用的 NewExtensionPointRegistry / NewExecutionEngine **不存在**
//   ⇒ 该包编译不过 ⇒ `go build ./...` 看不出来（不编译 _test.go），
//   而 `go test ./...` 与 `go vet ./...`（verify.sh 的两道门）直接失败
//   ⇒ **主分支的门是红的**。
//
// 这里给出根包对外入口（demo 以 pluginruntime.XXX 调用）；
// 具体实现放在 extpoints 包里（框架与实现同包，避免根包反向依赖）。
//
// ⚠ 在役边界：这套 16 阶段框架目前**只被 demo 使用**，尚未接入
//   真实派发链路（dispatch）。接入前不得把它当成在役能力。

// NewExtensionPointRegistry 返回一个空的扩展点注册表。
func NewExtensionPointRegistry() extpoints.ExtensionPointRegistry {
	return extpoints.NewExtensionPointRegistry()
}

// NewExecutionEngine 基于注册表构造 16 阶段执行引擎。
func NewExecutionEngine(registry extpoints.ExtensionPointRegistry) *extpoints.ExecutionEngine {
	return extpoints.NewExecutionEngine(registry)
}
