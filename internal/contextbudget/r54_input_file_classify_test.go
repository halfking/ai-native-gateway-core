package contextbudget

import "testing"

// TestR54InputFileDataURLClassifiesInline — B-P2-4（2026-11-14 复审，r1052
// 事故形状的 responses 文件族第 N+1 处）：`input_file.file_data` 是 OpenAI
// Responses 的内联 base64 data URL（字节逐进网关），此前与 `file_id` 一并
// 判 remote ⇒ 无 ExternalInputTokens 填充点下直发必 422 Uncountable——与
// image_url 的 data: URL 判 inline（classifyImageURLValue）不一致。
//
// 修复：file_id 仍 remote（provider 托管态，网关不可见）；file_data 走
// classifyImageURLValue 同口径（data: 前缀 ⇒ inline，其余 ⇒ remote）。
//
// 变异臂（红→绿）：把 file_data 臂还原为 `nonempty(v["file_data"]) ⇒ remote`
// ⇒ 本测试红。
func TestR54InputFileDataURLClassifiesInline(t *testing.T) {
	cases := []struct {
		name       string
		block      map[string]any
		wantInline bool
		wantRemote bool
	}{
		{
			name:       "file_data data URL is inline bytes",
			block:      map[string]any{"type": "input_file", "file_data": "data:text/plain;base64,SGVsbG8="},
			wantInline: true,
		},
		{
			name:       "file_id stays remote (provider-managed state)",
			block:      map[string]any{"type": "input_file", "file_id": "file-abc123"},
			wantRemote: true,
		},
		{
			name:       "non-data file_data string stays remote",
			block:      map[string]any{"type": "input_file", "file_data": "https://example.com/doc.pdf"},
			wantRemote: true,
		},
		{
			name:  "bare file type without payload neither inline nor remote",
			block: map[string]any{"type": "input_file"},
		},
	}
	for _, tc := range cases {
		inline, remote := classifyExternal(tc.block)
		if inline != tc.wantInline || remote != tc.wantRemote {
			t.Errorf("%s: classifyExternal = (inline=%v, remote=%v), want (inline=%v, remote=%v)",
				tc.name, inline, remote, tc.wantInline, tc.wantRemote)
		}
	}
}
