package ursm

import "github.com/redis/go-redis/v9"

// recordStateScript atomically reads, judges, and writes node state.
// Maintains two Redis keys in a single atomic call:
//
//	KEYS[1] = ursm:node:{credID}:{stdModel}   (node state HASH)
//	KEYS[2] = ursm:model:{stdModel}            (model index HASH)
//
// ARGV[1] = kind ("success" | "failure")
// ARGV[2] = error_kind (empty for success)
// ARGV[3] = now (unix seconds)
// ARGV[4] = latency_ms
// ARGV[5] = fail_threshold (default 2)
// ARGV[6] = cooldown_seconds (default 300)
// ARGV[7] = raw_model        (供应商 API 调用用)
// ARGV[8] = provider_id      (路由评分用)
// ARGV[9] = fp_slot_limit    (路由评分用)
// ARGV[10] = conc_limit      (路由评分用)
//
// Returns: {stateChanged, transitionReason, consecutiveFail, fromState, toState}
var recordStateScript = redis.NewScript(`
	local key = KEYS[1]
	local idx_key = KEYS[2]
	local kind = ARGV[1]
	local error_kind = ARGV[2]
	local now = tonumber(ARGV[3])
	local latency_ms = tonumber(ARGV[4])
	local fail_threshold = tonumber(ARGV[5])
	local cooldown = tonumber(ARGV[6])
	local raw_model = ARGV[7]
	local provider_id = ARGV[8]
	local fp_slot_limit = ARGV[9]
	local conc_limit = ARGV[10]

	local raw = redis.call('HGETALL', key)
	local state = {}
	for i = 1, #raw, 2 do
		state[raw[i]] = raw[i+1]
	end

	local available = state['available']
	local consecutive_fail = tonumber(state['consecutive_fail'] or '0')
	local disabled_until = tonumber(state['disabled_until'] or '0')
	local latency_avg = tonumber(state['latency_avg_ms'] or '0')

	if disabled_until > 0 and now >= disabled_until then
		available = '1'
		consecutive_fail = 0
		disabled_until = 0
	end

	local state_changed = false
	local from_state = available

	if kind == 'success' then
		consecutive_fail = 0
		available = '1'
		disabled_until = 0
		state['last_success_at'] = tostring(now)
		if from_state == '0' then
			state_changed = true
			state['transition_reason'] = 'request_recovered'
		end
	elseif kind == 'failure' then
		consecutive_fail = consecutive_fail + 1
		state['last_failure_at'] = tostring(now)
		state['last_error'] = error_kind
		if consecutive_fail >= fail_threshold then
			available = '0'
			disabled_until = now + cooldown
			if from_state == '1' then
				state_changed = true
				state['transition_reason'] = 'consecutive_' .. fail_threshold .. '_failures'
			end
		end
	end

	if latency_ms > 0 then
		if latency_avg == 0 then
			latency_avg = latency_ms
		else
			latency_avg = math.floor((latency_avg * 3 + latency_ms) / 4)
		end
		state['latency_avg_ms'] = tostring(latency_avg)
	end

	state['available'] = available
	state['consecutive_fail'] = tostring(consecutive_fail)
	state['disabled_until'] = tostring(disabled_until)
	state['updated_at'] = tostring(now)

	state['raw_model'] = raw_model
	state['provider_id'] = provider_id
	state['fp_slot_limit'] = fp_slot_limit
	state['concurrency_limit'] = conc_limit

	local args = {}
	for k, v in pairs(state) do
		table.insert(args, k)
		table.insert(args, v)
	end
	redis.call('HMSET', key, unpack(args))
	redis.call('EXPIRE', key, 604800)

	local cred_id = string.match(key, '^ursm:node:(%d+):')
	if cred_id then
		redis.call('HSET', idx_key, cred_id, raw_model)
		redis.call('EXPIRE', idx_key, 604800)
	end

	return {tostring(state_changed), state['transition_reason'] or '', tostring(consecutive_fail), from_state, available}
`)
