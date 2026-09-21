package paramledger

// redis_mirror.go — Full 部署模式的 Redis 镜像（2026-09-22）。
//
// 主存是进程内存（见 ledger.go 头注释：请求与响应在同一实例内完成，
// 逐帧还原不能打网络）。Redis 镜像只为跨进程审计/观测保留一份账目，
// 读路径永不使用；写失败静默（观测面不值得反压请求）。

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisMirror 返回把账目镜像到 Redis 的函数（key 前缀默认
// llmgw:paramledger，TTL 与内存一致）。rdb 为 nil 时返回 no-op。
func RedisMirror(rdb redis.UniversalClient, prefix string) mirrorFn {
	if rdb == nil {
		return nil
	}
	if prefix == "" {
		prefix = "llmgw:paramledger"
	}
	return func(requestID string, entry Entry) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		blob, err := json.Marshal(entry)
		if err != nil {
			return
		}
		if err := rdb.Set(ctx, prefix+":"+requestID, blob, entryTTL).Err(); err != nil {
			slog.Debug("paramledger redis mirror failed", "request_id", requestID, "error", err)
		}
	}
}
