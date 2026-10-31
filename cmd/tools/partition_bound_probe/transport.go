package main

// transport.go —— 测量用的两种「把 SQL 送进 PG 并取回原始输出」的通道。
//
// # 为什么要有两种
//
// ★★ **本地库与 252 的数据规模差 2 个量级**（★ 本地真库 session_turns 只有 2683 行；
//   252 是 114 万行）⇒ ⇒ **本地的读数只能证明「裁剪机制是否生效」，
//   证明不了「快多少」** ⇒ ⇒ ⇒ **收益必须在 252 上量**。
//
// ⇒ 而 252 的连接方式与本地不同：
//   · 本地：宿主 127.0.0.1:54318（容器 llm-gateway-pg）⇒ pgx 直连；
//   · 252：SSH key 认证 → 容器内 psql（★ pg_hba 的 `local all all trust` ⇒ **免密**）。
//
// ⇒ 两种通道都只跑 **SELECT / EXPLAIN**，不写库。

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// transport 是一个「执行 SQL、取回原始 stdout」的通道。
type transport interface {
	// Query 执行 sql，返回原始 stdout（不含 stderr）。
	Query(ctx context.Context, sql string) (string, error)
	// Describe 返回通道的一句话描述（写进读数，便于复核）。
	Describe() string
}

// ── pgxTransport：本地 / 直连 DSN ──────────────────────────────────────────

type pgxTransport struct {
	pool *pgxpool.Pool
	dsn  string
}

func (t *pgxTransport) Describe() string {
	return "pgx 直连（" + redactDSN(t.dsn) + "）"
}

func (t *pgxTransport) Query(ctx context.Context, sql string) (string, error) {
	rows, err := t.pool.Query(ctx, sql)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var one []byte
		if err := rows.Scan(&one); err != nil {
			return "", err
		}
		b.Write(one)
		b.WriteByte('\n')
	}
	return b.String(), rows.Err()
}

// redactDSN 把口令从 DSN 里抹掉（读数要进审计档，不能带凭据）。
func redactDSN(dsn string) string {
	at := strings.LastIndex(dsn, "@")
	sl := strings.Index(dsn, "://")
	if at < 0 || sl < 0 || at < sl {
		return "（dsn 形态异常）"
	}
	cred := dsn[sl+3 : at]
	if i := strings.Index(cred, ":"); i >= 0 {
		cred = cred[:i] + ":***"
	}
	return dsn[:sl+3] + cred + dsn[at:]
}

// ── sshTransport：252（SSH key → 容器内 psql）─────────────────────────────

type sshTransport struct {
	host      string
	port      int
	user      string
	container string
	dbUser    string
	dbName    string
	// controlPath 是 SSH 连接复用的 socket 路径。
	controlPath string
}

func (t *sshTransport) Describe() string {
	return fmt.Sprintf("ssh %s@%s:%d → docker exec %s → psql -U %s -d %s（只读）",
		t.user, t.host, t.port, t.container, t.dbUser, t.dbName)
}

func (t *sshTransport) Query(ctx context.Context, sql string) (string, error) {
	remote := fmt.Sprintf("docker exec -i %s psql -X -At -v ON_ERROR_STOP=1 -U %s -d %s -f -",
		t.container, t.dbUser, t.dbName)
	// ★ ControlMaster：同一台机要跑几十次 EXPLAIN（每个读点 2 变体 × N 次重复）
	//   ⇒ 不复用连接的话每次都要重新握手 ⇒ 光握手就比查询还慢。
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=8",
		// ★ 连接复用（否则 252 上的握手开销会淹没读数）
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + t.controlPath,
		"-o", "ControlPersist=600",
		"-p", strconv.Itoa(t.port),
		t.user + "@" + t.host,
		remote,
	}
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = strings.NewReader(sql)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("ssh/psql 失败: %w（stderr: %s）", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
