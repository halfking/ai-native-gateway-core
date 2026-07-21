package v2

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

func TestApplyProbeSuccessUpdatesAvailable(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	mgr := New(Dependencies{Redis: rdb, Config: DefaultConfig()})
	_ = mgr.SetReady(context.Background(), true)
	if err := mgr.ApplyProbe(context.Background(), api.ProbeOutcome{
		CredentialID: 1, RawModel: "m", Success: true,
	}); err != nil {
		t.Fatalf("probe: %v", err)
	}
}
