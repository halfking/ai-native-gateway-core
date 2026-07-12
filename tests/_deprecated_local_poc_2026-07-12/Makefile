# tests/local/Makefile — 便捷命令入口
#
# 用法：
#   make help          # 列出所有命令
#   make up            # 拉起基础设施（postgres + redis）
#   make seed          # 灌入测试数据
#   make scenario-01   # 基线验证（无开关，期望 200）
#   make scenario-02   # kill-switch 验证（关 session_cache，观察残留错误）
#   make scenario-03   # 高并发压测（k6 spike + soak）
#   make down          # 关停
#   make logs          # 看 gateway 日志

ENV_FILE ?= .env
include $(ENV_FILE)
export

POSTGRES_URL ?= postgres://llm_gateway:local_password@localhost:55432/llm_gateway?sslmode=disable
REDIS_URL    ?= redis://localhost:56379
GATEWAY_URL  ?= http://localhost:58781
TEST_API_KEY ?= sk-loc-1234567890abcdef

.DEFAULT_GOAL := help

help:
	@grep -E '^[a-zA-Z_-]+:' Makefile | sed 's/:.*//; s/^/  /' | sort
	@echo ""
	@echo "Examples:"
	@echo "  KILL_SESSION_CACHE=1 make up baseline"
	@echo "  make spike RPS=200 DUR=30s"

up:
	docker compose -f docker-compose.yml up -d postgres redis
	@echo "Waiting for services..."
	@./scripts/wait-healthy.sh postgres redis
	@echo "Run \`make seed\` to insert test data."

seed:
	docker compose -f docker-compose.yml exec -T postgres \
	    psql -U llm_gateway -d llm_gateway -f /docker-entrypoint-initdb.d/01-schema-bootstrap.sql 2>&1 | tail -5 || \
	    PGPASSWORD=local_password psql -h localhost -p 55432 -U llm_gateway -d llm_gateway \
	        -f seed/01-schema-bootstrap.sql | tail -5

down:
	docker compose -f docker-compose.yml down

restart:
	docker compose -f docker-compose.yml restart

ps:
	docker compose -f docker-compose.yml ps

# 启动 gateway（带 mock upstream）
gateway-up:
	docker compose -f docker-compose.yml --profile with-mock up -d mock-llm gateway
	./scripts/wait-healthy.sh gateway

gateway-down:
	docker compose -f docker-compose.yml stop gateway mock-llm

# 旁路启动 gateway 直接使用宿主机的 binary（适合调试）
gateway-local:
	KILL_SESSION_COMPRESSION=$(KILL_SESSION_COMPRESSION) \
	KILL_SESSION_CACHE=$(KILL_SESSION_CACHE) \
	KILL_CIRCUIT_DEGRADATION=$(KILL_CIRCUIT_DEGRADATION) \
	KILL_RATE_LIMITER=$(KILL_RATE_LIMITER) \
	KILL_FP_SLOT=$(KILL_FP_SLOT) \
	LLM_GATEWAY_DATABASE_URL=$(POSTGRES_URL) \
	LLM_GATEWAY_REDIS_ADDR=localhost:56379 \
	LLM_GATEWAY_LISTEN_ADDR=0.0.0.0:8781 \
	go run ./cmd/gateway

gateway-logs:
	docker compose -f docker-compose.yml logs -f gateway

db-shell:
	docker compose -f docker-compose.yml exec postgres psql -U llm_gateway -d llm_gateway

redis-shell:
	docker compose -f docker-compose.yml exec redis redis-cli

# 场景快捷入口
scenario-01:
	@./scenarios/01-baseline.sh

scenario-02:
	@./scenarios/02-killswitch.sh

scenario-03:
	@./scenarios/03-high-concurrency.sh

# 单调用工具
ping:
	curl -sS -m 5 $(GATEWAY_URL)/healthz | head -5

chat:
	curl -sS -m 30 $(GATEWAY_URL)/v1/chat/completions \
	  -H "Authorization: Bearer $(TEST_API_KEY)" \
	  -H "Content-Type: application/json" \
	  -d '{"model":"minimax-m3","messages":[{"role":"user","content":"hello"}],"max_tokens":15}' \
	  | python3 -m json.tool 2>&1 | head -25
