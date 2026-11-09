package v2

import (
	"context"
	"errors"
)

const boundedGovernanceScript = `-- governance-cache-bounded-read
local kind=redis.call('TYPE',KEYS[1]).ok
if kind == 'none' then return {} end
if kind ~= 'hash' then return redis.error_reply('WRONGTYPE governance cache requires hash') end
local fields={'injection_verdict','output_verdict','audited_at','sensitive'}
local total=0
for _,field in ipairs(fields) do
 total=total+redis.call('HSTRLEN',KEYS[1],field)
 if total>tonumber(ARGV[1]) then return redis.error_reply('governance metadata exceeds bounded read capacity') end
end
return redis.call('HMGET',KEYS[1],unpack(fields))`

func (r *RedisGovernanceCache) getBounded(ctx context.Context, key string) (map[string]string, error) {
	result, err := r.client.Eval(ctx, boundedGovernanceScript, []string{key}, strictColdMetaBytes).Slice()
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, nil
	}
	fields := []string{"injection_verdict", "output_verdict", "audited_at", "sensitive"}
	if len(result) != len(fields) {
		return nil, errors.New("invalid bounded governance cache result")
	}
	values := make(map[string]string, len(fields))
	for i, value := range result {
		if value == nil {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return nil, errors.New("invalid bounded governance cache field")
		}
		values[fields[i]] = text
	}
	return values, nil
}
