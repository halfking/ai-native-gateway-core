// Package all is the sole gather point for in-process plugin adapters.
// Only cmd/gateway (and tests) may blank-import this package.
package all

import (
	_ "github.com/kaixuan/llm-gateway-go/plugins/hello"
)
