package casdoor

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// userProvisioner 把 Casdoor 身份物化成本地 users 表行。
//
// ★ 为什么必须物化（2026-11-27 L135 轮，P0-1 断点①修复的一部分）：
//
//	auth_id_mapping.internal_id 是**独立 SERIAL**，与 users.id 是两个 ID 空间。
//	修复断点①之前，CasdoorAuthProvider.Authenticate 直接把映射 internal_id
//	当 AuthResult.UserID 下发 ⇒ 换发本地 JWT 后该 ID 会流进：
//	  · JWT claims.user_id ⇒ AdminMiddleware 的撤销探针（settings_kv 按 users.id
//	    记纪元）⇒ 撞上同号 native 用户 ⇒ **别人改密能把 casdoor 用户踢下线，
//	    casdoor 用户改密踢不掉自己的旧 token**；
//	  · 一切按 users.id 关联的管理面读路径（profile、key 归属、审计）。
//	⇒ 唯一安全形态：casdoor 登录 ⇒ 物化一行 users（随机 bcrypt 串、永不可
//	本地登录）⇒ JWT 里只写 users.id ⇒ 单一 ID 空间。
//
// ★ users_id 的关联存哪：auth_id_mapping.metadata JSONB 的 users_id 键
//
//	（966 建表时预留的扩展列，零 DDL）。物化后反查走它，不用用户名 join
//	（用户名两边可各自改名，metadata 键是物化时定死的强关联）。
//
// ★ 用户名冲突 fail-closed：users.username 有 UNIQUE 约束，casdoor 用户名
//
//	撞上存量 native 用户时**报错拒登**而不是静默改名——静默改名会让审计
//	主体与 casdoor 侧对不上，且两次登录可能物化出两行。
type userProvisioner struct {
	idMapper *IDMapper
}

func newUserProvisioner(idMapper *IDMapper) *userProvisioner {
	return &userProvisioner{idMapper: idMapper}
}

// ErrUsernameConflict 表示 casdoor 用户名与存量本地用户名冲突，拒绝物化。
var ErrUsernameConflict = errors.New("casdoor: username already exists in local users table")

// ensureLocalUser 返回该 casdoor 身份对应的本地 users.id，没有则物化一行。
//
// 流程：
//  1. MapOrCreate 落/取 auth_id_mapping 行（provider=casdoor）；
//  2. 读 metadata.users_id —— 已物化 ⇒ 直接返回（并顺带同步展示名/邮箱进
//     users 行，保持两边基本信息不漂）；
//  3. 未物化 ⇒ INSERT users（随机 bcrypt 串）+ 回写 metadata.users_id。
func (p *userProvisioner) ensureLocalUser(
	ctx context.Context,
	db DB,
	externalID, tenantID, username, displayName, email string,
) (int, error) {
	if db == nil {
		return 0, errors.New("casdoor: provisioner has no database handle")
	}
	if username == "" {
		return 0, errors.New("casdoor: username is required to provision a local user")
	}

	mappingID, err := p.idMapper.MapOrCreate(ctx, "casdoor", externalID, tenantID, username, displayName, email)
	if err != nil {
		return 0, fmt.Errorf("casdoor provision: id mapping: %w", err)
	}

	// ── 已物化？metadata.users_id 反查 ─────────────────────────────
	localID, ok, err := lookupProvisionedUserID(ctx, db, mappingID)
	if err != nil {
		return 0, fmt.Errorf("casdoor provision: lookup provisioned user: %w", err)
	}
	if ok {
		// 已物化：同步基本信息（best-effort，失败不挡登录）。
		syncProvisionedUserProfile(ctx, db, localID, displayName, email)
		return localID, nil
	}

	// ── 未物化：INSERT users + 回写 metadata.users_id ──────────────
	// 随机 32 字节 ⇒ bcrypt 后永不可被任何口令命中（成本参数与 native
	// 建号路径一致，用包内默认，避免跟 admin 侧的参数漂移）。
	provSecret, err := randomProvisionSecret()
	if err != nil {
		return 0, fmt.Errorf("casdoor provision: random secret: %w", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(provSecret), bcrypt.DefaultCost)
	if err != nil {
		return 0, fmt.Errorf("casdoor provision: bcrypt: %w", err)
	}

	var newID int
	err = db.QueryRow(ctx, `
		INSERT INTO users (tenant_id, username, password_hash, display_name, email, role, enabled, must_change_password)
		VALUES ($1, $2, $3, $4, $5, 'user', TRUE, FALSE)
		RETURNING id
	`, tenantID, username, string(hash), displayName, email).Scan(&newID)
	if err != nil {
		// 23505 = unique_violation：users.username UNIQUE 撞上存量行。
		// fail-closed：让登录失败并留下可判读的错误，而不是静默改名。
		if isUniqueViolation(err) {
			return 0, fmt.Errorf("%w: %q (tenant %q)", ErrUsernameConflict, username, tenantID)
		}
		return 0, fmt.Errorf("casdoor provision: insert user: %w", err)
	}

	// ★ 2026-11-27 12h 审计轮（W7 原子化）：INSERT users 与 metadata 回写此前
	//   非同事务——两步之间进程崩溃会留下无关联 users 行，后续登录反查 miss
	//   ⇒ 再 INSERT 撞 UNIQUE ⇒ ErrUsernameConflict 永久拒登死局（原补偿：
	//   回写失败删行，但崩溃窗口补偿不会执行）。底层连接可开事务时
	//   （*pgxpool.Pool/*pgx.Tx 都有 Begin），把回写放进同一事务原子提交；
	//   不可开事务（纯 mock 测试面 / Begin 失败）保留旧的补偿删除路径。
	if txable, ok := db.(interface {
		Begin(ctx context.Context) (pgx.Tx, error)
	}); ok {
		tx, txErr := txable.Begin(ctx)
		if txErr == nil {
			wErr := writeProvisionedUserID(ctx, tx, mappingID, newID)
			if wErr == nil {
				cErr := tx.Commit(ctx)
				if cErr == nil {
					slog.Info("casdoor: provisioned local user",
						"casdoor_external_id", externalID,
						"tenant_id", tenantID,
						"username", username,
						"users_id", newID,
						"atomic_tx", true)
					return newID, nil
				}
				// commit 失败：事务已 abort，users 行随事务回滚，直接报错重试。
				_ = tx.Rollback(ctx)
				return 0, fmt.Errorf("casdoor provision: commit link: %w", cErr)
			}
			// 回写失败：整体回滚，users 行不再半挂。
			_ = tx.Rollback(ctx)
			return 0, fmt.Errorf("casdoor provision: link mapping: %w", wErr)
		}
		// Begin 失败 → 落到下方补偿删除路径（幂等降级）。
	}

	if err := writeProvisionedUserID(ctx, db, mappingID, newID); err != nil {
		// users 行已建但关联没写上 ⇒ 下次登录会再物化一行。
		// 这里选择**回滚整体语义**：删除刚建的 users 行并报错，让本次登录失败、
		// 下次登录重走完整物化（幂等重试比留半状态好）。
		_, _ = db.Exec(ctx, `DELETE FROM users WHERE id = $1`, newID)
		return 0, fmt.Errorf("casdoor provision: link mapping: %w", err)
	}

	slog.Info("casdoor: provisioned local user",
		"casdoor_external_id", externalID,
		"tenant_id", tenantID,
		"username", username,
		"users_id", newID)
	return newID, nil
}

// lookupProvisionedUserID 读 auth_id_mapping.metadata->>'users_id'。
// 返回 ok=false 表示尚未物化（或 metadata 为空/不可解析——两者等价处理）。
// COALESCE 成空串 + 扫普通 string：NULL 与缺失都归一为 ""（对真 pgx 与
// pgxmock 的反射扫描都友好 —— **string 目标在 pgxmock 的 Scan 路径不支持）。
func lookupProvisionedUserID(ctx context.Context, db DB, mappingID int) (int, bool, error) {
	var raw string
	err := db.QueryRow(ctx, `
		SELECT COALESCE(metadata->>'users_id', '') FROM auth_id_mapping WHERE internal_id = $1
	`, mappingID).Scan(&raw)
	if err != nil {
		return 0, false, fmt.Errorf("query mapping metadata: %w", err)
	}
	if raw == "" {
		return 0, false, nil
	}
	var id int
	if _, err := fmt.Sscanf(raw, "%d", &id); err != nil {
		return 0, false, nil
	}
	if id <= 0 {
		return 0, false, nil
	}
	return id, true, nil
}

// writeProvisionedUserID 把 users.id 写进 metadata（jsonb_set，保留既有键）。
func writeProvisionedUserID(ctx context.Context, db DB, mappingID, userID int) error {
	tag, err := db.Exec(ctx, `
		UPDATE auth_id_mapping
		SET metadata = COALESCE(metadata, '{}'::jsonb) || jsonb_build_object('users_id', $2::int),
		    last_sync_at = now()
		WHERE internal_id = $1
	`, mappingID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mapping %d disappeared during provisioning", mappingID)
	}
	return nil
}

// syncProvisionedUserProfile 同步展示名/邮箱到已物化的 users 行。
// best-effort：失败只记日志（基本信息滞后一拍可接受，不该挡登录）。
func syncProvisionedUserProfile(ctx context.Context, db DB, localID int, displayName, email string) {
	_, err := db.Exec(ctx, `
		UPDATE users SET display_name = $2, email = $3, updated_at = now()
		WHERE id = $1 AND (display_name IS DISTINCT FROM $2 OR email IS DISTINCT FROM $3)
	`, localID, displayName, email)
	if err != nil {
		slog.Warn("casdoor: profile sync failed (non-fatal)", "users_id", localID, "error", err)
	}
}

// randomProvisionSecret 生成 32 字节随机串（base64，~43 字符），
// bcrypt 后作为不可登录的口令占位。
func randomProvisionSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// isUniqueViolation 判定 pg 错误码 23505。
func isUniqueViolation(err error) bool {
	var pkgErr interface{ SQLState() string }
	if errors.As(err, &pkgErr) {
		return pkgErr.SQLState() == "23505"
	}
	return false
}
