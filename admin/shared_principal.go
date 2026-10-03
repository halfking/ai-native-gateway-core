package admin

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/pkg/identity"
	"github.com/kaixuan/llm-gateway-go/pkg/identity/shadow"
)

// resolveSharedPrincipal checks the canonical link and the Gateway account
// afresh for each request. Foreign IDs, names and roles never select an account
// or grant permissions beyond that account's current local role.
func resolveSharedPrincipal(ctx context.Context, pool *pgxpool.Pool, verified *identity.Principal) (*identity.Principal, error) {
	if pool == nil || verified == nil || verified.Source != "multi_issuer" {
		return nil, identity.ErrInvalidToken
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	binding, err := shadow.NewDAO(identity.ShadowDatabase()).ResolveProvider(ctx, verified.Issuer, verified.Subject, verified.TenantID, "llm-gateway")
	if err != nil {
		return nil, identity.ErrInvalidToken
	}
	id, err := strconv.Atoi(binding.Subject)
	if err != nil || id <= 0 || strconv.Itoa(id) != binding.Subject {
		return nil, identity.ErrInvalidToken
	}
	resolved := *verified
	var localRole string
	err = withTenantTx(ctx, pool, verified.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT username, role, must_change_password
FROM users WHERE id = $1 AND tenant_id = $2 AND enabled = true`, id, verified.TenantID).
			Scan(&resolved.Username, &localRole, &resolved.MustChangePassword)
	})
	if err != nil || strings.TrimSpace(resolved.Username) == "" {
		return nil, identity.ErrInvalidToken
	}
	roleRank := map[string]int{"user": 1, "tenant_admin": 2, "super_admin": 3}
	if roleRank[localRole] == 0 || roleRank[verified.Role] == 0 {
		return nil, identity.ErrInvalidToken
	}
	if roleRank[localRole] < roleRank[verified.Role] {
		resolved.Role = localRole
	}
	resolved.UserID = id
	resolved.CanonicalUserID = binding.CanonicalUserID
	return &resolved, nil
}
