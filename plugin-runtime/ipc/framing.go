package ipc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// MaxMessageSize bounds each JSON request or response, including its whitespace.
const MaxMessageSize = 16 << 20

// ErrMessageTooLarge indicates that a JSON message exceeded MaxMessageSize.
var ErrMessageTooLarge = errors.New("IPC message exceeds size limit")

// ReadMessage reads one complete JSON value. Each connection carries exactly one
// request and one response; no length prefix is needed for existing JSON peers.
func ReadMessage(reader io.Reader) ([]byte, error) {
	limited := &io.LimitedReader{R: reader, N: MaxMessageSize + 1}
	var message json.RawMessage
	decoder := json.NewDecoder(limited)
	err := decoder.Decode(&message)
	if decoder.InputOffset() > MaxMessageSize || (err != nil && limited.N == 0) {
		return nil, ErrMessageTooLarge
	}
	if err != nil {
		return nil, fmt.Errorf("decode IPC message: %w", err)
	}
	return message, nil
}

// WriteMessage validates and writes the entire JSON value, including short
// writes. Objects and arrays retain the existing raw JSON wire representation;
// scalar values include a newline so decoding never needs to wait for EOF.
func WriteMessage(writer io.Writer, message []byte) error {
	if len(message) > MaxMessageSize {
		return ErrMessageTooLarge
	}
	if !json.Valid(message) {
		return errors.New("invalid IPC JSON message")
	}
	data := message
	if first := bytes.TrimSpace(message)[0]; first != '{' && first != '[' {
		data = append(append(make([]byte, 0, len(message)+1), message...), '\n')
	}
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
