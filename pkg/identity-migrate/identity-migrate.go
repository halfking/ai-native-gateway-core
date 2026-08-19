// identity-migrate: 创建/管理 identity_shadow 数据库。
//
// 用法：
//
//	identity-migrate --dsn "$DSN" ensure          # 创建库 + 应用 migrations
//	identity-migrate --dsn "$DSN" up              # 应用新 migrations
//	identity-migrate --dsn "$DSN" down            # 回滚 1 个 migration
//	identity-migrate --dsn "$DSN" version         # 打印当前 version
//	identity-migrate --admin-dsn "$ADMIN_DSN" --db identity_shadow ensure-from-postgres  # 从 llm-gateway-pg 创建 db
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
)

//go:embed all:migrations/*.sql
var migrationsFS embed.FS

func main() {
	var (
		dsn      = flag.String("dsn", os.Getenv("IDENTITY_SHADOW_DSN"), "目标 PG DSN (postgres://...)")
		adminDSN = flag.String("admin-dsn", os.Getenv("IDENTITY_ADMIN_DSN"), "管理员 DSN (用于创建 db)")
		dbName   = flag.String("db", "identity_shadow", "待创建的数据库名")
		cmd      = flag.String("cmd", "ensure", "ensure / up / down / version / ensure-from-postgres")
	)
	flag.Parse()

	switch *cmd {
	case "ensure":
		mustHaveDSN(*dsn)
		if err := ensureSchema(*dsn); err != nil {
			log.Fatalf("ensure failed: %v", err)
		}
		fmt.Println("✅ schema ensured")
	case "ensure-from-postgres":
		mustHaveDSN(*adminDSN)
		if err := ensureDBAndSchema(*adminDSN, *dbName, *dsn); err != nil {
			log.Fatalf("ensure-from-postgres failed: %v", err)
		}
		fmt.Printf("✅ db %q created + schema ensured\n", *dbName)
	case "up":
		mustHaveDSN(*dsn)
		v, err := applyUp(*dsn)
		if err != nil {
			log.Fatalf("up failed: %v", err)
		}
		fmt.Printf("✅ applied; current version=%d\n", v)
	case "down":
		mustHaveDSN(*dsn)
		v, err := applyDown(*dsn)
		if err != nil {
			log.Fatalf("down failed: %v", err)
		}
		fmt.Printf("✅ rolled back; current version=%d\n", v)
	case "version":
		mustHaveDSN(*dsn)
		v, err := currentVersion(*dsn)
		if err != nil {
			log.Fatalf("version failed: %v", err)
		}
		fmt.Printf("version=%d\n", v)
	default:
		log.Fatalf("unknown cmd %q", *cmd)
	}
}

func mustHaveDSN(dsn string) {
	if strings.TrimSpace(dsn) == "" {
		log.Fatal("--dsn (or IDENTITY_SHADOW_DSN) required")
	}
}

// ensureSchema 应用所有未应用的 up migrations。
func ensureSchema(dsn string) error {
	applied, err := loadApplied(dsn)
	if err != nil {
		return err
	}
	files, err := loadMigrationFiles()
	if err != nil {
		return err
	}
	for _, f := range files {
		if _, ok := applied[f.version]; ok {
			continue
		}
		if err := applyMigrationFile(dsn, f, "up"); err != nil {
			return fmt.Errorf("apply %s: %w", f.name, err)
		}
		fmt.Printf("  applied %04d_%s.up.sql\n", f.version, f.name)
	}
	return nil
}

// ensureDBAndSchema 先 CREATE DATABASE（如果不存在），再应用 migrations。
func ensureDBAndSchema(adminDSN, dbName, targetDSN string) error {
	admin, err := sql.Open("postgres", adminDSN)
	if err != nil {
		return fmt.Errorf("open admin: %w", err)
	}
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var exists bool
	if err := admin.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, dbName).Scan(&exists); err != nil {
		return fmt.Errorf("check db: %w", err)
	}
	if !exists {
		// 切到 postgres 库执行 CREATE DATABASE
		dsnNoDB := stripDB(adminDSN)
		conn, err := sql.Open("postgres", dsnNoDB)
		if err != nil {
			return fmt.Errorf("open no-db: %w", err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE %s OWNER kxuser", pq.QuoteIdentifier(dbName))); err != nil {
			return fmt.Errorf("create db: %w", err)
		}
		fmt.Printf("  created db %s (owner=kxuser)\n", dbName)
	}
	if err := ensureSchema(targetDSN); err != nil {
		return err
	}
	return nil
}

// stripDB 把 DSN 中的 /dbname 段去掉，替换为 /postgres。
func stripDB(dsn string) string {
	// postgres://user:pass@host:port/dbname?...
	i := strings.Index(dsn, "://")
	if i < 0 {
		return dsn
	}
	rest := dsn[i+3:]
	j := strings.Index(rest, "/")
	if j < 0 {
		return dsn
	}
	tail := rest[j+1:]
	k := strings.IndexAny(tail, "?")
	prefix := dsn[:i+3] + rest[:j+1]
	if k < 0 {
		return prefix + "postgres"
	}
	return prefix + "postgres" + tail[k:]
}

// applyUp 应用所有未应用的 migrations。
func applyUp(dsn string) (int, error) {
	applied, err := loadApplied(dsn)
	if err != nil {
		return 0, err
	}
	files, err := loadMigrationFiles()
	if err != nil {
		return 0, err
	}
	var maxVer int
	for _, f := range files {
		if _, ok := applied[f.version]; ok {
			continue
		}
		if err := applyMigrationFile(dsn, f, "up"); err != nil {
			return maxVer, err
		}
		fmt.Printf("  applied %04d_%s.up.sql\n", f.version, f.name)
		if f.version > maxVer {
			maxVer = f.version
		}
	}
	if maxVer == 0 {
		maxVer, _ = currentVersion(dsn)
	}
	return maxVer, nil
}

// applyDown 回滚最后一个 migration。
func applyDown(dsn string) (int, error) {
	v, err := currentVersion(dsn)
	if err != nil {
		return 0, err
	}
	if v == 0 {
		return 0, nil
	}
	files, err := loadMigrationFiles()
	if err != nil {
		return 0, err
	}
	var target *migrationFile
	for i := range files {
		if files[i].version == v {
			target = &files[i]
			break
		}
	}
	if target == nil {
		return 0, fmt.Errorf("down migration for version %d not found", v)
	}
	if err := applyMigrationFile(dsn, *target, "down"); err != nil {
		return v, err
	}
	fmt.Printf("  rolled back %04d_%s.down.sql\n", target.version, target.name)
	return v - 1, nil
}

// applyMigrationFile 在事务内执行 migration body，并更新 ownership。
func applyMigrationFile(dsn string, mf migrationFile, direction string) error {
	bodyKey := fmt.Sprintf("migrations/%04d_%s.%s.sql", mf.version, mf.name, direction)
	data, err := migrationsFS.ReadFile(bodyKey)
	if err != nil {
		return fmt.Errorf("read embed: %w", err)
	}
	body := strings.TrimSpace(string(data))
	if body == "" {
		return fmt.Errorf("empty migration body for %s", bodyKey)
	}

	db := openDB(dsn)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(821733401)`); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("exec body: %w", err)
	}
	if direction == "up" {
		checksum := checksumSQL(body)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO identity_migration_ownership (version, name, checksum, applied_by)
VALUES ($1, $2, $3, $4)
ON CONFLICT (version) DO UPDATE SET applied_at = NOW(), checksum = EXCLUDED.checksum, applied_by = EXCLUDED.applied_by`,
			mf.version, fmt.Sprintf("%04d_%s.up.sql", mf.version, mf.name), checksum, currentUser()); err != nil {
			return fmt.Errorf("record ownership: %w", err)
		}
	}
	return tx.Commit()
}

func checksumSQL(s string) string {
	digest := sha256.Sum256([]byte(s))
	return fmt.Sprintf("sha256:%x", digest[:])
}

func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "identity-migrate"
}

func openDB(dsn string) *sql.DB {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	return db
}

// loadApplied 返回已应用的 version 集合。
func loadApplied(dsn string) (map[int]struct{}, error) {
	db := openDB(dsn)
	defer db.Close()
	out := make(map[int]struct{})
	rows, err := db.Query(`SELECT version FROM identity_migration_ownership`)
	if err != nil {
		// ownership 表尚未创建 → 当成 empty
		if strings.Contains(err.Error(), "does not exist") {
			return out, nil
		}
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = struct{}{}
	}
	return out, rows.Err()
}

func currentVersion(dsn string) (int, error) {
	db := openDB(dsn)
	defer db.Close()
	var v sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(version) FROM identity_migration_ownership`).Scan(&v); err != nil {
		return 0, err
	}
	if !v.Valid {
		return 0, nil
	}
	return int(v.Int64), nil
}

type migrationFile struct {
	version int
	name    string
}

// loadMigrationFiles 从 embed 列出所有 0001_*.up.sql / 0001_*.down.sql。
func loadMigrationFiles() ([]migrationFile, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	var out []migrationFile
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		// 形如 0001_shadow_users.up.sql
		base := strings.TrimSuffix(name, ".up.sql")
		parts := strings.SplitN(base, "_", 2)
		if len(parts) < 2 {
			continue
		}
		v, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, fmt.Errorf("bad migration name %q: %w", name, err)
		}
		out = append(out, migrationFile{version: v, name: parts[1]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	if len(out) == 0 {
		return nil, fmt.Errorf("no migrations found")
	}
	// 验证 down 文件齐全
	for _, mf := range out {
		downKey := filepath.ToSlash(fmt.Sprintf("migrations/%04d_%s.down.sql", mf.version, mf.name))
		if _, err := migrationsFS.ReadFile(downKey); err != nil {
			return nil, fmt.Errorf("missing down migration for version %d: %w", mf.version, err)
		}
	}
	return out, nil
}
