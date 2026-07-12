package tenantops

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
)

// Handler exposes read-only tenant operations. It deliberately does not share
// the platform CRUD handlers, so tenant scope cannot be widened by a query.
type Handler struct{ db *pgxpool.Pool }

func NewHandler(db *pgxpool.Pool) *Handler { return &Handler{db: db} }

func (h *Handler) RegisterRoutes(g *echo.Group) {
	g.GET("/license/status", h.licenseStatus)
	g.GET("/autoupdate/check", h.updateCheck)
}

func tenantID(c echo.Context) (string, bool) {
	role, _ := c.Get("role").(string)
	id, _ := c.Get("tenant_id").(string)
	if role == "tenant_admin" && id != "" {
		return id, true
	}
	if role == "super_admin" {
		id = c.QueryParam("tenant")
		return id, id != ""
	}
	return "", false
}

func (h *Handler) licenseStatus(c echo.Context) error {
	id, ok := tenantID(c)
	if !ok {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "tenant scope required"})
	}
	rows, err := h.db.Query(c.Request().Context(), `
		SELECT id, license_key, customer_name, customer_email, max_devices,
		       subscription_tier, features, expires_at, created_at, revoked_at
		FROM licenses WHERE tenant_id = $1 ORDER BY created_at DESC`, id)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "license lookup failed"})
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var item map[string]any
		var id int64
		var key, name, email, tier *string
		var max int
		var features []byte
		var expires, created, revoked *any
		if err := rows.Scan(&id, &key, &name, &email, &max, &tier, &features, &expires, &created, &revoked); err != nil {
			return err
		}
		item = map[string]any{"id": id, "license_key": maskLicenseKey(value(key)), "customer_name": value(name), "customer_email": value(email), "max_devices": max, "subscription_tier": value(tier), "features": features, "expires_at": pointerValue(expires), "created_at": pointerValue(created), "revoked_at": pointerValue(revoked)}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "license lookup failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{"tenant_id": id, "licenses": items})
}

func (h *Handler) updateCheck(c echo.Context) error {
	id, ok := tenantID(c)
	if !ok {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "tenant scope required"})
	}
	rows, err := h.db.Query(c.Request().Context(), `
		SELECT id, version, build_seq, channel, title, description, changelog,
		       image_tag, image_digest, min_version, mandatory, created_by, created_at, published_at
		FROM releases WHERE published_at IS NOT NULL AND (tenant_id = $1 OR tenant_id = 'default')
		ORDER BY build_seq DESC, created_at DESC LIMIT 50`, id)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "update lookup failed"})
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var rid int64
		var version, channel, title, description, changelog, image, digest, min, createdBy *string
		var seq int
		var mandatory bool
		var created, published *any
		if err := rows.Scan(&rid, &version, &seq, &channel, &title, &description, &changelog, &image, &digest, &min, &mandatory, &createdBy, &created, &published); err != nil {
			return err
		}
		items = append(items, map[string]any{"id": rid, "version": value(version), "build_seq": seq, "channel": value(channel), "title": value(title), "description": value(description), "changelog": value(changelog), "image_tag": value(image), "image_digest": value(digest), "min_version": value(min), "mandatory": mandatory, "created_by": value(createdBy), "created_at": pointerValue(created), "published_at": pointerValue(published)})
	}
	if err := rows.Err(); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "update lookup failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{"tenant_id": id, "current_version": "", "items": items})
}

func value(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func pointerValue(v *any) any {
	if v == nil {
		return nil
	}
	return *v
}

func maskLicenseKey(key string) string {
	if len(key) <= 8 {
		return "********"
	}
	return key[:4] + "..." + key[len(key)-4:]
}
