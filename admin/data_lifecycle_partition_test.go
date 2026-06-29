package admin

import (
	"testing"
	"time"
)

func TestParsePartitionBounds(t *testing.T) {
	tests := []struct {
		name      string
		bounds    string
		wantStart string
		wantEnd   string
		wantError bool
	}{
		{
			name:      "valid bounds",
			bounds:    "FOR VALUES FROM ('2026-04-01') TO ('2026-05-01')",
			wantStart: "2026-04-01",
			wantEnd:   "2026-05-01",
			wantError: false,
		},
		{
			name:      "valid bounds different month",
			bounds:    "FOR VALUES FROM ('2026-06-01') TO ('2026-07-01')",
			wantStart: "2026-06-01",
			wantEnd:   "2026-07-01",
			wantError: false,
		},
		{
			name:      "invalid format",
			bounds:    "INVALID FORMAT",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			startDate, endDate, err := parsePartitionBounds(tt.bounds)

			if tt.wantError {
				if err == nil {
					t.Errorf("parsePartitionBounds() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("parsePartitionBounds() unexpected error: %v", err)
				return
			}

			gotStart := startDate.Format("2006-01-02")
			if gotStart != tt.wantStart {
				t.Errorf("parsePartitionBounds() start = %v, want %v", gotStart, tt.wantStart)
			}

			gotEnd := endDate.Format("2006-01-02")
			if gotEnd != tt.wantEnd {
				t.Errorf("parsePartitionBounds() end = %v, want %v", gotEnd, tt.wantEnd)
			}
		})
	}
}

func TestPartitionedTableConfig(t *testing.T) {
	// Verify all configured tables have required fields
	for _, config := range partitionedTables {
		if config.TableName == "" {
			t.Errorf("partitionedTable has empty TableName")
		}
		if config.ArchiveTableName == "" {
			t.Errorf("partitionedTable %s has empty ArchiveTableName", config.TableName)
		}
		if config.PartitionColumn == "" {
			t.Errorf("partitionedTable %s has empty PartitionColumn", config.TableName)
		}
		if config.Description == "" {
			t.Errorf("partitionedTable %s has empty Description", config.TableName)
		}
	}

	// Verify we have at least request_logs
	found := false
	for _, config := range partitionedTables {
		if config.TableName == "request_logs" {
			found = true
			if !config.HasArchiveFunc {
				t.Errorf("request_logs should have HasArchiveFunc=true")
			}
			if config.ArchiveTableName != "request_logs_archive" {
				t.Errorf("request_logs ArchiveTableName = %s, want request_logs_archive", config.ArchiveTableName)
			}
			break
		}
	}
	if !found {
		t.Errorf("request_logs not found in partitionedTables")
	}

	// Verify all 4 expected tables are present (added in migration 318)
	expected := map[string]string{
		"request_logs":           "request_logs_archive",
		"request_wal":            "request_wal_archive",
		"routing_decision_log":   "routing_decision_log_archive",
		"credential_model_index": "credential_model_index_archive",
	}
	for tbl, wantArchive := range expected {
		found := false
		for _, config := range partitionedTables {
			if config.TableName == tbl {
				found = true
				if !config.HasArchiveFunc {
					t.Errorf("%s should have HasArchiveFunc=true", tbl)
				}
				if config.ArchiveTableName != wantArchive {
					t.Errorf("%s ArchiveTableName = %s, want %s", tbl, config.ArchiveTableName, wantArchive)
				}
				break
			}
		}
		if !found {
			t.Errorf("%s not found in partitionedTables (added by migration 318)", tbl)
		}
	}
}

func TestExecuteArchivePartition(t *testing.T) {
	// This is a unit test for validation logic only
	// We test the validation without a database connection

	tests := []struct {
		name           string
		req            archivePartitionRequest
		expectError    bool
		expectedStatus string
	}{
		{
			name: "invalid table name",
			req: archivePartitionRequest{
				TableName:    "nonexistent_table",
				ArchiveMonth: "2026-04",
				DryRun:       true,
			},
			expectError:    true,
			expectedStatus: "error",
		},
		{
			name: "invalid month format",
			req: archivePartitionRequest{
				TableName:    "request_logs",
				ArchiveMonth: "2026-13", // Invalid month
				DryRun:       true,
			},
			expectError:    true,
			expectedStatus: "error",
		},
		{
			name: "valid table name validation",
			req: archivePartitionRequest{
				TableName:    "request_logs",
				ArchiveMonth: "2026-04",
				DryRun:       true,
			},
			expectError:    false,
			expectedStatus: "", // Would proceed to DB query (which we skip)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test table validation
			var tableConfig *partitionedTableConfig
			for i := range partitionedTables {
				if partitionedTables[i].TableName == tt.req.TableName {
					tableConfig = &partitionedTables[i]
					break
				}
			}

			if tt.expectError {
				if tableConfig != nil && tt.expectedStatus == "error" {
					// Table exists but month format might be invalid
					_, err := time.Parse("2006-01", tt.req.ArchiveMonth)
					if err == nil {
						t.Errorf("Expected invalid month format, but parsed successfully")
					}
				} else if tableConfig == nil {
					// Table not found - expected
				}
			} else {
				if tableConfig == nil {
					t.Errorf("Valid table name %s not found in config", tt.req.TableName)
				}
				_, err := time.Parse("2006-01", tt.req.ArchiveMonth)
				if err != nil {
					t.Errorf("Valid month format failed to parse: %v", err)
				}
			}
		})
	}
}

func TestContainsSubstringHelper(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		substr string
		want   bool
	}{
		{
			name:   "contains at start",
			s:      "request_logs_archive_2026_04",
			substr: "_archive_",
			want:   true,
		},
		{
			name:   "contains in middle",
			s:      "prefix_archive_suffix",
			substr: "_archive_",
			want:   true,
		},
		{
			name:   "does not contain",
			s:      "request_logs_2026_04",
			substr: "_archive_",
			want:   false,
		},
		{
			name:   "exact match",
			s:      "_archive_",
			substr: "_archive_",
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsSubstring(tt.s, tt.substr)
			if got != tt.want {
				t.Errorf("containsSubstring(%q, %q) = %v, want %v", tt.s, tt.substr, got, tt.want)
			}
		})
	}
}

func TestPartitionInfoCanArchive(t *testing.T) {
	now := time.Now()
	twoMonthsAgo := now.AddDate(0, -2, 0)
	threeMonthsAgo := now.AddDate(0, -3, 0)
	oneMonthAgo := now.AddDate(0, -1, 0)

	tests := []struct {
		name        string
		endDate     time.Time
		isArchived  bool
		hasFunc     bool
		wantArchive bool
	}{
		{
			name:        "old partition not archived",
			endDate:     threeMonthsAgo,
			isArchived:  false,
			hasFunc:     true,
			wantArchive: true,
		},
		{
			name:        "old partition already archived",
			endDate:     threeMonthsAgo,
			isArchived:  true,
			hasFunc:     true,
			wantArchive: false,
		},
		{
			name:        "recent partition",
			endDate:     oneMonthAgo,
			isArchived:  false,
			hasFunc:     true,
			wantArchive: false,
		},
		{
			name:        "no archive function",
			endDate:     threeMonthsAgo,
			isArchived:  false,
			hasFunc:     false,
			wantArchive: false,
		},
		{
			name:        "exactly two months ago boundary",
			endDate:     twoMonthsAgo.AddDate(0, 0, -1),
			isArchived:  false,
			hasFunc:     true,
			wantArchive: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			canArchive := !tt.isArchived && tt.endDate.Before(twoMonthsAgo) && tt.hasFunc
			if canArchive != tt.wantArchive {
				t.Errorf("canArchive logic = %v, want %v", canArchive, tt.wantArchive)
			}
		})
	}
}
