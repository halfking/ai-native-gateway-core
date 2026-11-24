package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage:")
		fmt.Println("  go run main.go list")
		fmt.Println("  go run main.go change-password <username> <new-password>")
		fmt.Println("  go run main.go create <username> <password> <role> <display_name>")
		os.Exit(1)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL environment variable not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("Unable to connect to database: %v\n", err)
	}
	defer pool.Close()

	command := os.Args[1]

	switch command {
	case "list":
		listUsers(ctx, pool)
	case "change-password":
		if len(os.Args) < 4 {
			log.Fatal("Usage: go run main.go change-password <username> <new-password>")
		}
		changePassword(ctx, pool, os.Args[2], os.Args[3])
	case "create":
		if len(os.Args) < 6 {
			log.Fatal("Usage: go run main.go create <username> <password> <role> <display_name>")
		}
		createUser(ctx, pool, os.Args[2], os.Args[3], os.Args[4], os.Args[5])
	default:
		log.Fatalf("Unknown command: %s", command)
	}
}

func listUsers(ctx context.Context, pool *pgxpool.Pool) {
	rows, err := pool.Query(ctx, `
		SELECT id, tenant_id, username, display_name, email, role, enabled, created_at
		FROM users
		ORDER BY id
	`)
	if err != nil {
		log.Fatalf("Query failed: %v", err)
	}
	defer rows.Close()

	fmt.Println("\n=== Users in Database ===")
	fmt.Printf("%-5s %-15s %-20s %-20s %-30s %-15s %-8s\n",
		"ID", "Tenant", "Username", "Display Name", "Email", "Role", "Enabled")
	fmt.Println("--------------------------------------------------------------------------------")

	for rows.Next() {
		var id int
		var tenantID, username, displayName, email, role string
		var enabled bool
		var createdAt interface{}

		err := rows.Scan(&id, &tenantID, &username, &displayName, &email, &role, &enabled, &createdAt)
		if err != nil {
			log.Fatalf("Scan failed: %v", err)
		}

		fmt.Printf("%-5d %-15s %-20s %-20s %-30s %-15s %-8t\n",
			id, tenantID, username, displayName, email, role, enabled)
	}
	// ★ rows 终检：迭代中断（连接断开 / 服务端错误）时 rows.Next() 会提前返回
	//   false ⇒ 不看 rows.Err() 就会**静默截断**这张用户表（打印半截、退出码 0）
	//   ⇒ ⇒ 工具的使用者会以为那就是全部用户。
	//   ★ 本工具是**一次性运维 CLI**：扫描失败已用 log.Fatalf 处理 ⇒ 这里同口径
	//     直接退出，不把半截表格当成完整结果交出去。
	if err := rows.Err(); err != nil {
		log.Fatalf("Rows iteration failed: %v", err)
	}
	fmt.Println()
}

func changePassword(ctx context.Context, pool *pgxpool.Pool, username, newPassword string) {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		log.Fatalf("Failed to hash password: %v", err)
	}

	result, err := pool.Exec(ctx, `
		UPDATE users 
		SET password_hash = $1, updated_at = NOW(), must_change_password = FALSE
		WHERE username = $2
	`, string(hash), username)
	if err != nil {
		log.Fatalf("Failed to update password: %v", err)
	}

	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		log.Fatalf("User '%s' not found", username)
	}

	fmt.Printf("✓ Password changed successfully for user '%s'\n", username)
}

func createUser(ctx context.Context, pool *pgxpool.Pool, username, password, role, displayName string) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		log.Fatalf("Failed to hash password: %v", err)
	}

	var userID int
	err = pool.QueryRow(ctx, `
		INSERT INTO users (tenant_id, username, password_hash, display_name, role, enabled, must_change_password)
		VALUES ('default', $1, $2, $3, $4, TRUE, FALSE)
		RETURNING id
	`, username, string(hash), displayName, role).Scan(&userID)

	if err != nil {
		log.Fatalf("Failed to create user: %v", err)
	}

	fmt.Printf("✓ User '%s' created successfully (ID: %d, Role: %s)\n", username, userID, role)
}
