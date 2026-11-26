#!/usr/bin/env python3
"""Verify the candidate slot serves the default V2 reconciliation day."""

import argparse
import csv
from datetime import date
import http.client
import io
import json
import sys
from urllib.parse import urlencode
from xml.etree import ElementTree
import zipfile


REPORT_VERSION = "request-reconciliation-v2"
PROJECTION_SOURCE = "request_metrics"
WORKBOOK_TYPE = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
MAX_RESPONSE_BYTES = 8 * 1024 * 1024


def read_candidate(port, token, path, content_type, query=None, payload=None):
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=35)
    target = path if query is None else f"{path}?{urlencode(query)}"
    try:
        if payload is None:
            connection.request("GET", target, headers={"Authorization": f"Bearer {token}"})
        else:
            connection.request("POST", target, payload, headers={"Content-Type": "application/json"})
        response = connection.getresponse()
        if response.status != 200:
            raise RuntimeError(f"{path} returned HTTP {response.status}")
        if response.headers.get_content_type() != content_type:
            raise RuntimeError(f"{path} returned an unexpected content type")
        body = response.read(MAX_RESPONSE_BYTES + 1)
        if len(body) > MAX_RESPONSE_BYTES:
            raise RuntimeError(f"{path} exceeded the response size limit")
        return response.headers, body
    except (OSError, http.client.HTTPException):
        raise RuntimeError(f"{path} could not be read from the candidate slot") from None
    finally:
        connection.close()


def json_object(body):
    value = json.loads(body)
    if not isinstance(value, dict):
        raise ValueError("candidate JSON must be an object")
    return value


def login_candidate(port, credentials):
    login = json_object(credentials)
    if not login.get("username") or not login.get("password"):
        raise RuntimeError("expected configured candidate admin credentials")
    _, body = read_candidate(port, None, "/api/auth/token", "application/json", payload=credentials)
    result = json_object(body)
    user, token = result.get("user"), result.get("access_token")
    if (
        not isinstance(user, dict)
        or user.get("username") != login["username"]
        or user.get("role") != "super_admin"
        or not isinstance(token, str)
        or not token
    ):
        raise RuntimeError("candidate login lacks a super_admin access token")
    return token


def workbook_xml(workbook, name):
    part = workbook.getinfo(name)
    if part.file_size > MAX_RESPONSE_BYTES:
        raise RuntimeError("V2 Excel workbook exceeds the XML size limit")
    return ElementTree.fromstring(workbook.read(part))


def check_workbook(body, day):
    sheet_ns = "{http://schemas.openxmlformats.org/spreadsheetml/2006/main}"
    ref_ns = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
    rel_ns = "{http://schemas.openxmlformats.org/package/2006/relationships}"
    with zipfile.ZipFile(io.BytesIO(body)) as workbook:
        # The rollup exporter writes the summary to sheet1. Check that Excel can
        # reach it through the workbook, and read the exporter’s metadata row.
        book = workbook_xml(workbook, "xl/workbook.xml")
        sheet = book.find(f"{sheet_ns}sheets/{sheet_ns}sheet")
        links = workbook_xml(workbook, "xl/_rels/workbook.xml.rels")
        if book.tag != f"{sheet_ns}workbook" or sheet is None or sheet.get("name") != "汇总":
            raise RuntimeError("V2 Excel workbook has no summary worksheet")
        if not sheet.get(f"{{{ref_ns}}}id") or not any(
            link.get("Id") == sheet.get(f"{{{ref_ns}}}id")
            and link.get("Type") == f"{ref_ns}/worksheet"
            and link.get("Target") == "worksheets/sheet1.xml"
            and link.get("TargetMode", "Internal") == "Internal"
            for link in links.findall(f"{rel_ns}Relationship")
        ):
            raise RuntimeError("V2 Excel workbook has no link to the summary worksheet")
        summary = workbook_xml(workbook, "xl/worksheets/sheet1.xml")
        metadata = summary.find(f"{sheet_ns}sheetData/{sheet_ns}row[@r='2']")
        if summary.tag != f"{sheet_ns}worksheet" or metadata is None:
            raise RuntimeError("V2 Excel workbook lacks summary metadata")
        cells = {
            cell.get("r"): cell.findtext(f"{sheet_ns}is/{sheet_ns}t")
            for cell in metadata.findall(f"{sheet_ns}c")
        }
        if cells.get("A2") != f"区间: {day} ~ {day}":
            raise RuntimeError("V2 Excel workbook returned the wrong day")
        if cells.get("D2") != f"统计来源: {PROJECTION_SOURCE}":
            raise RuntimeError("V2 Excel workbook lacks the request_metrics source")


def check_candidate(port, token):
    _, body = read_candidate(port, token, "/api/admin/capabilities", "application/json")
    calendar = json_object(body).get("daily_reconciliation_default")
    if not isinstance(calendar, dict):
        raise RuntimeError("candidate lacks daily_reconciliation_default")
    day, timezone = calendar.get("day"), calendar.get("timezone")
    if not isinstance(day, str) or date.fromisoformat(day).isoformat() != day:
        raise RuntimeError("candidate returned an invalid V2 default day")
    if not isinstance(timezone, str) or not timezone.strip():
        raise RuntimeError("candidate returned an invalid V2 default timezone")

    path = "/api/admin/usage/v2/request-reconciliation"
    query = dict(day=day, timezone=timezone, section="coverage", limit=1)
    _, body = read_candidate(port, token, path, "application/json", dict(query, format="json"))
    report = json_object(body)
    projection, summary = report.get("projection"), report.get("summary")
    if (
        (report.get("version"), report.get("section"), report.get("day"), report.get("timezone"))
        != (REPORT_VERSION, "coverage", day, timezone)
        or not isinstance(projection, dict)
        or projection.get("source") != PROJECTION_SOURCE
        or not isinstance(summary, dict)
        or type(summary.get("received_requests")) is not int
        or summary["received_requests"] < 0
    ):
        raise RuntimeError("candidate JSON lacks the expected V2 coverage and projection")

    headers, body = read_candidate(port, token, path, "text/csv", dict(query, format="csv-summary"))
    csv_report = csv.DictReader(io.StringIO(body.decode("utf-8")))
    if (
        headers.get("X-Report-Version") != REPORT_VERSION
        or headers.get("X-Report-Terminal-Usage-Source") != PROJECTION_SOURCE
        or headers.get("X-Report-Day") != day
        or headers.get("X-Report-Timezone") != timezone
        or headers.get("X-Report-Export") != "summary"
        or csv_report.fieldnames != ["metric", "value"]
        or not any(row["metric"] == "received_requests" and (row["value"] or "").isdigit() for row in csv_report)
    ):
        raise RuntimeError("candidate CSV lacks the expected V2 coverage and projection")

    query = dict(start=day, end=day, view="provider", group="provider", detail="summary")
    _, body = read_candidate(port, token, "/api/admin/report-rollup/v2/export", WORKBOOK_TYPE, query)
    check_workbook(body, day)
    print(f"V2 reconciliation ready: day={day} timezone={timezone}, coverage JSON/CSV and Excel verified")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("port", type=int)
    args = parser.parse_args()
    if not 1 <= args.port <= 65535:
        parser.error("expected a valid candidate port")
    credentials = sys.stdin.buffer.read()
    if not credentials.strip():
        parser.error("expected configured candidate admin credentials on stdin")
    try:
        check_candidate(args.port, login_candidate(args.port, credentials))
    except RuntimeError as error:
        print(error, file=sys.stderr)
        return 1
    except (ValueError, KeyError, csv.Error, ElementTree.ParseError, zipfile.BadZipFile):
        print("candidate returned invalid V2 reconciliation data", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
