package v2

import (
	"testing"
)

func TestSubmitModeDetector_HeaderDetection(t *testing.T) {
	detector := NewSubmitModeDetector()

	tests := []struct {
		name   string
		header string
		want   SubmitMode
	}{
		{
			name:   "explicit delta",
			header: "delta",
			want:   SubmitModeDelta,
		},
		{
			name:   "explicit snapshot",
			header: "snapshot",
			want:   SubmitModeSnapshot,
		},
		{
			name:   "explicit full",
			header: "full",
			want:   SubmitModeFull,
		},
		{
			name:   "case insensitive",
			header: "DELTA",
			want:   SubmitModeDelta,
		},
		{
			name:   "with whitespace",
			header: "  snapshot  ",
			want:   SubmitModeSnapshot,
		},
		{
			name:   "empty header defaults to full",
			header: "",
			want:   SubmitModeFull,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := DetectionContext{
				SubmitModeHeader: tt.header,
				ClientMessages:   []Message{},
				LastOutboundBody: []Message{},
			}
			got := detector.Detect(ctx)
			if got != tt.want {
				t.Errorf("Detect() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSubmitModeDetector_FirstTurn(t *testing.T) {
	detector := NewSubmitModeDetector()

	ctx := DetectionContext{
		SubmitModeHeader: "",
		ClientMessages: []Message{
			{Role: "user", Content: "Hello"},
		},
		LastOutboundBody: []Message{}, // No previous context
	}

	got := detector.Detect(ctx)
	if got != SubmitModeFull {
		t.Errorf("First turn should be SubmitModeFull, got %v", got)
	}
}

func TestSubmitModeDetector_MessageCountRegression(t *testing.T) {
	detector := NewSubmitModeDetector()

	// Previous turn had 5 messages
	lastOutbound := []Message{
		{Role: "user", Content: "Message 1"},
		{Role: "assistant", Content: "Response 1"},
		{Role: "user", Content: "Message 2"},
		{Role: "assistant", Content: "Response 2"},
		{Role: "user", Content: "Message 3"},
	}

	// Client sent only 2 messages (fewer than before)
	clientMessages := []Message{
		{Role: "user", Content: "Summary of previous messages"},
		{Role: "user", Content: "New question"},
	}

	ctx := DetectionContext{
		ClientMessages:   clientMessages,
		LastOutboundBody: lastOutbound,
	}

	got := detector.Detect(ctx)
	if got != SubmitModeInferredCompressed {
		t.Errorf("Message count regression should detect inferred_compressed, got %v", got)
	}
}

func TestSubmitModeDetector_SummaryMarker(t *testing.T) {
	detector := NewSubmitModeDetector()

	tests := []struct {
		name     string
		messages []Message
		want     SubmitMode
	}{
		{
			name: "contains smm_v1 marker",
			messages: []Message{
				{Role: "user", Content: "[smm_v1: Previous conversation summarized...]"},
				{Role: "user", Content: "New message"},
			},
			want: SubmitModeInferredCompressed,
		},
		{
			name: "contains summary marker",
			messages: []Message{
				{Role: "user", Content: "[summary: Earlier messages...]"},
			},
			want: SubmitModeInferredCompressed,
		},
	{
		name: "no marker with overlap",
		messages: []Message{
			{Role: "user", Content: "Normal message"},
			{Role: "user", Content: "Previous"},
		},
		want: SubmitModeFull,
	},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := DetectionContext{
				ClientMessages: tt.messages,
				LastOutboundBody: []Message{
					{Role: "user", Content: "Previous"},
				},
			}
			got := detector.Detect(ctx)
			if got != tt.want {
				t.Errorf("Detect() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSubmitModeDetector_OrphanedToolResult(t *testing.T) {
	detector := NewSubmitModeDetector()

	// Client sent a tool result without the corresponding tool_calls message
	clientMessages := []Message{
		{Role: "user", Content: "Calculate 2+2"},
		// Missing: assistant message with tool_calls
		{Role: "tool", ToolCallID: "call_123", Content: "4"},
		{Role: "user", Content: "Thanks"},
	}

	ctx := DetectionContext{
		ClientMessages: clientMessages,
		LastOutboundBody: []Message{
			{Role: "user", Content: "Previous"},
		},
	}

	got := detector.Detect(ctx)
	if got != SubmitModeInferredCompressed {
		t.Errorf("Orphaned tool result should detect inferred_compressed, got %v", got)
	}
}

func TestSubmitModeDetector_LCSOverlap(t *testing.T) {
	detector := NewSubmitModeDetector()

	// Previous outbound
	lastOutbound := []Message{
		{Role: "user", Content: "Message 1"},
		{Role: "assistant", Content: "Response 1"},
		{Role: "user", Content: "Message 2"},
		{Role: "assistant", Content: "Response 2"},
	}

	tests := []struct {
		name           string
		clientMessages []Message
		want           SubmitMode
		description    string
	}{
		{
			name: "high overlap - full mode",
			clientMessages: []Message{
				{Role: "user", Content: "Message 1"},
				{Role: "assistant", Content: "Response 1"},
				{Role: "user", Content: "Message 2"},
				{Role: "assistant", Content: "Response 2"},
				{Role: "user", Content: "Message 3"}, // Only new message
			},
			want:        SubmitModeFull,
			description: "Client sent full history with one new message",
		},
		{
			name: "low overlap - inferred compressed",
			clientMessages: []Message{
				{Role: "user", Content: "Completely different message"},
				{Role: "user", Content: "Another new message"},
			},
			want:        SubmitModeInferredCompressed,
			description: "Client sent completely different messages",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := DetectionContext{
				ClientMessages:   tt.clientMessages,
				LastOutboundBody: lastOutbound,
			}
			got := detector.Detect(ctx)
			if got != tt.want {
				t.Errorf("%s: Detect() = %v, want %v", tt.description, got, tt.want)
			}
		})
	}
}

func TestSubmitModeDetector_CalculateLCSOverlap(t *testing.T) {
	detector := NewSubmitModeDetector()

	tests := []struct {
		name         string
		client       []Message
		lastOutbound []Message
		wantMin      float64
		wantMax      float64
	}{
		{
			name: "identical messages",
			client: []Message{
				{Role: "user", Content: "Hello"},
				{Role: "assistant", Content: "Hi"},
			},
			lastOutbound: []Message{
				{Role: "user", Content: "Hello"},
				{Role: "assistant", Content: "Hi"},
			},
			wantMin: 0.9,
			wantMax: 1.0,
		},
		{
			name: "no overlap",
			client: []Message{
				{Role: "user", Content: "Message A"},
			},
			lastOutbound: []Message{
				{Role: "user", Content: "Message B"},
			},
			wantMin: 0.0,
			wantMax: 0.1,
		},
	{
		name: "partial overlap",
		client: []Message{
			{Role: "user", Content: "Message 1"},
			{Role: "user", Content: "Message 2"},
			{Role: "user", Content: "Message 3"},
		},
		lastOutbound: []Message{
			{Role: "user", Content: "Message 1"},
			{Role: "user", Content: "Message 2"},
		},
		wantMin: 0.9,
		wantMax: 1.1,
	},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			overlap := detector.calculateLCSOverlap(tt.client, tt.lastOutbound)
			if overlap < tt.wantMin || overlap > tt.wantMax {
				t.Errorf("calculateLCSOverlap() = %v, want between %v and %v",
					overlap, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestMessageFingerprint(t *testing.T) {
	tests := []struct {
		name string
		msg1 Message
		msg2 Message
		same bool
	}{
		{
			name: "identical messages",
			msg1: Message{Role: "user", Content: "Hello"},
			msg2: Message{Role: "user", Content: "Hello"},
			same: true,
		},
		{
			name: "different content",
			msg1: Message{Role: "user", Content: "Hello"},
			msg2: Message{Role: "user", Content: "World"},
			same: false,
		},
		{
			name: "different role",
			msg1: Message{Role: "user", Content: "Hello"},
			msg2: Message{Role: "assistant", Content: "Hello"},
			same: false,
		},
		{
			name: "with tool_call_id",
			msg1: Message{Role: "tool", ToolCallID: "call_123", Content: "Result"},
			msg2: Message{Role: "tool", ToolCallID: "call_123", Content: "Result"},
			same: true,
		},
		{
			name: "different tool_call_id",
			msg1: Message{Role: "tool", ToolCallID: "call_123", Content: "Result"},
			msg2: Message{Role: "tool", ToolCallID: "call_456", Content: "Result"},
			same: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fp1 := messageFingerprint(tt.msg1)
			fp2 := messageFingerprint(tt.msg2)

			if tt.same && fp1 != fp2 {
				t.Errorf("Expected same fingerprint, got %v and %v", fp1, fp2)
			}
			if !tt.same && fp1 == fp2 {
				t.Errorf("Expected different fingerprint, got both %v", fp1)
			}
		})
	}
}

func TestSubmitModeDetector_RealWorldScenarios(t *testing.T) {
	detector := NewSubmitModeDetector()

	t.Run("Cursor/VSCode incremental mode", func(t *testing.T) {
		// Cursor typically sends full history each time
		ctx := DetectionContext{
			SubmitModeHeader: "", // No header
			ClientMessages: []Message{
				{Role: "user", Content: "First question"},
				{Role: "assistant", Content: "First answer"},
				{Role: "user", Content: "Second question"},
				{Role: "assistant", Content: "Second answer"},
				{Role: "user", Content: "Third question"},
			},
			LastOutboundBody: []Message{
				{Role: "user", Content: "First question"},
				{Role: "assistant", Content: "First answer"},
				{Role: "user", Content: "Second question"},
				{Role: "assistant", Content: "Second answer"},
			},
		}

		got := detector.Detect(ctx)
		if got != SubmitModeFull {
			t.Errorf("Cursor incremental should detect SubmitModeFull, got %v", got)
		}
	})

	t.Run("Client with compression", func(t *testing.T) {
		// Client compressed the history
		ctx := DetectionContext{
			ClientMessages: []Message{
				{Role: "user", Content: "[smm_v1: Previous conversation about math...]"},
				{Role: "user", Content: "Now solve 5+5"},
			},
			LastOutboundBody: []Message{
				{Role: "user", Content: "What is 2+2?"},
				{Role: "assistant", Content: "4"},
				{Role: "user", Content: "What is 3+3?"},
				{Role: "assistant", Content: "6"},
			},
		}

		got := detector.Detect(ctx)
		if got != SubmitModeInferredCompressed {
			t.Errorf("Client compression should detect SubmitModeInferredCompressed, got %v", got)
		}
	})

	t.Run("Explicit delta mode", func(t *testing.T) {
		// Well-behaved client with explicit header
		ctx := DetectionContext{
			SubmitModeHeader: "delta",
			ClientMessages: []Message{
				{Role: "user", Content: "Latest message only"},
			},
			LastOutboundBody: []Message{
				{Role: "user", Content: "Previous messages"},
			},
		}

		got := detector.Detect(ctx)
		if got != SubmitModeDelta {
			t.Errorf("Explicit delta should be respected, got %v", got)
		}
	})
}

func TestSubmitModeDetector_AttachmentOnlyChange(t *testing.T) {
	detector := NewSubmitModeDetector()

	t.Run("Same messages, different attachments", func(t *testing.T) {
		ctx := DetectionContext{
			ClientMessages: []Message{
				{Role: "user", Content: "Look at this image"},
				{Role: "assistant", Content: "I can see it"},
			},
			LastOutboundBody: []Message{
				{Role: "user", Content: "Look at this image"},
				{Role: "assistant", Content: "I can see it"},
			},
			CurrentAttachments: []AttachmentRef{
				{ObjectKey: "new_image.png", SHA256: "abc123"},
			},
			PreviousAttachments: []AttachmentRef{
				{ObjectKey: "old_image.png", SHA256: "def456"},
			},
		}

		got := detector.Detect(ctx)
		if got != SubmitModeAttachmentOnly {
			t.Errorf("Same messages with different attachments should detect SubmitModeAttachmentOnly, got %v", got)
		}
	})

	t.Run("Same messages, added attachment", func(t *testing.T) {
		ctx := DetectionContext{
			ClientMessages: []Message{
				{Role: "user", Content: "Analyze this"},
			},
			LastOutboundBody: []Message{
				{Role: "user", Content: "Analyze this"},
			},
			CurrentAttachments: []AttachmentRef{
				{ObjectKey: "document.pdf", SHA256: "xyz789"},
			},
			PreviousAttachments: []AttachmentRef{},
		}

		got := detector.Detect(ctx)
		if got != SubmitModeAttachmentOnly {
			t.Errorf("Adding attachment with same messages should detect SubmitModeAttachmentOnly, got %v", got)
		}
	})

	t.Run("Same messages, removed attachment", func(t *testing.T) {
		ctx := DetectionContext{
			ClientMessages: []Message{
				{Role: "user", Content: "What do you think?"},
			},
			LastOutboundBody: []Message{
				{Role: "user", Content: "What do you think?"},
			},
			CurrentAttachments: []AttachmentRef{},
			PreviousAttachments: []AttachmentRef{
				{ObjectKey: "file.txt", SHA256: "aaa111"},
			},
		}

		got := detector.Detect(ctx)
		if got != SubmitModeAttachmentOnly {
			t.Errorf("Removing attachment with same messages should detect SubmitModeAttachmentOnly, got %v", got)
		}
	})

	t.Run("Different messages, different attachments - not attachment-only", func(t *testing.T) {
		ctx := DetectionContext{
			ClientMessages: []Message{
				{Role: "user", Content: "Look at this NEW image"},
			},
			LastOutboundBody: []Message{
				{Role: "user", Content: "Look at this OLD image"},
			},
			CurrentAttachments: []AttachmentRef{
				{ObjectKey: "new.png", SHA256: "abc"},
			},
			PreviousAttachments: []AttachmentRef{
				{ObjectKey: "old.png", SHA256: "def"},
			},
		}

		got := detector.Detect(ctx)
		if got == SubmitModeAttachmentOnly {
			t.Errorf("Different messages should NOT detect SubmitModeAttachmentOnly, got %v", got)
		}
	})

	t.Run("Same messages, same attachments - not attachment-only", func(t *testing.T) {
		ctx := DetectionContext{
			ClientMessages: []Message{
				{Role: "user", Content: "Hello"},
			},
			LastOutboundBody: []Message{
				{Role: "user", Content: "Hello"},
			},
			CurrentAttachments: []AttachmentRef{
				{ObjectKey: "file.txt", SHA256: "same123"},
			},
			PreviousAttachments: []AttachmentRef{
				{ObjectKey: "file.txt", SHA256: "same123"},
			},
		}

		got := detector.Detect(ctx)
		if got == SubmitModeAttachmentOnly {
			t.Errorf("Same messages and attachments should NOT detect SubmitModeAttachmentOnly, got %v", got)
		}
	})

	t.Run("Nearly identical messages (90%+ match), different attachments", func(t *testing.T) {
		// 5 messages, 4 identical = 80% match, should still trigger with high LCS overlap
		ctx := DetectionContext{
			ClientMessages: []Message{
				{Role: "user", Content: "Message 1"},
				{Role: "assistant", Content: "Response 1"},
				{Role: "user", Content: "Message 2"},
				{Role: "assistant", Content: "Response 2"},
				{Role: "user", Content: "Message 3"},
			},
			LastOutboundBody: []Message{
				{Role: "user", Content: "Message 1"},
				{Role: "assistant", Content: "Response 1"},
				{Role: "user", Content: "Message 2"},
				{Role: "assistant", Content: "Response 2"},
				{Role: "user", Content: "Message 3"},
			},
			CurrentAttachments: []AttachmentRef{
				{ObjectKey: "new_file.pdf", SHA256: "new123"},
			},
			PreviousAttachments: []AttachmentRef{
				{ObjectKey: "old_file.pdf", SHA256: "old123"},
			},
		}

		got := detector.Detect(ctx)
		// With 100% message match and different attachments, should be attachment-only
		if got != SubmitModeAttachmentOnly {
			t.Errorf("Identical messages with different attachments should detect SubmitModeAttachmentOnly, got %v", got)
		}
	})
}

// TestDetectWithRequest_ConvenienceMethod tests the DetectWithRequest convenience method
func TestDetectWithRequest_ConvenienceMethod(t *testing.T) {
	detector := NewSubmitModeDetector()

	tests := []struct {
		name                string
		req                 *ProcessedRequest
		header              string
		previousAttachments []AttachmentRef
		want                SubmitMode
	}{
		{
			name: "First turn with no previous attachments",
			req: &ProcessedRequest{
				RequestBody: []Message{
					{Role: "user", Content: "Hello"},
				},
				LastOutboundBody: []Message{},
				Attachments: []AttachmentRef{
					{ObjectKey: "file1.pdf", SHA256: "abc123"},
				},
			},
			header:              "",
			previousAttachments: []AttachmentRef{},
			want:                SubmitModeFull,
		},
		{
			name: "Explicit header takes precedence",
			req: &ProcessedRequest{
				RequestBody: []Message{
					{Role: "user", Content: "Hello"},
				},
				LastOutboundBody: []Message{
					{Role: "user", Content: "Hello"},
				},
				Attachments: []AttachmentRef{
					{ObjectKey: "file2.pdf", SHA256: "def456"},
				},
			},
			header: "attachment_only",
			previousAttachments: []AttachmentRef{
				{ObjectKey: "file1.pdf", SHA256: "abc123"},
			},
			want: SubmitModeAttachmentOnly,
		},
		{
			name: "Attachment change with same messages",
			req: &ProcessedRequest{
				RequestBody: []Message{
					{Role: "user", Content: "Analyze this document"},
					{Role: "assistant", Content: "Sure"},
				},
				LastOutboundBody: []Message{
					{Role: "user", Content: "Analyze this document"},
					{Role: "assistant", Content: "Sure"},
				},
				Attachments: []AttachmentRef{
					{ObjectKey: "file2.pdf", SHA256: "def456"},
				},
			},
			header: "",
			previousAttachments: []AttachmentRef{
				{ObjectKey: "file1.pdf", SHA256: "abc123"},
			},
			want: SubmitModeAttachmentOnly,
		},
		{
			name: "Different messages with different attachments - not attachment-only",
			req: &ProcessedRequest{
				RequestBody: []Message{
					{Role: "user", Content: "New question"},
				},
				LastOutboundBody: []Message{
					{Role: "user", Content: "Old question"},
				},
				Attachments: []AttachmentRef{
					{ObjectKey: "file2.pdf", SHA256: "def456"},
				},
			},
			header: "",
			previousAttachments: []AttachmentRef{
				{ObjectKey: "file1.pdf", SHA256: "abc123"},
			},
			want: SubmitModeInferredCompressed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detector.DetectWithRequest(tt.req, tt.header, tt.previousAttachments)
			if got != tt.want {
				t.Errorf("DetectWithRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}
