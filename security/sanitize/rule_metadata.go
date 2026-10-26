package sanitize

import "fmt"

func (p patternEntry) appliesScope(scope DetectionScope) bool {
	if len(p.scopes) == 0 {
		return true
	}
	for _, v := range p.scopes {
		if v == scope {
			return true
		}
	}
	return false
}

func patternMetadata(category, name string, p patternDefinition) (string, []DetectionScope, string, error) {
	kind := p.Kind
	if kind == "" {
		switch name {
		case "password":
			kind = "credential_declaration"
		case "account":
			kind = "account_declaration"
		case "credential_pair_account", "credential_pair_secret":
			kind = "credential_pair"
		default:
			kind = "pattern"
		}
	}
	switch kind {
	case "pattern", "review_combination", "vendor_key_shape", "network_destination", "credential_declaration", "credential_assignment", "account_declaration", "account_assignment", "credential_pair":
	default:
		return "", nil, "", fmt.Errorf("unsupported rule kind")
	}
	var scopes []DetectionScope
	for _, v := range p.Scopes {
		if (kind == "credential_declaration" || kind == "account_declaration" || kind == "credential_pair" || kind == "review_combination") && v != "input" {
			return "", nil, "", fmt.Errorf("natural declarations are input-only")
		}
		if kind == "network_destination" && v != "egress" {
			return "", nil, "", fmt.Errorf("network destinations are egress-only")
		}
		switch v {
		case "input":
			scopes = append(scopes, ScopeInputDeclarations)
		case "output":
			scopes = append(scopes, ScopeRedact)
		case "egress":
			scopes = append(scopes, ScopeEgress)
		default:
			return "", nil, "", fmt.Errorf("unsupported rule scope")
		}
	}
	action := p.Action
	if action == "" {
		action = "mask"
	}
	if action != "mask" && action != "block" && action != "log" {
		return "", nil, "", fmt.Errorf("unsupported rule action")
	}
	return kind, scopes, action, nil
}
