"""Read-only SQL contract: run with a psql command, e.g. python3 this.py psql -X -At -v ON_ERROR_STOP=1.
Fixtures are CTEs; no real table is changed. Executes the production query text.
"""
import re
import subprocess
import sys
from pathlib import Path

root = Path(__file__).resolve().parents[1]
source = (root / 'admin/client_configs_featured.go').read_text()
query = re.search(r'const clientConfigFeaturedSQL = `(.+?)`', source, re.S).group(1)
fixtures = """
WITH routing_policy(id, tenant_id, featured_models) AS (VALUES
 (1,'tenant-a',ARRAY['first','alias','first','']),
 (2,'tenant-a',ARRAY['second','alias']),
 (3,'tenant-b',ARRAY['private'])),
models_canonical(id, canonical_name, context_window) AS (VALUES
 (1,'first',32000),(2,'canonical',64000),(3,'second',128000),(4,'deprecated',999)),
model_aliases(canonical_id,raw_name,status) AS (VALUES
 (2,'alias','active'),(4,'alias','deprecated')),
"""
query = re.sub(r'^\s*WITH\s+', fixtures, query, count=1)
sql = 'BEGIN READ ONLY;\n' + query.replace('$1', "'tenant-a'") + ';\n' + query.replace('$1', "'tenant-b'") + ';\nROLLBACK;\n'
result = subprocess.run(sys.argv[1:], input=sql, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True)
rows = [line for line in result.stdout.splitlines() if '|' in line]
expected = ['first|32000','alias|64000','second|128000','private|']
if rows != expected:
    raise SystemExit('Featured SQL contract failed: ' + repr(rows))
print('PASS: policy union, deduplication, tenant scope, alias metadata and deprecated alias exclusion')
