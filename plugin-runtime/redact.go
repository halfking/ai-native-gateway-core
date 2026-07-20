package pluginruntime

import (
	"bytes"
	"io"
	"strings"
)

// NewRedactingWriter wraps next so that each complete line has occurrences of
// secret replaced with ***REDACTED***. Empty secret = passthrough (returns next directly).
// Used to filter plugin stdout/stderr so a plugin logging its env can't leak the
// context secret into gateway logs.
func NewRedactingWriter(secret []byte, next io.Writer) io.Writer {
	if len(secret) == 0 {
		return next
	}
	return &redactingWriter{secret: string(secret), next: next}
}

type redactingWriter struct {
	secret string
	next   io.Writer
	buf    bytes.Buffer
}

func (w *redactingWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		idx := bytes.IndexByte(w.buf.Bytes(), '\n')
		if idx < 0 {
			break
		}
		line := w.buf.Next(idx + 1) // includes the newline
		cleaned := strings.ReplaceAll(string(line), w.secret, "***REDACTED***")
		if _, err := io.WriteString(w.next, cleaned); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}
