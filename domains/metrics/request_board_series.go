package metrics

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"
)

// Keep the time series in the board's single SQL snapshot. PostgreSQL's
// binary scalar encodings avoid serializing the chart rows to JSON twice.
const boardPointEncodingSQL = `timestamptz_send(bucket) || int4send(extract(timezone from bucket)::int)
	|| int8send(requests::bigint) || int8send(tokens::bigint) || int8send(credits::bigint) || float8send(cost_usd::float8)`

const boardTimeSeriesSQL = `
	coalesce((SELECT string_agg(` + boardPointEncodingSQL + `
		|| int8send(success::bigint) || float8send(avg_latency_ms), ''::bytea ORDER BY bucket)
		FROM trends), ''::bytea),
	coalesce((SELECT string_agg(` + boardPointEncodingSQL + `
		|| int4send(octet_length(convert_to(series,'UTF8'))) || convert_to(series,'UTF8'), ''::bytea ORDER BY bucket, series)
		FROM chart_points), ''::bytea)`

const (
	boardPointSize       = 44
	boardTrendSize       = boardPointSize + 16
	boardModelHeaderSize = boardPointSize + 4
	postgresEpochMicros  = 946684800000000 // 2000-01-01 UTC in Unix microseconds
)

func decodeBoardTimeSeries(trends, models []byte) ([]BoardTrendPoint, []BoardModelPoint, error) {
	if len(trends)%boardTrendSize != 0 {
		return nil, nil, fmt.Errorf("truncated board trend")
	}
	zones := map[int]*time.Location{0: time.UTC}
	trendPoints := make([]BoardTrendPoint, 0, len(trends)/boardTrendSize)
	for offset := 0; offset < len(trends); offset += boardTrendSize {
		row := trends[offset : offset+boardTrendSize]
		point, err := decodeBoardPoint(row, zones)
		if err != nil {
			return nil, nil, err
		}
		trendPoints = append(trendPoints, BoardTrendPoint{
			BoardPoint:   point,
			Success:      int64(binary.BigEndian.Uint64(row[44:52])),
			AvgLatencyMS: math.Float64frombits(binary.BigEndian.Uint64(row[52:60])),
		})
	}

	modelPoints := make([]BoardModelPoint, 0, len(models)/boardModelHeaderSize)
	for len(models) > 0 {
		if len(models) < boardModelHeaderSize {
			return nil, nil, fmt.Errorf("truncated board model point")
		}
		size := binary.BigEndian.Uint32(models[boardPointSize:boardModelHeaderSize])
		if uint64(size) > uint64(len(models)-boardModelHeaderSize) {
			return nil, nil, fmt.Errorf("truncated board model name")
		}
		end := boardModelHeaderSize + int(size)
		point, err := decodeBoardPoint(models, zones)
		if err != nil {
			return nil, nil, err
		}
		modelPoints = append(modelPoints, BoardModelPoint{Model: string(models[boardModelHeaderSize:end]), BoardPoint: point})
		models = models[end:]
	}
	return trendPoints, modelPoints, nil
}

func decodeBoardPoint(row []byte, zones map[int]*time.Location) (BoardPoint, error) {
	micros := int64(binary.BigEndian.Uint64(row[:8]))
	if micros == math.MinInt64 || micros > math.MaxInt64-postgresEpochMicros {
		return BoardPoint{}, fmt.Errorf("board bucket is outside the supported timestamp range")
	}
	offset := int(int32(binary.BigEndian.Uint32(row[8:12])))
	zone := zones[offset]
	if zone == nil {
		zone = time.FixedZone("", offset)
		zones[offset] = zone
	}
	return BoardPoint{
		Bucket:   time.UnixMicro(micros + postgresEpochMicros).In(zone),
		Requests: int64(binary.BigEndian.Uint64(row[12:20])),
		Tokens:   int64(binary.BigEndian.Uint64(row[20:28])),
		Credits:  int64(binary.BigEndian.Uint64(row[28:36])),
		CostUSD:  math.Float64frombits(binary.BigEndian.Uint64(row[36:44])),
	}, nil
}
