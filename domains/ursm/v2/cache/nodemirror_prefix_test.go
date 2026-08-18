package cache

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

func TestNodeMirrorConfiguredPrefixAndTenantIsolation(t *testing.T) {
	mirror := NewNodeMirrorWithPrefix(32, time.Minute, "contract:")
	mirror.ApplyFromAPI(api.NodeView{TenantID: "tenant-a", CredentialID: 7, RawModel: "model-a", Available: true, Generation: 1})

	if got, ok := mirror.GetForTenant("tenant-a", 7, "model-a"); !ok || !got.Available {
		t.Fatalf("configured-prefix tenant entry missing: %+v, ok=%v", got, ok)
	}
	if _, ok := mirror.GetForTenant("tenant-b", 7, "model-a"); ok {
		t.Fatal("tenant-b must not read tenant-a mirror state")
	}
	if _, ok := mirror.GetForTenant("tenant-a", 7, "model-b"); ok {
		t.Fatal("different model must not read tenant-a/model-a mirror state")
	}
}
