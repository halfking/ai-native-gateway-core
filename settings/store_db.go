package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// poolDB is the minimal PG surface StoreDB uses.
//
// ★ 为什么要抽成接口（2026-11-02）：租户级写路径的 42P10（生产库没有
//   (tenant_id, key) 唯一约束 ⇒ ON CONFLICT 必炸）之所以能一路漏到生产，
//   根因之一就是**这条写路径在默认测试里没有任何可执行的判据**
//   （仓内的 settings DB 测试要么是 //go:build integration 的真库腿，
//   要么根本不碰写路径）⇒ ★ 抽出接口后单测可以注入 pgxmock，
//   写路径就能被钉住（见 store_db_tenant_test.go）。
//
// ★ 三个方法与 *pgxpool.Pool / pgxmock 的签名逐字一致 ⇒ 生产路径零行为变更。
type poolDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// StoreDB persists settings to settings_kv / tenant_settings_kv.
// It is the write side of the priority chain (DB > env > default).
type StoreDB struct {
	pool poolDB
}

// NewStoreDB wires the backend to a PG connection pool.
func NewStoreDB(pool *pgxpool.Pool) *StoreDB { return &StoreDB{pool: pool} }

// newStoreDBForTest wires the backend to an arbitrary poolDB（单测注入 mock）。
func newStoreDBForTest(db poolDB) *StoreDB { return &StoreDB{pool: db} }

// Pool returns the underlying pool. Useful for tests and clean-up tasks.
// Returns nil when the store was wired to a mock (test-only).
func (s *StoreDB) Pool() *pgxpool.Pool {
	if p, ok := s.pool.(*pgxpool.Pool); ok {
		return p
	}
	return nil
}

// settingsQueryTimeout 是 StoreDB 单条查询的超时上限。Get 走的是无 ctx
// 的读路径（Global.EffectiveValue → 60s sweep 等热路径），DB 挂起时调用方
// 曾无限阻塞；5s 与仓内 DB 调用超时惯例一致（grep context.WithTimeout 的
// 众数）。
const settingsQueryTimeout = 5 * time.Second

// Get returns the DB value or (nil, nil) if no row.
func (s *StoreDB) Get(scope Scope, key string) (jsonRawMessage, error) {
	if scope == ScopeTenant {
		return nil, fmt.Errorf("use GetTenant for tenant scope")
	}
	var raw []byte
	ctx, cancel := context.WithTimeout(context.Background(), settingsQueryTimeout)
	defer cancel()
	err := s.pool.QueryRow(ctx, `
		SELECT value::text FROM settings_kv WHERE key = $1
	`, key).Scan(&raw)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return raw, nil
}

// Set writes the value, stashing the old value in prev_value for rollback.
// Returns the previous value (or nil if first write).
func (s *StoreDB) Set(scope Scope, key string, value any) (jsonRawMessage, error) {
	if scope == ScopeTenant {
		return nil, fmt.Errorf("use SetTenant for tenant scope")
	}
	if isSessionWriteKey(key) {
		return s.mutateSessionWriteSetting(key, value, false)
	}
	spec := Global.Spec(key)
	if spec == nil {
		return nil, fmt.Errorf("unknown setting: %s", key)
	}
	valueType := string(spec.Type)
	category := string(spec.Category)
	newVal, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	newValStr := string(newVal)
	var oldVal []byte
	if key == "sanitize.output_check_enabled" {
		if err := spec.Validate(value); err != nil {
			return nil, err
		}
		// Preserve the effective env/default on the first write. An atomic
		// upsert uses the real previous DB value if another gateway wins first.
		prior := "true"
		if !GetPlatformBool(key, true) {
			prior = "false"
		}
		ctx, cancel := context.WithTimeout(context.Background(), settingsQueryTimeout)
		defer cancel()
		err = s.pool.QueryRow(ctx, `
		 INSERT INTO settings_kv(key,value,value_type,scope,category,updated_at,prev_value,prev_updated_at)
		 VALUES($1,$2::jsonb,$3,'platform',$4,now(),$5::jsonb,now())
		 ON CONFLICT(key) DO UPDATE SET
		 value=EXCLUDED.value, prev_value=settings_kv.value,
		 value_type=EXCLUDED.value_type, category=EXCLUDED.category,
		 prev_updated_at=settings_kv.updated_at, updated_at=now()
		 RETURNING prev_value::text`, key, newValStr, valueType, category, prior).Scan(&oldVal)
		if err != nil {
			return nil, fmt.Errorf("set output check: %w", err)
		}
		InvalidatePlatformValue(key)
		return oldVal, nil
	}
	// Try UPDATE first; if no row matched, INSERT.
	err = s.pool.QueryRow(context.Background(), `
		UPDATE settings_kv
		   SET value = $2::jsonb,
		       value_type = $3,
		       category = $4,
		       updated_at = now(),
		       prev_value = value, prev_updated_at = updated_at
		 WHERE key = $1
		RETURNING prev_value
	`, key, newValStr, valueType, category).Scan(&oldVal)
	if err == pgx.ErrNoRows {
		// No existing row → INSERT.
		_, err = s.pool.Exec(context.Background(), `
			INSERT INTO settings_kv (key, value, value_type, scope, category, updated_at)
			VALUES ($1, $2::jsonb, $3, 'platform', $4, now())
		`, key, newValStr, valueType, category)
		if err != nil {
			return nil, fmt.Errorf("insert: %w", err)
		}
		InvalidatePlatformValue(key)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	InvalidatePlatformValue(key)
	return oldVal, nil
}

// GetTenant is the per-tenant variant.
func (s *StoreDB) GetTenant(tenantID, key string) (jsonRawMessage, error) {
	var raw []byte
	err := s.pool.QueryRow(context.Background(), `
		SELECT value::text FROM tenant_settings_kv
		 WHERE tenant_id = $1 AND key = $2
	`, tenantID, key).Scan(&raw)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return raw, nil
}

// SetTenant writes a tenant-scoped value.
//
// ★★ 2026-11-02（chat tab 空态根修链的一环）：本函数原先用
//
//	INSERT ... ON CONFLICT (tenant_id, key) DO UPDATE
//
//	⇒ ★ 它要求 tenant_settings_kv 上**存在** (tenant_id, key) 唯一约束；
//	  而【生产库（211 / 252 共享库）与全新建库基线 sql/schema/01-schema.sql
//	  都没有这条约束】（迁移 022 声明了 PRIMARY KEY，但基线是从缺约束的库
//	  dump 出来的，dump 又把缺口固化回基线）⇒ ⇒ 每次租户级设置写入都
//	  **42P10**（there is no unique or exclusion constraint matching the
//	  ON CONFLICT specification）⇒ ⇒ **所有租户级设置写入全废**，
//	  其中就包括「会话正文录制同意」开关 ⇒ ⇒ 录制同意永远关着 ⇒
//	  ⇒ 会话正文永不落库 ⇒ ⇒ 前端「对话」页四个角色页签全空。
//	  ★ 实测证据（211 生产，2026-11-02）：PUT /api/admin/tenants/default/
//	    recording-consent {"consent":true} → HTTP 500
//	    err="upsert tenant setting: ERROR: ... (SQLSTATE 42P10)"；
//	    且 tenant_settings_kv 在 211 上**一行都没有**（从没成功写进过一行）。
//
// ★ 修法：**与平台级 Set 完全同款**的「先 UPDATE，未命中再 INSERT」形态
//   （同文件上方的 Set 已经是这个写法，注释原文「Try UPDATE first; if no
//   row matched, INSERT」）⇒ 不再依赖那条并不存在的唯一约束 ⇒
//   ★ 与平台级写法同源 ⇒ 一把尺一份实现。
//   ★ 不改 schema：补约束要走新迁移 + 基线 + 对象台账刷新（体量大、
//     跨环境），而写路径本就不需要它 ⇒ 先让写路径活起来。
//   ★ 语义保持：UPDATE 分支仍然回填 prev_value / prev_updated_at（回滚按钮
//     与 prev_value 审计依赖它），INSERT 分支返回 nil（无旧值）。
//
// ★★ 2026-11-02 加固：并发写同一 (tenant_id, key) ⇒ **重复行**。
//   ★ 上面的「先 UPDATE 再 INSERT」只在**单写者**前提下等价于 ON CONFLICT。
//     而 tenant_settings_kv 上**没有**唯一约束可依赖 ⇒ 两个并发写者可以
//     各自 UPDATE 未命中、然后各自 INSERT ⇒ 落两行同 key 的行。
//   ★ 后果是**静默**的：读侧 GetTenant 用 QueryRow 取首行、ListTenant 用
//     map 覆盖（后者取到后写的）⇒ 哪一行胜出取决于物理顺序 ⇒ 可能读到
//     旧值。而本键控制的正是「会话正文录制同意」⇒ 一旦读到旧值 ⇒
//     正文又停止落库 ⇒ **本次修复的孪生缺陷**（且更难诊断）。
//   ★ 修法：**事务 + 事务级咨询锁**（pg_advisory_xact_lock），按
//     (tenant_id, key) 串行化 ⇒ 并发写变成排队写 ⇒ 恒等「先 UPDATE 再
//     INSERT」的语义。**不改 schema**（补唯一约束要走新迁移 + 基线 +
//     对象台账刷新，体量大且跨环境）；咨询锁是纯运行时手段。
func (s *StoreDB) SetTenant(tenantID, key string, value any) (jsonRawMessage, error) {
	spec := Global.Spec(key)
	if spec == nil {
		return nil, fmt.Errorf("unknown setting: %s", key)
	}
	valueType := string(spec.Type)
	category := string(spec.Category)
	newVal, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	newValStr := string(newVal)
	ctx := context.Background()

	// ★ 事务 + 事务级咨询锁：把同一 (tenant_id, key) 的并发写串行化。
	//   （无唯一约束 ⇒ 必须自己保证「同一 key 至多一行」的语义）
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`,
		"tenant_settings_kv:"+tenantID+":"+key); err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}

	var oldVal []byte
	err = tx.QueryRow(ctx, `
		UPDATE tenant_settings_kv
		   SET value = $3::jsonb,
		       value_type = $4,
		       category = $5,
		       updated_at = now(),
		       prev_value = value, prev_updated_at = updated_at
		 WHERE tenant_id = $1 AND key = $2
		RETURNING prev_value
	`, tenantID, key, newValStr, valueType, category).Scan(&oldVal)
	if err == pgx.ErrNoRows {
		// No existing row → INSERT（同平台级 Set 的形态）。
		if _, err := tx.Exec(ctx, `
			INSERT INTO tenant_settings_kv (tenant_id, key, value, value_type, category, updated_at)
			VALUES ($1, $2, $3::jsonb, $4, $5, now())
		`, tenantID, key, newValStr, valueType, category); err != nil {
			return nil, fmt.Errorf("insert: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return oldVal, nil
}

// List returns all platform-scoped keys and their values.
//
// R66（category 5）：settings_kv 是配置优先链的最上游（DB > env > default）。
// 迭代被静默截断 = 若干配置项凭空消失并**静默回落到 default**，无任何
// 报错——这正是「改了配置不生效」最难查的一类。必须上抛。
func (s *StoreDB) List(scope Scope) (map[string]jsonRawMessage, error) {
	if scope == ScopeTenant {
		return nil, fmt.Errorf("use ListTenant for tenant scope")
	}
	rows, err := s.pool.Query(context.Background(), `
		SELECT key, value::text FROM settings_kv ORDER BY key
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]jsonRawMessage)
	for rows.Next() {
		var k string
		var v []byte
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("settings.StoreDB.List: iterate rows: %w", err)
	}
	return out, nil
}

// ListTenant returns all settings for a tenant.
//
// R66（category 5）：同 List——租户配置被静默截断会整体回落 default。
func (s *StoreDB) ListTenant(tenantID string) (map[string]jsonRawMessage, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT key, value::text FROM tenant_settings_kv
		 WHERE tenant_id = $1 ORDER BY key
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]jsonRawMessage)
	for rows.Next() {
		var k string
		var v []byte
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("settings.StoreDB.ListTenant: iterate rows: %w", err)
	}
	return out, nil
}

// Rollback reverts key to its prev_value. Returns the value that was
// written as the new "current" value.
func (s *StoreDB) Rollback(scope Scope, key, tenantID string) (jsonRawMessage, error) {
	if scope != ScopeTenant && isSessionWriteKey(key) {
		return s.mutateSessionWriteSetting(key, nil, true)
	}
	var newVal []byte
	var err error
	if scope == ScopeTenant {
		err = s.pool.QueryRow(context.Background(), `
			UPDATE tenant_settings_kv
			   SET value = prev_value, prev_value = value,
			       updated_at = now(), prev_updated_at = updated_at
			 WHERE tenant_id = $1 AND key = $2 AND prev_value IS NOT NULL
			RETURNING value::text
		`, tenantID, key).Scan(&newVal)
	} else {
		err = s.pool.QueryRow(context.Background(), `
			UPDATE settings_kv
			   SET value = prev_value, prev_value = value,
			       updated_at = now(), prev_updated_at = updated_at
			 WHERE key = $1 AND prev_value IS NOT NULL
			RETURNING value::text
		`, key).Scan(&newVal)
	}
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("no previous value to rollback to")
		}
		return nil, err
	}
	if scope != ScopeTenant {
		InvalidatePlatformValue(key)
	}
	return newVal, nil
}

// Delete removes a setting (platform or tenant).
func (s *StoreDB) Delete(scope Scope, key, tenantID string) error {
	var err error
	if scope == ScopeTenant {
		_, err = s.pool.Exec(context.Background(),
			`DELETE FROM tenant_settings_kv WHERE tenant_id = $1 AND key = $2`,
			tenantID, key)
	} else {
		_, err = s.pool.Exec(context.Background(),
			`DELETE FROM settings_kv WHERE key = $1`, key)
		if err == nil {
			InvalidatePlatformValue(key)
		}
	}
	return err
}

// ensure PoolExec is satisfied for audit.go to compile.
var _ = time.Now
var _ = slog.Default
