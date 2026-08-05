// cmd/scenario_driver/main.go
//
// S22/S23 子用例驱动工具: 在 Go 进程内完成 chat rounds + SQL query + mock
// group state 切换, 输出 JSON 供 shell 解析. 解决 macOS bash 5.3 子 shell
// 嵌套 EOF bug (S22 22.3/22.4/23.3 之前因此 PENDING).
//
// 用法:
//
//	go run ./cmd/scenario_driver \
//	  --scenario s22-3 \
//	  --gateway http://127.0.0.1:8793 \
//	  --api-key sk-loadtest-01 \
//	  --rounds 60 \
//	  --prompt medium
//
// 子命令 (--scenario):
//   s22-3   60 轮长 prompt → 验证 request_logs_bodies_hot delta ≥ 50
//   s22-4   20 轮 + mock server_error → 验证 request_logs_hot.success=false delta ≥ 1
//   s23-3   80 轮 → 验证 request_logs_bodies_hot delta ≥ 70
//
// DB 连接默认从 LLM_GATEWAY_DATABASE_URL 取, 或 PGPASSWORD/PGHOST 等 env.
//
// 2026-08-06: 首次实现, 解决 bash 5.3.9 子 shell 嵌套 EOF bug.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jackc/pgx/v5"
)

// Config from flags
type Config struct {
	Scenario  string
	Gateway   string
	APIKey    string
	Rounds    int
	Prompt    string
	DBURL     string
	PGHost    string
	PGPort    string
	PGUser    string
	PGPass    string
	PGDB      string
	MockGroup string // for s22-4 server_error injection
}

// Result JSON shape
type Result struct {
	Scenario       string `json:"scenario"`
	Passed         bool   `json:"passed"`
	ChatRoundsSucc int    `json:"chat_rounds_succ"`
	ChatRoundsFail int    `json:"chat_rounds_fail"`
	BaseBodies     int    `json:"base_bodies"`
	AfterBodies    int    `json:"after_bodies"`
	DeltaBodies    int    `json:"delta_bodies"`
	BaseFail       int    `json:"base_fail"`
	AfterFail      int    `json:"after_fail"`
	DeltaFail      int    `json:"delta_fail"`
	Message        string `json:"message"`
	Error          string `json:"error,omitempty"`
}

func main() {
	cfg := parseFlags()
	res := run(cfg)
	out, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(out))
	if !res.Passed {
		os.Exit(1)
	}
}

func parseFlags() *Config {
	cfg := &Config{}
	flag.StringVar(&cfg.Scenario, "scenario", "", "scenario name (s22-3, s22-4, s23-3)")
	flag.StringVar(&cfg.Gateway, "gateway", "http://127.0.0.1:8793", "gateway base URL")
	flag.StringVar(&cfg.APIKey, "api-key", "sk-loadtest-01", "Bearer token")
	flag.IntVar(&cfg.Rounds, "rounds", 60, "chat rounds")
	flag.StringVar(&cfg.Prompt, "prompt", "medium", "prompt preset: short / medium / long")
	flag.StringVar(&cfg.DBURL, "db-url", os.Getenv("LLM_GATEWAY_DATABASE_URL"), "postgres URL (overrides envs)")
	flag.StringVar(&cfg.PGHost, "pg-host", "localhost", "postgres host (used if --db-url empty)")
	flag.StringVar(&cfg.PGPort, "pg-port", "5432", "postgres port")
	flag.StringVar(&cfg.PGUser, "pg-user", "llm_gateway", "postgres user")
	flag.StringVar(&cfg.PGPass, "pg-pass", "llm_gateway_db_pass_2026_secure", "postgres password")
	flag.StringVar(&cfg.PGDB, "pg-db", "llm_gateway", "postgres database")
	flag.StringVar(&cfg.MockGroup, "mock-group", "", "if non-empty, set mock group state (e.g. server_error)")
	flag.Parse()
	return cfg
}

func run(cfg *Config) *Result {
	res := &Result{Scenario: cfg.Scenario}

	// 1. DB connect
	conn, err := connectDB(cfg)
	if err != nil {
		res.Error = "db connect: " + err.Error()
		return res
	}
	defer conn.Close(context.Background())

	// 2. Take base count
	baseBodies, err := queryScalarInt(conn, "SELECT count(*) FROM request_logs_bodies_hot WHERE ts > NOW() - INTERVAL '1 hour'")
	if err != nil {
		res.Error = "base bodies query: " + err.Error()
		return res
	}
	res.BaseBodies = baseBodies
	baseFail, _ := queryScalarInt(conn, "SELECT count(*) FROM request_logs_hot WHERE ts > NOW() - INTERVAL '1 minute' AND success = false")
	res.BaseFail = baseFail

	// 3. (s22-4 only) inject server_error on all groups
	if cfg.Scenario == "s22-4" {
		if err := setAllMockGroups(cfg, "server_error"); err != nil {
			res.Error = "set all groups server_error: " + err.Error()
			return res
		}
		time.Sleep(2 * time.Second)
	}

	// 4. Chat rounds via gateway
	succ, fail, err := runChatRounds(cfg)
	if err != nil {
		res.Error = "chat rounds: " + err.Error()
		return res
	}
	res.ChatRoundsSucc = succ
	res.ChatRoundsFail = fail

	// 5. (s22-4 only) reset mock
	if cfg.Scenario == "s22-4" {
		_ = setAllMockGroups(cfg, "healthy")
	}

	// 6. After count
	time.Sleep(3 * time.Second)
	afterBodies, _ := queryScalarInt(conn, "SELECT count(*) FROM request_logs_bodies_hot WHERE ts > NOW() - INTERVAL '1 hour'")
	res.AfterBodies = afterBodies
	res.DeltaBodies = afterBodies - baseBodies

	afterFail, _ := queryScalarInt(conn, "SELECT count(*) FROM request_logs_hot WHERE ts > NOW() - INTERVAL '1 minute' AND success = false")
	res.AfterFail = afterFail
	res.DeltaFail = afterFail - baseFail

	// 7. Pass/fail logic per scenario
	switch cfg.Scenario {
	case "s22-3":
		// 60 轮长 prompt 后 request_logs_bodies_hot delta >= 50
		res.Passed = res.DeltaBodies >= 50
		res.Message = fmt.Sprintf("delta_bodies=%d, expected >= 50", res.DeltaBodies)
	case "s22-4":
		// 20 轮 LLM 失败后 success=false delta >= 1
		res.Passed = res.DeltaFail >= 1
		res.Message = fmt.Sprintf("delta_fail=%d, expected >= 1", res.DeltaFail)
	case "s23-3":
		// 80 轮 chat 后 request_logs_bodies_hot delta >= 70
		res.Passed = res.DeltaBodies >= 70
		res.Message = fmt.Sprintf("delta_bodies=%d, expected >= 70", res.DeltaBodies)
	default:
		res.Error = fmt.Sprintf("unknown scenario: %s", cfg.Scenario)
	}

	return res
}

func connectDB(cfg *Config) (*pgx.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var connStr string
	if cfg.DBURL != "" {
		connStr = cfg.DBURL
	} else {
		connStr = fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
			cfg.PGUser, cfg.PGPass, cfg.PGHost, cfg.PGPort, cfg.PGDB)
	}
	return pgx.Connect(ctx, connStr)
}

func queryScalarInt(conn *pgx.Conn, sql string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var v int
	if err := conn.QueryRow(ctx, sql).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

func setAllMockGroups(cfg *Config, state string) error {
	groups := []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K", "L"}
	for _, g := range groups {
		// ports are 19080-19139
		basePort := 19080
		groupIdx := int(g[0] - 'A')
		basePort += groupIdx * 5
		for inst := 0; inst < 5; inst++ {
			port := basePort + inst
			url := fmt.Sprintf("http://127.0.0.1:%d/admin/state", port)
			body := fmt.Sprintf(`{"state": "%s"}`, state)
			resp, err := http.Post(url, "application/json", strings.NewReader(body))
			if err != nil {
				return fmt.Errorf("set group %s instance %d port %d: %w", g, inst, port, err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}
	return nil
}

func runChatRounds(cfg *Config) (succ, fail int, err error) {
	content := makeContent(cfg.Prompt)
	sessionID := fmt.Sprintf("driver-%s-%d", cfg.Scenario, time.Now().UnixNano())
	messages := []map[string]interface{}{}

	for i := 1; i <= cfg.Rounds; i++ {
		userMsg := strings.Replace(content, "{i}", fmt.Sprintf("%d", i), 1)
		messages = append(messages, map[string]interface{}{
			"role":    "user",
			"content": userMsg,
		})

		body, _ := json.Marshal(map[string]interface{}{
			"model":       "loadtest-mini-alpha",
			"messages":    messages,
			"max_tokens":  10,
		})

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "POST", cfg.Gateway+"/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		req.Header.Set("X-Gw-Session-Id", sessionID)

		resp, err := http.DefaultClient.Do(req)
		cancel()
		if err != nil {
			return succ, fail, fmt.Errorf("round %d: %w", i, err)
		}
		if resp.StatusCode == 200 {
			succ++
			// parse assistant reply and append
			var respBody struct {
				Choices []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				} `json:"choices"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&respBody); err == nil &&
				len(respBody.Choices) > 0 {
				messages = append(messages, map[string]interface{}{
					"role":    "assistant",
					"content": respBody.Choices[0].Message.Content,
				})
			}
		} else {
			fail++
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	return succ, fail, nil
}

func makeContent(preset string) string {
	var chars int
	switch preset {
	case "short":
		chars = 50
	case "medium":
		chars = 500
	case "long":
		chars = 3000
	default:
		chars = 500
	}
	var sb strings.Builder
	sb.WriteString("Round {i}: ")
	for i := 0; i < chars; i++ {
		sb.WriteString("x")
	}
	return sb.String()
}
