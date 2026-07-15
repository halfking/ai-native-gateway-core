// Package main — attachment_storage_init.go
//
// Storage backend 选择的 boot 接线 (Phase 3D, 2026-07-15)
//
// 设计目标：
//  1. 默认行为不变 — 当 LLM_GATEWAY_STORAGE_TYPE 未设置或为空时，
//     仍然用 LocalStorageBackend (LLM_GATEWAY_ATTACHMENT_DIR).
//     这是 245 / 154 当前线上运行的行为，不允许破坏。
//  2. 显式 opt-in — 只有当 LLM_GATEWAY_STORAGE_TYPE 被显式设置为
//     "oss" / "s3" / "minio" / "cloudreve" 之一时，才走 LoadStorageConfigFromEnv
//     + NewStorageBackendFromConfig 的新路径。
//  3. Fail-safe — 新路径任何错误都 warn + 退化到 LocalStorageBackend。
//     启动期 storage backend 失败不应阻塞整个 gateway 启动
//     (matches the existing log.Warn 而不是 fatal 的语义)。
//  4. 验证驱动 — selectAttachmentStorage 函数本身可以在 _test.go 里单测。
//
// 实现要点：
//   - 走 attachments.LoadStorageConfigFromEnv() 解析 env（已有）
//   - 走 attachments.NewStorageBackendFromConfig() 构造 backend（已有）
//   - 走 attachments.NewStorageWithBackend(backend) 包成 *Storage（已有）
//
// build tag 注意：
//   - 这个文件不带任何 build tag。它只 import "domains/attachments"，
//     而 storage_backend_cloudreve / _oss / _s3 是 build-tag-gated 的。
//   - 当只起 default build 时，LoadStorageConfigFromEnv 的 switch 命中
//     "cloudreve"/"oss"/"s3" 会调用 NewCloudreveStorageBackend / NewOSSStorageBackend
//     / NewS3StorageBackend（来自对应 build-tag 文件）。
//   - 不带对应 tag 时这些是 stub，会返回 "not available - build with -tags ..."。
//   - InitAttachmentStorage 收到这个错误时 warn + 退化到 LocalStorageBackend，
//     让 gateway 仍然能起来（虽然 attachment extraction 失败）。
package main

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/attachments"
)

// attachmentBackendTypeAcceptable 列出 valid 的显式 opt-in 值。
// "filesystem" / "local" / "" 不在这里 —— 它们走 default LocalStorageBackend path。
//
// 注意：minio 在 storage_config.go 的 switch 里被 alias 到 s3，所以也接受。
var attachmentBackendTypeAcceptable = map[string]struct{}{
	"oss":       {},
	"s3":        {},
	"minio":     {},
	"cloudreve": {},
}

// initAttachmentStorage 主入口。覆盖原 main.go:1190-1212 的硬编码 LocalStorageBackend 路径。
//
// 返回：
//   - *attachments.Storage 永远非 nil；出错时退化为 LocalStorageBackend
//   - string backendType 名 ("filesystem" / "oss" / "s3" / "cloudreve")
//
// 调用方应当：
//   - 用返回的 storage 替换原有 *attachments.Storage
//   - 用返回的 backendType 打 slog.Info 便于 ops 排查
func initAttachmentStorage(defaultBaseDir string) (*attachments.Storage, string) {
	storageType := os.Getenv("LLM_GATEWAY_STORAGE_TYPE")

	// 未设置 / 空 / filesystem / local → 旧路径
	if storageType == "" || isLocalStorageType(storageType) {
		return initLocalAttachmentStorage(defaultBaseDir)
	}

	// 显式 opt-in 到非 local backend。type 合法性检查：
	// 拒绝 typo / 拼错,避免静默回退到文件系统而 ops 不知道配置错了
	if !isAcceptableBackendType(storageType) {
		slog.Warn("attachment storage: unknown LLM_GATEWAY_STORAGE_TYPE, falling back to filesystem",
			"type", storageType,
			"acceptable", "filesystem, local, oss, s3, minio, cloudreve")
		return initLocalAttachmentStorage(defaultBaseDir)
	}

	// 走新路径：LoadStorageConfigFromEnv + NewStorageBackendFromConfig
	cfg := attachments.LoadStorageConfigFromEnv()
	if err := attachments.ValidateStorageConfig(cfg); err != nil {
		slog.Warn("attachment storage: env config validation failed, falling back to filesystem",
			"type", storageType,
			"error", err)
		return initLocalAttachmentStorage(defaultBaseDir)
	}

	backend, err := attachments.NewStorageBackendFromConfig(cfg)
	if err != nil {
		// 不带对应 build tag 时这里是 "not available - build with -tags ..."。
		// 退化到 LocalStorageBackend 而不是 fatal，让 gateway 仍能起来。
		slog.Warn("attachment storage: backend construct failed, falling back to filesystem",
			"type", storageType,
			"error", err,
			"hint", "if you set "+storageType+" but the binary was built without -tags storage_"+mapBackendTag(storageType)+", rebuild with the tag")
		return initLocalAttachmentStorage(defaultBaseDir)
	}

	// 用 backend 包成 *Storage（保留 baseDir 字段给 admin mux 用）
	storage := attachments.NewStorageWithBackend(backend)

	// 单文件 max size（同旧路径行为）
	if maxSizeStr := os.Getenv("LLM_GATEWAY_ATTACHMENT_MAX_SIZE"); maxSizeStr != "" {
		if maxSize, parseErr := strconv.ParseInt(maxSizeStr, 10, 64); parseErr == nil && maxSize > 0 {
			storage.MaxSize = maxSize
		}
	}

	// 启动期 health-check：让 ops 在 boot 阶段就知道 backend 连不连通。
	// 这是 best-effort，错误不阻塞启动（match 原 LocalStorageBackend path 的语义）。
	if err := backend.HealthCheck(context.Background()); err != nil {
		slog.Warn("attachment storage: backend health check failed at boot; will retry on first use",
			"type", storageType,
			"error", err)
		// 仍然继续。attachment extraction 阶段会再 fail 一遍，但 boot 不阻塞。
	}

	return storage, storageType
}

// initLocalAttachmentStorage 是 initAttachmentStorage 的 "filesystem / local / fallback"
// 路径，保持与原 main.go:1195-1211 行为兼容。
func initLocalAttachmentStorage(baseDir string) (*attachments.Storage, string) {
	storage, err := attachments.NewStorage(baseDir)
	if err != nil {
		slog.Warn("attachment storage init failed, extraction disabled",
			"error", err,
			"dir", baseDir)
		return nil, "filesystem"
	}
	if maxSizeStr := os.Getenv("LLM_GATEWAY_ATTACHMENT_MAX_SIZE"); maxSizeStr != "" {
		if maxSize, parseErr := strconv.ParseInt(maxSizeStr, 10, 64); parseErr == nil && maxSize > 0 {
			storage.MaxSize = maxSize
		}
	}
	return storage, "filesystem"
}

// isLocalStorageType 把 "filesystem" / "local" / "fs"（任意大小写组合）视为同一路径。
//
// 大小写不敏感是为了兼容 ops 在不同 shell / 配置管理工具里写出的
// "Filesystem" / "LOCAL" 等变体。如果未来需要严格大小写匹配
// （比如 enforce canonical env values），把 ToLower 去掉即可。
func isLocalStorageType(t string) bool {
	switch strings.ToLower(t) {
	case "filesystem", "local", "fs":
		return true
	}
	return false
}

// isAcceptableBackendType 确认 opt-in type 是已知的 backend 之一。
func isAcceptableBackendType(t string) bool {
	_, ok := attachmentBackendTypeAcceptable[t]
	return ok
}

// mapBackendTag 把 env 里的 type 映射到对应 build tag 后缀，
// 用来给 ops 一个清晰的错误提示（"build with -tags storage_xxx"）。
func mapBackendTag(t string) string {
	switch t {
	case "oss":
		return "oss"
	case "s3", "minio":
		return "s3"
	case "cloudreve":
		return "cloudreve_storage"
	}
	return ""
}
