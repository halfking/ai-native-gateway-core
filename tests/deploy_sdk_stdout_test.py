#!/usr/bin/env python3
"""Run the actual SDK resolver with a noisy recoverable-delete shim."""
import pathlib
import shutil
import subprocess
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]


def _find_bash():
    """Locate a bash interpreter.

    `subprocess.run(['bash', ...])` resolves 'bash' through PATH. On Windows the
    Git-for-Windows bash is `bash.exe` and is usually NOT on PATH, so the call
    fails with FileNotFoundError before the assertion under test is ever
    reached. Resolve an explicit interpreter here instead of relying on PATH.
    """
    found = shutil.which('bash')
    if found:
        return found
    for candidate in (
        r'C:\Program Files\Git\bin\bash.exe',
        r'C:\Program Files\Git\usr\bin\bash.exe',
        '/bin/bash',
        '/usr/bin/bash',
    ):
        if pathlib.Path(candidate).is_file():
            return candidate
    return None


BASH = _find_bash()


class SDKStdoutContract(unittest.TestCase):
    def test_resolver_stdout_contains_only_sdk_path(self):
        # encoding 必须显式指定：read_text() 的缺省编码取自 locale，在非 UTF-8
        # locale（Windows 中文默认 cp936 / GBK）下会 UnicodeDecodeError。
        source = (ROOT / 'scripts/deploy-local.sh').read_text(encoding='utf-8')
        start = source.index('dl_resolve_sdkroot() {')
        end = source.index('\n}\n', start) + 3
        resolver = source[start:end]
        work = ROOT / '.audit-validation' / 'sdk-stdout'
        work.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(dir=work) as probe:
            script = resolver + r'''
uname() { printf 'Darwin\n'; }
ls() { printf 'MacOSX26.5.sdk\n'; }
clang() { return 0; }
rm() { printf 'mavis-trash: moved to trash\n'; }
mktemp() { printf '%s\n' "$TEST_PROBE"; }
unset DL_HOST_SDKROOT
value=$(dl_resolve_sdkroot)
rc=$?
printf '%s\n' "$value"
exit "$rc"
'''
            import os
            env = dict(os.environ, TEST_PROBE=probe)
            result = subprocess.run([BASH, '-c', script], env=env, text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.strip(), '/Library/Developer/CommandLineTools/SDKs/MacOSX26.5.sdk')
            self.assertIn('mavis-trash:', result.stderr)


# ★ 这道门断言的是 **macOS 专属** 的 SDK 解析路径
#   （/Library/Developer/CommandLineTools/SDKs 与 /Applications/Xcode.app/...）：
#   resolver 用 `[[ -d "$base" ]]` 逐个探测这两个真实目录 ⇒ 它们不在，循环走完
#   就是 `return 1` ⇒ 在非 macOS 主机上必然红。
#   而 shim 覆盖的是 uname/ls/clang/mktemp/rm，**覆盖不到 `[[ -d ]]` 目录判定**
#   ⇒ 这不是「测试写错了」，而是「这道门本来就是 macOS 专属」。
#   （断言逻辑本身已实测为正确：把 resolver 的两个 base 路径改写到可创建的临时
#    目录后，stdout 恰是 SDK 路径、trash 日志恰在 stderr。）
# ⇒ 非 macOS 上显式跳过并说明原因，而不是留一个「在任何非 macOS 机器上必红」的门。
if not pathlib.Path('/Library/Developer/CommandLineTools/SDKs').is_dir():
    SDKStdoutContract = unittest.skip(
        'macOS-only: resolver probes /Library/Developer/CommandLineTools/SDKs, '
        'which does not exist on this host'
    )(SDKStdoutContract)


# 没有可用 bash 时**显式跳过**并说明原因，而不是让它崩成 FileNotFoundError ——
# 崩在启动器里看不出「这是环境限制」，而「跳过了」才是诚实的读数。
# 判据本身（resolver 的 stdout 只含 SDK 路径）在有 bash 的机器上照常跑。
if BASH is None:
    SDKStdoutContract = unittest.skip('no bash interpreter available on PATH')(
        SDKStdoutContract
    )


if __name__ == '__main__':
    unittest.main()
