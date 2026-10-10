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
)

const mobilePolicyMarker = "managed-by=llmgw-mobile-node"

// NetBirdGatewayAccess keeps the mobile administrator group connected to one
// explicitly configured gateway group with a single TCP/443 policy.
type NetBirdGatewayAccess struct {
	apiURL, token, gatewayGroupID, mobileAdminGroupID string
	policyName                                        string
	client                                            *http.Client
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
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Enabled       bool     `json:"enabled"`
	Action        string   `json:"action"`
	Bidirectional bool     `json:"bidirectional"`
	Protocol      string   `json:"protocol"`
	Ports         []string `json:"ports"`
	Sources       []string `json:"sources"`
	Destinations  []string `json:"destinations"`
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
		client: &http.Client{Timeout: 8 * time.Second},
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
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err := decoder.Decode(output); err != nil {
		return errors.New("NetBird API returned an invalid response")
	}
	return nil
}

func (n *NetBirdGatewayAccess) EnsureGatewayAccess(ctx context.Context, nodeID, peerID string) error {
	if peerID == "" {
		return errors.New("gateway NetBird peer ID is missing")
	}
	var group netBirdGroup
	if err := n.request(ctx, http.MethodGet, "/groups/"+url.PathEscape(n.gatewayGroupID), nil, &group); err != nil {
		return fmt.Errorf("load configured gateway group: %w", err)
	}
	if group.ID != n.gatewayGroupID {
		return errors.New("NetBird API returned a different gateway group than configured")
	}
	peerPresent := false
	peerIDs := make([]string, 0, len(group.Peers)+1)
	for _, peer := range group.Peers {
		if peer.ID == "" {
			continue
		}
		peerIDs = append(peerIDs, peer.ID)
		peerPresent = peerPresent || peer.ID == peerID
	}
	if !peerPresent {
		peerIDs = append(peerIDs, peerID)
		input := map[string]any{"name": group.Name, "peers": peerIDs}
		if len(group.Resources) > 0 {
			input["resources"] = group.Resources
		}
		if err := n.request(ctx, http.MethodPut, "/groups/"+url.PathEscape(group.ID), input, nil); err != nil {
			return fmt.Errorf("add gateway peer to NetBird group: %w", err)
		}
	}

	var policies []netBirdPolicy
	if err := n.request(ctx, http.MethodGet, "/policies", nil, &policies); err != nil {
		return fmt.Errorf("load NetBird policies: %w", err)
	}
	policy := netBirdPolicy{
		Name:        n.policyName,
		Description: mobilePolicyMarker + " node=" + safeNodeID(nodeID),
		Enabled:     true,
		Rules: []netBirdRule{{
			Name: "Mobile HTTPS", Description: "Allow logged-in mobile operators to reach the gateway HTTPS endpoint",
			Enabled: true, Action: "accept", Bidirectional: false, Protocol: "tcp", Ports: []string{"443"},
			Sources: []string{n.mobileAdminGroupID}, Destinations: []string{n.gatewayGroupID},
		}},
	}
	for _, existing := range policies {
		if existing.Name != n.policyName {
			continue
		}
		if !strings.Contains(existing.Description, mobilePolicyMarker) || !isManagedMobilePolicy(existing, n.mobileAdminGroupID, n.gatewayGroupID) {
			return errors.New("a NetBird policy already uses the configured mobile policy name with unmanaged rules")
		}
		policy.ID = existing.ID
		policy.SourcePostureChecks = existing.SourcePostureChecks
		break
	}
	if policy.ID == "" {
		return n.request(ctx, http.MethodPost, "/policies", policy, nil)
	}
	return n.request(ctx, http.MethodPut, "/policies/"+url.PathEscape(policy.ID), policy, nil)
}

func isManagedMobilePolicy(policy netBirdPolicy, mobileAdminGroupID, gatewayGroupID string) bool {
	if !policy.Enabled || len(policy.Rules) != 1 {
		return false
	}
	rule := policy.Rules[0]
	return rule.Enabled && rule.Name == "Mobile HTTPS" && rule.Action == "accept" &&
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
