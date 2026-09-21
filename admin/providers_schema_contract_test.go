package admin

import (
	"os"
	"strings"
	"testing"
)

// TestUpdateProviderUsesSchemaName pins the providers schema contract while
// keeping the legacy name JSON alias available to existing clients.
func TestUpdateProviderUsesSchemaName(t *testing.T) {
	src, err := os.ReadFile("providers.go")
	if err != nil {
		t.Fatalf("read providers.go: %v", err)
	}
	body := string(src)

	if strings.Contains(body, "UPDATE providers SET name") {
		t.Fatal("updateProvider still writes nonexistent providers.name")
	}
	if !strings.Contains(body, "if req.Name != nil && req.DisplayName == nil") {
		t.Fatal("legacy name alias must only update display_name when display_name is absent")
	}
	if !strings.Contains(body, "UPDATE providers SET display_name = $1, updated_at = now() WHERE id = $2") {
		t.Fatal("provider name updates must use providers.display_name")
	}
}

func TestProvidersSchemaDoesNotDefineNameColumn(t *testing.T) {
	schema, err := os.ReadFile("../sql/objects/tables/providers.sql")
	if err != nil {
		t.Fatalf("read providers schema: %v", err)
	}
	if strings.Contains(string(schema), " name ") {
		t.Fatal("providers schema unexpectedly defines a name column; use display_name consistently")
	}
	if !strings.Contains(string(schema), "display_name") {
		t.Fatal("providers schema is missing display_name")
	}
}
