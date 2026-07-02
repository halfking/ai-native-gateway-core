// Package attachments - storage_metrics.go
//
// 提供存储操作的监控埋点支持

package attachments

import (
	"time"
)

// StorageMetrics 存储操作指标收集器接口
// 使用回调模式，避免直接依赖特定的监控库（如 Prometheus）
type StorageMetrics interface {
	// RecordOperation 记录一次存储操作
	// op: 操作类型（save、load、delete、stat、exists）
	// backend: 后端类型（local、oss、s3）
	// success: 是否成功
	// duration: 操作耗时
	// bytes: 传输字节数（仅 save/load）
	RecordOperation(op, backend string, success bool, duration time.Duration, bytes int64)

	// RecordHealthCheck 记录健康检查结果
	// backend: 后端类型
	// success: 是否成功
	// duration: 检查耗时
	RecordHealthCheck(backend string, success bool, duration time.Duration)
}

// NoopMetrics 空实现，不记录任何指标
type NoopMetrics struct{}

func (m *NoopMetrics) RecordOperation(op, backend string, success bool, duration time.Duration, bytes int64) {
}

func (m *NoopMetrics) RecordHealthCheck(backend string, success bool, duration time.Duration) {
}

// globalMetrics 全局指标收集器，默认为空实现
var globalMetrics StorageMetrics = &NoopMetrics{}

// SetMetrics 设置全局指标收集器
// 在 main.go 中初始化时调用，传入实际的 Prometheus/StatsD 实现
func SetMetrics(m StorageMetrics) {
	if m != nil {
		globalMetrics = m
	}
}

// recordOp 记录操作（内部辅助函数）
func recordOp(op, backend string, start time.Time, err error, bytes int64) {
	duration := time.Since(start)
	globalMetrics.RecordOperation(op, backend, err == nil, duration, bytes)
}

// recordHealthCheck 记录健康检查（内部辅助函数）
func recordHealthCheck(backend string, start time.Time, err error) {
	duration := time.Since(start)
	globalMetrics.RecordHealthCheck(backend, err == nil, duration)
}
