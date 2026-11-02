package admin

// hotzone_body_source_test.go —— 热区镜像兜底正文源（2026-11-02 chat tab 空态根修）。
//
// ★ 这道门守的是「统一详情在 PG 正文缺失时能不能把正文救回来」：
//
//	PG 会话族正文的写入受录制同意门控（同意关闭 ⇒ 一行不写）⇒ 统一详情
//	body_status=unavailable ⇒ 前端「对话」页四个页签全空。
//	热区镜像里同一批请求的正文仍在磁盘上 ⇒ 兜底回源是唯一的补救路径。
//
// ★ 判据覆盖：命中/未命中、日期目录兜底扫描、路径穿越拒绝、超限拒绝、
//   ★ 以及**回源后 body_status 必须重算**（否则「有正文」也会显示成「未落库」）。

import (
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/requestdetail"
	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// writeMirrorFile 写一个 gzip 镜像文件（{dir}/{tenant}/{day}/{rid}.{kind}.json.gz）。
func writeMirrorFile(t *testing.T, requestsDir, tenant, day, rid, kind, payload string) {
	t.Helper()
	dir := filepath.Join(requestsDir, tenant, day)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	f, err := os.Create(filepath.Join(dir, rid+"."+kind+".json.gz"))
	require.NoError(t, err)
	defer f.Close()
	zw := gzip.NewWriter(f)
	_, err = zw.Write([]byte(payload))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
}

func TestHotzoneBodySourceFindsRequestAndResponseByRequestDate(t *testing.T) {
	dir := t.TempDir()
	writeMirrorFile(t, dir, "default", "2026-11-02", "rid-1", "req", `[{"role":"system","content":"s"}]`)
	writeMirrorFile(t, dir, "default", "2026-11-02", "rid-1", "resp", `[{"role":"assistant","content":"a"}]`)

	src := NewHotzoneBodySource(dir)
	require.NotNil(t, src)
	at := time.Date(2026, 11, 2, 3, 22, 48, 0, time.UTC)

	bodies, ok, err := src.Lookup("default", "rid-1", at)
	require.NoError(t, err)
	require.True(t, ok)
	require.JSONEq(t, `[{"role":"system","content":"s"}]`, string(bodies.RequestBody))
	require.JSONEq(t, `[{"role":"assistant","content":"a"}]`, string(bodies.ResponseBody))
}

func TestHotzoneBodySourceFallsBackToDayDirScanWhenDateDiffers(t *testing.T) {
	dir := t.TempDir()
	// 镜像投递在另一个 UTC 日（投递时刻与请求时刻跨日 / 漂移）。
	writeMirrorFile(t, dir, "default", "2026-10-28", "rid-2", "req", `[{"role":"user","content":"u"}]`)

	src := NewHotzoneBodySource(dir)
	src.now = func() time.Time { return time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC) }

	// at 指向一个**没有**镜像文件的日期 ⇒ 必须靠目录扫描兜底命中。
	bodies, ok, err := src.Lookup("default", "rid-2", time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.True(t, ok)
	require.JSONEq(t, `[{"role":"user","content":"u"}]`, string(bodies.RequestBody))
}

func TestHotzoneBodySourceMissIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	writeMirrorFile(t, dir, "default", "2026-11-02", "other", "req", `[]`)

	src := NewHotzoneBodySource(dir)
	bodies, ok, err := src.Lookup("default", "rid-missing", time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.False(t, ok)
	require.Empty(t, bodies.RequestBody)
}

func TestHotzoneBodySourceRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	src := NewHotzoneBodySource(dir)
	for _, bad := range []string{"../etc", "a/b", "..", "."} {
		_, _, err := src.Lookup("default", bad, time.Now())
		require.Error(t, err, "路径段 %q 必须被拒", bad)
	}
	_, _, err := src.Lookup("../tenant", "rid", time.Now())
	require.Error(t, err, "租户段必须被拒")
}

// TestReadSessionTurnsBodiesFallsBackToHotzoneMirror 是本轮的核心判据：
// PG 三件套全空 ⇒ 回源热区镜像 ⇒ 正文有值 + body_source=hotzone_mirror
// + body_status=available（**不是**未落库）。
func TestReadSessionTurnsBodiesFallsBackToHotzoneMirror(t *testing.T) {
	dir := t.TempDir()
	writeMirrorFile(t, dir, "default", "2026-11-02", "rid-fb", "req", `[{"role":"user","content":"hi"}]`)
	writeMirrorFile(t, dir, "default", "2026-11-02", "rid-fb", "resp", `[{"role":"assistant","content":"yo"}]`)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery(`(?s)SELECT t.session_id, t.turn_no,.*FROM public.session_turns_with_current_month t`).
		WithArgs("rid-fb").
		WillReturnRows(pgxmock.NewRows([]string{
			"session_id", "turn_no", "tenant_id", "request_delta", "response_delta", "outbound_body", "model", "latency_ms", "ts",
		}).AddRow("session-1", 1, "default", nil, nil, nil, "model-a", 10, time.Date(2026, 11, 2, 3, 22, 48, 0, time.UTC)))

	// ★ 回源受录制同意门控（与写入面同一个开关）⇒ 测试里显式打开。
	consentOn := true
	settings.TestingConsentOverride = &consentOn
	t.Cleanup(func() { settings.TestingConsentOverride = nil })

	reader := &pgBodyReader{db: mock, hotzone: NewHotzoneBodySource(dir)}
	bodies, meta, err := reader.ReadSessionTurnsBodies(context.Background(), "rid-fb", false)
	require.NoError(t, err)
	require.JSONEq(t, `[{"role":"user","content":"hi"}]`, string(bodies.RequestBody))
	require.JSONEq(t, `[{"role":"assistant","content":"yo"}]`, string(bodies.ResponseBody))
	require.Equal(t, requestdetail.SourceHotzoneMirror, meta.BodySource)
	// ★ 正文有了 ⇒ body_status 必须重算成 available（否则前端仍显示「未落库」）。
	require.Equal(t, "available", meta.BodyStatus)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestReadSessionTurnsBodiesKeepsPGBodiesWhenPresent 判据：PG 有正文时
// **绝不**用镜像覆盖（权威载荷优先），body_source 仍是 session_turns。
func TestReadSessionTurnsBodiesKeepsPGBodiesWhenPresent(t *testing.T) {
	dir := t.TempDir()
	writeMirrorFile(t, dir, "default", "2026-11-02", "rid-pg", "req", `[{"role":"user","content":"from-mirror"}]`)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery(`(?s)SELECT t.session_id, t.turn_no,.*FROM public.session_turns_with_current_month t`).
		WithArgs("rid-pg").
		WillReturnRows(pgxmock.NewRows([]string{
			"session_id", "turn_no", "tenant_id", "request_delta", "response_delta", "outbound_body", "model", "latency_ms", "ts",
		}).AddRow("session-1", 1, "default", []byte(`{"messages":["from-pg"]}`), nil, nil, "model-a", 10, time.Date(2026, 11, 2, 3, 22, 48, 0, time.UTC)))

	reader := &pgBodyReader{db: mock, hotzone: NewHotzoneBodySource(dir)}
	bodies, meta, err := reader.ReadSessionTurnsBodies(context.Background(), "rid-pg", false)
	require.NoError(t, err)
	require.JSONEq(t, `{"messages":["from-pg"]}`, string(bodies.RequestBody))
	require.Equal(t, requestdetail.SourceSessionTurns, meta.BodySource)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestHotzoneBodySourceDisabledIsNoOp 判据：未装配热区（requestsDir 为空）
// ⇒ 读面行为与改动前完全一致（不回源、不报错）。
func TestHotzoneBodySourceDisabledIsNoOp(t *testing.T) {
	require.Nil(t, NewHotzoneBodySource("  "))
	src := NewHotzoneBodySource("")
	require.Nil(t, src)
	bodies, ok, err := src.Lookup("default", "rid", time.Now())
	require.NoError(t, err)
	require.False(t, ok)
	require.Empty(t, bodies.RequestBody)
}
