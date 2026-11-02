package admin

// hotzone_body_source.go —— 统一详情读面的**热区镜像兜底正文源**（2026-11-02）。
//
// ★ 背景（chat tab 全空的根修链）：
//
//	PG 会话族正文（public.session_bodies）的写入**受录制同意开关门控**
//	（domains/session/v2/bodies_writer.go：RecordingConsentAllowsWrite）。
//	同意关闭 ⇒ 写点直接 return nil ⇒ **一行正文都不落库**
//	⇒ 统一详情读面拿到 body_status=unavailable + bodies={}
//	⇒ 前端「对话」页的 system/user/tool/assistant 四个页签**全空**。
//
//	而热区请求侧镜像（H3 RequestMirror，storage/file/request_mirror.go）
//	**不受该门控**（它是 fire-and-forget 的落盘副本，投递点在事务提交之后）
//	⇒ 同一批请求的正文**仍然在磁盘上**：
//	   {HotZone.Dir}/requests/{tenantID}/{YYYY-MM-DD}/{requestID}.{req|resp|out}.json.gz
//
// ★ 本文件做的事：**兜底**回源 —— PG 有正文时绝不用镜像；PG 没正文时补位。
//
// ★ 为什么这不违反 H3 原设计（2026-09-24 方案 §3-H3.3「读路径不参与 L3 回源」）：
//
//	原决策的理由是「避免文件缺失导致详情 404 的语义分裂」——那是把镜像当
//	**主**读源时的顾虑。本实现是**兜底**：文件缺失只会让 body_status 停在
//	unavailable，**不会**把详情变成 404 ⇒ 原顾虑不成立。
//	★ 另有一条纪律：镜像里的 `.req` 载荷对会话请求是**本轮增量**（session v2
//	  的 request_delta），不是整段历史 ⇒ 回源只补「本轮正文」，跨轮历史仍由
//	  会话页承担（前端 pickTurnRequestMessages 同款取舍）。
//
// ★ 租户隔离：路径里的 tenantID 取自**已被租户作用域过滤过的行**（见
//   admin/unified_detail.go 的 LookupScope 门），不来自请求参数 ⇒ 不存在
//   跨租户读越权。仍做路径段校验（validMirrorSegment）防路径穿越。

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/requestdetail"
)

// hotzoneMirrorMaxScanDays 限定「扫租户日目录找正文」时最多回看多少天。
//
// ★ 为什么要有这个扫描兜底：镜像的日期目录取的是**投递时刻**（UTC 日历日），
//   而调用方给的 at 通常是**请求时间戳** —— 两者在跨日/时钟漂移时可能不同。
//   ⇒ 先按 at 的日期精确命中（常见路径，零扫描），命中不了再有限扫描最近的
//   若干日目录（兜底）。扫描**有界**（默认 32 天）⇒ 不会在超大目录上做无界 IO。
const hotzoneMirrorMaxScanDays = 32

// HotzoneBodySource 从本地热区请求侧镜像读回请求/响应正文。
//
// 零值不可用（requestsDir 为空 ⇒ Lookup 恒 miss）。并发安全（无共享可变状态）。
type HotzoneBodySource struct {
	// requestsDir 是 H3 镜像子树根（{HotZone.Dir}/requests）。
	requestsDir string
	// maxScanDays 覆盖 hotzoneMirrorMaxScanDays（测试可调小）。
	maxScanDays int
	// now 可注入（测试用；nil ⇒ time.Now）。
	now func() time.Time
}

// NewHotzoneBodySource 构造镜像正文源。requestsDir 为空时返回 nil（读面据此
// 跳过镜像回源，避免到处判空）。
func NewHotzoneBodySource(requestsDir string) *HotzoneBodySource {
	dir := strings.TrimSpace(requestsDir)
	if dir == "" {
		return nil
	}
	return &HotzoneBodySource{requestsDir: dir, maxScanDays: hotzoneMirrorMaxScanDays}
}

// RequestsDir 返回镜像子树根（诊断/日志用）。
func (s *HotzoneBodySource) RequestsDir() string {
	if s == nil {
		return ""
	}
	return s.requestsDir
}

// Lookup 读该 request 在镜像里的正文。
//
// 返回值：(bodies, 命中?, 错误)。命中为 false 时 bodies 为零值。
// ★ 只在**三件套全空**时才算命中（有 req 没 resp 也算命中 —— 前端要能分别
//   展示请求侧与响应侧；"有请求无响应"是正常形态，例如失败流）。
func (s *HotzoneBodySource) Lookup(tenantID, requestID string, at time.Time) (requestdetail.Bodies, bool, error) {
	if s == nil || s.requestsDir == "" {
		return requestdetail.Bodies{}, false, nil
	}
	if !validMirrorSegment(tenantID) || !validMirrorSegment(requestID) {
		return requestdetail.Bodies{}, false, fmt.Errorf("hotzone body source: invalid path segment")
	}
	tenantDir := filepath.Join(s.requestsDir, tenantID)
	for _, day := range s.candidateDays(tenantDir, at) {
		base := filepath.Join(tenantDir, day)
		reqBody, reqOK, err := readMirrorPayload(filepath.Join(base, requestID+".req.json.gz"))
		if err != nil {
			return requestdetail.Bodies{}, false, err
		}
		respBody, respOK, err := readMirrorPayload(filepath.Join(base, requestID+".resp.json.gz"))
		if err != nil {
			return requestdetail.Bodies{}, false, err
		}
		if !reqOK && !respOK {
			continue
		}
		outBody, _, err := readMirrorPayload(filepath.Join(base, requestID+".out.json.gz"))
		if err != nil {
			return requestdetail.Bodies{}, false, err
		}
		return requestdetail.Bodies{
			RequestBody:  reqBody,
			ResponseBody: respBody,
			OutboundBody: outBody,
		}, true, nil
	}
	return requestdetail.Bodies{}, false, nil
}

// candidateDays 给出候选日期目录（按优先级、去重）：
//
//	① at 的 UTC 日历日（镜像投递时刻≈请求时刻时的精确命中路径）；
//	② 今天 / 昨天（at 缺失或与投递时刻跨日时的兜底）；
//	③ 租户目录下最近 N 个日目录（倒序）。
func (s *HotzoneBodySource) candidateDays(tenantDir string, at time.Time) []string {
	seen := make(map[string]bool, 8)
	out := make([]string, 0, 8)
	add := func(day string) {
		if day == "" || seen[day] {
			return
		}
		seen[day] = true
		out = append(out, day)
	}
	if !at.IsZero() {
		add(at.UTC().Format("2006-01-02"))
	}
	nowFn := s.now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn().UTC()
	add(now.Format("2006-01-02"))
	add(now.AddDate(0, 0, -1).Format("2006-01-02"))

	entries, err := os.ReadDir(tenantDir)
	if err != nil {
		return out
	}
	dirs := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	// 日期目录名是 YYYY-MM-DD ⇒ 字典序倒序 == 时间倒序。
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	limit := s.maxScanDays
	if limit <= 0 {
		limit = hotzoneMirrorMaxScanDays
	}
	for _, d := range dirs {
		if len(out) >= limit {
			break
		}
		add(d)
	}
	return out
}

// readMirrorPayload 读一个 gzip 镜像文件，返回其 JSON 原文。
//
// 不存在 ⇒ (nil, false, nil)（**不是错误**：镜像按设计可缺）。
// 超过 MaxBodyFileSize ⇒ ErrBodyTooLarge（与 requestdetail 的口径一致）。
func readMirrorPayload(path string) (json.RawMessage, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("hotzone body source: open %s: %w", path, err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, false, fmt.Errorf("hotzone body source: gzip %s: %w", path, err)
	}
	defer zr.Close()
	raw, err := io.ReadAll(io.LimitReader(zr, requestdetail.MaxBodyFileSize+1))
	if err != nil {
		return nil, false, fmt.Errorf("hotzone body source: read %s: %w", path, err)
	}
	if int64(len(raw)) > requestdetail.MaxBodyFileSize {
		return nil, false, fmt.Errorf("%w: %s exceeds %d bytes", requestdetail.ErrBodyTooLarge, path, requestdetail.MaxBodyFileSize)
	}
	if len(raw) == 0 {
		return nil, false, nil
	}
	if !json.Valid(raw) {
		return nil, false, fmt.Errorf("hotzone body source: %s is not valid JSON", path)
	}
	return json.RawMessage(raw), true, nil
}

// validMirrorSegment 校验路径段（与 storage/file 的 validMirrorID 同款惯例）：
// 非空、非 "." / ".."、不含路径分隔符。
func validMirrorSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\`)
}
