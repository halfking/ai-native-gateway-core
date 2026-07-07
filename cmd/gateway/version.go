package main

// 版本信息 — 由 build 时通过 ldflags -X 注入
//
// 用法:
//   go build -ldflags="-X main.Version=2.4.1 -X main.GitCommit=8b101e36 \
//     -X main.BuildDate=20260708 -X main.BuildNumber=947" ./cmd/gateway
//
// 历史:
//   - 2026-07-08 新增 (deploy-154 配套). 之前 scripts/deploy.sh 第 536-539 行注入的
//     -X main.Version=... 一直是死代码 (源码没声明这些 var).
//   - 修复后 binary 启动时会在 stdout 打印 "gateway starting v=... sha=... seq=..."
var (
	// Version 是去掉前缀 v 的 semver (e.g. "2.4.1")
	Version = "dev"
	// GitCommit 是 8 字符短 hash
	GitCommit = "unknown"
	// BuildDate 是 YYYYMMDD (UTC)
	BuildDate = "unknown"
	// BuildNumber 是 build_seq, 与 .deploy_seq / VERSION 文件保持 lockstep
	BuildNumber = "0"
)

// FullVersion 返回人类可读版本字符串, 格式与 VERSION 文件一致:
//   "2.4.1-8b101e36-20260708-947"
func FullVersion() string {
	return Version + "-" + GitCommit + "-" + BuildDate + "-" + BuildNumber
}