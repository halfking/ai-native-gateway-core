package main

import "github.com/kaixuan/llm-gateway-go/domains/dispatch"

func gatewayQueueProjectionSnapshot() *dispatch.SnapshotView {
	if gatewayQueueProjection == nil {
		return &dispatch.SnapshotView{Enabled: dispatch.IsDispatchEnabled(), Wired: false, Models: []dispatch.LaneView{}, Credentials: []dispatch.LaneView{}}
	}
	return gatewayQueueProjection.Snapshot()
}
