package audit

import "testing"

func TestCurrentAttemptUsageExcludesBankedContinuationUsage(t *testing.T) {
	c := &StreamCapture{}
	p, out, cache, write := 9, 4, 2, 0
	c.ObserveUsage(&p, &out, &cache, &write)
	c.bankedPromptTokens = 100
	u := c.CurrentAttemptUsage()
	if u.PromptTokens == nil || *u.PromptTokens != 9 || u.CompletionTokens == nil || *u.CompletionTokens != 4 {
		t.Fatalf("current usage = %#v", u)
	}
	if u.CacheReadTokens == nil || *u.CacheReadTokens != 2 || u.CacheWriteTokens == nil || *u.CacheWriteTokens != 0 {
		t.Fatalf("cache usage = %#v", u)
	}
	if u.PromptTokens == &p {
		t.Fatal("snapshot must not expose mutable capture memory")
	}
}
