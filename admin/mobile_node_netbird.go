package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const mobilePolicyMarker = "managed-by=llmgw-mobile-node"

// NetBirdGatewayAccess keeps the mobile administrator group connected to one
// explicitly configured gateway group with a single TCP/443 policy.
type NetBirdGatewayAccess struct {
	apiURL, token, gatewayGroupID, mobileAdminGroupID string
	policyName                                        string
	client                                            *http.Client
	lock                                              func(context.Context, string) (func(), error)
}

type netBirdGroup struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Peers []struct {
		ID string `json:"id"`
	} `json:"peers"`
	Resources []json.RawMessage `json:"resources"`
}

type netBirdRule struct {
	Name                string              `json:"name"`
	Description         string              `json:"description"`
	Enabled             bool                `json:"enabled"`
	Action              string              `json:"action"`
	Bidirectional       bool                `json:"bidirectional"`
	Protocol            string              `json:"protocol"`
	Ports               []string            `json:"ports"`
	Sources             netBirdGroupIDs     `json:"sources"`
	Destinations        netBirdGroupIDs     `json:"destinations"`
	PortRanges          []json.RawMessage   `json:"port_ranges,omitempty"`
	AuthorizedGroups    map[string][]string `json:"authorized_groups,omitempty"`
	SourceResource      *json.RawMessage    `json:"sourceResource,omitempty"`
	DestinationResource *json.RawMessage    `json:"destinationResource,omitempty"`
}

// NetBird GET returns group objects; POST accepts group IDs.
type netBirdGroupIDs []string

func (ids *netBirdGroupIDs) UnmarshalJSON(data []byte) error {
	var entries []json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		return err
	}
	for _, entry := range entries {
		var id string
		if err := json.Unmarshal(entry, &id); err != nil {
			var group struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(entry, &group); err != nil {
				return err
			}
			id = group.ID
		}
		if id == "" {
			return errors.New("NetBird group reference has no ID")
		}
		*ids = append(*ids, id)
	}
	return nil
}

// Keep the session lock across the complete read/validate/write sequence.
// All replicas managing a node must share the gateway database.
func netBirdDatabaseLock(pool *pgxpool.Pool) func(context.Context, string) (func(), error) {
	return func(ctx context.Context, key string) (func(), error) {
		if pool == nil {
			return nil, errors.New("NetBird reconciliation requires a database lock")
		}
		conn, err := pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(hashtextextended($1, 0))", key); err != nil {
			// Acquisition can succeed just before cancellation; never recycle this session.
			closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = conn.Hijack().Close(closeCtx)
			return nil, err
		}
		return func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var unlocked bool
			err := conn.QueryRow(closeCtx, "SELECT pg_advisory_unlock(hashtextextended($1, 0))", key).Scan(&unlocked)
			if err != nil || !unlocked {
				_ = conn.Hijack().Close(closeCtx)
				return
			}
			conn.Release()
		}, nil
	}
}

type netBirdPolicy struct {
	ID                  string        `json:"id"`
	Name                string        `json:"name"`
	Description         string        `json:"description"`
	Enabled             bool          `json:"enabled"`
	SourcePostureChecks []string      `json:"source_posture_checks,omitempty"`
	Rules               []netBirdRule `json:"rules"`
}

func NewNetBirdGatewayAccessFromEnv() (*NetBirdGatewayAccess, error) {
	token := strings.TrimSpace(os.Getenv("NETBIRD_API_TOKEN"))
	gatewayGroup := strings.TrimSpace(os.Getenv("NETBIRD_GATEWAY_GROUP_ID"))
	adminGroup := strings.TrimSpace(os.Getenv("NETBIRD_MOBILE_ADMIN_GROUP_ID"))
	if token == "" || gatewayGroup == "" || adminGroup == "" {
		return nil, errors.New("NetBird origin requires NETBIRD_API_TOKEN, NETBIRD_GATEWAY_GROUP_ID, and NETBIRD_MOBILE_ADMIN_GROUP_ID")
	}
	if gatewayGroup == adminGroup {
		return nil, errors.New("NetBird gateway and mobile-admin groups must be different")
	}
	apiURL := strings.TrimRight(strings.TrimSpace(os.Getenv("NETBIRD_API_URL")), "/")
	if apiURL == "" {
		apiURL = "https://api.netbird.io"
	}
	u, err := url.Parse(apiURL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) {
		return nil, errors.New("NETBIRD_API_URL must be HTTPS (HTTP is allowed only on localhost)")
	}
	policyName := strings.TrimSpace(os.Getenv("NETBIRD_MOBILE_POLICY_NAME"))
	if policyName == "" {
		policyName = "llmgw-mobile-access-" + safeNodeID(os.Getenv("LLMGW_NODE_ID"))
		if strings.TrimSuffix(policyName, "-") == "llmgw-mobile-access" {
			return nil, errors.New("LLMGW_NODE_ID is required to name the managed NetBird policy")
		}
	}
	return &NetBirdGatewayAccess{
		apiURL: apiURL, token: token, gatewayGroupID: gatewayGroup,
		mobileAdminGroupID: adminGroup, policyName: policyName,
		client: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func (n *NetBirdGatewayAccess) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return errors.New("encode NetBird request")
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, n.apiURL+"/api"+path, body)
	if err != nil {
		return errors.New("create NetBird request")
	}
	req.Header.Set("Authorization", "Token "+n.token)
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("NetBird API request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("NetBird API returned HTTP %d", resp.StatusCode)
	}
	if output == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 || json.Unmarshal(data, output) != nil {
		return errors.New("NetBird API returned an invalid response")
	}

	return nil
}

func (n *NetBirdGatewayAccess) EnsureGatewayAccess(ctx context.Context, nodeID, peerID string) error {
	if peerID == "" {
		return errors.New("gateway NetBird peer ID is missing")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if n.lock == nil {
		return errors.New("NetBird reconciliation lock is not configured")
	}
	unlock, err := n.lock(ctx, "llmgw-mobile-netbird:"+n.apiURL+":"+n.gatewayGroupID)
	if err != nil {
		return fmt.Errorf("lock NetBird reconciliation: %w", err)
	}
	defer unlock()
	var group, mobileGroup netBirdGroup
	if err := n.request(ctx, http.MethodGet, "/groups/"+url.PathEscape(n.gatewayGroupID), nil, &group); err != nil {
		return fmt.Errorf("load gateway group: %w", err)
	}
	if group.ID != n.gatewayGroupID || group.Name == "" {
		return errors.New("invalid configured gateway group")
	}
	if err := n.request(ctx, http.MethodGet, "/groups/"+url.PathEscape(n.mobileAdminGroupID), nil, &mobileGroup); err != nil {
		return fmt.Errorf("load mobile admin group: %w", err)
	}
	if mobileGroup.ID != n.mobileAdminGroupID {
		return errors.New("invalid configured mobile admin group")
	}
	var peer struct {
		ID string `json:"id"`
	}
	if err := n.request(ctx, http.MethodGet, "/peers/"+url.PathEscape(peerID), nil, &peer); err != nil {
		return fmt.Errorf("load gateway peer: %w", err)
	}
	if peer.ID != peerID {
		return errors.New("invalid configured gateway peer")
	}
	// A node owns an exclusive destination group, avoiding cross-node grants.
	peerPresent := false
	for _, member := range group.Peers {
		if member.ID != peerID {
			return errors.New("gateway group contains a different peer; use a dedicated group per node")
		}
		peerPresent = true
	}
	if len(group.Resources) > 0 {
		return errors.New("gateway group contains network resources; use a dedicated peer group")
	}
	var policies []netBirdPolicy
	if err := n.request(ctx, http.MethodGet, "/policies", nil, &policies); err != nil {
		return fmt.Errorf("load NetBird policies: %w", err)
	}
	managedCount := 0
	for _, existing := range policies {
		if existing.Name != n.policyName {
			continue
		}
		managedCount++
		if existing.ID == "" || existing.Description != mobilePolicyMarker+" node="+safeNodeID(nodeID) || !isManagedMobilePolicy(existing, n.mobileAdminGroupID, n.gatewayGroupID) {
			return errors.New("configured NetBird policy name has unmanaged rules")
		}
	}
	if managedCount > 1 {
		return errors.New("duplicate managed NetBird policies; reconcile them before pairing")
	}
	// Validation is complete before any mutation. Reuse matching policies verbatim
	// (including their posture checks), rather than rewriting them on every QR scan.
	if managedCount == 0 {
		policy := netBirdPolicy{Name: n.policyName, Description: mobilePolicyMarker + " node=" + safeNodeID(nodeID), Enabled: true,
			Rules: []netBirdRule{{Name: "Mobile HTTPS", Description: "Mobile gateway HTTPS", Enabled: true, Action: "accept", Protocol: "tcp", Ports: []string{"443"}, Sources: netBirdGroupIDs{n.mobileAdminGroupID}, Destinations: netBirdGroupIDs{n.gatewayGroupID}}}}
		if err := n.request(ctx, http.MethodPost, "/policies", policy, nil); err != nil {
			return fmt.Errorf("create mobile HTTPS policy: %w", err)
		}
	}
	if !peerPresent {
		input := map[string]any{"name": group.Name, "peers": []string{peerID}, "resources": []any{}}
		if err := n.request(ctx, http.MethodPut, "/groups/"+url.PathEscape(group.ID), input, nil); err != nil {
			return fmt.Errorf("add gateway peer to NetBird group: %w", err)
		}
	}
	return nil
}

func isManagedMobilePolicy(policy netBirdPolicy, mobileAdminGroupID, gatewayGroupID string) bool {
	if !policy.Enabled || len(policy.Rules) != 1 {
		return false
	}
	rule := policy.Rules[0]
	return len(rule.PortRanges) == 0 && len(rule.AuthorizedGroups) == 0 && rule.SourceResource == nil && rule.DestinationResource == nil && rule.Enabled && rule.Name == "Mobile HTTPS" && rule.Action == "accept" &&
		rule.Protocol == "tcp" && !rule.Bidirectional &&
		len(rule.Ports) == 1 && rule.Ports[0] == "443" &&
		len(rule.Sources) == 1 && rule.Sources[0] == mobileAdminGroupID &&
		len(rule.Destinations) == 1 && rule.Destinations[0] == gatewayGroupID
}

func safeNodeID(nodeID string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(nodeID) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
		if b.Len() >= 48 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}
