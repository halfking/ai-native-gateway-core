set -e
PID=$(podman inspect -f '{{.State.Pid}}' pg-252-pg17)
echo "container_pid=$PID"
echo "--- A: podman exec (current path) ---"
time podman exec -e PGPAGER=cat pg-252-pg17 psql -U postgres -d llm_gateway -X -q -A -t -c 'select 1'
echo "--- B: nsenter (bypass podman) ---"
time nsenter -t $PID -m -u -i -n -p -- psql -U postgres -d llm_gateway -X -q -A -t -c 'select 1'
echo "__D252SH_RC__=0"