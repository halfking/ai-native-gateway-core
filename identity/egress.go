package identity

import "fmt"

// EgressIdentity is the upstream-facing virtual fingerprint for one credential slot.
type EgressIdentity struct {
	EgressSeed      string
	IdentityHash    string
	VirtualClientID string
	VirtualIP       string
	VirtualMAC      string
	SlotIndex       int
	CredentialID    int
}

// BuildEgressIdentity mirrors Python build_egress_identity().
func BuildEgressIdentity(credentialID, slotIndex int, tenantID string) EgressIdentity {
	if tenantID == "" {
		tenantID = "default"
	}
	seed := fmt.Sprintf("llmgw-cred%d-fp%d", credentialID, slotIndex)
	identityKey := fmt.Sprintf("%s:egress:%s", tenantID, seed)
	hash := sha256Hex(identityKey)
	return EgressIdentity{
		EgressSeed:      seed,
		IdentityHash:    hash,
		VirtualClientID: "vc-" + hash[:16],
		VirtualIP:       deriveVirtualIP(hash),
		VirtualMAC:      deriveVirtualMAC(hash),
		SlotIndex:       slotIndex,
		CredentialID:    credentialID,
	}
}
