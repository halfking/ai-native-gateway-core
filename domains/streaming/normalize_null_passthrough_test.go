// Package streaming — normalize_null_passthrough_test.go
//
// R40 审计 ⚠️4 钉测：携带规范 "finish_reason":null 的标准流式帧必须
// 字节直通——此前 isJSONNullOrEmptyString 对真 null 也返回 true，置
// modified 后整帧被解析+重序列化（键序字典序化、空白紧凑化），语义
// 等价但每帧多一次 Marshal 且帧字节形态改变。空串 "" → null 的归一
// 行为（Xcode/glm-5.2 修复）不受影响。
package streaming

import (
	"bytes"
	"testing"
)

func TestNormalizeChunkRealNullFinishReasonPassesThrough(t *testing.T) {
	n := NewNormalizer()

	// 标准 OpenAI 非终帧：finish_reason:null、键序故意非字典序。
	frame := []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n")
	got := n.NormalizeChunk(frame, true)
	if !bytes.Equal(got, frame) {
		t.Fatalf("real-null finish_reason frame must pass through byte-identical:\n in: %s\ngot: %s", frame, got)
	}

	// 终帧：finish_reason:"stop" 无需归一时也应直通。
	final := []byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	got = n.NormalizeChunk(final, true)
	if !bytes.Equal(got, final) {
		t.Fatalf("canonical stop frame must pass through byte-identical:\n in: %s\ngot: %s", final, got)
	}
}

func TestNormalizeChunkEmptyStringFinishReasonStillNormalized(t *testing.T) {
	n := NewNormalizer()
	// 回归保护："" → null 的 Xcode/glm-5.2 归一不受上面直通分支影响。
	frame := []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"\"}]}\n\n")
	got := n.NormalizeChunk(frame, true)
	if !bytes.Contains(got, []byte(`"finish_reason":null`)) {
		t.Fatalf(`empty-string finish_reason must normalize to null, got: %s`, got)
	}
}
