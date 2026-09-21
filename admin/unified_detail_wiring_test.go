package admin

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/requestdetail"
	"github.com/stretchr/testify/require"
)

func TestSetLiveStreamRedisStorePreservesRetryConfig(t *testing.T) {
	h := &Handler{}
	store, err := requestdetail.NewStoreWithOptions(t.TempDir(), requestdetail.StoreOptions{})
	require.NoError(t, err)

	h.SetRequestDetailStore(store, LocatorRetryConfig{Count: 7, Delay: 250 * time.Millisecond})
	h.SetLiveStreamRedisStore(nil)

	require.NotNil(t, h.requestDetailLocator)
	require.Equal(t, 7, h.requestDetailLocator.DBRetryCount)
	require.Equal(t, 250*time.Millisecond, h.requestDetailLocator.DBRetryDelay)
}
