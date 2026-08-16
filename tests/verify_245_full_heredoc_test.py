#!/usr/bin/env python3
import ast
from pathlib import Path


repo_root = Path(__file__).resolve().parents[1]
script = (repo_root / "scripts/ops/verify-245-full.sh").read_text()
start_marker = (
    '  "EXPECTED_BUILD_SEQ=$EXPECTED_BUILD_SEQ '
    'EXPECTED_GIT_SHA=$EXPECTED_GIT_SHA python3 -" <<\'PY\'\n'
)

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

environment_keys = {
    node.slice.value
    for node in ast.walk(tree)
    if isinstance(node, ast.Subscript)
    and isinstance(node.value, ast.Attribute)
    and isinstance(node.value.value, ast.Name)
    and node.value.value.id == "os"
    and node.value.attr == "environ"
    and isinstance(node.slice, ast.Constant)
    and isinstance(node.slice.value, str)
}
assert {"EXPECTED_BUILD_SEQ", "EXPECTED_GIT_SHA"} <= environment_keys

version_keys = {
    call.args[0].value
    for call in ast.walk(tree)
    if isinstance(call, ast.Call)
    and isinstance(call.func, ast.Attribute)
    and call.func.attr == "get"
    and call.args
    and isinstance(call.args[0], ast.Constant)
    and isinstance(call.args[0].value, str)
}
assert {"build_seq", "git_sha"} <= version_keys

version_match_function = next(
    node
    for node in tree.body
    if isinstance(node, ast.FunctionDef) and node.name == "version_matches"
)
namespace = {"expected_build_seq": 1572, "expected_git_sha": "46e6b841"}
exec(
    compile(ast.Module(body=[version_match_function], type_ignores=[]), "version_matches", "exec"),
    namespace,
)
version_matches = namespace["version_matches"]
assert version_matches({"build_seq": 1572, "git_sha": "46e6b841"})
assert not version_matches({"build_seq": 1571, "git_sha": "46e6b841"})
assert not version_matches({"build_seq": 1572, "git_sha": "wrong-sha"})

assert "'\"'\"'" not in python_source, "remote Python must not contain shell quote artifacts"
print("PASS: remote Python heredoc compiles and rejects unexpected versions")
