package pluginruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const entitlementRequestTimeout = 3 * time.Second
const entitlementTokenTTL = 30 * time.Second

// EntitlementClient queries maintain for the caller tenant's effective module access.
type EntitlementClient struct {
	baseURL string
	secret  []byte
	issuer  string
	client  *http.Client
}

// NewEntitlementClient returns a maintain entitlement client using the shared HS256 secret.
func NewEntitlementClient(baseURL, secret, issuer string) *EntitlementClient {
	return &EntitlementClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		secret:  []byte(secret),
		issuer:  issuer,
		client:  &http.Client{Timeout: entitlementRequestTimeout},
	}
}

// Allowed returns false without an error only when maintain reports a valid inactive entitlement.
// All transport, HTTP status, and response validation failures return an error so callers fail closed.
func (c *EntitlementClient) Allowed(ctx context.Context, tenantID, moduleID string) (bool, error) {
	claims := jwt.MapClaims{
		"tenant_id": tenantID,
		"username":  "service:gateway",
		"role":      "service",
		"scopes":    []string{"module:entitlement:read"},
		"iss":       c.issuer,
		"exp":       time.Now().Add(entitlementTokenTTL).Unix(),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(c.secret)
	if err != nil {
		return false, fmt.Errorf("sign entitlement token: %w", err)
	}

	requestURL := c.baseURL + "/maintain-api/internal/modules/" + url.PathEscape(moduleID) + "/entitlement"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return false, fmt.Errorf("create entitlement request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)

	response, err := c.client.Do(request)
	if err != nil {
		return false, fmt.Errorf("request entitlement: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("entitlement response status %d", response.StatusCode)
	}

	var body struct {
		ModuleID        string `json:"module_id"`
		Active          *bool  `json:"active"`
		EffectiveStatus string `json:"effective_status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return false, fmt.Errorf("decode entitlement response: %w", err)
	}
	if body.ModuleID != moduleID || body.Active == nil || body.EffectiveStatus == "" {
		return false, fmt.Errorf("decode entitlement response: invalid payload")
	}
	return *body.Active, nil
}
