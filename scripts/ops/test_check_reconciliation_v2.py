import io
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import subprocess
import sys
import threading
import unittest
from urllib.parse import parse_qs, urlsplit
import zipfile


CHECK = Path(__file__).with_name("check-reconciliation-v2.py")
DAY, ZONE = "2026-11-25", "Asia/Shanghai"
VERSION = "request-reconciliation-v2"
TOKEN = "ops_fixture"
USER = "fixture-admin"
PASSWORD = "fixture-admin-password"
WORKBOOK_TYPE = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"


def workbook(case):
    stream = io.BytesIO()
    target = "worksheets/sheet2.xml" if case == "orphan_workbook" else "worksheets/sheet1.xml"
    day = "2026-11-24" if case == "wrong_workbook_day" else DAY
    source = "legacy" if case in ("legacy_workbook", "spoofed_workbook_source") else "request_metrics"
    misplaced = '<c r="A3" t="inlineStr"><is><t>Debug: request_metrics</t></is></c>' if case == "spoofed_workbook_source" else ""
    with zipfile.ZipFile(stream, "w") as output:
        output.writestr(
            "xl/workbook.xml",
            '<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" '
            'xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">'
            '<sheets><sheet name="汇总" sheetId="1" r:id="rId1"/></sheets></workbook>',
        )
        output.writestr(
            "xl/_rels/workbook.xml.rels",
            '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
            '<Relationship Id="rId1" '
            'Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" '
            f'Target="{target}"/></Relationships>',
        )
        output.writestr(
            "xl/worksheets/sheet1.xml",
            '<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">'
            '<sheetData><row r="2">'
            f'<c r="A2" t="inlineStr"><is><t>区间: {day} ~ {day}</t></is></c>'
            f'<c r="D2" t="inlineStr"><is><t>统计来源: {source}</t></is></c>'
            f'</row><row r="3">{misplaced}</row></sheetData></worksheet>',
        )
    return stream.getvalue()


class CandidateHandler(BaseHTTPRequestHandler):
    def do_POST(self):
        self.server.requests.append((self.path, {}, None))
        creds = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if self.path != "/api/auth/token" or creds != {"username": USER, "password": PASSWORD}:
            self.send_error(401)
            return
        role = "tenant_admin" if self.server.case == "tenant_login" else "super_admin"
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps({"user": {"username": USER, "role": role}, "access_token": TOKEN}).encode())

    def do_GET(self):
        args = urlsplit(self.path)
        query = parse_qs(args.query)
        self.server.requests.append((args.path, query, self.headers.get("Authorization")))
        if self.headers.get("Authorization") != f"Bearer {TOKEN}":
            self.send_error(401)
            return
        case = self.server.case
        headers = {}
        if args.path == "/api/admin/capabilities":
            content_type = "application/json"
            reply = {} if case == "missing_default" else {"daily_reconciliation_default": {"day": DAY, "timezone": ZONE}}
            body = json.dumps(reply).encode()
        elif args.path == "/api/admin/usage/v2/request-reconciliation" and query.get("format") == ["json"]:
            if case == "missing_route":
                self.send_error(404)
                return
            content_type = "text/html" if case == "spa_fallback" else "application/json"
            body = json.dumps({
                "version": "legacy" if case == "legacy_report" else VERSION,
                "section": "coverage",
                "day": DAY,
                "timezone": ZONE,
                "projection": {"source": "request_metrics"},
                "summary": {"received_requests": 0},
            }).encode()
        elif args.path == "/api/admin/usage/v2/request-reconciliation" and query.get("format") == ["csv-summary"]:
            content_type = "text/csv; charset=utf-8"
            body = b"metric,value\nreceived_requests,0\n"
            headers = {
                "X-Report-Version": VERSION,
                "X-Report-Day": DAY,
                "X-Report-Timezone": ZONE,
                "X-Report-Export": "summary",
                "X-Report-Terminal-Usage-Source": "legacy" if case == "legacy_csv" else "request_metrics",
            }
        else:
            content_type = WORKBOOK_TYPE
            body = b"invalid zip" if case == "broken_workbook" else workbook(case)
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        for name, value in headers.items():
            self.send_header(name, value)
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass


class ReconciliationCutoverTest(unittest.TestCase):
    def check(self, case, password=PASSWORD):
        with ThreadingHTTPServer(("127.0.0.1", 0), CandidateHandler) as server:
            server.case = case
            server.requests = []
            worker = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01})
            worker.start()
            try:
                result = subprocess.run(
                    [sys.executable, str(CHECK), str(server.server_port)],
                    input=json.dumps({"username": USER, "password": password}),
                    text=True, capture_output=True, timeout=10, check=False,
                )
            finally:
                server.shutdown()
                worker.join()
        self.assertNotIn(password, result.stdout + result.stderr)
        self.assertNotIn(TOKEN, result.stdout + result.stderr)
        return result, server.requests

    def test_candidate_passes_with_zero_receipts_and_reuses_default_day(self):
        result, requests = self.check("ready")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(requests), 5)
        self.assertEqual(requests[0][0], "/api/auth/token")
        for _, query, _ in requests[2:4]:
            self.assertEqual({name: query[name] for name in ("day", "timezone")}, {"day": [DAY], "timezone": [ZONE]})
        self.assertEqual(requests[-1][:2], ("/api/admin/report-rollup/v2/export", {
            "start": [DAY], "end": [DAY], "view": ["provider"], "group": ["provider"], "detail": ["summary"],
        }))
        self.assertTrue(all(auth == f"Bearer {TOKEN}" for _, _, auth in requests[1:]))

    def test_candidate_fails_closed(self):
        for variant, message, requests_before_stop in (
            ("tenant_login", "super_admin access token", 1),
            ("missing_default", "daily_reconciliation_default", 2),
            ("missing_route", "HTTP 404", 3),
            ("spa_fallback", "content type", 3),
            ("legacy_report", "expected V2 coverage", 3),
            ("legacy_csv", "candidate CSV", 4),
            ("broken_workbook", "invalid V2 reconciliation data", 5),
            ("legacy_workbook", "workbook lacks", 5),
            ("orphan_workbook", "no link to the summary", 5),
            ("wrong_workbook_day", "returned the wrong day", 5),
            ("spoofed_workbook_source", "workbook lacks the request_metrics source", 5),
        ):
            with self.subTest(variant=variant):
                result, requests = self.check(variant)
                self.assertEqual(result.returncode, 1, result.stdout)
                self.assertIn(message, result.stderr)
                self.assertEqual(len(requests), requests_before_stop)

    def test_candidate_rejects_bad_authentication(self):
        result, requests = self.check("ready", "invalid-fixture-token")
        self.assertEqual(result.returncode, 1)
        self.assertIn("HTTP 401", result.stderr)
        self.assertEqual(len(requests), 1)


if __name__ == "__main__":
    unittest.main()
