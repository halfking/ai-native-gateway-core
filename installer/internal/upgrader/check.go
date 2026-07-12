package upgrader

import (
	"context"
	"fmt"
)

// CheckUpdate 检查是否有更新
func CheckUpdate(masterURL, currentVersion, channel string) (*Release, error) {
	ctx := context.Background()
	client := NewClient(masterURL)

	resp, err := client.CheckUpdate(ctx, currentVersion, channel)
	if err != nil {
		return nil, fmt.Errorf("check update: %w", err)
	}

	if !resp.HasUpdate {
		return nil, nil
	}

	return resp.Release, nil
}
