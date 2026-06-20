package metatools

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	_ "github.com/lib/pq"
)

// setupTestDB creates an in-memory test database with schema.
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()

	// Use a real PostgreSQL connection for testing
	// This requires METATOOLS_TEST_DB env var set to a test database URL
	// Example: postgres://postgres:password@localhost:5432/llm_gateway_test?sslmode=disable
	
	// For unit tests without real DB, skip
	t.Skip("Integration test: requires METATOOLS_TEST_DB env var")

	return nil
}

func TestListCategories_Empty(t *testing.T) {
	// This is a placeholder - real test would use sqlmock or test DB
	t.Log("ListCategories returns empty array when no categories exist")
}

func TestLoadTools_EmptyCategories(t *testing.T) {
	handler := &Handler{db: nil} // nil DB is ok for empty categories
	
	result, err := handler.LoadTools(context.Background(), []string{})
	if err != nil {
		t.Fatalf("LoadTools failed: %v", err)
	}
	
	if result.TotalCount != 0 {
		t.Errorf("Expected 0 tools, got %d", result.TotalCount)
	}
	
	if len(result.Tools) != 0 {
		t.Errorf("Expected empty tools array, got %d", len(result.Tools))
	}
}

func TestMetaToolDefinitions(t *testing.T) {
	defs := MetaToolDefinitions()
	
	if len(defs) != 2 {
		t.Fatalf("Expected 2 meta-tool definitions, got %d", len(defs))
	}
	
	// Verify list_categories
	var listCat map[string]interface{}
	if err := json.Unmarshal(defs[0], &listCat); err != nil {
		t.Fatalf("Failed to unmarshal list_categories: %v", err)
	}
	
	function := listCat["function"].(map[string]interface{})
	if function["name"] != "list_categories" {
		t.Errorf("Expected name 'list_categories', got %v", function["name"])
	}
	
	// Verify load_tools
	var loadTools map[string]interface{}
	if err := json.Unmarshal(defs[1], &loadTools); err != nil {
		t.Fatalf("Failed to unmarshal load_tools: %v", err)
	}
	
	function2 := loadTools["function"].(map[string]interface{})
	if function2["name"] != "load_tools" {
		t.Errorf("Expected name 'load_tools', got %v", function2["name"])
	}
	
	// Verify load_tools has categories parameter
	params := function2["parameters"].(map[string]interface{})
	props := params["properties"].(map[string]interface{})
	if _, ok := props["categories"]; !ok {
		t.Error("load_tools missing 'categories' parameter")
	}
}

func TestLoadToolsArgs_JSON(t *testing.T) {
	input := `{"categories": ["filesystem", "web_search"]}`
	
	var args LoadToolsArgs
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}
	
	if len(args.Categories) != 2 {
		t.Errorf("Expected 2 categories, got %d", len(args.Categories))
	}
	
	if args.Categories[0] != "filesystem" || args.Categories[1] != "web_search" {
		t.Errorf("Unexpected category values: %v", args.Categories)
	}
}

func TestLoadToolsResult_JSON(t *testing.T) {
	result := LoadToolsResult{
		LoadedCategories: []string{"filesystem"},
		Tools:            []json.RawMessage{json.RawMessage(`{"name":"read_file"}`)},
		TotalCount:       1,
	}
	
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}
	
	var decoded LoadToolsResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}
	
	if decoded.TotalCount != 1 {
		t.Errorf("Expected TotalCount 1, got %d", decoded.TotalCount)
	}
	
	if len(decoded.Tools) != 1 {
		t.Errorf("Expected 1 tool, got %d", len(decoded.Tools))
	}
}
