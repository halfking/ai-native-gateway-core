// Package dbinit 提供数据库初始化能力（应用 schema + seed）
package dbinit

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Runner DB 初始化执行器
type Runner struct {
	CitusContainer string  // "kx-citus"
	DBUser         string  // "kxuser"
	DBName         string  // "llm_gateway"
	SQLDir         string  // 包含 00-prereqs.sql / 01-schema.sql / 02-seed.sql
}

// NewRunner 创建 Runner
func NewRunner(citusContainer, dbUser, dbName, sqlDir string) *Runner {
	return &Runner{
		CitusContainer: citusContainer,
		DBUser:         dbUser,
		DBName:         dbName,
		SQLDir:         sqlDir,
	}
}

// WaitForPG 等待 PG 就绪（最多 timeout 秒）
func (r *Runner) WaitForPG(timeout int) error {
	for i := 0; i < timeout; i++ {
		cmd := exec.Command("docker", "exec", r.CitusContainer, "pg_isready", "-U", r.DBUser)
		if err := cmd.Run(); err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("PostgreSQL 启动超时 (%ds)", timeout)
}

// InitSchema 应用 3 个 SQL 文件（00-prereqs → 01-schema → 02-seed）
func (r *Runner) InitSchema(logger func(string)) error {
	files := []struct {
		name string
		desc string
	}{
		{"00-prereqs.sql", "扩展依赖"},
		{"01-schema.sql", "完整 schema"},
		{"02-seed.sql", "配置字典数据"},
	}

	for _, f := range files {
		logger(fmt.Sprintf("  ▶ 应用 %s (%s) ...", f.name, f.desc))
		if err := r.applySQL(f.name); err != nil {
			return fmt.Errorf("应用 %s 失败: %w", f.name, err)
		}
		logger(fmt.Sprintf("  ✅ %s 完成", f.name))
	}
	return nil
}

// applySQL 应用单个 SQL 文件（通过 docker exec + stdin）
func (r *Runner) applySQL(filename string) error {
	sqlPath := fmt.Sprintf("%s/%s", r.SQLDir, filename)
	content, err := readFile(sqlPath)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", "exec", "-i",
		r.CitusContainer, "psql",
		"-U", r.DBUser,
		"-d", r.DBName,
		"-v", "ON_ERROR_STOP=1",
		"--single-transaction",
	)
	cmd.Stdin = bytes.NewReader(content)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("psql 失败: %w\n%s", err, truncate(string(out), 500))
	}
	return nil
}

// VerifySchema 检查 schema 是否加载成功
func (r *Runner) VerifySchema() (int, error) {
	cmd := exec.Command("docker", "exec", r.CitusContainer, "psql",
		"-U", r.DBUser, "-d", r.DBName,
		"-t", "-c", "SELECT count(*) FROM information_schema.tables WHERE table_schema='public';")
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	var count int
	fmt.Sscanf(string(out), "%d", &count)
	return count, nil
}

// readFile 读取文件内容
// readFile 读取文件全部内容（用 os.ReadFile 自动处理短读）
func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
