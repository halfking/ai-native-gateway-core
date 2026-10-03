mkdir -p /tmp/d252t
printf 'hello %s\n' "$(date +%T)" > /tmp/d252t/probe.txt
cat > /tmp/d252t/run.sh <<'EOS'
#!/bin/sh
sleep 45
echo FINISHED > /tmp/d252t/probe.txt
EOS
chmod +x /tmp/d252t/run.sh
setsid /tmp/d252t/run.sh </dev/null >/dev/null 2>&1 &
echo "launched, returning immediately"
date +%T