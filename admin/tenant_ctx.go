package admin

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// withTenantTx runs a read-only callback with tenant RLS context. A non-empty
// tenant sets app.current_tenant; all-tenant callers use the explicit bypass
// helper below and must already be authenticated as super_admin/admin_key.
func withTenantTx(ctx context.Context, pool *pgxpool.Pool, tenantID string, fn func(tx pgx.Tx) error) error {
	return withReadOnlyTx(ctx, pool, func(tx pgx.Tx) error {
		if err := setLocalTenantGUC(ctx, tx, tenantID); err != nil {
			return err
		}
		return fn(tx)
	})
}

func withAllTenantReadOnlyTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	return withReadOnlyTx(ctx, pool, func(tx pgx.Tx) error {
		if err := setAllTenantGUC(ctx, tx); err != nil {
			return err
		}
		return fn(tx)
	})
}

// withAllTenantTx runs a read-write callback with the explicit super-admin RLS
// bypass context. Callers must already be authenticated as super_admin/admin_key.
func withAllTenantTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	return withTx(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := setAllTenantGUC(ctx, tx); err != nil {
			return err
		}
		return fn(tx)
	})
}

// withTenantOrAllReadOnlyTx 按 tenantID 是否为空分派租户上下文。
//
// 为什么需要它（2026-10-16）：`withTenantTx` 对空 tenantID 不是"不设 GUC"，
// 而是让 `setLocalTenantGUC` 返回 `empty tenant_id for SET LOCAL app.current_tenant`
// 这个错误。历史上有多处代码注释假定"super_admin 传空 tenant ⇒ 不设 GUC ⇒
// RLS policy 会按 'default' 过滤" —— 那个前提从来没成立过：结果是直接报错。
//
// 分派口径（与 withTenantTx 自身的文档一致）：
//   - tenantID 非空 → 设 app.current_tenant，RLS 按该租户收窄（精确，不越权）；
//   - tenantID 为空 → 走显式 super-admin bypass。
//
// **bypass 的授权前提**：调用方必须保证「tenantID 为空」只可能出现在
// super_admin / admin_key 身上。本仓的既有写法（session_audit.go 的
// `if !isSuper { req.TenantID = callerTenant }`）已经保证了这点：非 super
// 的调用方 tenantID 永远被赋成非空的 callerTenant。任何新调用方若不满足这个
// 前提，就**不能**用本 helper，应直接用 withTenantTx 让空值报错。
func withTenantOrAllReadOnlyTx(ctx context.Context, pool *pgxpool.Pool, tenantID string, fn func(tx pgx.Tx) error) error {
	if tenantID == "" {
		return withAllTenantReadOnlyTx(ctx, pool, fn)
	}
	return withTenantTx(ctx, pool, tenantID, fn)
}

func withReadOnlyTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	return withTx(ctx, pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, fn)
}

type txBeginner interface {
	BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error)
}

// ErrNilDatabasePool 表示连接池根本没配（或是指针为 nil 的空池）。
// 用哨兵错误而不是裸字符串，调用方才能用 errors.Is 判定「这是装配问题、
// 属于存储降级」，而不必去匹配易碎的错误文案（Subtask 4 / §6）。
var ErrNilDatabasePool = errors.New("nil database pool")

func withTx(ctx context.Context, pool txBeginner, options pgx.TxOptions, fn func(tx pgx.Tx) error) error {
	if pool == nil || isNilTxBeginner(pool) {
		return ErrNilDatabasePool
	}
	tx, err := pool.BeginTx(ctx, options)
	if err != nil {
		return fmt.Errorf("begin database tx: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func isNilTxBeginner(pool txBeginner) bool {
	value := reflect.ValueOf(pool)
	return value.Kind() == reflect.Ptr && value.IsNil()
}

func setAllTenantGUC(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_role', 'super_admin', true)"); err != nil {
		return fmt.Errorf("set super-admin role GUC: %w", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.bypass_rls', 'true', true)"); err != nil {
		return fmt.Errorf("set RLS bypass GUC: %w", err)
	}
	return nil
}

func withTenantQueryRow(ctx context.Context, pool *pgxpool.Pool, tenantID string, fn func(tx pgx.Tx) error) error {
	return withTenantTx(ctx, pool, tenantID, fn)
}

func setLocalTenantGUC(ctx context.Context, tx pgx.Tx, tenantID string) error {
	if strings.TrimSpace(tenantID) == "" {
		return fmt.Errorf("empty tenant_id for SET LOCAL app.current_tenant")
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tenantID); err != nil {
		return fmt.Errorf("set tenant GUC: %w", err)
	}
	return nil
}
