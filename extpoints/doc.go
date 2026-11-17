// Package extpoints 是 16 阶段请求管线的扩展点框架（注册表 + 执行引擎）。
//
// ★ 在役边界（2026-11-17）：
//
//	本包目前**只被** plugin-runtime/demo 与本包单测使用，**尚未接入**
//	真实派发链路（domains/dispatch / cmd/gateway）。
//	运维与产品不得把「16-phase ExtensionPoint」当成已上线能力；
//	接入裁决与接线归独立轮次。
//
// 本包补齐具体实现的直接原因：同窗口 demo 引用了 NewExtensionPointRegistry /
// NewExecutionEngine，而此前只有接口 ⇒ demo 包编译不过 ⇒ go test/vet 门红。
package extpoints
