package main

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

func TestDataHorizonCanaryHookSharesMirrorEligibilityAndUsesPersistTime(t *testing.T) {
	hub := admin.NewLiveStreamSSEHub(nil, admin.LiveStreamConfig{})
	now := time.Date(2026, 10, 16, 3, 50, 0, 0, time.UTC)
	hook := dataHorizonCanaryHook(hub, func() time.Time { return now })

	inProgress := telemetry.RequestStatusInProgress
	hook(&telemetry.RequestLogEntry{RequestID: "req-in-progress", RequestStatus: &inProgress})
	probeStage := "node_probe"
	hook(&telemetry.RequestLogEntry{RequestID: "req-probe", Success: true, OriginStage: &probeStage})
	internal := true
	requestType := "title"
	session := "gw-user"
	hook(&telemetry.RequestLogEntry{RequestID: "req-loopback", Success: true, IsAutoRequest: &internal, RequestType: &requestType, GwSessionID: &session})
	if got := hub.DataHorizonCanaries(); len(got) != 0 {
		t.Fatalf("mirror-excluded entries entered canary ring: %+v", got)
	}

	oldStart := now.Add(-2 * time.Hour)
	success := telemetry.RequestStatusSuccess
	hook(&telemetry.RequestLogEntry{RequestID: "req-business", Success: true, RequestStatus: &success, EventAt: &oldStart})
	got := hub.DataHorizonCanaries()
	if len(got) != 1 || got[0].RequestID != "req-business" || !got[0].SeenAt.Equal(now) {
		t.Fatalf("canary=%+v; must use persisted-hook clock, not old EventAt", got)
	}
}
