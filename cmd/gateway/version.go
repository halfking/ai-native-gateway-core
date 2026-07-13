package main

// 版本信息 — 统一从 version.json 读取（SSOT）。
//
// 2026-07-14 重构：
//   - 移除 ldflags -X 注入（之前需要部署脚本正确传参，容易出错）
//   - 运行时从 version.json 文件读取，与 admin/misc.go loadVersionInfo() 一致
//   - 唯一的 SSOT 文件：/opt/llm-gateway-go/version.json
//   - bump-version.sh 负责更新 version.json，部署脚本只负责 scp

import (
	"encoding/json"
	"os"
	"strconv"
	"sync"
)

var (
	versionOnce sync.Once
	vInfo       versionInfoStruct
)

type versionInfoStruct struct {
	Version   string `json:"version"`
	GitTag    string `json:"git_tag"`
	GitSHA    string `json:"git_sha"`
	BuildSeq  int    `json:"build_seq"`
	BuildDate string `json:"build_date"`
	Module    string `json:"module"`
}

// versionJSONPaths 按优先级返回 version.json 候选路径。
func versionJSONPaths() []string {
	return []string{
		"/opt/llm-gateway-go/version.json",
		"version.json",
	}
}

// loadVersionOnce 从 version.json 加载版本信息（只执行一次）。
func loadVersionOnce() {
	versionOnce.Do(func() {
		vInfo = versionInfoStruct{
			Version:   "dev",
			GitTag:    "v0.0.0",
			GitSHA:    "unknown",
			BuildDate: "unknown",
		}
		for _, path := range versionJSONPaths() {
			raw, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var v versionInfoStruct
			if err := json.Unmarshal(raw, &v); err != nil {
				continue
			}
			if v.Version != "" {
				vInfo = v
				return
			}
		}
	})
}

// Version 是去掉前缀 v 的 semver (e.g. "2.4.1")
func Version() string {
	loadVersionOnce()
	return vInfo.Version
}

// GitCommit 是 8 字符短 hash
func GitCommit() string {
	loadVersionOnce()
	return vInfo.GitSHA
}

// BuildDate 是 YYYYMMDD (UTC)
func BuildDate() string {
	loadVersionOnce()
	return vInfo.BuildDate
}

// BuildNumber 是 build_seq（与 version.json 保持一致）
func BuildNumber() string {
	loadVersionOnce()
	return strconv.Itoa(vInfo.BuildSeq)
}

// BuildSeqInt 返回 build_seq 的整数形式
func BuildSeqInt() int {
	loadVersionOnce()
	return vInfo.BuildSeq
}

// FullVersion 返回人类可读版本字符串:
//   "v2.4.1-e0e9e1e2e-20260714-1001"
func FullVersion() string {
	loadVersionOnce()
	return vInfo.Version + "-" + vInfo.GitSHA + "-" + vInfo.BuildDate + "-" + strconv.Itoa(vInfo.BuildSeq)
}
