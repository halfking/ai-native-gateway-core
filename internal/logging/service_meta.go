package logging

// 服务标识 / 版本 / 环境的统一来源（rule 71 §4.1 的四个资源字段）。
//
// 为什么放在 logging 包里：
//   - 本仓有 30+ 个 cmd 入口，各自都可能需要写日志；
//   - 资源字段必须**全局一致**——同一服务的两个入口不能一个写
//     "llm-gateway"、另一个写 "gateway"，否则 AI 聚合会分裂成两个服务；
//   - 版本与环境的读取逻辑（version.json / 环境变量）不该在每个 main 里
//     重复实现，更不该各自实现出不同的行为。
//
// 环境变量：
//
//	LLM_GATEWAY_ENV       部署环境（production/prod/staging/dev/local/test）
//	LLM_GATEWAY_SERVICE   服务标识覆盖（默认 "llm-gateway"）
//	LLM_GATEWAY_VERSION   版本覆盖（默认从 version.json 读，取不到则 "dev"）

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
)

// DefaultServiceName 是本仓的主服务标识。
//
// 必须与 deploy/platform/services.json 的服务键一致（kebab-case）。
const DefaultServiceName = "llm-gateway"

// versionFilePaths 按优先级返回 version.json 候选路径。
//
// 与 cmd/gateway/version.go 的 versionJSONPaths() 保持一致——
// 两处若不一致，日志里的 version 与 /api/version 端点会对不上。
var versionFilePaths = []string{
	"/opt/llm-gateway-go/version.json",
	"version.json",
}

var (
	versionOnce sync.Once
	versionInfo struct {
		Version   string `json:"version"`
		GitSHA    string `json:"git_sha"`
		BuildSeq  int    `json:"build_seq"`
		BuildDate string `json:"build_date"`
	}
)

// loadVersionOnce 从 version.json 加载版本信息（只执行一次）。
func loadVersionOnce() {
	versionOnce.Do(func() {
		versionInfo.Version = "dev"
		versionInfo.GitSHA = "unknown"
		versionInfo.BuildDate = "unknown"

		paths := versionFilePaths
		if p := strings.TrimSpace(os.Getenv("LLM_GATEWAY_VERSION_FILE")); p != "" {
			paths = append([]string{p}, paths...)
		}
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var v struct {
				Version   string `json:"version"`
				GitSHA    string `json:"git_sha"`
				BuildSeq  int    `json:"build_seq"`
				BuildDate string `json:"build_date"`
			}
			if err := json.Unmarshal(raw, &v); err != nil {
				continue
			}
			if v.Version != "" {
				versionInfo.Version = v.Version
				versionInfo.GitSHA = v.GitSHA
				versionInfo.BuildSeq = v.BuildSeq
				versionInfo.BuildDate = v.BuildDate
				return
			}
		}
	})
}

// ServiceName 返回服务标识（kebab-case）。
func ServiceName() string {
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_SERVICE")); v != "" {
		return v
	}
	return DefaultServiceName
}

// ServiceVersion 返回服务版本。
//
// 来源优先级：LLM_GATEWAY_VERSION 环境变量 > version.json > "dev"。
// "dev" 是合法非空值——可过门禁，同时明确表示「非发布构建」。
func ServiceVersion() string {
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_VERSION")); v != "" {
		return v
	}
	loadVersionOnce()
	return versionInfo.Version
}

// ServiceBuildNumber 返回构建序号（build_seq），取不到时为空串。
func ServiceBuildNumber() string {
	loadVersionOnce()
	if versionInfo.BuildSeq == 0 {
		return ""
	}
	return strconv.Itoa(versionInfo.BuildSeq)
}

// ServiceGitCommit 返回短 git hash，取不到时为空串。
func ServiceGitCommit() string {
	loadVersionOnce()
	if versionInfo.GitSHA == "" || versionInfo.GitSHA == "unknown" {
		return ""
	}
	return versionInfo.GitSHA
}

// ServiceEnv 返回部署环境，取值限定在 rule 71 允许的集合内：
// local / dev / test / staging / prod。
//
// 本仓历史上用 "production" 表示生产环境（见 config.DeployEnv），
// 这里做一次归一——**日志的 env 字段必须有固定取值集合**，
// 否则 AI 侧按 env 分组会出现 "prod" 与 "production" 两个桶。
//
// 未识别值回落到 "local"：写入非法值会让日志被门禁判为不合规，
// 而「服务起不来」比「环境标记不准」严重得多。
func ServiceEnv() string {
	raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ENV"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("GO_ENV"))
	}
	switch strings.ToLower(raw) {
	case "production", "prod":
		return "prod"
	case "staging", "stage":
		return "staging"
	case "dev", "development":
		return "dev"
	case "test", "testing":
		return "test"
	case "local":
		return "local"
	default:
		return "local"
	}
}

// NewServiceLogger 返回一个符合 rule 71 的 logger（stdout 输出）。
//
// 供各 cmd 入口统一使用——本仓有 30+ 个入口，格式不统一会让 AI 消费
// 同一服务需要多套解析分支。
//
// 参数解析失败时退回一个 JSON handler（仍保证是 JSONL，只是缺规范字段），
// 并把错误写到 stderr——绝不让服务因为日志初始化失败而起不来。
func NewServiceLogger(level slog.Level) *slog.Logger {
	handler, err := NewStdoutHandler(StandardOptions{
		Service: ServiceName(),
		Version: ServiceVersion(),
		Env:     ServiceEnv(),
		Level:   level,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "logging: standard handler init failed: %v\n", err)
		return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	}
	return slog.New(handler)
}

// NewServiceStderrLogger 是 NewServiceLogger 的 stderr 版本。
func NewServiceStderrLogger(level slog.Level) *slog.Logger {
	handler, err := NewStderrHandler(StandardOptions{
		Service: ServiceName(),
		Version: ServiceVersion(),
		Env:     ServiceEnv(),
		Level:   level,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "logging: standard handler init failed: %v\n", err)
		return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	}
	return slog.New(handler)
}
