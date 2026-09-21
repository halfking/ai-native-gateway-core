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

func TestRequestDateShardsRoundTripAcrossCalendarBoundaries(t *testing.T) {
	s, _ := tempStore(t)
	defer s.Close()

	cases := []struct {
		id   string
		date time.Time
	}{
		{id: "month-end", date: time.Date(2026, time.January, 31, 23, 59, 59, 0, time.UTC)},
		{id: "month-start", date: time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)},
		{id: "year-end", date: time.Date(2026, time.December, 31, 23, 59, 59, 0, time.UTC)},
		{id: "year-start", date: time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		if err := s.PutRequest(RequestRecord{ID: tc.id, StartedAt: tc.date}); err != nil {
			t.Fatalf("PutRequest(%s): %v", tc.id, err)
		}
	}

	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			got, err := s.GetRequest(tc.id)
			if err != nil {
				t.Fatalf("GetRequest(%s): %v", tc.id, err)
			}
			if got.ID != tc.id {
				t.Fatalf("GetRequest(%s).ID = %q", tc.id, got.ID)
			}
			ids, err := s.ListRequestsInDay(tc.date.Format("2006-01-02"))
			if err != nil {
				t.Fatalf("ListRequestsInDay: %v", err)
			}
			if len(ids) != 1 || ids[0] != tc.id {
				t.Fatalf("ListRequestsInDay(%s) = %v", tc.date.Format("2006-01-02"), ids)
			}
		})
	}
}
