package distribution

import (
	"testing"
	"time"
)

func TestTicketSignerRoundTrip(t *testing.T) {
	signer := NewTicketSigner([]byte("test-secret"), 5*time.Minute)
	token, exp, err := signer.Issue("req-1", "v2.4.5", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if exp.Before(time.Now()) {
		t.Fatal("expires in past")
	}
	claims, err := signer.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.RequestID != "req-1" || claims.Platform != "linux" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestTicketSignerExpired(t *testing.T) {
	signer := NewTicketSigner([]byte("test-secret"), -time.Minute)
	token, _, err := signer.Issue("req-2", "v1.0.0", "darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer.Verify(token); err == nil {
		t.Fatal("expected expired error")
	}
}

func TestArtifactFileName(t *testing.T) {
	if got := artifactFileName("v2.4.5", "linux", "amd64"); got == "" {
		t.Fatal("empty name")
	}
	if got := artifactFileName("v2.4.5", "windows", "amd64"); len(got) < 4 || got[len(got)-4:] != ".zip" {
		t.Fatalf("windows should be zip: %s", got)
	}
}
