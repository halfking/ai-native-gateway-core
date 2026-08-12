from pathlib import Path
import psycopg

url = None
for line in Path('/etc/llm-gateway-go/env').read_text(errors='ignore').splitlines():
    if line.startswith('LLM_GATEWAY_DATABASE_URL='):
        url = line.split('=', 1)[1].strip().strip('"').strip("'")
        break
if not url:
    raise SystemExit('missing LLM_GATEWAY_DATABASE_URL')

with psycopg.connect(url) as conn:
    with conn.cursor() as cur:
        print('== credentials/offers for gpt-5.6-terra ==')
        cur.execute('''
            SELECT c.id, c.label, p.display_name, c.status, c.lifecycle_status, c.availability_state,
                   c.quota_state, c.circuit_state, c.manual_disabled, mo.raw_model_name, mo.available,
                   mo.unavailable_reason
            FROM model_offers mo
            JOIN credentials c ON c.id = mo.credential_id
            JOIN providers p ON p.id = c.provider_id
            WHERE lower(mo.raw_model_name) = lower(%s)
               OR lower(COALESCE(mo.outbound_model_name, '')) = lower(%s)
               OR lower(COALESCE(mo.standardized_name, '')) = lower(%s)
            ORDER BY c.id
        ''', ('gpt-5.6-terra', 'gpt-5.6-terra', 'gpt-5.6-terra'))
        rows = cur.fetchall()
        for row in rows:
            print(row)
        print('rows', len(rows))

        print('== lifecycle_status counts ==')
        cur.execute('''
            SELECT lifecycle_status, count(*)
            FROM credentials
            GROUP BY lifecycle_status
            ORDER BY count(*) DESC, lifecycle_status NULLS LAST
        ''')
        for row in cur.fetchall():
            print(row)

        print('== invalid lifecycle_status sample ==')
        cur.execute('''
            SELECT c.id, c.label, p.display_name, c.status, c.lifecycle_status, c.availability_state,
                   c.updated_at
            FROM credentials c
            JOIN providers p ON p.id = c.provider_id
            WHERE COALESCE(c.lifecycle_status, '') NOT IN ('active', 'disabled', 'suspended', 'retired')
            ORDER BY c.updated_at DESC NULLS LAST
            LIMIT 30
        ''')
        for row in cur.fetchall():
            print(row)

        print('== recent request logs terra ==')
        cur.execute('''
            SELECT ts, success, status_code, error_type, left(COALESCE(error_message,''), 220), provider, credential_id, outbound_model
            FROM request_logs_hot
            WHERE lower(COALESCE(client_model,'')) = 'gpt-5.6-terra'
               OR lower(COALESCE(outbound_model,'')) = 'gpt-5.6-terra'
            ORDER BY ts DESC
            LIMIT 30
        ''')
        for row in cur.fetchall():
            print(row)
