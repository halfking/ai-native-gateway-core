package distribution

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

type PublishRun struct {
	ID             int64      `json:"id"`
	ReleaseVersion string     `json:"release_version"`
	BuildSeq       int        `json:"build_seq"`
	Status         string     `json:"status"`
	ArtifactCount  int        `json:"artifact_count"`
	TestPassed     bool       `json:"test_passed"`
	LogSummary     string     `json:"log_summary,omitempty"`
	CreatedBy      string     `json:"created_by,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
}

type PublishStore interface {
	CreatePublishRun(ctx context.Context, run *PublishRun) error
	UpdatePublishRun(ctx context.Context, run *PublishRun) error
	ListPublishRuns(ctx context.Context, limit int) ([]PublishRun, error)
}

type PublishAdminAPI struct {
	store   Store
	catalog *CatalogService
	publish PublishStore
}

func NewPublishAdminAPI(store Store, catalog *CatalogService) *PublishAdminAPI {
	ps, _ := store.(PublishStore)
	return &PublishAdminAPI{store: store, catalog: catalog, publish: ps}
}

func (api *PublishAdminAPI) RegisterRoutes(g *echo.Group) {
	g.GET("/publish-runs", api.ListRuns)
	g.POST("/publish", api.Publish)
}

func (api *PublishAdminAPI) ListRuns(c echo.Context) error {
	if api.publish == nil {
		return c.JSON(http.StatusOK, map[string]interface{}{"items": []PublishRun{}})
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	if limit <= 0 {
		limit = 20
	}
	items, err := api.publish.ListPublishRuns(c.Request().Context(), limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list failed"})
	}
	if items == nil {
		items = []PublishRun{}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"items": items})
}

func (api *PublishAdminAPI) Publish(c echo.Context) error {
	var req struct {
		Version  string `json:"version"`
		BuildSeq int    `json:"build_seq"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}

	ctx := c.Request().Context()
	version := strings.TrimSpace(req.Version)
	buildSeq := req.BuildSeq
	if version == "" {
		cat, err := api.catalog.BuildCatalog(ctx)
		if err != nil || cat.Version == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "version required"})
		}
		version = cat.Version
		buildSeq = cat.BuildSeq
	}

	run := &PublishRun{
		ReleaseVersion: version,
		BuildSeq:       buildSeq,
		Status:         "running",
		CreatedBy:      actorFromContext(c),
		CreatedAt:      time.Now(),
	}
	if api.publish != nil {
		if err := api.publish.CreatePublishRun(ctx, run); err != nil {
			slog.Error("create publish run failed", "error", err)
		}
	}

	summary, artifactCount, testOK, err := executePublishScript(version)
	now := time.Now()
	run.FinishedAt = &now
	run.TestPassed = testOK
	run.ArtifactCount = artifactCount
	run.LogSummary = summary
	if err != nil {
		run.Status = "failed"
		if api.publish != nil {
			_ = api.publish.UpdatePublishRun(ctx, run)
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   err.Error(),
			"summary": summary,
			"run":     run,
		})
	}
	run.Status = "success"
	if api.publish != nil {
		_ = api.publish.UpdatePublishRun(ctx, run)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"status":  "success",
		"summary": summary,
		"run":     run,
	})
}

func actorFromContext(c echo.Context) string {
	if u := c.Get("username"); u != nil {
		if s, ok := u.(string); ok && s != "" {
			return s
		}
	}
	return "admin"
}

func executePublishScript(version string) (summary string, artifactCount int, testPassed bool, err error) {
	script := os.Getenv("DOWNLOAD_PUBLISH_SCRIPT")
	if script == "" {
		script = filepath.Join("scripts", "publish-download-release.sh")
	}
	if _, statErr := os.Stat(script); statErr != nil {
		return "", 0, false, fmt.Errorf("publish script not found: %s", script)
	}

	testPassed = true
	if os.Getenv("DOWNLOAD_PUBLISH_SKIP_TEST") != "true" {
		testCmd := exec.Command("go", "test", "./distribution/...", "-count=1")
		testCmd.Env = os.Environ()
		var testOut bytes.Buffer
		testCmd.Stdout = &testOut
		testCmd.Stderr = &testOut
		if runErr := testCmd.Run(); runErr != nil {
			testPassed = false
			return testOut.String(), 0, false, fmt.Errorf("distribution tests failed: %w", runErr)
		}
	}

	cmd := exec.Command("bash", script, version)
	cmd.Env = os.Environ()
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if runErr := cmd.Run(); runErr != nil {
		return out.String(), 0, testPassed, fmt.Errorf("publish script failed: %w", runErr)
	}

	var result struct {
		ArtifactCount int    `json:"artifact_count"`
		Summary       string `json:"summary"`
	}
	if jsonErr := json.Unmarshal(out.Bytes(), &result); jsonErr == nil && result.Summary != "" {
		return result.Summary, result.ArtifactCount, testPassed, nil
	}
	return strings.TrimSpace(out.String()), 1, testPassed, nil
}
