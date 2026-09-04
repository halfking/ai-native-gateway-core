// Package main — storage_metrics_endpoint.go
//
// 双模式存储架构（Task 5.3）：存储分层监控指标端点。
//
// GET /metrics/storage 返回 JSON：
//
//	{
//	  "storage":    <monitoring.StorageMetrics.Snapshot()>,   // 分层命中/延迟/写入计数
//	  "file_cache": <v2.FileCache.Stats() | null>,            // lite 模式为 L1.5 文件缓存占用；full 模式为 null
//	  "mode":       "lite" | "full"
//	}
//
// 鉴权：与 /metrics 一致（AdminTokenMiddleware + LLM_GATEWAY_ADMIN_API_KEY，
// NET-008 同款考量）——命中率、缓存占用、写入量属内部运维数据，不应匿名
// 暴露；复用同一中间件避免维护第二套 token 逻辑。
//
// 接入点：main() 在 /metrics 注册处调用 registerStorageMetricsHandler 一行。
// 端点在任何模式下均可用：full / 未启用双模式（storageRuntime == nil）时
// file_cache 为 null、mode 为 "full"，storage 快照为零值计数。
package main

import (
	"encoding/json"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/middleware"
	"github.com/kaixuan/llm-gateway-go/monitoring"
)

// storageMetricsResponse 是 GET /metrics/storage 的响应契约。
// FileCache 为 nil map 时 encoding/json 输出 null（full 模式语义）。
type storageMetricsResponse struct {
	Storage   map[string]interface{} `json:"storage"`
	FileCache map[string]interface{} `json:"file_cache"`
	Mode      string                 `json:"mode"`
}

// registerStorageMetricsHandler 在 mux 上注册 GET /metrics/storage。
//
//   - rt 可为 nil（full / 未启用双模式），端点照常注册并返回 mode="full"、
//     file_cache=null，保证运维面在任何模式下可用；
//   - adminToken 传 cfg.AdminAPIKey（LLM_GATEWAY_ADMIN_API_KEY）；
//   - 指标取 monitoring.Default() 进程级单例。
func registerStorageMetricsHandler(mux *http.ServeMux, rt *storageRuntime, adminToken string) {
	mux.Handle("GET /metrics/storage",
		middleware.NewAdminTokenMiddleware(adminToken).Wrap(newStorageMetricsHandler(rt, monitoring.Default())))
}

// newStorageMetricsHandler 构造端点 handler。
// metrics 参数化是为了测试可注入独立实例（单例状态跨测试串扰）；
// 生产路径经 registerStorageMetricsHandler 固定传 Default()。
// 方法语义（仅 GET）由 ServeMux 的 "GET /path" 模式路由保证，非 GET 返回 405。
func newStorageMetricsHandler(rt *storageRuntime, metrics *monitoring.StorageMetrics) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := storageMetricsResponse{
			Storage: metrics.Snapshot(),
			Mode:    storageModeLabel(rt),
		}
		if rt != nil && rt.fileCache != nil {
			resp.FileCache = rt.fileCache.Stats()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
}

// storageModeLabel 返回端点上报的存储模式：
// runtime 存在时用其 mode（当前恒为 "lite"），否则为 "full"。
func storageModeLabel(rt *storageRuntime) string {
	if rt != nil && rt.mode != "" {
		return rt.mode
	}
	return "full"
}
