package reqprobe

import (
	"context"
	"testing"
)

// 2026-11-20 时间范围统一轮：Filter.DayFrom/DayTo 范围过滤钉测。
// Record.Day 是 YYYY-MM-DD 字符串 ⇒ 字典序即时间序，范围含首尾。
func TestFilterMatchesDayRange(t *testing.T) {
	mk := func(day string) Record { return Record{Day: day, ProviderCode: "p1"} }

	cases := []struct {
		name  string
		f     Filter
		day   string
		match bool
	}{
		{"no range matches all", Filter{}, "2026-11-01", true},
		{"from only - inside", Filter{DayFrom: "2026-11-10"}, "2026-11-15", true},
		{"from only - boundary inclusive", Filter{DayFrom: "2026-11-15"}, "2026-11-15", true},
		{"from only - before", Filter{DayFrom: "2026-11-10"}, "2026-11-09", false},
		{"to only - inside", Filter{DayTo: "2026-11-20"}, "2026-11-15", true},
		{"to only - boundary inclusive", Filter{DayTo: "2026-11-20"}, "2026-11-20", true},
		{"to only - after", Filter{DayTo: "2026-11-20"}, "2026-11-21", false},
		{"window inside", Filter{DayFrom: "2026-11-10", DayTo: "2026-11-20"}, "2026-11-15", true},
		{"window boundary start", Filter{DayFrom: "2026-11-10", DayTo: "2026-11-20"}, "2026-11-10", true},
		{"window boundary end", Filter{DayFrom: "2026-11-10", DayTo: "2026-11-20"}, "2026-11-20", true},
		{"window outside below", Filter{DayFrom: "2026-11-10", DayTo: "2026-11-20"}, "2026-11-09", false},
		{"window outside above", Filter{DayFrom: "2026-11-10", DayTo: "2026-11-20"}, "2026-11-21", false},
		// Day（单日）与范围同时给出时取交集。
		{"day intersects range", Filter{Day: "2026-11-15", DayFrom: "2026-11-10", DayTo: "2026-11-20"}, "2026-11-15", true},
		{"day outside range", Filter{Day: "2026-11-15", DayFrom: "2026-11-20", DayTo: "2026-11-25"}, "2026-11-15", false},
		{"day mismatch within range", Filter{Day: "2026-11-16", DayFrom: "2026-11-10", DayTo: "2026-11-20"}, "2026-11-15", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.matches(mk(tc.day)); got != tc.match {
				t.Fatalf("day=%s filter=%+v match=%v want %v", tc.day, tc.f, got, tc.match)
			}
		})
	}
}

// MemoryStore.List 走 f.matches ⇒ 范围过滤在 store 层生效（分页总数同步）。
func TestMemoryStoreListDayRange(t *testing.T) {
	s := NewMemoryStore()
	seed := []Record{
		{ID: 1, Day: "2026-11-01", ProviderCode: "p1", Fingerprint: "f1"},
		{ID: 2, Day: "2026-11-15", ProviderCode: "p1", Fingerprint: "f2"},
		{ID: 3, Day: "2026-11-20", ProviderCode: "p2", Fingerprint: "f3"},
		{ID: 4, Day: "2026-12-05", ProviderCode: "p1", Fingerprint: "f4"},
	}
	for _, r := range seed {
		if _, err := s.Upsert(nil, r); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	got, total, err := s.List(context.Background(), Filter{DayFrom: "2026-11-10", DayTo: "2026-11-25"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(got) != 2 {
		t.Fatalf("total=%d len=%d want 2/2", total, len(got))
	}
	for _, r := range got {
		if r.Day != "2026-11-15" && r.Day != "2026-11-20" {
			t.Fatalf("unexpected day %s in window result", r.Day)
		}
	}
}
