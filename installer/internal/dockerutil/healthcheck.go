package dockerutil

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// HealthReport 健康检查结果
type HealthReport struct {
	ContainersOK bool
	HealthzOK    bool
	PGReadyOK    bool
	RedisOK      bool
	SchemaOK     bool
}

// String 格式化输出
func (h *HealthReport) String() string {
	return fmt.Sprintf(
		"  容器 Running:     %s\n  /healthz:        %s\n  PostgreSQL:      %s\n  Redis:           %s\n  Schema loaded:   %s",
		boolMark(h.ContainersOK),
		boolMark(h.HealthzOK),
		boolMark(h.PGReadyOK),
		boolMark(h.RedisOK),
		boolMark(h.SchemaOK),
	)
}

func boolMark(b bool) string {
	if b {
		return "✅"
	}
	return "❌"
}

// AllOK 全部通过？
func (h *HealthReport) AllOK() bool {
	return h.ContainersOK && h.HealthzOK && h.PGReadyOK && h.RedisOK && h.SchemaOK
}

// HealthChecker 健康检查器
type HealthChecker struct {
	AppPort        int
	CitusContainer string
	RedisContainer string
	RedisPassword  string
	DBUser         string
	DBName         string
}

// NewHealthChecker 创建 HealthChecker
func NewHealthChecker(appPort int, citusContainer, redisContainer, redisPassword, dbUser, dbName string) *HealthChecker {
	return &HealthChecker{
		AppPort:        appPort,
		CitusContainer: citusContainer,
		RedisContainer: redisContainer,
		RedisPassword:  redisPassword,
		DBUser:         dbUser,
		DBName:         dbName,
	}
}

// RunAll 运行所有健康检查
func (h *HealthChecker) RunAll(logger func(string)) (*HealthReport, error) {
	r := &HealthReport{}

	logger("▶ [1/5] 检查容器状态 ...")
	r.ContainersOK = h.checkContainers()
	logger(fmt.Sprintf("  %s", boolMark(r.ContainersOK)))

	logger("▶ [2/5] 检查应用 /healthz 端点 ...")
	r.HealthzOK = h.checkHealthz()
	logger(fmt.Sprintf("  %s", boolMark(r.HealthzOK)))

	logger("▶ [3/5] 检查 PostgreSQL ...")
	r.PGReadyOK = h.checkPG()
	logger(fmt.Sprintf("  %s", boolMark(r.PGReadyOK)))

	logger("▶ [4/5] 检查 Redis ...")
	r.RedisOK = h.checkRedis()
	logger(fmt.Sprintf("  %s", boolMark(r.RedisOK)))

	logger("▶ [5/5] 检查数据库 schema ...")
	r.SchemaOK = h.checkSchema()
	logger(fmt.Sprintf("  %s", boolMark(r.SchemaOK)))

	return r, nil
}

// checkContainers 检查 3 个容器都在 Running
func (h *HealthChecker) checkContainers() bool {
	containers := []string{h.CitusContainer, h.RedisContainer, "kx-llm-gateway-go"}
	for _, c := range containers {
		if !h.containerRunning(c) {
			return false
		}
	}
	return true
}

func (h *HealthChecker) containerRunning(name string) bool {
	cmd := exec.Command("docker", "ps", "--filter", fmt.Sprintf("name=%s", name), "--format", "{{.Names}}")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), name)
}

// checkHealthz 检查应用 healthz
func (h *HealthChecker) checkHealthz() bool {
	url := fmt.Sprintf("http://localhost:%d/healthz", h.AppPort)
	client := &http.Client{Timeout: 5 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// checkPG 检查 PG ready
func (h *HealthChecker) checkPG() bool {
	cmd := exec.Command("docker", "exec", h.CitusContainer, "pg_isready", "-U", h.DBUser)
	return cmd.Run() == nil
}

// checkRedis 检查 Redis PONG
func (h *HealthChecker) checkRedis() bool {
	cmd := exec.Command("docker", "exec", "-e", "REDISCLI_AUTH="+h.RedisPassword,
		h.RedisContainer, "redis-cli", "ping")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "PONG")
}

// checkSchema 检查 schema 已加载（public 表数 > 0）
func (h *HealthChecker) checkSchema() bool {
	cmd := exec.Command("docker", "exec", h.CitusContainer, "psql",
		"-U", h.DBUser, "-d", h.DBName,
		"-t", "-c", "SELECT count(*) FROM information_schema.tables WHERE table_schema='public';")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	var count int
	fmt.Sscanf(string(out), "%d", &count)
	return count > 0
}
