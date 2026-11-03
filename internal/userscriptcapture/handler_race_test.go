// handler_race_test.go — ServeHTTP 的并发安全回归门（12h 审计 P1-1）。
//
// 钉住「惰性初始化写在请求路径上 ⇒ 读改写共享字段 ⇒ 数据竞争」这一形态：
// Handler 是**进程级单例**（main 装配后挂 mux，net/http 每请求一 goroutine）
// ⇒ 并发请求同时读写 h.sessionExists 是数据竞争（go test -race 会红；
// 生产上是未定义行为——接口值可能被读成撕裂的 nil ⇒ nil 指针 panic）。
//
// 本门在 -race 下才有牙；普通 `go test` 跑它只是防「未来又改回共享写」。
package userscriptcapture

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestHandlerConcurrentSessionExistsIsRaceFree 命中 M3 canonical session
// 标注分支（X-Gw-Session-Id），并发发起多路请求。
//
// 期望：全部 200，且 -race 下无竞争报告。
func TestHandlerConcurrentSessionExistsIsRaceFree(t *testing.T) {
	secret := "secret-32-bytes-xxxxxxxxxxxxxxxxx"
	b := validBatch()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal batch: %v", err)
	}

	h := NewHandler(HandlerConfig{Secret: secret, Keys: fakeKeys{tenant: "tenant-1"}})
	// store 保持非 nil（惰性初始化分支的前提）；Begin 直接报错 ⇒
	// SessionExists 返回 false，全程不碰真库。
	h.store = NewStore(PoolBeginner(func(ctx context.Context) (pgx.Tx, error) {
		return nil, errors.New("unit test: no db")
	}))
	h.persistHook = func(context.Context, string, string, Batch) (Ack, error) {
		return Ack{BatchID: "b1", Accepted: 1}, nil
	}

	const workers = 32
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			ts := strconv.FormatInt(time.Now().Unix(), 10)
			nonce := "race-" + strconv.Itoa(i)
			req := httptest.NewRequest(http.MethodPost, "/v1/userscript/captures", bytes.NewReader(raw))
			req.Header.Set("Authorization", "Bearer sk-valid")
			req.Header.Set("X-End-User-Id", "user-1")
			req.Header.Set(HeaderTimestamp, ts)
			req.Header.Set(HeaderNonce, nonce)
			req.Header.Set(HeaderSignature, sign(secret, ts, nonce, raw))
			// 命中惰性初始化分支（M3：canonical 会话存在性校验）。
			req.Header.Set("X-Gw-Session-Id", "s-1")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Errorf("status = %d body=%s", w.Code, w.Body.String())
			}
		}(i)
	}
	wg.Wait()
}

// TestHandlerSessionExistsWithoutStoreIsSafe 钉住「store 未装配 ⇒ 不 panic」。
// SessionExists 走 s.db.Begin；db 为 nil 时旧实现会 nil 指针 panic。
func TestHandlerSessionExistsWithoutStoreIsSafe(t *testing.T) {
	secret := "secret-32-bytes-xxxxxxxxxxxxxxxxx"
	b := validBatch()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal batch: %v", err)
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "nostore-1"

	h := NewHandler(HandlerConfig{Secret: secret, Keys: fakeKeys{tenant: "tenant-1"}})
	h.store = &Store{} // db 为 nil —— 装配缺失的形态
	h.persistHook = func(context.Context, string, string, Batch) (Ack, error) {
		return Ack{BatchID: "b1"}, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/userscript/captures", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer sk-valid")
	req.Header.Set(HeaderTimestamp, ts)
	req.Header.Set(HeaderNonce, nonce)
	req.Header.Set(HeaderSignature, sign(secret, ts, nonce, raw))
	req.Header.Set("X-Gw-Session-Id", "s-1")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}
