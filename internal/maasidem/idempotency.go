// Package maasidem 收敛 MaaS 三个领域服务（ledger / inventory / trading）
// 各自的幂等实现。
//
// ★ D6 / D8（2026-11-19 审计轮）：
//
// 三个服务各自复制了一份 checkIdempotency / recordIdempotency，两份缺陷
// 在三处同时存在：
//
//	① **D8：幂等键空串不被拒**。契约明写「Required for all commands」，
//	   实现却零校验 ⇒ 同一 tenant + command_type 下所有空键命令**互相误判
//	   为重放**（第二个命令直接返回第一个的响应）。
//	② **D6：重放返回零值空响应**。`recordIdempotency` 落库固定写
//	   `'{}'::jsonb` ⇒ request/response payload **从未序列化**；调用方又写
//	   `// TODO: Unmarshal cached response` ⇒ 重放拿到空结构（空 hold_id /
//	   空 status / 空 entry_id），客户端无法对账。
//
// 本包把三者收敛到一份实现，并提供：
//
//	· RequireKey —— 幂等键非空校验（命令入口调用）；
//	· Check      —— 按 (tenant, command_type, key) 取回已落库的响应 JSON；
//	· Record     —— 真正序列化 request/response 后落库；
//	· Replay     —— 把缓存 JSON 反序列化成对应命令的响应类型。
package maasidem

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
)

// Queryer 是幂等查询所需的最小 DB 能力面。
//
// ★ 用窄接口而不是 *pgxpool.Pool：单测可以用 pgxmock 的池注入，
// 不必为「查一行」这种事去拉真库。
type Queryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Execer 是幂等落库所需的最小 DB 能力面（pgx.Tx 满足）。
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ErrorIdempotencyKeyRequired 是幂等键缺失时的机器可读错误码。
//
// ★ D8：契约（contracts/maascontracts/types.go 的 CommandMeta 注释）明写
// 「Required for all commands」，此前实现零校验 ⇒ 空键命令互相误判为重放。
const ErrorIdempotencyKeyRequired = "MAAS_IDEMPOTENCY_KEY_REQUIRED"

// idempotencyTTL 是幂等记录的有效期。
const idempotencyTTL = 7 * 24 * time.Hour

// RequireKey 校验幂等键非空。
//
// 空串（含纯空白）⇒ 返回 maascontracts.Error（错误码
// MAAS_IDEMPOTENCY_KEY_REQUIRED，retryable=false —— 重试同一个空键没有
// 意义，客户端必须先补一个键）。
func RequireKey(idempotencyKey string) error {
	if strings.TrimSpace(idempotencyKey) == "" {
		return &maascontracts.Error{
			Code:      ErrorIdempotencyKeyRequired,
			Message:   "idempotency key is required for all write commands",
			Retryable: false,
		}
	}
	return nil
}

// Check 按 (tenant_id, command_type, idempotency_key) 取回已落库的
// **响应 JSON**。无记录 ⇒ (nil, nil)。
//
// ★ D6：返回原始 JSON（json.RawMessage），由调用方按 commandType
// 反序列化成对应的响应类型（见 Replay）。
func Check(ctx context.Context, pool Queryer, tenantID, commandType, idempotencyKey string) (json.RawMessage, error) {
	if pool == nil {
		return nil, fmt.Errorf("maasidem: nil pool")
	}
	var responsePayload []byte
	err := pool.QueryRow(ctx, `
		SELECT response_payload
		FROM maas_idempotency_log
		WHERE tenant_id = $1 AND command_type = $2 AND idempotency_key = $3
		AND status = 'success'
		AND expires_at > NOW()
	`, tenantID, commandType, idempotencyKey).Scan(&responsePayload)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query idempotency log: %w", err)
	}
	return json.RawMessage(responsePayload), nil
}

// Record 落库一次成功执行的幂等记录。
//
// ★ D6：request / response **真正序列化**后落 JSONB（旧实现固定写
// `'{}'::jsonb` ⇒ payload 从未落库，重放只能解出空结构）。
func Record(ctx context.Context, tx Execer, tenantID, commandType, idempotencyKey string, request, response any) error {
	if tx == nil {
		return fmt.Errorf("maasidem: nil transaction")
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("marshal idempotency request payload: %w", err)
	}
	responseJSON, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("marshal idempotency response payload: %w", err)
	}
	expiresAt := time.Now().UTC().Add(idempotencyTTL)

	_, err = tx.Exec(ctx, `
		INSERT INTO maas_idempotency_log (
			tenant_id, command_type, idempotency_key,
			request_payload, response_payload, status, executed_at, expires_at
		) VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, 'success', NOW(), $6)
		ON CONFLICT (tenant_id, command_type, idempotency_key) DO NOTHING
	`, tenantID, commandType, idempotencyKey, requestJSON, responseJSON, expiresAt)

	return err
}

// Replay 把缓存的响应 JSON 反序列化进 out。
//
// ★ D6：反序列化失败必须**返回明确错误**，不得像旧实现那样返回零值空
// 响应（那正是本缺陷本身）。调用方应把该错误当作「幂等记录损坏」处理。
func Replay(cached json.RawMessage, out any) error {
	if len(cached) == 0 {
		return fmt.Errorf("maasidem: empty cached payload")
	}
	if err := json.Unmarshal(cached, out); err != nil {
		return fmt.Errorf("unmarshal cached idempotent response: %w", err)
	}
	return nil
}
