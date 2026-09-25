//go:build !cgo

package routingopt

// ml_selector_nocgo.go — CGO_ENABLED=0 构建下的 ML 选择器降级桩。
//
// onnxruntime_go 是纯 cgo 绑定，!cgo 下没有可编译文件（2026-09-24 打包
// 审计 N-1：2.5.4 起无条件 import 导致网关无法出包）。按 ML 路由的既有
// 设计原则降级：NewMLSelector 返回包装 ErrMLUnavailable 的错误，调用方
// （routing_optimizer_init / MLReranker.StartAutoReload）按共享库缺失的
// 同一路径回落纯规则排序 —— 网关照常编译、照常启动、照常路由。
//
// 注意：与本文件同构建标签的还有真实实现 ml_selector.go（//go:build cgo），
// 公开签名以 ml_selector_types.go 为契约源，两侧不得漂移。

import (
	"context"
	"fmt"
)

// MLSelector 占位类型：NewMLSelector 在 !cgo 下永不构造成功，因此不存在
// 实例；类型仅让 in-package 签名（MLReranker 的 atomic.Pointer 等）可编译。
type MLSelector struct {
	manifest *MLManifest
}

// NewMLSelector 在无 cgo 构建中始终失败 —— 与共享库缺失同语义。
func NewMLSelector(ctx context.Context, cfg MLSelectorConfig) (*MLSelector, error) {
	_ = cfg
	return nil, fmt.Errorf("%w: binary built with CGO_ENABLED=0 (onnxruntime requires cgo)",
		ErrMLUnavailable)
}

// Manifest 暴露加载的 manifest；!cgo 下不可达（无实例）。
func (s *MLSelector) Manifest() *MLManifest { return s.manifest }

// Predict 在无 cgo 构建中始终返回 ErrMLUnavailable（调用方回落规则排序）。
func (s *MLSelector) Predict(ctx context.Context, f MLRouteFeatures) (*MLPrediction, error) {
	return nil, ErrMLUnavailable
}

// Close 在无 cgo 构建中是 no-op。
func (s *MLSelector) Close() error { return nil }
