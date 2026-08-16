#!/usr/bin/env python3
import ast
from pathlib import Path


repo_root = Path(__file__).resolve().parents[1]
script = (repo_root / "scripts/ops/verify-245-full.sh").read_text()
start_marker = "ssh \"${SSH_OPTS[@]}\" \"$SSH_HOST\" 'python3 -' <<'PY'\n"

assert start_marker in script, "remote Python must use a quoted local heredoc"
python_source = script.split(start_marker, 1)[1].split("\nPY\n", 1)[0]
tree = ast.parse(python_source, filename="verify-245-full.sh heredoc")
compile(tree, "verify-245-full.sh heredoc", "exec")

required_keys = {
    "LLM_GATEWAY_CENTER_URL",
    "OPS_COLLECT_URL",
    "OPS_NODE_REGION",
    "OPS_COLLECT_LICENSE_KEY",
}
string_env_keys = {
    call.args[0].value
    for call in ast.walk(tree)
    if isinstance(call, ast.Call)
    and isinstance(call.func, ast.Attribute)
    and isinstance(call.func.value, ast.Name)
    and call.func.value.id == "env"
    and call.func.attr == "get"
    and call.args
    and isinstance(call.args[0], ast.Constant)
    and isinstance(call.args[0].value, str)
}

missing = required_keys - string_env_keys
assert not missing, f"env.get keys must remain Python strings: {sorted(missing)}"
print("PASS: remote Python heredoc compiles and env keys remain strings")
