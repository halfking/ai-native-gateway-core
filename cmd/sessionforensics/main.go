// Command sessionforensics — 运维平台 CLI 入口。
//
// 用法：
//
//	# 从生产 admin 下载会话到本地
//	sessionforensics download --id=gw_xxxxx --out=tests/session_replay/sessions/
//
//	# 从本地 JSON 回放并打印报告
//	sessionforensics replay --in=sessions/session_gw_xxx.json --model=gpt-4o
//
//	# 给定会话生成摘要 + 标题（写到 stdout）
//	sessionforensics summarize --in=sessions/session_gw_xxx.json
//
//	# 列出最近活跃的会话
//	sessionforensics list --tenant=default --limit=20
//
//	# 跨主机迁移：把会话从 host A 推到 host B
//	sessionforensics migrate --id=gw_xxx --from=https://A --to=https://B
//
// 环境变量：
//
//	SESSION_FORENSICS_BASE_URL  默认 admin base URL（可选 — 通常用 --from / --to）
//	SESSION_FORENSICS_BEARER    admin JWT
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sessionforensics"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	ctx := context.Background()

	switch cmd {
	case "download":
		runDownload(ctx, args)
	case "replay":
		runReplay(ctx, args)
	case "summarize":
		runSummarize(ctx, args)
	case "list":
		runList(ctx, args)
	case "migrate":
		runMigrate(ctx, args)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `sessionforensics — 运维平台会话调试 CLI

Usage:
  sessionforensics download --id=<gw_session_id> [--out=DIR] [--from=URL] [--bearer=TOKEN]
  sessionforensics replay   --in=<file> [--model=NAME] [--window=N]
  sessionforensics summarize --in=<file> [--persist]
  sessionforensics list     [--tenant=ID] [--limit=N] [--from=URL] [--bearer=TOKEN]
  sessionforensics migrate  --id=<sid> --from=URL --to=URL [--bearer=TOKEN]

Environment variables:
  SESSION_FORENSICS_BASE_URL  默认 admin base URL
  SESSION_FORENSICS_BEARER    admin JWT (Authorization: Bearer ...)`)
}

func runDownload(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("download", flag.ExitOnError)
	id := fs.String("id", "", "gw_session_id (required)")
	out := fs.String("out", "", "落盘目录；空 → 仅写到 stdout")
	from := fs.String("from", os.Getenv("SESSION_FORENSICS_BASE_URL"), "admin base URL")
	bearer := fs.String("bearer", os.Getenv("SESSION_FORENSICS_BEARER"), "admin JWT")
	_ = fs.Parse(args)
	if *id == "" {
		fmt.Fprintln(os.Stderr, "--id is required")
		os.Exit(1)
	}

	svc := sessionforensics.NewService(sessionforensics.ServiceConfig{
		HTTPBaseURL:   *from,
		Bearer:        *bearer,
		LocalStoreDir: *out,
	})
	pack, err := svc.DownloadSession(ctx, *id, "default")
	if err != nil {
		fmt.Fprintln(os.Stderr, "download failed:", err)
		os.Exit(1)
	}
	if *out == "" {
		b, _ := json.MarshalIndent(pack, "", "  ")
		fmt.Println(string(b))
	} else {
		fmt.Printf("download ok (id=%s title=%q, %d messages)\n",
			pack.SessionMeta.ID, pack.SessionMeta.Title, len(pack.Messages))
	}
}

func runReplay(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	in := fs.String("in", "", "输入 session JSON (required，支持 SessionPack 或 extract.py 两种 schema)")
	model := fs.String("model", "", "覆盖 model 字段（验证模型替换）")
	window := fs.Int("window", 128_000, "目标模型 context window tokens")
	_ = fs.Parse(args)
	if *in == "" {
		fmt.Fprintln(os.Stderr, "--in is required")
		os.Exit(1)
	}
	pack, err := sessionforensics.LoadExtractPyFile(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load:", err)
		os.Exit(1)
	}
	rp := sessionforensics.NewReplayer()
	rep := rp.Replay(ctx, pack, sessionforensics.ReplayOptions{
		ContextWindow: *window,
		ModelOverride: *model,
	})
	b, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Println(string(b))
}

func runSummarize(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("summarize", flag.ExitOnError)
	in := fs.String("in", "", "输入 session JSON (required)")
	persist := fs.Bool("persist", false, "是否回写 session_summaries 表")
	_ = fs.Parse(args)
	if *in == "" {
		fmt.Fprintln(os.Stderr, "--in is required")
		os.Exit(1)
	}
	pack, err := sessionforensics.LoadExtractPyFile(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load:", err)
		os.Exit(1)
	}
	svc := sessionforensics.NewService(sessionforensics.ServiceConfig{})
	res, err := svc.Summarize(ctx, pack, sessionforensics.SummarizeArgs{
		Persist: persist,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "summarize failed:", err)
		os.Exit(1)
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(b))
}

func runList(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	tenant := fs.String("tenant", "default", "tenant_id")
	limit := fs.Int("limit", 50, "返回 session 数")
	from := fs.String("from", os.Getenv("SESSION_FORENSICS_BASE_URL"), "admin base URL")
	bearer := fs.String("bearer", os.Getenv("SESSION_FORENSICS_BEARER"), "admin JWT")
	_ = fs.Parse(args)
	if *from == "" {
		fmt.Fprintln(os.Stderr, "--from or SESSION_FORENSICS_BASE_URL is required")
		os.Exit(1)
	}
	// 调 admin /api/admin/sessions/list 端点（无需本地 DB）。
	q := url.Values{}
	q.Set("tenant", *tenant)
	q.Set("limit", strconv.Itoa(*limit))
	endpoint := *from + "/api/admin/sessions/list?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build request:", err)
		os.Exit(1)
	}
	if *bearer != "" {
		req.Header.Set("Authorization", "Bearer "+*bearer)
	}
	req.Header.Set("Accept", "application/json")
	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "list failed:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		fmt.Fprintf(os.Stderr, "list status=%d body=%s\n", resp.StatusCode, string(body)[:min200(len(body))])
		os.Exit(1)
	}
	fmt.Println(string(body))
}

// min200 returns first 200 chars of s (or full if shorter).
func min200(n int) int {
	if n > 200 {
		return 200
	}
	return n
}

func runMigrate(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	id := fs.String("id", "", "gw_session_id (required)")
	from := fs.String("from", "", "source admin base URL (required)")
	to := fs.String("to", "", "target admin base URL (required)")
	bearer := fs.String("bearer", os.Getenv("SESSION_FORENSICS_BEARER"), "admin JWT")
	_ = fs.Parse(args)
	if *id == "" || *from == "" || *to == "" {
		fmt.Fprintln(os.Stderr, "--id, --from, --to are all required")
		os.Exit(1)
	}
	src := sessionforensics.NewClient(*from)
	src.Bearer = *bearer
	dst := sessionforensics.NewClient(*to)
	dst.Bearer = *bearer

	pack, err := src.Download(ctx, *id, "default")
	if err != nil {
		fmt.Fprintln(os.Stderr, "download:", err)
		os.Exit(1)
	}
	fmt.Printf("downloaded: %s title=%q messages=%d\n",
		pack.SessionMeta.ID, pack.SessionMeta.Title, len(pack.Messages))

	packID, err := dst.Upload(ctx, pack)
	if err != nil {
		fmt.Fprintln(os.Stderr, "upload:", err)
		os.Exit(1)
	}
	fmt.Printf("uploaded → pack_id=%s\n", packID)
}
