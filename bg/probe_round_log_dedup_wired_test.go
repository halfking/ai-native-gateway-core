package bg

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestLogProbeRoundRootCauseDedupWired —— 接线级验证（变异臂）：
// logProbeRoundRootCause 真的走了闸——同 (provider, model, rootCause) 的
// 重复轮只出 1 条；窗后放行且 detail 带 suppressed_repeats。
func TestLogProbeRoundRootCauseDedupWired(t *testing.T) {
	orig := probeRoundLogGateInstance
	probeRoundLogGateInstance = newProbeRoundLogGate(50 * time.Millisecond)
	defer func() { probeRoundLogGateInstance = orig }()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	round := nodeProbeRoundResult{
		ok:            false,
		providerID:    24,
		outboundModel: "glm-5.3-flash",
		httpStatus:    429,
		errCode:       "http_429",
		errDetail:     "upstream returned HTTP 429",
		rootCause:     ProbeRootCauseNode,
	}
	logProbeRoundRootCause("direct", round)
	logProbeRoundRootCause("direct", round)
	if n := strings.Count(buf.String(), "node probe failed"); n != 1 {
		t.Fatalf("window repeats must collapse to 1 log line, got %d in:\n%s", n, buf.String())
	}

	time.Sleep(60 * time.Millisecond)
	logProbeRoundRootCause("direct", round)
	tail := buf.String()[strings.LastIndex(buf.String(), "node probe failed"):]
	if !strings.Contains(tail, "suppressed_repeats=1") {
		t.Fatalf("post-cooldown detail must carry suppressed_repeats=1, got:\n%s", tail)
	}
}

// TestLogProbeRoundRootCauseRootCauseChangeNotGated —— 根因变化（node→protocol）
// 是新的第一现场，立刻放行。
func TestLogProbeRoundRootCauseRootCauseChangeNotGated(t *testing.T) {
	orig := probeRoundLogGateInstance
	probeRoundLogGateInstance = newProbeRoundLogGate(1 * time.Hour)
	defer func() { probeRoundLogGateInstance = orig }()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	nodeRound := nodeProbeRoundResult{ok: false, providerID: 24, outboundModel: "m", errCode: "http_503", rootCause: ProbeRootCauseNode}
	protoRound := nodeProbeRoundResult{ok: false, providerID: 24, outboundModel: "m", errCode: "http_404", rootCause: ProbeRootCauseProtocol}

	logProbeRoundRootCause("direct", nodeRound)
	logProbeRoundRootCause("direct", protoRound)
	out := buf.String()
	if !strings.Contains(out, "protocol mismatch") {
		t.Fatalf("root cause change must be logged immediately, got:\n%s", out)
	}
}
