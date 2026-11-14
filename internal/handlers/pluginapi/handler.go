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
	"sync"
	"time"

	pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// PluginListResponse represents the API response for listing plugins
type PluginListResponse struct {
	Success bool             `json:"success"`
	Data    []PluginInfo     `json:"data"`
	Total   int              `json:"total"`
	Error   string           `json:"error,omitempty"`
	Meta    ResponseMetadata `json:"meta"`
}

// PluginInfo represents detailed plugin information for the frontend
type PluginInfo struct {
	PluginID     string         `json:"plugin_id"`
	DisplayName  string         `json:"display_name"`
	Version      string         `json:"version"`
	Status       string         `json:"status"`
	Enabled      bool           `json:"enabled"`
	Essential    bool           `json:"essential"`
	Dependencies []string       `json:"dependencies"`
	Capabilities []string       `json:"capabilities"`
	Pages        []PageInfo     `json:"pages"`
	WebMount     WebMountInfo   `json:"web_mount"`
	Metadata     PluginMetadata `json:"metadata"`
}

// PageInfo represents plugin page information
type PageInfo struct {
	Path     string `json:"path"`
	Type     string `json:"type"`
	NavGroup string `json:"nav_group,omitempty"`
	NavLabel string `json:"nav_label,omitempty"`
	NavOrder int    `json:"nav_order,omitempty"`
}

// WebMountInfo represents plugin web mount configuration
type WebMountInfo struct {
	Mount    string `json:"mount"`
	BasePath string `json:"base_path"`
	Entry    string `json:"entry"`
}

// PluginMetadata contains plugin metadata
type PluginMetadata struct {
	BuildSeq     int       `json:"build_seq"`
	Channel      string    `json:"channel"`
	APIContract  string    `json:"api_contract"`
	MinGWVersion string    `json:"min_gateway_version,omitempty"`
	MaxGWVersion string    `json:"max_gateway_version,omitempty"`
	LoadedAt     time.Time `json:"loaded_at"`
	ManifestPath string    `json:"manifest_path,omitempty"`
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
	// disabled记录运行期被停用的插件。文件注册表本身无状态（状态恒为
	// registered/Enabled=true），而 Shell的启用/禁用按钮需要可观察的状态变更，
	//故在进程内维护停用集合；网关重启后回到 manifest默认（启用）。
	mu       sync.RWMutex
	disabled map[string]bool
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
		disabled:  make(map[string]bool),
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

	// Apply runtime enable/disable overlay so the Shell sees real state changes.
	filtered = h.applyRuntimeState(filtered)

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
	// 2026-11-14 12h 审计轮补：路径穿越门。Go ServeMux 已对 %2e/%2f URL 解码，
	// id=".." 会拼出 pluginDir/../plugin-manifest.json（越一级读任意目录中
	// 恰好叫 plugin-manifest.json 的文件）。合法插件 ID 是单段目录名，
	// 含分隔符/相对段一律 400（fail-closed）。
	if pluginID == "" || pluginID == "." || pluginID == ".." ||
		strings.ContainsAny(pluginID, "/\\") || filepath.Base(pluginID) != pluginID {
		h.sendError(w, http.StatusBadRequest, "Invalid plugin ID")
		return
	}

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

// ActivatePluginHandler handles POST /api/v1/plugins/:id/activate
// 注意：仅切换运行期启用标记，不拉起/停止插件进程。真实进程编排由
// plugin-runtime.Supervisor负责，接入点见 DAY3-P0-FIX.md遗留项。
func (h *PluginAPIHandler) ActivatePluginHandler(w http.ResponseWriter, r *http.Request) {
	h.setEnabled(w, r, true)
}

// DeactivatePluginHandler handles POST /api/v1/plugins/:id/deactivate
func (h *PluginAPIHandler) DeactivatePluginHandler(w http.ResponseWriter, r *http.Request) {
	h.setEnabled(w, r, false)
}

func (h *PluginAPIHandler) setEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	if r.Method != http.MethodPost {
		h.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	pluginID, ok := pluginIDFromPath(r.URL.Path)
	if !ok {
		h.sendError(w, http.StatusBadRequest, "Invalid plugin ID")
		return
	}

	//插件必须真实存在，避免把任意 ID写进状态集合。
	if _, err := h.registry.GetPlugin(pluginID); err != nil {
		h.sendError(w, http.StatusNotFound, "Plugin not found: "+err.Error())
		return
	}

	h.mu.Lock()
	if enabled {
		delete(h.disabled, pluginID)
	} else {
		h.disabled[pluginID] = true
	}
	h.mu.Unlock()

	plugin, err := h.registry.GetPlugin(pluginID)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, err.Error())
		return
	}
	info := h.withRuntimeState(*plugin)

	response := PluginListResponse{
		Success: true,
		Data:    []PluginInfo{info},
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

// applyRuntimeState overlays the in-memory disabled set onto a plugin list.
func (h *PluginAPIHandler) applyRuntimeState(plugins []PluginInfo) []PluginInfo {
	for i := range plugins {
		plugins[i] = h.withRuntimeState(plugins[i])
	}
	return plugins
}

func (h *PluginAPIHandler) withRuntimeState(p PluginInfo) PluginInfo {
	h.mu.RLock()
	disabled := h.disabled[p.PluginID]
	h.mu.RUnlock()

	if disabled {
		p.Enabled = false
		p.Status = "disabled"
	}
	return p
}

// pluginIDFromPath extracts and validates the :id segment of /api/v1/plugins/:id[/action].
func pluginIDFromPath(path string) (string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	// api / v1 / plugins / <id> [/ <action>]
	if len(parts) < 4 {
		return "", false
	}
	id := parts[3]
	if id == "" || id == "." || id == ".." ||
		strings.ContainsAny(id, "/\\") || filepath.Base(id) != id {
		return "", false
	}
	return id, true
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
		Enabled:      true,         // Default enabled
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
