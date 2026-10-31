package main

// transport.go —— R179 轮：把 SQL 送进 PG 并取回原始 stdout 的两种通道。
//
// # 为什么要两种通道
//
// ★★ **本地库与 252 的数据规模差 2 个量级**（★ 本地 session_turns 2,763 行；
//   252 是 114 万行、54 个日分区）⇒ ⇒ **本地读数只能证明「机制是否生效」，
//   证明不了「生产上这条查询走的是哪种计划形态」** ⇒ ⇒ ⇒ **形态必须在 252 上量**。
//
// ⇒ 两条通道都只跑 **SELECT / EXPLAIN**，不写库。
//
// ★ 252 是**只读**库 ⇒ 本工具对它**只做 EXPLAIN（不带 ANALYZE）**：
//   ⇒ 不执行查询、不产生行、不加锁 ⇒ ⇒ 只读库上安全；
//   ⇒ 而【规划器的选择】只依赖统计信息 ⇒ ⇒ EXPLAIN（不带 ANALYZE）已经能给出
//   ⇒ ⇒ **生产上真正会选的计划形态**。

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
	Query(ctx context.Context, sql string) (string, error)
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
		vals, err := rows.Values()
		if err != nil {
			return "", err
		}
		// ★ 多列 ⇒ 用 \t 连接（★ 与 psql -A -F '\t' 的口径一致 ⇒ 两条通道可对拍）。
		parts := make([]string, 0, len(vals))
		for _, v := range vals {
			parts = append(parts, fmt.Sprintf("%v", v))
		}
		b.WriteString(strings.Join(parts, "\t"))
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
	host        string
	port        int
	user        string
	container   string
	dbUser      string
	dbName      string
	controlPath string
}

func (t *sshTransport) Describe() string {
	return fmt.Sprintf("ssh %s@%s:%d → docker exec %s → psql -U %s -d %s（只读）",
		t.user, t.host, t.port, t.container, t.dbUser, t.dbName)
}

func (t *sshTransport) Query(ctx context.Context, sql string) (string, error) {
	// ★ -F '<TAB>'：**必须**指定字段分隔符（★ psql 的 -A 默认用 | 分隔 ⇒
	//   ⇒ 多列结果会粘成一行 ⇒ ⇒ 探针就取不到第 2 列起的值）。
	//   ★★ 注意：★ 这里塞的是**真 tab 字节**，★ 不是两个字符的 \t
	//   ⇒ psql 不会把字面量 "\t" 解释成制表符（★ 实测踩过：分隔符变成了 \t 这两个字符）。
	remote := fmt.Sprintf("docker exec -i %s psql -X -q -A -t -F '%s' -v ON_ERROR_STOP=1 -U %s -d %s -f -",
		t.container, "\t", t.dbUser, t.dbName)
	// ★ ControlMaster：同一台机要跑几十次 EXPLAIN ⇒ 不复用连接的话每次都重新握手
	//   ⇒ 光握手就比查询还慢。
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=8",
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
