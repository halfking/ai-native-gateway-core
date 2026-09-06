package main

import (
	"net/http"
	"sync"

	"github.com/labstack/echo/v4"

	"github.com/kaixuan/llm-gateway-go/routingopt"
)

// routing_ml_admin.go — P2.5: ML重排序的诊断端点。
//
//	GET /api/admin/routing-opt/ml
//	→ {enabled, manifest{schema,model,labels,inputs}, stats{...}, ab_test{...}}
//
// ML禁用时返回 {enabled: false}。只读、无DB访问，供运维/A/B观测。

var (
	routingOptMLMu sync.RWMutex
	routingOptML   *routingopt.RealOptimizer
)

// setRoutingOptML records the live optimizer for the diagnostics handler
// (called from buildRoutingOptimizer; nil clears).
func setRoutingOptML(o *routingopt.RealOptimizer) {
	routingOptMLMu.Lock()
	defer routingOptMLMu.Unlock()
	routingOptML = o
}

// registerRoutingOptMLRoutes mounts GET /api/admin/routing-opt/ml on the
// already-authenticated admin group.
func registerRoutingOptMLRoutes(g *echo.Group) {
	g.GET("/routing-opt/ml", handleRoutingOptML)
}

func handleRoutingOptML(c echo.Context) error {
	routingOptMLMu.RLock()
	opt := routingOptML
	routingOptMLMu.RUnlock()

	if opt == nil {
		return c.JSON(http.StatusOK, map[string]any{
			"enabled": false,
			"reason":  "optimizer disabled (ROUTING_OPT_ENABLED=false)",
		})
	}
	diag := opt.MLDiagnostics()
	if diag == nil {
		return c.JSON(http.StatusOK, map[string]any{
			"enabled": false,
			"reason":  "ML re-ranker disabled (ROUTING_ML_ENABLED=false)",
		})
	}
	diag["enabled"] = true
	return c.JSON(http.StatusOK, diag)
}
