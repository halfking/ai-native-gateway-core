package fsstore

import (
	"testing"
	"time"
)

func TestListRequestsInDayAcceptsCanonicalDateFormats(t *testing.T) {
	s, _ := tempStore(t)
	defer s.Close()

	startedAt := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	if err := s.PutRequest(RequestRecord{ID: "date-format", StartedAt: startedAt}); err != nil {
		t.Fatalf("PutRequest: %v", err)
	}

	for _, date := range []string{"2026-08-26", "2026/08/26"} {
		t.Run(date, func(t *testing.T) {
			ids, err := s.ListRequestsInDay(date)
			if err != nil {
				t.Fatalf("ListRequestsInDay(%q): %v", date, err)
			}
			if len(ids) != 1 || ids[0] != "date-format" {
				t.Fatalf("ListRequestsInDay(%q) = %v", date, ids)
			}
		})
	}
}

func TestListRequestsInDayRejectsInvalidDate(t *testing.T) {
	s, _ := tempStore(t)
	defer s.Close()

	for _, date := range []string{"", "../2026/08/26", "2026/13/01", "2026-8-26", "2026/08/26/.."} {
		t.Run(date, func(t *testing.T) {
			if _, err := s.ListRequestsInDay(date); err == nil {
				t.Fatalf("ListRequestsInDay(%q) unexpectedly succeeded", date)
			}
		})
	}
}
