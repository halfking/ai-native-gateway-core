// Unit tests for the storage overview helpers (2026-07-01).
//
// We test pure functions (humanBytes, queryFilesystem) without a real DB.
// The DB-bound functions (queryDatabaseStorage, queryTableSizes) require
// a live pgxpool.Pool and are exercised via the existing data lifecycle
// partition_test.go in CI with the test harness.

package admin

import (
	"strings"
	"testing"
)

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1 MB"},
		{2 * 1024 * 1024 * 1024, "2 GB"},
		{5 * 1024 * 1024 * 1024 * 1024, "5 TB"},
	}
	for _, tt := range tests {
		got := humanBytes(tt.in)
		if got != tt.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestHumanBytesExportAlias(t *testing.T) {
	// humanBytesExport should be the same as humanBytes
	if humanBytesExport(1024) != humanBytes(1024) {
		t.Error("export alias mismatch")
	}
}

func TestFormatInt(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{42, "42"},
		{12345, "12345"},
		{-7, "-7"},
	}
	for _, tt := range tests {
		got := formatInt(tt.in)
		if got != tt.want {
			t.Errorf("formatInt(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFormatFloat(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{0, "0.0"},
		{1.5, "1.5"},
		{10.25, "10.2"},
		{1234.5, "1234.5"},
	}
	for _, tt := range tests {
		got := formatFloat(tt.in)
		if got != tt.want {
			t.Errorf("formatFloat(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestQueryFilesystem(t *testing.T) {
	// 探测当前目录（保证测试在所有环境都能跑）
	info, err := queryFilesystem(".")
	if err != nil {
		t.Fatalf("queryFilesystem failed: %v", err)
	}
	if info.TotalBytes <= 0 {
		t.Errorf("expected TotalBytes > 0, got %d", info.TotalBytes)
	}
	if info.FreeBytes < 0 {
		t.Errorf("expected FreeBytes >= 0, got %d", info.FreeBytes)
	}
	if info.UsedBytes+info.FreeBytes > info.TotalBytes {
		t.Errorf("used(%d) + free(%d) > total(%d)",
			info.UsedBytes, info.FreeBytes, info.TotalBytes)
	}
	if info.UsedPercent < 0 || info.UsedPercent > 100 {
		t.Errorf("UsedPercent out of range: %d", info.UsedPercent)
	}
	if !strings.HasPrefix(info.Path, "/") {
		t.Errorf("Path should be absolute, got %q", info.Path)
	}
}

func TestQueryDirectory(t *testing.T) {
	// 不存在的路径
	info := queryDirectory("/this/path/should/not/exist/1234567")
	if info.Exists {
		t.Error("expected Exists=false for non-existent path")
	}
	if info.Files != 0 || info.SizeBytes != 0 {
		t.Errorf("expected zero counts, got files=%d bytes=%d", info.Files, info.SizeBytes)
	}

	// "." 一定存在
	cwd := queryDirectory(".")
	if !cwd.Exists {
		t.Error("expected Exists=true for current dir")
	}
	if !strings.HasPrefix(cwd.Path, "/") {
		t.Errorf("Path should be absolute, got %q", cwd.Path)
	}
}

func TestSortTablesDesc(t *testing.T) {
	tables := []tableSizeInfo{
		{Table: "a", TotalBytes: 100},
		{Table: "b", TotalBytes: 500},
		{Table: "c", TotalBytes: 200},
	}
	sortTablesDesc(tables)
	if tables[0].Table != "b" || tables[1].Table != "c" || tables[2].Table != "a" {
		t.Errorf("sort failed: %v", []string{tables[0].Table, tables[1].Table, tables[2].Table})
	}
}
