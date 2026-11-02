package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseLiveStreamWantProbe(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want bool
	}{
		{"absent", "/api/admin/live-stream", false},
		{"probe=1", "/api/admin/live-stream?probe=1", true},
		{"probe=true", "/api/admin/live-stream?probe=true", true},
		{"probe=0", "/api/admin/live-stream?probe=0", false},
		{"lanes with probe", "/api/admin/live-stream?lanes=business,probe", true},
		{"lanes business only", "/api/admin/live-stream?lanes=business", false},
		{"lanes empty", "/api/admin/live-stream?lanes=", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.url, nil)
			if got := parseLiveStreamWantProbe(r); got != tc.want {
				t.Errorf("parseLiveStreamWantProbe(%s) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

func TestFilterLiveRequestsByProbeSub(t *testing.T) {
	items := []LiveRequest{
		{RequestID: "b1", IsProbe: false},
		{RequestID: "p1", IsProbe: true},
		{RequestID: "b2", IsProbe: false},
	}
	got := filterLiveRequestsByProbeSub(items, false)
	if len(got) != 2 || got[0].RequestID != "b1" || got[1].RequestID != "b2" {
		t.Fatalf("wantProbe=false must drop probes, got %+v", got)
	}
	kept := filterLiveRequestsByProbeSub(items, true)
	if len(kept) != 3 {
		t.Fatalf("wantProbe=true must keep all, got %d", len(kept))
	}
}

func TestMergeLiveRequestsByTs(t *testing.T) {
	a := []LiveRequest{
		{RequestID: "a", Ts: "2026-11-02T10:00:00Z"},
		{RequestID: "dup", Ts: "2026-11-02T10:01:00Z"},
	}
	b := []LiveRequest{
		{RequestID: "dup", Ts: "2026-11-02T10:01:00Z"},
		{RequestID: "c", Ts: "2026-11-02T09:59:00Z"},
	}
	got := mergeLiveRequestsByTs(a, b)
	if len(got) != 3 {
		t.Fatalf("want 3 after dedupe, got %d", len(got))
	}
	if got[0].RequestID != "c" {
		t.Errorf("want oldest first, got %s", got[0].RequestID)
	}
}

func TestShouldDeliverDropsProbeWithoutSubscription(t *testing.T) {
	h := &LiveStreamSSEHub{}
	c := &liveStreamClient{isSuper: true, wantProbe: false}
	probe := LiveRequest{RequestID: "p", TenantID: "t", IsProbe: true}
	biz := LiveRequest{RequestID: "b", TenantID: "t", IsProbe: false}
	if h.shouldDeliver(c, LiveStreamEnvelope{Request: &probe}) {
		t.Fatal("super client without wantProbe must not receive probe tile")
	}
	if !h.shouldDeliver(c, LiveStreamEnvelope{Request: &biz}) {
		t.Fatal("super client must still receive business tile")
	}
	c.wantProbe = true
	if !h.shouldDeliver(c, LiveStreamEnvelope{Request: &probe}) {
		t.Fatal("wantProbe=true must receive probe tile")
	}
}

func TestFilterSnapshotByProbeSub(t *testing.T) {
	sn := &LiveStreamSnapshot{
		Dimensions: map[string][]LiveStreamLane{
			"model": {{
				ID: "m1",
				Requests: []LiveStreamTile{
					{RequestID: "b", IsProbe: false},
					{RequestID: "p", IsProbe: true},
				},
			}},
		},
	}
	out := filterSnapshotByProbeSub(sn, false)
	if len(out.Dimensions["model"][0].Requests) != 1 || out.Dimensions["model"][0].Requests[0].RequestID != "b" {
		t.Fatalf("unsubscribed snapshot must drop probe tiles, got %+v", out.Dimensions["model"][0].Requests)
	}
	if filterSnapshotByProbeSub(sn, true) != sn {
		t.Fatal("wantProbe=true must return same snapshot pointer")
	}
}
