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
// 接线状态（2026-09-30 P4 落地；2026-10-01 F5 补第三落库点）：
//  1. ✅ telemetry（request_logs_bodies_hot 三件套）：Client.persistRequestLog
//     顶部（PG 往返与 degraded 早退之前）经 SetBodyMirror 注入闭包投递，
//     PG 不可用时镜像仍写入；S4 停写门同键同门。
//  2. ✅ session bodies 三件套（turn+bodies 事务**提交成功**后 + final_full 行）：
//     SessionWriterV2.SetBodyMirror（domains/session/v2），装配闭包由
//     cmd/gateway storageRuntime.bodyMirrorFn() 注入两个消费方。镜像在
//     tx.Commit 之后投递（2026-09-30 审计 F-A：commit 失败回滚时 PG 无行，
//     先投递即成孤儿镜像）。
//  3. ✅ admin HTTP ingest（/api/telemetry/request-log，request_logs_bodies_hot
//     的 req/resp 两件套）：admin.SetIngesterBodyMirror 注入，persistRequestLog
//     顶部投递——与第 1 点同一边界语义（入口即投递，PG 停机期间仍落镜像），
//     区别是本路径无 outbound body 字段，故只投 req/resp。
//  4. ✖ admin 查询详情路径**有意不接**镜像兜底读——方案 §3-H3.3 明确读路径
//     不参与 L3 回源（避免文件缺失导致详情 404 的语义分裂），镜像只服务
//     灾备人工取数 + 文件命中观察。
//  5. ✅ cfg.HotZone.Dir 驱动子树父目录（NewRequestMirror(hz.Dir, 0)）。
//
// 装配开关：HotZone.RequestMirrorEnabled（默认 true）；lite 模式不装配
// （沿用既有 session_bodies 写入路径，不重复镜像）。
package file

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
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
// 构造后字段全部不可变，无需锁；底层复用 AsyncFileWriter（自身并发安全，fail-open）。
// 路径段经过 validPathID 校验避免路径遍历。
type RequestMirror struct {
	baseDir string
	writer  *AsyncFileWriter
	codec   Codec
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

// ConvertBodyPayload 是「*string 正文 → 镜像 JSON 字面量」的**单一事实源**
// （2026-10-01 F5）：nil → "null"；空串或非法 JSON → "{}"；否则原样返回。
//
// 换算必须与 request_logs_bodies_hot 的 $N::jsonb 绑定换算逐字一致，否则
// 「镜像 gunzip 内容 == PG 列内容」的对账承诺在某一侧悄悄失配。三个消费方
// 共用本函数：telemetry.Client.mirrorRequestBodies（经 strPtrToJSON 转调）、
// admin ingester（F5 第三落库点）、以及它们共用的落库绑定路径。
func ConvertBodyPayload(s *string) string {
	if s == nil {
		return "null"
	}
	if *s == "" || !json.Valid([]byte(*s)) {
		return "{}"
	}
	return *s
}

// MirrorablePayload 报告换算后的载荷是否值得写镜像（**单一事实源**）：
// "null"（无数据）与 "{}"（空串/非法 JSON 的收敛值）跳过——无正文内容可对账，
// 落这两个字面量只是噪声文件。
//
// 与 PG 侧不对称：PG 的 NULLIF 只剔除 'null'，"{}" 会照落库。所以对账脚本
// 必须按 F4 口径豁免 PG 侧的 null/{} 行；镜像侧两类都不写。
func MirrorablePayload(payload string) bool {
	return payload != "null" && payload != "{}"
}
