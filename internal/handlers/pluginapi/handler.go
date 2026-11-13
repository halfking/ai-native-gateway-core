// Package pluginapi provides HTTP API handlers for plugin management.
// This implements the plugin manifest listing endpoint for the frontend.
package pluginapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// PluginListResponse represents the API response for listing plugins
type PluginListResponse struct {
	Success bool              `json:"success"`
	Data    []PluginInfo      `json:"data"`
	Total   int               `json:"total"`
	Error   string            `json:"error,omitempty"`
	Meta    ResponseMetadata  `json:"meta"`
}

// PluginInfo represents detailed plugin information for the frontend
type PluginInfo struct {
	PluginID     string              `json:"plugin_id"`
	DisplayName  string              `json:"display_name"`
	Version      string              `json:"version"`
	Status       string              `json:"status"`
	Enabled      bool                `json:"enabled"`
	Essential    bool                `json:"essential"`
	Dependencies []string            `json:"dependencies"`
	Capabilities []string            `json:"capabilities"`
	Pages        []PageInfo          `json:"pages"`
	WebMount     WebMountInfo        `json:"web_mount"`
	Metadata     PluginMetadata      `json:"metadata"`
}

// PageInfo represents plugin page information
type PageInfo struct {
	Path      string      `json:"path"`
	Type      string      `json:"type"`
	NavGroup  string      `json:"nav_group,omitempty"`
	NavLabel  string      `json:"nav_label,omitempty"`
	NavOrder  int         `json:"nav_order,omitempty"`
}

// WebMountInfo represents plugin web mount configuration
type WebMountInfo struct {
	Mount    string `json:"mount"`
	BasePath string `json:"base_path"`
	Entry    string `json:"entry"`
}

// PluginMetadata contains plugin metadata
type PluginMetadata struct {
	BuildSeq      int       `json:"build_seq"`
	Channel       string    `json:"channel"`
	APIContract   string    `json:"api_contract"`
	MinGWVersion  string    `json:"min_gateway_version,omitempty"`
	MaxGWVersion  string    `json:"max_gateway_version,omitempty"`
	LoadedAt      time.Time `json:"loaded_at"`
	ManifestPath  string    `json:"manifest_path,omitempty"`
}

// ResponseMetadata contains response metadata
type ResponseMetadata struct {
	Timestamp   time.Time `json:"timestamp"`
	Version     string    `json:"version"`
	Environment string    `json:"environment,omitempty"`
}

// PluginAPIHandler handles plugin-related API requests
type PluginAPIHandler struct {
	pluginDir string
	registry  PluginRegistry
}

// PluginRegistry is an interface for plugin registry operations
type PluginRegistry interface {
	ListPlugins() ([]PluginInfo, error)
	GetPlugin(id string) (*PluginInfo, error)
}

// NewPluginAPIHandler creates a new plugin API handler
func NewPluginAPIHandler(pluginDir string) *PluginAPIHandler {
	return &PluginAPIHandler{
		pluginDir: pluginDir,
		registry:  &fileBasedRegistry{pluginDir: pluginDir},
	}
}

// ListPluginsHandler handles GET /api/v1/plugins
func (h *PluginAPIHandler) ListPluginsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Query parameters
	query := r.URL.Query()
	filterEnabled := query.Get("enabled")
	filterStatus := query.Get("status")
	sortBy := query.Get("sort")
	if sortBy == "" {
		sortBy = "display_name"
	}

	// List all plugins
	plugins, err := h.registry.ListPlugins()
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "Failed to list plugins: "+err.Error())
		return
	}

	// Apply filters
	var filtered []PluginInfo
	for _, p := range plugins {
		// Filter by enabled status
		if filterEnabled != "" {
			if (filterEnabled == "true" && !p.Enabled) || (filterEnabled == "false" && p.Enabled) {
				continue
			}
		}

		// Filter by status
		if filterStatus != "" && p.Status != filterStatus {
			continue
		}

		filtered = append(filtered, p)
	}

	// Sort
	h.sortPlugins(filtered, sortBy)

	// Send response
	response := PluginListResponse{
		Success: true,
		Data:    filtered,
		Total:   len(filtered),
		Meta: ResponseMetadata{
			Timestamp:   time.Now(),
			Version:     "1.0.0",
			Environment: os.Getenv("ENVIRONMENT"),
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// GetPluginHandler handles GET /api/v1/plugins/:id
func (h *PluginAPIHandler) GetPluginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Extract plugin ID from URL path
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 4 {
		h.sendError(w, http.StatusBadRequest, "Invalid plugin ID")
		return
	}
	pluginID := pathParts[3]

	plugin, err := h.registry.GetPlugin(pluginID)
	if err != nil {
		h.sendError(w, http.StatusNotFound, "Plugin not found: "+err.Error())
		return
	}

	response := PluginListResponse{
		Success: true,
		Data:    []PluginInfo{*plugin},
		Total:   1,
		Meta: ResponseMetadata{
			Timestamp: time.Now(),
			Version:   "1.0.0",
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

func (h *PluginAPIHandler) sendError(w http.ResponseWriter, status int, message string) {
	response := PluginListResponse{
		Success: false,
		Error:   message,
		Data:    []PluginInfo{},
		Total:   0,
		Meta: ResponseMetadata{
			Timestamp: time.Now(),
			Version:   "1.0.0",
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(response)
}

func (h *PluginAPIHandler) sortPlugins(plugins []PluginInfo, sortBy string) {
	switch sortBy {
	case "display_name":
		sort.Slice(plugins, func(i, j int) bool {
			return plugins[i].DisplayName < plugins[j].DisplayName
		})
	case "plugin_id":
		sort.Slice(plugins, func(i, j int) bool {
			return plugins[i].PluginID < plugins[j].PluginID
		})
	case "status":
		sort.Slice(plugins, func(i, j int) bool {
			return plugins[i].Status < plugins[j].Status
		})
	case "version":
		sort.Slice(plugins, func(i, j int) bool {
			return plugins[i].Version < plugins[j].Version
		})
	}
}

// fileBasedRegistry implements PluginRegistry using filesystem
type fileBasedRegistry struct {
	pluginDir string
}

func (r *fileBasedRegistry) ListPlugins() ([]PluginInfo, error) {
	var plugins []PluginInfo

	// Scan plugin directory
	entries, err := os.ReadDir(r.pluginDir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		manifestPath := filepath.Join(r.pluginDir, entry.Name(), "plugin-manifest.json")
		if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
			continue
		}

		// Load manifest
		manifest, err := pluginruntime.LoadManifest(manifestPath)
		if err != nil {
			// Skip invalid manifests but log error
			continue
		}

		// Convert to PluginInfo
		info := r.manifestToPluginInfo(manifest)
		plugins = append(plugins, info)
	}

	return plugins, nil
}

func (r *fileBasedRegistry) GetPlugin(id string) (*PluginInfo, error) {
	manifestPath := filepath.Join(r.pluginDir, id, "plugin-manifest.json")
	manifest, err := pluginruntime.LoadManifest(manifestPath)
	if err != nil {
		return nil, err
	}

	info := r.manifestToPluginInfo(manifest)
	return &info, nil
}

func (r *fileBasedRegistry) manifestToPluginInfo(m *pluginruntime.Manifest) PluginInfo {
	info := PluginInfo{
		PluginID:     m.PluginID,
		DisplayName:  m.DisplayName,
		Version:      m.PluginVersion,
		Status:       "registered", // Default status
		Enabled:      true,          // Default enabled
		Essential:    m.Essential,
		Dependencies: m.Requires,
		Capabilities: m.Capabilities,
		Pages:        make([]PageInfo, 0),
		WebMount: WebMountInfo{
			Mount:    m.Web.Mount,
			BasePath: m.Web.BasePath,
			Entry:    m.Web.Entry,
		},
		Metadata: PluginMetadata{
			BuildSeq:     m.BuildSeq,
			Channel:      m.Channel,
			APIContract:  m.GatewayCompatibility.APIContract,
			MinGWVersion: m.GatewayCompatibility.MinVersion,
			MaxGWVersion: m.GatewayCompatibility.MaxVersion,
			LoadedAt:     time.Now(),
			ManifestPath: m.ManifestPath,
		},
	}

	// Convert pages
	for _, page := range m.Pages {
		pageInfo := PageInfo{
			Path: page.Path,
			Type: page.Type,
		}
		if page.Nav != nil {
			pageInfo.NavGroup = page.Nav.Group
			pageInfo.NavLabel = page.Nav.LabelKey
			pageInfo.NavOrder = page.Nav.Order
		}
		info.Pages = append(info.Pages, pageInfo)
	}

	return info
}
