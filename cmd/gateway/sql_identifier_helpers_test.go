package main

// containsIdentifier reports whether needle appears in hay delimited by
// identifier boundaries, so `client_model` does not match `client_model_v2`.
//
// This file carries no build tag on purpose: both the !integration shape
// (s4_gate_measurement_test.go) and the untagged shape
// (s4_gate_coverage_faces_test.go) assert on the SQL constants in
// dual_read_validator.go, and a helper locked to one shape breaks
// `go vet -tags=integration ./...` the moment the other shape uses it —
// exactly the break 0490235bb shipped (guards red on the integration tree).
func containsIdentifier(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] != needle {
			continue
		}
		beforeOK := i == 0 || !isIdentByte(hay[i-1])
		after := i + len(needle)
		if beforeOK && (after >= len(hay) || !isIdentByte(hay[after])) {
			return true
		}
	}
	return false
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
