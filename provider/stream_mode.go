package provider

import (
	"fmt"
	"strings"
)

type StreamMode string

const (
	StreamModeBoth          StreamMode = "both"
	StreamModeStreamOnly    StreamMode = "stream_only"
	StreamModeNonStreamOnly StreamMode = "non_stream_only"
)

func ParseStreamMode(value string) (StreamMode, error) {
	mode := StreamMode(strings.ToLower(strings.TrimSpace(value)))
	if mode == "" {
		return StreamModeBoth, nil
	}
	switch mode {
	case StreamModeBoth, StreamModeStreamOnly, StreamModeNonStreamOnly:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid stream mode %q", value)
	}
}

func (m StreamMode) Normalized() StreamMode {
	if m == "" {
		return StreamModeBoth
	}
	return m
}

func (m StreamMode) Allows(stream bool) bool {
	switch m.Normalized() {
	case StreamModeBoth:
		return true
	case StreamModeStreamOnly:
		return stream
	case StreamModeNonStreamOnly:
		return !stream
	default:
		return false
	}
}
