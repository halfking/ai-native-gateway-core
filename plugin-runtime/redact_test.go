package pluginruntime

import (
	"bytes"
	"strings"
	"testing"
)

func TestRedactingWriter_FiltersSecret(t *testing.T) {
	var buf bytes.Buffer
	secret := "super-secret-value"
	w := NewRedactingWriter([]byte(secret), &buf)
	_, _ = w.Write([]byte("before secret=super-secret-value after\n"))
	_, _ = w.Write([]byte("no secret here\n"))
	_, _ = w.Write([]byte("partial super-sec\n"))
	got := buf.String()
	if strings.Contains(got, "super-secret-value") {
		t.Fatalf("secret leaked: %s", got)
	}
	if !strings.Contains(got, "no secret here") {
		t.Fatalf("non-secret line dropped: %s", got)
	}
	if !strings.Contains(got, "partial super-sec") {
		t.Fatalf("partial match wrongly filtered: %s", got)
	}
}

func TestRedactingWriter_EmptySecretPassthrough(t *testing.T) {
	var buf bytes.Buffer
	w := NewRedactingWriter(nil, &buf)
	_, _ = w.Write([]byte("anything\n"))
	if buf.String() != "anything\n" {
		t.Fatalf("empty secret should passthrough, got %q", buf.String())
	}
}
