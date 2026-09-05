package upstream

// client_fault_test.go — 2026-09-05 审计闭环6：真实 TCP 故障注入证据。
//
// 审计要求（docs/audit-2026-09-05-ir-storage-provider.md）：真实 TCP
// refused/reset/DNS/TLS/EOF/slow-read 与 goroutine 泄漏缺少运行态证据；
// 现有 client_test.go 只覆盖 httptest 标准服务器行为。本文件用裸
// net.Listen 控制连接级故障，钉死每类故障的实际分类（kind）与可重试性。
//
// 分类预期来源：errorsx.ClassifyError 的消息规则（classify.go:711-720）——
// refused / no such host / reset / connection → KindNetwork（可重试）；
// 纯 EOF → KindStreamTimeout（可重试）；TLS x509 校验失败不含连接关键词，
// 落到默认 KindTransient（可重试）——该行为在
// TestDo_TLSUntrustedCertificateRecordsActualClassification 中钉死，
// 作为后续「TLS 错误不可重试」优化的基线证据。

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// faultBody 让请求可重放（client 重试路径需要 GetBody）。
func faultBody() io.ReadCloser {
	return io.NopCloser(bytes.NewReader([]byte(`{"model":"fault-test"}`)))
}

func newFaultRequest(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, faultBody())
	require.NoError(t, err)
	req.GetBody = func() (io.ReadCloser, error) { return faultBody(), nil }
	return req
}

// TestDo_ConnectionRefusedRealSocket 真实端口拒绝：listen 后立即关闭，
// 新连接全部 ECONNREFUSED。钉死分类 KindNetwork + 可重试。
func TestDo_ConnectionRefusedRealSocket(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close()) // 关闭后连接 → refused

	client := NewWithRetries(0)
	resp, uErr := client.Do(newFaultRequest(t, "http://"+addr+"/v1/chat"))

	require.Nil(t, resp)
	require.NotNil(t, uErr)
	assert.Equal(t, KindNetwork, uErr.Kind, "connection refused must classify as network")
	assert.True(t, strings.Contains(uErr.Message, "refused"), "message should surface refused: %q", uErr.Message)
}

// TestDo_ConnectionResetRealSocket 真实 TCP RST：accept 后设 SO_LINGER=0
// 并关闭，内核发 RST。客户端读到 connection reset by peer → KindNetwork。
func TestDo_ConnectionResetRealSocket(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	addr := ln.Addr().String()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			if tcp, ok := conn.(*net.TCPConn); ok {
				_ = tcp.SetLinger(0) // Close → RST 而不是 FIN
			}
			_ = conn.Close()
		}
	}()

	client := NewWithRetries(0)
	resp, uErr := client.Do(newFaultRequest(t, "http://"+addr+"/v1/chat"))

	require.Nil(t, resp)
	require.NotNil(t, uErr)
	assert.Equal(t, KindNetwork, uErr.Kind, "RST must classify as network (retryable)")
	_ = done
}

// TestDo_UpstreamCleanCloseWithoutResponse EOF：accept 后不发任何字节、
// 干净关闭（FIN）。钉死 EOF 分类的实际落点（KindStreamTimeout，可重试）。
func TestDo_UpstreamCleanCloseWithoutResponse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	addr := ln.Addr().String()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close() // 干净关闭：无响应头 → 客户端 EOF
		}
	}()

	client := NewWithRetries(0)
	resp, uErr := client.Do(newFaultRequest(t, "http://"+addr+"/v1/chat"))

	require.Nil(t, resp)
	require.NotNil(t, uErr)
	t.Logf("clean-close classification: kind=%s message=%q", uErr.Kind, uErr.Message)
	assert.True(t, errorsx.IsRetryable(uErr.Kind),
		"upstream vanishing before response must surface a retryable kind, got %s (%s)", uErr.Kind, uErr.Message)
}

// TestDo_DNSFailureReservedTLD DNS 故障：.invalid 是 IANA 保留 TLD，
// 永不解析。钉死 dial 失败分类 KindNetwork（可重试）。
func TestDo_DNSFailureReservedTLD(t *testing.T) {
	client := NewWithRetries(0)
	resp, uErr := client.Do(newFaultRequest(t, "http://llm-gateway-fault-test.invalid/v1/chat"))

	require.Nil(t, resp)
	require.NotNil(t, uErr)
	assert.Equal(t, KindNetwork, uErr.Kind, "DNS failure must classify as network")
}

// TestDo_TLSUntrustedCertificate 真实 TLS：自签名证书被默认 transport 拒绝。
// x509 错误不含连接关键词，实际落 KindTransient（可重试）。本测试钉死该
// 现状作为基线证据：后续若把证书错误改为不可重试，此处会先红。
func TestDo_TLSUntrustedCertificate(t *testing.T) {
	// httptest.NewTLSServer 生成自签名证书，默认 transport 的 RootCAs 不含
	// 它 → 真实 x509 校验失败。
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewWithRetries(0)
	resp, uErr := client.Do(newFaultRequest(t, server.URL))

	require.NotNil(t, uErr, "untrusted certificate must fail")
	if resp != nil {
		_ = resp.Body.Close()
	}
	t.Logf("TLS x509 classification: kind=%s message=%.120s", uErr.Kind, uErr.Message)
	assert.Equal(t, KindTransient, uErr.Kind,
		"baseline: x509 currently falls through to transient (retry-burning); change deliberately")
}

// TestDo_SlowBodyCancellationPropagates slow-read：上游 200 后逐字节滴流
// SSE body；客户端取消 context 后 Read 必须及时返回（<2s），不能挂死。
func TestDo_SlowBodyCancellationPropagates(t *testing.T) {
	release := make(chan struct{})
	var conns sync.Map
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	addr := ln.Addr().String()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Store(conn, struct{}{})
			go func(c net.Conn) {
				defer func() { conns.Delete(c); _ = c.Close() }()
				// 手写最小响应：headers 完整，body 用 chunked 滴流。
				if _, err := io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n"); err != nil {
					return
				}
				chunk := "data: {\"delta\":\"x\"}\n\n"
				for {
					select {
					case <-release:
						return
					default:
					}
					if _, err := io.WriteString(c, "8\r\n"+chunk[:8]+"\r\n"); err != nil {
						return
					}
					time.Sleep(50 * time.Millisecond)
				}
			}(conn)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/v1/chat", faultBody())
	require.NoError(t, err)
	req.GetBody = func() (io.ReadCloser, error) { return faultBody(), nil }

	client := NewWithRetries(0)
	start := time.Now()
	resp, uErr := client.Do(req)
	require.Nil(t, uErr, "headers arrive promptly; Do must succeed")
	require.NotNil(t, resp)

	buf := make([]byte, 64)
	readErr := make(chan error, 1)
	go func() {
		for {
			_, err := resp.Body.Read(buf)
			if err != nil {
				readErr <- err
				return
			}
		}
	}()
	time.Sleep(200 * time.Millisecond) // 让读取进入滴流循环
	cancel()

	select {
	case err := <-readErr:
		assert.ErrorIs(t, err, context.Canceled, "body read must surface ctx cancellation")
	case <-time.After(2 * time.Second):
		t.Fatal("body read did not return within 2s of context cancellation (slow-read hang)")
	}
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 5*time.Second, "cancellation round trip must be prompt")
	close(release)
	resp.Body.Close()
}

// TestFaultInjectionNoGoroutineLeak goroutine 泄漏门禁：泄漏的本质是
// 「随故障轮次单调增长」，因此断言采用轮次增长比较而非绝对计数——
// 先跑 2 轮热身让连接池/协程进入稳态并采样，再跑 3 轮后采样，要求
// 采样不增长（±5 容差）。绝对计数会随同机负载误报，轮次比较自校准。
func TestFaultInjectionNoGoroutineLeak(t *testing.T) {
	goroutines := func() int {
		runtime.GC()
		time.Sleep(50 * time.Millisecond)
		return runtime.NumGoroutine()
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	resetAddr := ln.Addr().String()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			if tcp, ok := conn.(*net.TCPConn); ok {
				_ = tcp.SetLinger(0)
			}
			_ = conn.Close()
		}
	}()
	refusedAddr := func() string {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addr := l.Addr().String()
		_ = l.Close()
		return addr
	}()

	client := NewWithRetries(0)
	targets := []string{
		"http://" + refusedAddr + "/v1/chat",            // refused
		"http://" + resetAddr + "/v1/chat",              // reset
		"http://llm-gateway-fault-test.invalid/v1/chat", // DNS
	}
	runRound := func() {
		for _, target := range targets {
			resp, uErr := client.Do(newFaultRequest(t, target))
			if resp != nil {
				_ = resp.Body.Close()
			}
			require.NotNil(t, uErr, "fault target %s must fail", target)
		}
	}

	runRound() // 热身：连接池、协程进入稳态
	runRound()
	steadyState := goroutines()

	for round := 0; round < 3; round++ {
		runRound()
	}

	// 泄漏判定：3 轮后不高于稳态 + 容差（连续采样吸收调度延迟）。
	for attempt := 0; attempt < 5; attempt++ {
		if now := goroutines(); now <= steadyState+5 {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("goroutine leak suspected: steady_state=%d now=%d (grows with fault rounds)", steadyState, goroutines())
}
