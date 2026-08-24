package dispatch

import "testing"

func TestFailoverSummaryDoesNotExposeUpstreamBody(t *testing.T) {
	got := failoverSummary("upstream_down", 502, "switch")
	want := "上游请求失败（upstream_down (HTTP 502)），正在切换到备用节点..."
	if got != want {
		t.Fatalf("failoverSummary() = %q, want %q", got, want)
	}
}
