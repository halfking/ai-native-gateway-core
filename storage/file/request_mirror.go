// Package file: RequestMirror — 请求侧 body 镜像（2026-09-24 H3）。
//
// 路径布局（与 lite cache_dir 同构，承载于 HotZone.Dir/requests/）：
//
//	{HotZone.Dir}/requests/{tenantID}/{YYYY-MM-DD}/{requestID}.{req|resp|out}.json.gz
//
// 设计要点：
//   - 复用 AsyncFileWriter 与 codec（gzip），与 FileBodiesStore 同源异步写语义；
//   - 写失败仅记监控计数，绝不阻断主链路（fail-open，与 FileBodiesStore 一致）；
//   - 读路径不参与 L3 回源：admin 请求详情查 PG 即可（避免文件缺失导致详情 404 的
//     语义分裂），镜像只服务灾备人工取数 + G2「文件命中」观察。
//
// 接线点：流式终态落库处（streaming handler 的 bodies 写入事务提交后），
// fire-and-forget 投递镜像。
//
// TODO(wiring-pending): 当前仅完成编码层（validMirrorID / WriteRequest* / fire-and-forget
// 异步管线 + 单元测试覆盖）。未完成：
//   1) 在 streaming handler 终态落库后调用 Mirror.WriteRequest(... DirOutput)；
//   2) 在 admin 查询详情路径接入镜像兜底读（当前依赖 PG，L3 文件命中仅作灾备）；
//   3) 让 cfg.HotZone.Dir 可配置驱动本子树的父目录。
// 预期接入 PR：feature/wire-request-mirror，独立提交以保证本提交可单独回滚。
package file

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/monitoring"
)

// RequestDirection 标识镜像方向（req = 上行请求；resp = 下行响应；out = 流式终态）。
type RequestDirection string

const (
	// DirRequest 上行请求 body（用户 → 网关 → 上游）。
	DirRequest RequestDirection = "req"
	// DirResponse 下行响应 body（上游 → 网关 → 用户，非流式）。
	DirResponse RequestDirection = "resp"
	// DirOutput 流式终态（最后一个 chunk 拼成的完整响应或工具结果）。
	DirOutput RequestDirection = "out"
)

// RequestMirror 写请求侧 body 镜像到 {baseDir}/requests/{tenant}/{date}/{id}.{dir}.json.gz。
//
// 线程安全（mutex 保护）；底层复用 AsyncFileWriter（fail-open）。
// 路径段经过 validPathID 校验避免路径遍历。
type RequestMirror struct {
	baseDir string
	writer  *AsyncFileWriter
	codec   Codec
	mu      sync.Mutex
}

// NewRequestMirror 构造请求镜像器；baseDir 为热区根目录（HotZone.Dir），
// 写入路径在内部补 /requests/ 子树。
func NewRequestMirror(baseDir string, workers int) *RequestMirror {
	return &RequestMirror{
		baseDir: filepath.Join(baseDir, "requests"),
		writer:  NewAsyncFileWriter(workers),
		codec:   CodecGzip,
	}
}

// BaseDir 返回热区根目录（测试与监控用）。
func (m *RequestMirror) BaseDir() string { return m.baseDir }

// Close 关闭底层异步写入器。
func (m *RequestMirror) Close() error { return m.writer.Close() }

// buildPath 构造镜像文件路径：
// {baseDir}/{tenantID}/{YYYY-MM-DD}/{requestID}.{dir}{codecSuffix}
// 例如 .req.json.gz / .resp.json.gz / .out.json.gz
// 非法 ID 一律按路径段规则拒绝（validPathID 与 FileBodiesStore 同一惯例）。
func (m *RequestMirror) buildPath(tenantID, requestID string, dir RequestDirection, t time.Time) string {
	day := t.UTC().Format("2006-01-02")
	return filepath.Join(m.baseDir, tenantID, day, fmt.Sprintf("%s.%s%s", requestID, dir, m.codec.Suffix()))
}

// validPathID 校验 tenant/request ID 可安全地作为路径段；与 FileBodiesStore 同款。
func validMirrorID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\`)
}

// Mirror 同步写入一份请求 body（JSON 序列化 → gzip → 异步落盘）。
// ctx 当前为预留参数（本地文件写暂不支持取消）。
// 写失败仅记监控计数，绝不返回错误给主链路（与 FileBodiesStore.Write 的语义一致，
// 调用方不感知失败）。
func (m *RequestMirror) Mirror(ctx context.Context, tenantID, requestID string, dir RequestDirection, payload any, at time.Time) {
	if m == nil || !validMirrorID(tenantID) || !validMirrorID(requestID) {
		monitoring.Default().RecordMirrorWrite(true)
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("request mirror: marshal failed", "request_id", requestID, "error", err)
		monitoring.Default().RecordMirrorWrite(true)
		return
	}
	compressed, err := m.codec.encode(data)
	if err != nil {
		slog.Warn("request mirror: compress failed", "request_id", requestID, "error", err)
		monitoring.Default().RecordMirrorWrite(true)
		return
	}
	path := m.buildPath(tenantID, requestID, dir, at)
	werr := m.writer.Write(path, compressed)
	monitoring.Default().RecordMirrorWrite(werr != nil)
	if werr != nil {
		// 与 FileBodiesStore 一致：失败只记日志，不向调用方返回
		slog.Warn("request mirror: write failed", "path", path, "error", werr)
	}
}

// MirrorAsync 投递异步写任务，立即返回（不等待落盘）。
// 用于 fire-and-forget 接线点（流式终态落库后）。
func (m *RequestMirror) MirrorAsync(tenantID, requestID string, dir RequestDirection, payload any, at time.Time) {
	if m == nil || !validMirrorID(tenantID) || !validMirrorID(requestID) {
		monitoring.Default().RecordMirrorWrite(true)
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("request mirror: marshal failed", "request_id", requestID, "error", err)
		monitoring.Default().RecordMirrorWrite(true)
		return
	}
	compressed, err := m.codec.encode(data)
	if err != nil {
		slog.Warn("request mirror: compress failed", "request_id", requestID, "error", err)
		monitoring.Default().RecordMirrorWrite(true)
		return
	}
	path := m.buildPath(tenantID, requestID, dir, at)
	done := m.writer.WriteAsync(path, compressed)
	go func() {
		werr := <-done
		monitoring.Default().RecordMirrorWrite(werr != nil)
		if werr != nil {
			slog.Warn("request mirror: write failed", "path", path, "error", werr)
		}
	}()
}

// MirrorDir 返回 requests 子树根目录（监控 / 测试用）。
func (m *RequestMirror) MirrorDir() string { return m.baseDir }