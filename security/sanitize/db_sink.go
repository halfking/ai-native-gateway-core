// Package sanitize — db_sink.go
//
// 存储优化方案 v2 S1a（migration 706 / plan §1 session_censors 行）：把
// SmartSaniGuard 的占位符→原始值映射双写进 public.session_censors_hot。
// 此前映射只存 Redis（TTL 30min 即永久丢失），无法审计回溯；双写后 DB 为
// 权威、Redis 降级为热缓存（plan D5）。
//
// 合规（plan §1/§9）：原文按 AES-GCM 加密落库（LLM_GATEWAY_SESSION_CENSOR_KEY，
// 任意非空口令派生）；密钥未配置时降级为只存 type+占位符
// （original_encrypted=NULL），可在部署侧按需启用可逆。
//
// R71 更正（2026-10-01，实测真库）：上述「DB 为权威 / 可逆」在当前部署下
// **不成立**，验收时不要按此前提判 PASS。
//   - 真库 public.session_censors 29410 行，count(original_encrypted) = 0：
//     envs.samples 的 LLM_GATEWAY_SESSION_CENSOR_KEY 默认为空，线上未配置。
//   - 全仓没有任何解密读取方（grep original_encrypted 只命中本文件）。
//
// 因此本表现实语义是「脱敏元数据审计（哪条会话脱敏过哪几类、多少条）」，
// **不是**「原文可回溯」。要恢复可回溯需同时满足：配置密钥 + 补一个带读
// 审计的读取端点；只配密钥会让列里出现无人能解的密文。
// 另一个后果：RLS 策略当前只有 USING 没有 WITH CHECK，且写入路径不设
// app.current_tenant —— 之所以没暴露，是因为网关连接角色是
// rolsuper/rolbypassrls。改用非 superuser 角色前必须先补这两项。
package sanitize

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// censorEntryArgsPerRow is the number of bind parameters each mapping row
// contributes to the batch INSERT. The failure log used to divide by 6, which
// is only right when the row count is a multiple of 3.
const censorEntryArgsPerRow = 7

// CensorEntry is one placeholder→original mapping row.
type CensorEntry struct {
	Placeholder   string // {SENSITIVE:phone:3}
	SensitiveType string // phone / id_card / email / ...
	Original      string // 原始敏感值（明文传入，sink 侧加密）
}

// CensorSink persists sanitize mappings for audit/recovery beyond the Redis
// TTL. Implementations must be best-effort safe: SaveCensorMappings is called
// on the request path's persistence step and must never block it beyond its
// own budget (implementations own their timeouts and error handling).
type CensorSink interface {
	SaveCensorMappings(ctx context.Context, tenantID, sessionID string, entries []CensorEntry)
}

// censorAEADKey loads the AES-256 key derived from
// LLM_GATEWAY_SESSION_CENSOR_KEY. Returns nil when unset — degraded mode
// (original_encrypted=NULL).
func censorAEADKey() cipher.AEAD {
	secret := strings.TrimSpace(os.Getenv("LLM_GATEWAY_SESSION_CENSOR_KEY"))
	if secret == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil
	}
	return aead
}

// encryptOriginal returns nonce||ciphertext, base64-encoded; "" on degraded
// mode or failure (caller then persists NULL).
func encryptOriginal(aead cipher.AEAD, plaintext string) string {
	if aead == nil {
		return ""
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return ""
	}
	sealed := aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed)
}

// PostgresCensorSink writes sanitize mappings into public.session_censors_hot
// (PartitionManager promotes rows to monthly partitions). Rows are
// insert-only; per-row failures are logged, never panicked.
type PostgresCensorSink struct {
	db    *sql.DB
	skip  bool // nil-db constructor sentinel: every call is a no-op
	limit int  // per-request row cap (defence against pathological maps)
}

// NewPostgresCensorSink wires the session_censors sink on the gateway's
// stdlib DB handle. db may be nil (tests / no-DB mode): the sink then no-ops.
func NewPostgresCensorSink(db *sql.DB) *PostgresCensorSink {
	return &PostgresCensorSink{db: db, skip: db == nil, limit: 200}
}

// SaveCensorMappings persists one request's mapping batch. Best-effort:
// bounded to 2s, failures warn once per batch and never propagate.
func (s *PostgresCensorSink) SaveCensorMappings(ctx context.Context, tenantID, sessionID string, entries []CensorEntry) {
	if s == nil || s.skip || len(entries) == 0 || sessionID == "" {
		return
	}
	if tenantID == "" {
		tenantID = "_unknown"
	}
	if len(entries) > s.limit {
		slog.Warn("sanitize censor sink: entry cap reached, audit rows dropped",
			"session_id", sessionID, "tenant_id", tenantID,
			"entries", len(entries), "cap", s.limit)
		entries = entries[:s.limit]
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	aead := censorAEADKey()
	now := time.Now().UTC()
	partitionDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	var b strings.Builder
	b.WriteString(`INSERT INTO public.session_censors_hot
		(session_id, tenant_id, placeholder, sensitive_type, original_encrypted, created_at, partition_date)
		VALUES `)
	args := make([]any, 0, len(entries)*6)
	for i, e := range entries {
		if i > 0 {
			b.WriteString(",")
		}
		base := i * censorEntryArgsPerRow
		fmt.Fprintf(&b, "($%d,$%d,$%d,$%d,$%d,$%d,$%d)", base+1, base+2, base+3, base+4, base+5, base+6, base+7)
		enc := encryptOriginal(aead, e.Original)
		var encArg any
		if enc != "" {
			encArg = enc
		}
		args = append(args,
			sessionID, tenantID, e.Placeholder, e.SensitiveType, encArg, now, partitionDate)
	}

	if _, err := s.db.ExecContext(ctx, b.String(), args...); err != nil {
		slog.Warn("sanitize censor sink: persist mappings failed (audit degraded to redis-only)",
			"session_id", sessionID, "rows", len(args)/censorEntryArgsPerRow, "error", err)
	}
}
