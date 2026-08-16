package upgrader

import (
	"context"
	"fmt"
	"runtime"
)

// CheckUpdate 检查是否有更新
func CheckUpdate(masterURL, currentVersion, channel string) (*Release, error) {
	ctx := context.Background()
	client := NewClient(masterURL)

	resp, err := client.CheckUpdateDistribution(ctx, currentVersion, channel, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return nil, fmt.Errorf("check update: %w", err)
	}

	if !resp.HasUpdate {
		return nil, nil
	}

	return resp.Release, nil
}
