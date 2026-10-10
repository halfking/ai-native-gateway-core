package admin

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const mobileNodeDescriptorTTL = 5 * time.Minute

// MobileNodeDescriptorService creates short-lived, signed QR payloads used by
// the native client to add a gateway without transferring user credentials.
type MobileNodeDescriptorService struct {
	nodeID, name, publicOrigin, netbirdOrigin, peerID string
	signingKey                                        ed25519.PrivateKey
	netbird                                           *NetBirdGatewayAccess
}

type MobileNodeDescriptor struct {
	Version       int    `json:"version"`
	NodeID        string `json:"node_id"`
	Name          string `json:"name"`
	PublicOrigin  string `json:"public_origin,omitempty"`
	NetBirdOrigin string `json:"netbird_origin,omitempty"`
	PeerID        string `json:"peer_id,omitempty"`
	ExpiresAt     string `json:"expires_at"`
	Nonce         string `json:"nonce"`
	PublicKey     string `json:"public_key"`
}

// NewMobileNodeDescriptorServiceFromEnv returns nil when QR provisioning is
// not configured. A private NetBird origin is advertised only when the
// management API can also ensure the peer group and HTTPS policy.
func NewMobileNodeDescriptorServiceFromEnv() (*MobileNodeDescriptorService, error) {
	keyText := strings.TrimSpace(os.Getenv("LLMGW_NODE_DESCRIPTOR_PRIVATE_KEY"))
	if keyText == "" {
		return nil, nil
	}
	decode := func(s string) ([]byte, error) {
		if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
			return b, nil
		}
		return base64.StdEncoding.DecodeString(s)
	}
	keyBytes, err := decode(keyText)
	if err != nil {
		return nil, errors.New("invalid mobile node signing key encoding")
	}
	var signingKey ed25519.PrivateKey
	switch len(keyBytes) {
	case ed25519.SeedSize:
		signingKey = ed25519.NewKeyFromSeed(keyBytes)
	case ed25519.PrivateKeySize:
		signingKey = ed25519.PrivateKey(keyBytes)
	default:
		return nil, errors.New("mobile node signing key must contain an Ed25519 seed or private key")
	}

	publicOrigin, err := normalizeGatewayOrigin(os.Getenv("LLMGW_PUBLIC_ORIGIN"))
	if err != nil {
		return nil, fmt.Errorf("invalid LLMGW_PUBLIC_ORIGIN: %w", err)
	}
	netbirdOrigin, err := normalizeGatewayOrigin(os.Getenv("LLMGW_NETBIRD_ORIGIN"))
	if err != nil {
		return nil, fmt.Errorf("invalid LLMGW_NETBIRD_ORIGIN: %w", err)
	}
	if publicOrigin == "" && netbirdOrigin == "" {
		return nil, errors.New("at least one mobile gateway origin must be configured")
	}

	service := &MobileNodeDescriptorService{
		nodeID:        strings.TrimSpace(os.Getenv("LLMGW_NODE_ID")),
		name:          strings.TrimSpace(os.Getenv("LLMGW_NODE_NAME")),
		publicOrigin:  publicOrigin,
		netbirdOrigin: netbirdOrigin,
		peerID:        strings.TrimSpace(os.Getenv("NETBIRD_GATEWAY_PEER_ID")),
		signingKey:    signingKey,
	}
	if service.nodeID == "" || service.name == "" {
		return nil, errors.New("LLMGW_NODE_ID and LLMGW_NODE_NAME are required")
	}
	if netbirdOrigin != "" {
		access, err := NewNetBirdGatewayAccessFromEnv()
		if err != nil {
			return nil, err
		}
		service.netbird = access
		if service.peerID == "" {
			return nil, errors.New("NETBIRD_GATEWAY_PEER_ID is required when a NetBird origin is configured")
		}
	}
	return service, nil
}

func normalizeGatewayOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("must be an origin without a path, credentials, query, or fragment")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")) {
		return "", errors.New("HTTPS is required outside localhost")
	}
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	if (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
		u.Host = u.Hostname()
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

func (s *MobileNodeDescriptorService) Create(r *http.Request) (string, time.Time, error) {
	if s == nil {
		return "", time.Time{}, errors.New("mobile node QR provisioning is not configured")
	}
	if s.netbird != nil {
		if err := s.netbird.EnsureGatewayAccess(r.Context(), s.nodeID, s.peerID); err != nil {
			return "", time.Time{}, err
		}
	}
	expires := time.Now().UTC().Add(mobileNodeDescriptorTTL)
	nonce := make([]byte, 18)
	if _, err := rand.Read(nonce); err != nil {
		return "", time.Time{}, fmt.Errorf("create mobile node nonce: %w", err)
	}
	publicKey := s.signingKey.Public().(ed25519.PublicKey)
	payload := MobileNodeDescriptor{
		Version: 1, NodeID: s.nodeID, Name: s.name, PublicOrigin: s.publicOrigin,
		NetBirdOrigin: s.netbirdOrigin, PeerID: s.peerID, ExpiresAt: expires.Format(time.RFC3339),
		Nonce: base64.RawURLEncoding.EncodeToString(nonce), PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("encode mobile node descriptor: %w", err)
	}
	signature := ed25519.Sign(s.signingKey, payloadBytes)
	envelope := "llmgw-node:v1." + base64.RawURLEncoding.EncodeToString(payloadBytes) + "." + base64.RawURLEncoding.EncodeToString(signature)
	return envelope, expires, nil
}

func (h *Handler) handleMobileNodeDescriptor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.mobileNodeDescriptor == nil {
		http.Error(w, "mobile node provisioning is not configured", http.StatusServiceUnavailable)
		return
	}
	payload, expires, err := h.mobileNodeDescriptor.Create(r)
	if err != nil {
		// Avoid reflecting provider/network errors or credentials to the caller.
		http.Error(w, "could not prepare gateway access; check NetBird and node configuration", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"payload": payload, "expires_at": expires.Format(time.RFC3339)})
}
