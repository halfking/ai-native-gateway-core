package streaming

import (
	"net/http/httptest"
	"testing"
)

func TestRetryCommitWriter_TracksWrittenBytes(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := &retryCommitWriter{ResponseWriter: recorder}

	if _, err := writer.Write(nil); err != nil {
		t.Fatal(err)
	}
	if writer.wrote.Load() {
		t.Fatal("empty write must not commit a streaming retry attempt")
	}
	if _, err := writer.Write([]byte("data: partial\n\n")); err != nil {
		t.Fatal(err)
	}
	if !writer.wrote.Load() {
		t.Fatal("non-empty write must commit a streaming retry attempt")
	}
}
