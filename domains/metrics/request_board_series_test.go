package metrics

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestDecodeBoardTimeSeries(t *testing.T) {
	zone := time.FixedZone("", -4*60*60)
	bucket := time.Date(2026, 11, 25, 0, 20, 0, 123456000, zone)
	point := binary.BigEndian.AppendUint64(nil, uint64(bucket.UnixMicro()-postgresEpochMicros))
	point = binary.BigEndian.AppendUint32(point, uint32(int64(-4*60*60)&0xffffffff))
	for _, n := range []uint64{12, 1<<54 + 3, 123, math.Float64bits(0.000123)} {
		point = binary.BigEndian.AppendUint64(point, n)
	}
	trends := binary.BigEndian.AppendUint64(append([]byte(nil), point...), 8)
	trends = binary.BigEndian.AppendUint64(trends, math.Float64bits(12.5))
	model := "模型-a"
	models := append(binary.BigEndian.AppendUint32(append([]byte(nil), point...), uint32(len(model))), model...)
	a, b, err := decodeBoardTimeSeries(trends, models)
	if err != nil || len(a) != 1 || len(b) != 1 {
		t.Fatalf("trend=%+v model=%+v err=%v", a, b, err)
	}
	if b[0].Model != model || a[0].BoardPoint != b[0].BoardPoint || a[0].Success != 8 || a[0].AvgLatencyMS != 12.5 {
		t.Fatalf("trend=%+v model=%+v", a[0], b[0])
	}
	encoded, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"bucket":"2026-11-25T00:20:00.123456-04:00","requests":12,"tokens":18014398509481987,"credits":123,"cost_usd":0.000123,"success":8,"avg_latency_ms":12.5}]`
	if string(encoded) != want {
		t.Fatalf("got %s, want %s", encoded, want)
	}
	emptyTrends, emptyModels, err := decodeBoardTimeSeries(nil, nil)
	if err != nil || emptyTrends == nil || emptyModels == nil || len(emptyTrends) != 0 || len(emptyModels) != 0 {
		t.Fatalf("expected empty result slices, got %v %v (%v)", emptyTrends, emptyModels, err)
	}
}

func TestDecodeBoardTimeSeriesRejectsMalformedRows(t *testing.T) {
	truncatedName := make([]byte, boardModelHeaderSize)
	binary.BigEndian.PutUint32(truncatedName[boardPointSize:], 1)
	outOfRange := make([]byte, boardTrendSize)
	binary.BigEndian.PutUint64(outOfRange, math.MaxInt64)
	for _, tc := range []struct {
		name   string
		trends []byte
		models []byte
	}{
		{name: "truncated trend", trends: make([]byte, boardTrendSize-1)},
		{name: "truncated model header", models: make([]byte, boardModelHeaderSize-1)},
		{name: "truncated model name", models: truncatedName},
		{name: "out of range timestamp", trends: outOfRange},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := decodeBoardTimeSeries(tc.trends, tc.models); err == nil {
				t.Fatal("malformed series decoded successfully")
			}
		})
	}
}
