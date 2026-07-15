package distribution

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
)

// FileHandler serves offline artifacts with ticket validation.
type FileHandler struct {
	root    string
	tickets *TicketSigner
	store   Store
}

func NewFileHandler(root string, tickets *TicketSigner, store Store) *FileHandler {
	if root == "" {
		root = "/var/www/download/llm-gateway-go"
	}
	return &FileHandler{root: filepath.Clean(root), tickets: tickets, store: store}
}

func (h *FileHandler) RegisterRoutes(e *echo.Echo) {
	e.GET("/llm-gateway-go/:version/:filename", h.Serve)
}

func (h *FileHandler) Serve(c echo.Context) error {
	token := strings.TrimSpace(c.QueryParam("ticket"))
	if token == "" {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "ticket required"})
	}
	claims, err := h.tickets.Verify(token)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid or expired ticket"})
	}

	version := c.Param("version")
	filename := c.Param("filename")
	if version == "" || filename == "" || strings.Contains(filename, "..") {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid path"})
	}
	if claims.ReleaseVersion != version {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "ticket version mismatch"})
	}
	expected := artifactFileName(version, claims.Platform, claims.Arch)
	if filename != expected {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "ticket artifact mismatch"})
	}

	path := filepath.Join(h.root, version, filename)
	if !strings.HasPrefix(filepath.Clean(path), h.root+string(os.PathSeparator)) && filepath.Clean(path) != h.root {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid path"})
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Warn("artifact missing", "path", path, "request_id", claims.RequestID)
			return c.JSON(http.StatusNotFound, map[string]string{"error": "artifact not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "open failed"})
	}
	_ = f.Close()

	c.Response().Header().Set("X-Download-Request-ID", claims.RequestID)
	return c.Attachment(path, filename)
}
