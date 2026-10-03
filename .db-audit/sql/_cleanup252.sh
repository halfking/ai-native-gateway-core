# 清理审计超时可能残留在 252 的临时目录（宿主机 + 容器内）
echo "--- before ---"
ls -d /tmp/d252* 2>/dev/null || echo "host: none"
rm -rf /tmp/d252-* /tmp/d252dump-* 2>/dev/null || true
podman exec pg-252-pg17 sh -c 'rm -rf /tmp/d252-* /tmp/d252dump-* 2>/dev/null' || true
echo "--- after ---"
ls -d /tmp/d252* 2>/dev/null || echo "host: none"
podman exec pg-252-pg17 sh -c 'ls -d /tmp/d252* 2>/dev/null || echo "container: none"'
echo "--- load now ---"
cat /proc/loadavg
echo "__D252SH_RC__=0"