// Package metatools implements Phase 2 meta-tool handlers for layered tool loading.
//
// Meta-tools allow clients to start with minimal tool definitions (list_categories, load_tools)
// and dynamically load tool categories on demand, reducing initial request size by 96%.
package metatools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/lib/pq"
)

// Category represents a tool category with metadata.
type Category struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	ToolCount   int    `json:"tool_count"`
}

// LoadToolsArgs represents arguments for load_tools meta-tool.
type LoadToolsArgs struct {
	Categories []string `json:"categories"`
}

// LoadToolsResult represents the result of loading tools from categories.
type LoadToolsResult struct {
	LoadedCategories []string          `json:"loaded_categories"`
	Tools            []json.RawMessage `json:"tools"`
	TotalCount       int               `json:"total_count"`
}

// Handler provides meta-tool operations (list_categories, load_tools).
type Handler struct {
	db *sql.DB
}

// NewHandler creates a new meta-tool handler.
func NewHandler(db *sql.DB) *Handler {
	return &Handler{db: db}
}

// ListCategories returns all available tool categories with tool counts.
func (h *Handler) ListCategories(ctx context.Context) ([]Category, error) {
	query := `
		SELECT c.id, c.name, c.description, COUNT(t.id) as tool_count
		FROM tool_categories c
		LEFT JOIN tool_registry t ON c.id = t.category AND t.enabled = true
		WHERE c.enabled = true
		GROUP BY c.id, c.name, c.description
		ORDER BY c.display_order
	`

	rows, err := h.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query categories: %w", err)
	}
	defer rows.Close()

	var categories []Category
	for rows.Next() {
		var cat Category
		if err := rows.Scan(&cat.ID, &cat.Name, &cat.Description, &cat.ToolCount); err != nil {
			return nil, fmt.Errorf("scan category: %w", err)
		}
		categories = append(categories, cat)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate categories: %w", err)
	}

	return categories, nil
}

// LoadTools loads tool definitions from specified categories.
func (h *Handler) LoadTools(ctx context.Context, categories []string) (*LoadToolsResult, error) {
	if len(categories) == 0 {
		return &LoadToolsResult{
			LoadedCategories: []string{},
			Tools:            []json.RawMessage{},
			TotalCount:       0,
		}, nil
	}

	query := `
		SELECT tool_name, tool_definition
		FROM tool_registry
		WHERE category = ANY($1) AND enabled = true
		ORDER BY priority DESC, tool_name
	`

	rows, err := h.db.QueryContext(ctx, query, pq.Array(categories))
	if err != nil {
		return nil, fmt.Errorf("query tools: %w", err)
	}
	defer rows.Close()

	var tools []json.RawMessage
	for rows.Next() {
		var name string
		var def json.RawMessage
		if err := rows.Scan(&name, &def); err != nil {
			return nil, fmt.Errorf("scan tool: %w", err)
		}
		tools = append(tools, def)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tools: %w", err)
	}

	return &LoadToolsResult{
		LoadedCategories: categories,
		Tools:            tools,
		TotalCount:       len(tools),
	}, nil
}

// MetaToolDefinitions returns the two meta-tool definitions that clients should use initially.
func MetaToolDefinitions() []json.RawMessage {
	return []json.RawMessage{
		json.RawMessage(`{
			"type": "function",
			"function": {
				"name": "list_categories",
				"description": "List all available tool categories with descriptions and tool counts. Use this to discover what tool categories are available before loading specific tools.",
				"parameters": {
					"type": "object",
					"properties": {},
					"required": []
				}
			}
		}`),
		json.RawMessage(`{
			"type": "function",
			"function": {
				"name": "load_tools",
				"description": "Load tool definitions from specified categories. The loaded tools will be available for use in subsequent requests. Use list_categories first to see available categories.",
				"parameters": {
					"type": "object",
					"properties": {
						"categories": {
							"type": "array",
							"items": {
								"type": "string"
							},
							"description": "List of category IDs to load (e.g., ['filesystem', 'web_search'])"
						}
					},
					"required": ["categories"]
				}
			}
		}`),
	}
}
