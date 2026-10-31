package registration

import (
	"testing"
)

// The spellings the Go SDK's envBool accepts. Kept as a table so that a change
// on either side shows up as a failing case rather than as a silent difference
// between services.
func TestEnvBoolAcceptsTheSameSpellingsAsTheGoSDK(t *testing.T) {
	on := []string{"1", "t", "true", "TRUE", "yes", "y", "on", " true "}
	off := []string{"0", "f", "false", "FALSE", "no", "n", "off"}
	for _, value := range on {
		got, err := envBool(func(string) string { return value }, "GOACOS_ENABLED", false)
		if err != nil || !got {
			t.Errorf("GOACOS_ENABLED=%q should be ON, got %v (err=%v)", value, got, err)
		}
	}
	for _, value := range off {
		got, err := envBool(func(string) string { return value }, "GOACOS_ENABLED", true)
		if err != nil || got {
			t.Errorf("GOACOS_ENABLED=%q should be OFF, got %v (err=%v)", value, got, err)
		}
	}
	// Empty ⇒ fallback.
	got, err := envBool(func(string) string { return "" }, "GOACOS_ENABLED", false)
	if err != nil || got {
		t.Errorf("empty GOACOS_ENABLED should fall back to false, got %v (err=%v)", got, err)
	}
	// Anything else is an **error**, not "off".
	if _, err := envBool(func(string) string { return "sure" }, "GOACOS_ENABLED", false); err == nil {
		t.Error("a non-boolean GOACOS_ENABLED must be an error")
	}
}

func TestListenPortReadsThePortTheProcessActuallyBinds(t *testing.T) {
	cases := map[string]int{
		":8781":          8781,
		"0.0.0.0:8782":   8782,
		"127.0.0.1:8781": 8781,
	}
	for addr, want := range cases {
		got, err := ListenPort(addr)
		if err != nil {
			t.Fatalf("ListenPort(%q) returned %v", addr, err)
		}
		if got != want {
			t.Errorf("ListenPort(%q) = %d, want %d", addr, got, want)
		}
	}
	for _, addr := range []string{"", ":", ":abc", ":0", ":70000", "not-an-addr"} {
		if _, err := ListenPort(addr); err == nil {
			t.Errorf("ListenPort(%q) should fail", addr)
		}
	}
}

func TestRegistrationIsOffByDefault(t *testing.T) {
	outcome := ReadSettings(func(string) string { return "" }, ":8781")
	if outcome.Enabled {
		t.Fatal("registration must be off when GOACOS_ENABLED is absent")
	}
	if outcome.Reason == "" {
		t.Error("a skipped registration must say why")
	}
}

func TestANonBooleanSwitchDoesNotRegister(t *testing.T) {
	outcome := ReadSettings(func(key string) string {
		if key == "GOACOS_ENABLED" {
			return "sure"
		}
		return ""
	}, ":8781")
	if outcome.Enabled {
		t.Fatal("a non-boolean GOACOS_ENABLED must not register")
	}
	if outcome.Reason == "" {
		t.Error("the reason must be sayable in the log")
	}
}

func TestAnExplicitIPIsRequired(t *testing.T) {
	outcome := ReadSettings(func(key string) string {
		switch key {
		case "GOACOS_ENABLED":
			return "true"
		case "GOACOS_IP":
			return "   "
		}
		return ""
	}, ":8781")
	if outcome.Enabled {
		t.Fatal("a missing GOACOS_IP must not register — the address is never guessed")
	}
}

func TestTheAdvertisedPortComesFromTheListenAddressNotFromTheEnvironment(t *testing.T) {
	// The blue/green deployment moves the active port between 8781 and 8782
	// every day. The advertised port must follow the process, not a variable
	// that deployment may have left behind.
	outcome := ReadSettings(func(key string) string {
		switch key {
		case "GOACOS_ENABLED":
			return "true"
		case "GOACOS_IP":
			return "100.106.178.9"
		case "GOACOS_PORT":
			return "9999" // a stale value must be ignored
		}
		return ""
	}, ":8782")
	if !outcome.Enabled {
		t.Fatalf("expected registration to be enabled: %s", outcome.Reason)
	}
	if outcome.Settings.Port != 8782 {
		t.Errorf("advertised port = %d, want 8782 (the port the process binds)", outcome.Settings.Port)
	}
}

// A face WITH a port-mapping layer (the k3s NodePort face on 184) cannot
// derive the published port from the bind address: peers dial the node port
// (10023), not the container port (8781). That face declares it instead.
//
// The declaration is per face, so the same binary keeps its bind-derived
// behaviour on every face that does not declare one.
func TestTheFaceLevelPortDeclarationOverridesTheBindDerivedPort(t *testing.T) {
	outcome := ReadSettings(func(key string) string {
		switch key {
		case "GOACOS_ENABLED":
			return "true"
		case "GOACOS_IP":
			return "172.31.0.4"
		case PublishedPortEnv:
			return "10023"
		}
		return ""
	}, ":8781")
	if !outcome.Enabled {
		t.Fatalf("expected registration to be enabled: %s", outcome.Reason)
	}
	if outcome.Settings.Port != 10023 {
		t.Errorf("advertised port = %d, want 10023 (the port peers dial on this face)",
			outcome.Settings.Port)
	}
}

// The other half of the same rule: a face that declares nothing keeps the
// bind-derived port. This is what the systemd faces (154 / 245) rely on, and
// their behaviour must not change because another face got a new mechanism.
func TestWithoutTheFaceLevelDeclarationThePortStillComesFromTheBindAddress(t *testing.T) {
	outcome := ReadSettings(func(key string) string {
		switch key {
		case "GOACOS_ENABLED":
			return "true"
		case "GOACOS_IP":
			return "100.106.178.9"
		}
		return ""
	}, ":8782")
	if !outcome.Enabled {
		t.Fatalf("expected registration to be enabled: %s", outcome.Reason)
	}
	if outcome.Settings.Port != 8782 {
		t.Errorf("advertised port = %d, want 8782 (the port the process binds)",
			outcome.Settings.Port)
	}
}

// GOACOS_PORT is the fleet-wide name for "the published port of a
// containerised service". This service must never read it: on the
// blue/green faces the published port follows the bind address, and a
// leftover variable would advertise a port nobody listens on.
func TestTheFaceLevelDeclarationDoesNotReviveTheForbiddenPortVariable(t *testing.T) {
	outcome := ReadSettings(func(key string) string {
		switch key {
		case "GOACOS_ENABLED":
			return "true"
		case "GOACOS_IP":
			return "172.31.0.4"
		case "GOACOS_PORT":
			return "9999"
		case PublishedPortEnv:
			return "10023"
		}
		return ""
	}, ":8781")
	if !outcome.Enabled {
		t.Fatalf("expected registration to be enabled: %s", outcome.Reason)
	}
	if outcome.Settings.Port != 10023 {
		t.Errorf("advertised port = %d, want 10023 — GOACOS_PORT must be ignored",
			outcome.Settings.Port)
	}
}

// A declaration that is not a port must not fall back to the bind address:
// falling back would advertise the container-internal port, which is the
// exact "registry says healthy, real traffic 502" failure.
func TestAnIllegalFaceLevelDeclarationDoesNotRegister(t *testing.T) {
	for _, value := range []string{"abc", "0", "-1", "70000", "8781/tcp", "87 81"} {
		outcome := ReadSettings(func(key string) string {
			switch key {
			case "GOACOS_ENABLED":
				return "true"
			case "GOACOS_IP":
				return "172.31.0.4"
			case PublishedPortEnv:
				return value
			}
			return ""
		}, ":8781")
		if outcome.Enabled {
			t.Errorf("%s=%q must not register (it would advertise %d or a fallback)",
				PublishedPortEnv, value, outcome.Settings.Port)
		}
		if outcome.Reason == "" {
			t.Errorf("%s=%q must say why it did not register", PublishedPortEnv, value)
		}
	}
}

func TestPublishedPortReportsWhetherItWasDeclared(t *testing.T) {
	if port, declared, err := PublishedPort(func(string) string { return "" }); err != nil ||
		declared || port != 0 {
		t.Errorf("an absent declaration = (0, false, nil), got (%d, %t, %v)", port, declared, err)
	}
	if port, declared, err := PublishedPort(func(string) string { return "  10023  " }); err != nil ||
		!declared || port != 10023 {
		t.Errorf("a padded declaration = (10023, true, nil), got (%d, %t, %v)", port, declared, err)
	}
	for _, value := range []string{"abc", "0", "-1", "70000"} {
		if _, declared, err := PublishedPort(func(string) string { return value }); err == nil ||
			!declared {
			t.Errorf("%q must be an error on a declared-but-illegal port", value)
		}
	}
}

func TestTheNamespaceDefaultsToPublicAndTheNameFollowsTheDeclaration(t *testing.T) {
	outcome := ReadSettings(func(key string) string {
		switch key {
		case "GOACOS_ENABLED":
			return "true"
		case "GOACOS_IP":
			return "100.106.178.9"
		}
		return ""
	}, ":8781")
	if outcome.Settings.NamespaceID != DefaultNamespace {
		t.Errorf("namespace = %q, want %q", outcome.Settings.NamespaceID, DefaultNamespace)
	}
	if outcome.Settings.ServiceName != ServiceName {
		t.Errorf("service name = %q, want %q", outcome.Settings.ServiceName, ServiceName)
	}
}

func TestMetadataIsParsedAsCommaSeparatedKeyValue(t *testing.T) {
	got := ParseMetadata("app=llm,component=gateway,broken,,=x")
	want := map[string]string{"app": "llm", "component": "gateway"}
	if len(got) != len(want) {
		t.Fatalf("metadata = %v, want %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("metadata[%q] = %q, want %q", key, got[key], value)
		}
	}
}
