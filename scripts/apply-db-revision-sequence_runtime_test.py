#!/usr/bin/env python3
"""Bounded runner transport/name/provenance fixtures; no database credentials.

Uses a stateful psql double, not a SQL engine. Real PG replay is separate.
Artifacts stay under workspace .audit-validation for inspection.
"""
import hashlib
import json
import os
from pathlib import Path
from urllib.parse import urlparse
import re
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / 'scripts/apply-db-revision-sequence.sh'
# encoding 必须显式指定：read_text() 的缺省编码取自 locale，在非 UTF-8 locale
# （Windows 中文默认 cp936 / GBK）下解不出仓库脚本里的 UTF-8 中文 ⇒
# UnicodeDecodeError ⇒ 这道门在任何非 UTF-8 机器上必然崩（而不是红在判据上）。
SOURCE = SCRIPT.read_text(encoding='utf-8')
PREFIX = 'session-summary-and-integrity-2026-09:'

SHIM = r'''import json, os, re, sys
from pathlib import Path
state_path = Path(os.environ['FIXTURE_STATE'])
s = json.loads(state_path.read_text())
args = sys.argv[1:]
if '-Atqc' in args:
    q = args[args.index('-Atqc')+1]
    if 'WITH family(name)' in q:
        print(s['generation'])
    elif 'SELECT CASE' in q:
        print('canonical')
    elif 'SELECT content_sha256' in q:
        m = re.search("sequence_name='([^']+)'", q).group(1)
        print(s['ledger'].get(m, {}).get('source') or '')
    elif 'SELECT 1 FROM public.gateway_db_revision_sequences' in q:
        m = re.search("sequence_name='([^']+)'", q).group(1)
        if m in s['ledger']: print('1')
    elif q.startswith('INSERT INTO public.gateway_db_revision_sequences'):
        m, source, execution, adapter = re.search(r"VALUES \('([^']+)', '([^']+)', '([^']+)', '([^']+)'\)", q).groups()
        s['ledger'].setdefault(m, dict(source=source, execution=execution, adapter=adapter))
    elif q.startswith('UPDATE public.gateway_db_revision_sequences'):
        source, execution, adapter, m = re.search(r"content_sha256='([^']+)', execution_sha256='([^']+)', runtime_adaptation='([^']+)' WHERE sequence_name='([^']+)'", q).groups()
        s['ledger'][m] = dict(source=source, execution=execution, adapter=adapter)
else:
    sql = sys.stdin.read()
    if 'runner_fixture_fail' in sql:
        sys.exit(3)
    if 'fixture860' in sql:
        # Rename probes must not be rewritten, even on a renamed database.
        assert "to_regclass('public.request_logs')" in sql
        assert "to_regclass('public._request_logs')" in sql
        s['generation'] = 'renamed'
    else:
        renamed = s['generation'] == 'renamed'
        expected = '_request_logs' if renamed else 'request_logs'
        assert 'ALTER TABLE public.' + expected + ' ADD' in sql, sql
        for token in ['promote_request_logs_hot_to_partition', 'request_logs_with_current_month',
                      'request_logs_2026_10', 'request_logs_default', 'request_logs_',
                      'request_logs$custom', 'érequest_logs', '_request_logs_hot']:
            assert token in sql, token
        assert '__request_logs' not in sql
    s['executed'].append(sql)
state_path.write_text(json.dumps(s))
'''

SQL = '''-- lexical fixture, not a parsed-semantic claim
ALTER TABLE public.request_logs ADD COLUMN fixture integer;
SELECT 'request_logs_hot', 'request_logs_bodies', 'request_logs_bodies_hot',
       'request_logs_archive', 'request_logs_id_seq', 'public.request_logs';
SELECT 'promote_request_logs_hot_to_partition', 'request_logs_with_current_month',
       'request_logs_2026_10', 'request_logs_default', 'request_logs_',
       'request_logs$custom', 'érequest_logs', '_request_logs_hot';
DO $body$ BEGIN PERFORM 'request_logs'; END $body$;
'''
RENAME = "-- fixture860\nSELECT to_regclass('public.request_logs'), to_regclass('public._request_logs');\n"

# 这道门在设计上就依赖 POSIX 语义，无法在 Windows 上运行：
#   ① fixture 用 symlink 把 bash/psql/python3 等可执行文件放进一个「封闭 PATH」目录，
#      而 Windows 的 symlink 创建需要 SeCreateSymbolicLinkPrivilege（管理员或开发者模式）
#      ⇒ 未启用时抛 OSError [WinError 1314]；
#   ② 被测脚本以 '/bin/bash' 绝对路径启动，Windows 上没有这个路径。
# ⇒ 在不满足前置条件时**显式跳过**并说明原因，而不是让它崩成 10 个 ERROR ——
#    崩在 setup 里看不出「这是环境限制」，而「跳过了」才是诚实的读数。
#    判据本身（投递路径契约）在 scripts/apply-db-revision-sequence_test.sh 里，
#    那部分跨平台可用，不受此跳过影响。
#
# ★ 判据用「**实测能力**」而不是「平台名」：
#   平台名（os.name / sys.platform）会把「Windows 开发者模式已开、symlink 可用」
#   这种真实可跑的情况误判成不可跑；而 macOS/Linux 恒满足。
#   ⇒ 这里直接探测「能否创建 symlink」＋「有没有 /bin/bash」，两个条件都是这道门
#     真正依赖的东西，探测本身零副作用（在临时目录里建一个链接再删掉）。
_POSIX_ONLY_REASON = 'requires POSIX symlinks + /bin/bash (symlink creation or /bin/bash unavailable)'


def _posix_runtime_available():
    """True iff this host can actually run this gate's fixture."""
    if not os.path.isfile('/bin/bash'):
        return False
    probe_dir = tempfile.mkdtemp(prefix='symlink-probe-')
    try:
        target = Path(probe_dir) / 'target'
        link = Path(probe_dir) / 'link'
        target.write_text('probe')
        try:
            link.symlink_to(target)
        except (OSError, NotImplementedError, AttributeError):
            return False
        return True
    finally:
        shutil.rmtree(probe_dir, ignore_errors=True)


class RuntimeFixture(unittest.TestCase):
    def fixture(self, transport, generation):
        artifacts = ROOT / '.audit-validation'
        artifacts.mkdir(exist_ok=True)
        root = Path(tempfile.mkdtemp(prefix='revision-runtime-', dir=artifacts))
        (root / 'scripts').mkdir()
        migrations = root / 'sql/migrations/startup'
        migrations.mkdir(parents=True)
        names = ['001_fixture.sql', '860_rename_request_logs_family_pending_delete.sql',
                 '543_request_logs_discard_events.sql']
        for name, text in zip(names, [SQL, RENAME, SQL]):
            (migrations / name).write_text(text)
        body = re.sub(r'^files=\(.*?^\)', 'files=(\n' + '\n'.join(
            '  "$ROOT_DIR/sql/migrations/startup/' + n + '"' for n in names) + '\n)',
            SOURCE, count=1, flags=re.M | re.S)
        body = re.sub(r'^intentional_function_chains=\(.*?^\)', 'intentional_function_chains=()',
                      body, count=1, flags=re.M | re.S)
        body = re.sub(r'^legacy_content_replays=\(.*?^\)', 'legacy_content_replays=(\'\')',
                      body, count=1, flags=re.M | re.S)
        (root / 'scripts/apply-db-revision-sequence.sh').write_text(body)
        state = root / 'state.json'
        state.write_text(json.dumps(dict(generation=generation, ledger={}, executed=[])))
        bin_dir = root / 'bin'
        bin_dir.mkdir()
        # A closed PATH forces Docker fallback even if host psql is installed.
        for command in ['bash', 'dirname', 'basename', 'awk', 'grep', 'sed', 'cat',
                        'cut', 'sha256sum', 'shasum', 'python3']:
            executable = shutil.which(command)
            if executable:
                (bin_dir / command).symlink_to(executable)
        shim = root / 'shim.py'
        shim.write_text(SHIM)
        launcher = '#!/bin/bash\nexec ' + shutil.which('python3') + ' ' + str(shim) + ' "$@"\n'
        (bin_dir / transport).write_text(launcher)
        (bin_dir / transport).chmod(0o755)
        env = dict(os.environ, PATH=str(bin_dir), DATABASE_URL='fixture-not-a-real-dsn',
                   FIXTURE_STATE=str(state), LLM_GATEWAY_PG_CONTAINER='fixture-container',
                   LLM_GATEWAY_PG_PASSWORD='fixture-password')
        return root, state, env, names

    def run_fixture(self, root, env, expected=0):
        result = subprocess.run(['/bin/bash', str(root / 'scripts/apply-db-revision-sequence.sh')],
                                env=env, capture_output=True, text=True)
        self.assertEqual(result.returncode, expected, result.stdout + result.stderr)
        return result

    def test_transports_generations_provenance_and_idempotency(self):
        for transport in ['psql', 'docker']:
            for generation in ['legacy', 'renamed']:
                with self.subTest(transport=transport, generation=generation):
                    root, state, env, names = self.fixture(transport, generation)
                    self.run_fixture(root, env)
                    s = json.loads(state.read_text())
                    self.assertEqual(len(s['executed']), 3)
                    for name, delivered in zip(names, s['executed']):
                        row = s['ledger'][PREFIX + name]
                        raw = (root / 'sql/migrations/startup' / name).read_bytes()
                        self.assertEqual(row['source'], hashlib.sha256(raw).hexdigest())
                        self.assertEqual(row['execution'], hashlib.sha256(delivered.encode()).hexdigest())
                    last = s['ledger'][PREFIX + names[-1]]
                    self.assertEqual(last['adapter'], 'request-logs-physical-v1')
                    self.assertNotEqual(last['source'], last['execution'])
                    self.assertEqual(s['ledger'][PREFIX + names[1]]['adapter'], 'none')
                    self.run_fixture(root, env)
                    self.assertEqual(s, json.loads(state.read_text()))
                    # Source changes replay once, storing SOURCE hash, not delivered hash.
                    file = root / 'sql/migrations/startup' / names[-1]
                    file.write_text(SQL + '-- replay bytes\n')
                    self.run_fixture(root, env)
                    s = json.loads(state.read_text())
                    self.assertEqual(len(s['executed']), 4)
                    self.assertEqual(s['ledger'][PREFIX + names[-1]]['source'],
                                     hashlib.sha256(file.read_bytes()).hexdigest())
                    self.run_fixture(root, env)
                    self.assertEqual(s, json.loads(state.read_text()))

    def test_failed_delivery_has_no_marker(self):
        for transport in ['psql', 'docker']:
            root, state, env, names = self.fixture(transport, 'renamed')
            (root / 'sql/migrations/startup' / names[-1]).write_text(SQL + 'SELECT runner_fixture_fail;\n')
            self.run_fixture(root, env, expected=3)
            s = json.loads(state.read_text())
            self.assertNotIn(PREFIX + names[-1], s['ledger'])
            self.assertEqual(len(s['executed']), 2)

    def test_ambiguous_or_mixed_refuses_before_ledger_mutation(self):
        for transport in ['psql', 'docker']:
            for generation in ['ambiguous', 'mixed']:
                root, state, env, _ = self.fixture(transport, generation)
                before = state.read_bytes()
                self.run_fixture(root, env, expected=4)
                self.assertEqual(before, state.read_bytes())

    def test_legacy_marker_does_not_invent_execution_provenance(self):
        root, state, env, names = self.fixture('psql', 'renamed')
        s = json.loads(state.read_text())
        s['ledger'][PREFIX + names[-1]] = dict(source=None)
        state.write_text(json.dumps(s))
        self.run_fixture(root, env)
        s = json.loads(state.read_text())
        self.assertEqual(s['ledger'][PREFIX + names[-1]], dict(source=None))
        self.assertEqual(len(s['executed']), 2)

    def test_bootstrap_dependencies_are_unique_and_ordered(self):
        block = re.search(r'^files=\(.*?^\)', SOURCE, re.M | re.S).group()
        names = re.findall(r'"\$ROOT_DIR/[^"\n]+/([^/"\n]+\.sql)"', block)
        dependencies = [
            ('683b_session_dim_table_prelude.sql', '357_session_analytics_aggregation_views.sql'),
            ('392_candidate_failure_logs_monthly_partition.sql', '617_candidate_failure_logs_hot_contract.sql'),
            ('617_candidate_failure_logs_hot_contract.sql', '627_candidate_failure_logs_aggregation_id_unified.sql'),
            ('803_candidate_failure_logs_hot_column_reconcile.sql', '627_candidate_failure_logs_aggregation_id_unified.sql'),
            ('627_candidate_failure_logs_aggregation_id_unified.sql', '861_candidate_failure_view_absorb_session_id.sql'),
        ]
        for before, after in dependencies:
            self.assertEqual(names.count(before), 1, before)
            self.assertEqual(names.count(after), 1, after)
            self.assertLess(names.index(before), names.index(after), (before, after))

    def test_708_empty_backfill_guard_is_bounded_and_locks_first(self):
        root, _, _, _ = self.fixture('psql', 'renamed')
        file = root / '708_session_bodies_s1a.sql'
        raw = (ROOT / 'sql/migrations/startup' / file.name).read_bytes()
        file.write_bytes(raw)
        helper = SOURCE[SOURCE.index('request_logs_sql() {'):SOURCE.index('prepare_runtime_sql() {')]
        cmd = ['/bin/bash', '-c', helper + '\nruntime_adaptation=none+708-empty-backfill-v1\nrequest_logs_sql "$1"', '_', str(file)]
        result = subprocess.run(cmd, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        text = result.stdout.decode()
        self.assertIn('LOCK TABLE public.%I IN SHARE ROW EXCLUSIVE MODE', text)
        lock = text.index('LOCK TABLE public.%I IN SHARE ROW EXCLUSIVE MODE')
        probe = text.index('SELECT EXISTS (SELECT 1 FROM public.%I WHERE turn_no > 0 AND kind = %L)')
        skip = text.index('IF NOT part_needs_backfill THEN CONTINUE; END IF;')
        update = text.index('UPDATE public.%I SET kind = %L WHERE turn_no > 0 AND kind = %L')
        self.assertLess(lock, probe)
        self.assertLess(probe, skip)
        self.assertLess(skip, update)
        # The remainder (hot UPDATE, indexes, promote and old-column cleanup)
        # must remain byte-identical; do not silently retire historical work.
        tail = b'-- hot \xe8\xa1\xa8'
        self.assertEqual(result.stdout[result.stdout.index(tail):], raw[raw.index(tail):])
        file.write_bytes(raw.replace(b'    part RECORD;', b'    part RECORD; -- drift'))
        result = subprocess.run(cmd, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(b'historical anchors drifted', result.stderr)
        self.assertEqual(result.stdout, b'')

    def test_811_maps_only_derived_insert_parent_not_child_probe(self):
        root, _, _, _ = self.fixture('psql', 'renamed')
        file = root / '811_partition_bounds_shanghai_midnight_repair.sql'
        raw = (ROOT / 'sql/migrations/startup' / file.name).read_bytes()
        file.write_bytes(raw)
        helper = SOURCE[SOURCE.index('request_logs_sql() {'):SOURCE.index('prepare_runtime_sql() {')]
        cmd = ['/bin/bash', '-c', helper + '\nruntime_adaptation=request-logs-physical-v1+811-derived-parent-v1\nrequest_logs_sql "$1"', '_', str(file)]
        result = subprocess.run(cmd, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        text = result.stdout.decode()
        self.assertEqual(text.count("WHEN 'request' || '_logs' THEN '_request_logs'"), 1)
        start, end = raw.index(b'        IF v_beyond > 0 THEN'), raw.index(b"        EXECUTE format('INSERT INTO public.%I")
        self.assertEqual(result.stdout[result.stdout.index(b'        IF v_beyond > 0 THEN'):result.stdout.index(b"        EXECUTE format('INSERT INTO public.%I")], raw[start:end])
        self.assertIn(b'GET DIAGNOSTICS v_moved = ROW_COUNT;', result.stdout)
        self.assertIn(b'IF v_moved <> v_bak_rows THEN', result.stdout)
        file.write_bytes(raw.replace(b"''), r.bak);", b"''), r.bak /* drift */);"))
        result = subprocess.run(cmd, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(b'historical anchor drifted', result.stderr)
        self.assertEqual(result.stdout, b'')

    @unittest.skipUnless(os.environ.get('LLM_REVISION_RUNTIME_LIVE') == '1',
                         'opt-in local PG fixture (LLM_REVISION_RUNTIME_LIVE=1)')
    def test_live_local_pg_name_generations_and_view_tail(self):
        dsn = os.environ['DATABASE_URL']
        target = urlparse(dsn)
        self.assertIn(target.hostname, ['127.0.0.1', 'localhost'])
        self.assertEqual(target.port, 54318, 'only the assigned LOCAL PG is authorized')
        adapter = SOURCE[SOURCE.index('request_logs_sql() {'):SOURCE.index('prepare_runtime_sql() {')]
        view_helper = SOURCE[SOURCE.index('repair_request_logs_view_tail() {'):SOURCE.index('\nfor file in "${files[@]}"; do\n  [[ -f "$file" ]]')]
        # Capture exactly the SQL used by the runner; isolate public -> pg_temp
        # so no fixture object touches existing application tables or markers.
        view_sql = subprocess.run(['/bin/bash', '-c', 'psql_query() { printf "%s" "$1"; }\n' +
                                   view_helper + '\nrepair_request_logs_view_tail'],
                                  capture_output=True, text=True, check=True).stdout.replace('public.', 'pg_temp.').replace("table_schema='public'", "table_schema LIKE 'pg_temp_%'")
        for generation in ['legacy', 'renamed']:
            with self.subTest(generation=generation):
                root, _, _, _ = self.fixture('psql', generation)
                file = root / 'live.sql'
                old_sql = '''ALTER TABLE pg_temp.request_logs ADD COLUMN discard_events jsonb;
ALTER TABLE pg_temp.request_logs_hot ADD COLUMN discard_events jsonb;
ALTER TABLE pg_temp.request_logs_bodies ADD COLUMN fixture integer;
ALTER TABLE pg_temp.request_logs_bodies_hot ADD COLUMN fixture integer;
ALTER TABLE pg_temp.request_logs_archive ADD COLUMN fixture integer;
SELECT nextval('pg_temp.request_logs_id_seq');
CREATE INDEX idx_request_logs_hot_fixture ON pg_temp.request_logs_hot(request_id);
CREATE FUNCTION pg_temp.promote_request_logs_hot_to_partition() RETURNS bigint LANGUAGE sql
AS $$ SELECT count(*) FROM pg_temp.request_logs_hot $$;
'''
                file.write_text(old_sql)
                mode = 'none' if generation == 'legacy' else 'request-logs-physical-v1'
                delivered = subprocess.run(['/bin/bash', '-c', adapter + '\nruntime_adaptation=' + mode +
                                            '\nrequest_logs_sql "$1"', '_', str(file)],
                                           capture_output=True, text=True, check=True).stdout
                prefix = '' if generation == 'legacy' else '_'
                parent, hot = prefix + 'request_logs', prefix + 'request_logs_hot'
                setup = 'BEGIN;\n'
                for table in [parent, hot]:
                    setup += f'CREATE TEMP TABLE {table}(request_id text, ts timestamptz, is_final_success boolean, system_fingerprint text, raw_model_name text);\n'
                for suffix in ['_bodies', '_bodies_hot', '_archive']:
                    setup += f'CREATE TEMP TABLE {prefix}request_logs{suffix}(id integer);\n'
                setup += f'CREATE TEMP SEQUENCE {prefix}request_logs_id_seq;\n'
                setup += f"INSERT INTO {hot} VALUES ('fixture', now(), true, 'fp', 'model');\n"
                setup += 'CREATE TEMP TABLE request_logs_2026_10(id integer);\n'
                setup += f'CREATE TEMP VIEW request_logs_with_current_month WITH (security_invoker=true) AS SELECT request_id, ts, is_final_success FROM {hot};\n'
                checks = '''
DO $$
DECLARE names text[];
BEGIN
 SELECT array_agg(attname::text ORDER BY attnum) INTO names FROM pg_attribute
 WHERE attrelid='pg_temp.request_logs_with_current_month'::regclass AND attnum>0;
 IF names <> ARRAY['request_id','ts','is_final_success','system_fingerprint','raw_model_name'] THEN
   RAISE EXCEPTION 'view tail/order lost: %', names;
 END IF;
 IF (SELECT count(*) FROM pg_temp.request_logs_with_current_month WHERE system_fingerprint='fp' AND raw_model_name='model' AND is_final_success) <> 1 THEN
   RAISE EXCEPTION 'view data lost';
 END IF;
 IF NOT EXISTS(SELECT 1 FROM pg_class WHERE oid='pg_temp.request_logs_with_current_month'::regclass AND 'security_invoker=true'=ANY(reloptions)) THEN
   RAISE EXCEPTION 'view security option lost';
 END IF;
 IF to_regprocedure('pg_temp.promote_request_logs_hot_to_partition()') IS NULL
 OR to_regclass('pg_temp.idx_request_logs_hot_fixture') IS NULL
 OR to_regclass('pg_temp.request_logs_2026_10') IS NULL THEN
   RAISE EXCEPTION 'function/index/partition token collision';
 END IF;
END $$;
ROLLBACK;
'''
                full_sql = setup + delivered + view_sql + view_sql + checks
                (root / 'live-fixture.sql').write_text(full_sql)
                result = subprocess.run(['psql', '-X', '-v', 'ON_ERROR_STOP=1', dsn, '-f', '-'],
                                        input=full_sql, capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_all_six_exact_tokens_and_byte_stability(self):
        # Exercise the actual helper, including CRLF and $/Unicode boundaries.
        root, _, _, _ = self.fixture('psql', 'renamed')
        file = root / 'tokens.sql'
        raw = SQL.replace('\n', '\r\n').encode()
        file.write_bytes(raw)
        helper = SOURCE[SOURCE.index('request_logs_sql() {'):SOURCE.index('prepare_runtime_sql() {')]
        result = subprocess.run(['/bin/bash', '-c', helper + '\nruntime_adaptation=request-logs-physical-v1\nrequest_logs_sql "$1"', '_', str(file)], capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        expected = raw
        for token in ['request_logs_bodies_hot', 'request_logs_bodies', 'request_logs_hot',
                      'request_logs_archive', 'request_logs_id_seq', 'request_logs']:
            # Independent literal expectations, not a copy of adapter regexp.
            expected = expected.replace(("'" + token + "'").encode(), ("'_" + token + "'").encode())
        expected = expected.replace(b'public.request_logs ADD', b'public._request_logs ADD')
        expected = expected.replace(b"'public.request_logs'", b"'public._request_logs'")
        self.assertEqual(result.stdout, expected)


if not _posix_runtime_available():
    RuntimeFixture = unittest.skip(_POSIX_ONLY_REASON)(RuntimeFixture)


if __name__ == '__main__':
    unittest.main(verbosity=2)
