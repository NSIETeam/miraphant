#!/usr/bin/env python3
"""Create a privacy-minimized, read-only inventory from an explicit SQLite backup."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sqlite3
import stat
import sys
import tempfile
from datetime import datetime, timezone
from decimal import Decimal, InvalidOperation
from pathlib import Path
from typing import Any


FORMAT_VERSION = "miraphant-legacy-inventory-v1"
SIDECAR_SUFFIXES = ("-wal", "-shm", "-journal")
LEGACY_BILLING_OPTIONS = (
    "QuotaForNewUser",
    "QuotaForInviter",
    "QuotaForInvitee",
    "QuotaRemindThreshold",
    "PreConsumedQuota",
    "ModelRatio",
    "GroupRatio",
    "CompletionRatio",
    "TopUpLink",
    "QuotaPerUnit",
    "DisplayInCurrencyEnabled",
    "DisplayTokenStatEnabled",
    "ApproximateTokenEnabled",
)
INTEGER_OPTIONS = {"QuotaForNewUser", "QuotaForInviter", "QuotaForInvitee", "QuotaRemindThreshold", "PreConsumedQuota"}
DECIMAL_OPTIONS = {"QuotaPerUnit"}
BOOLEAN_OPTIONS = {"DisplayInCurrencyEnabled", "DisplayTokenStatEnabled", "ApproximateTokenEnabled"}
RATIO_OPTIONS = {"ModelRatio", "GroupRatio", "CompletionRatio"}
POINT_HOLD_STATES = {"held", "pending", "settled", "released", "needs_review"}


class InventoryError(Exception):
    def __init__(self, code: str):
        super().__init__(code)
        self.code = code


def _sha256_file(path: Path) -> tuple[str, int]:
    digest = hashlib.sha256()
    total = 0
    try:
        with path.open("rb") as source:
            while True:
                chunk = source.read(1024 * 1024)
                if not chunk:
                    break
                digest.update(chunk)
                total += len(chunk)
    except OSError as exc:
        raise InventoryError("input_read_failed") from exc
    return digest.hexdigest(), total


def _check_no_sidecars(path: Path) -> None:
    try:
        for suffix in SIDECAR_SUFFIXES:
            if os.path.lexists(str(path) + suffix):
                raise InventoryError("sqlite_sidecar_present")
    except InventoryError:
        raise
    except OSError as exc:
        raise InventoryError("input_sidecar_check_failed") from exc


def _check_input(path: Path) -> Path:
    try:
        if path.is_symlink():
            raise InventoryError("input_symlink_rejected")
        resolved = path.resolve(strict=True)
        mode = resolved.stat().st_mode
    except InventoryError:
        raise
    except OSError as exc:
        raise InventoryError("input_unavailable") from exc
    if not stat.S_ISREG(mode):
        raise InventoryError("input_must_be_regular_file")
    _check_no_sidecars(resolved)
    try:
        with resolved.open("rb") as source:
            header = source.read(100)
    except OSError as exc:
        raise InventoryError("input_read_failed") from exc
    if len(header) < 100 or header[:16] != b"SQLite format 3\x00":
        raise InventoryError("input_not_sqlite_database")
    if header[18] != 1 or header[19] != 1:
        raise InventoryError("sqlite_must_use_rollback_journal")
    return resolved


def _columns(connection: sqlite3.Connection, table: str) -> set[str]:
    # All table identifiers are constants from this file, never caller input.
    try:
        return {row[1] for row in connection.execute(f'PRAGMA table_info("{table}")')}
    except sqlite3.Error as exc:
        raise InventoryError("database_schema_unreadable") from exc


def _require_table(connection: sqlite3.Connection, table: str, required: set[str]) -> bool:
    try:
        found = connection.execute(
            "SELECT type FROM sqlite_master WHERE name=? AND type IN ('table','view')", (table,)
        ).fetchone()
    except sqlite3.Error as exc:
        raise InventoryError("database_schema_unreadable") from exc
    if found is None:
        raise InventoryError("required_table_missing")
    if found[0] != "table":
        raise InventoryError("table_must_be_physical_table")
    if not required.issubset(_columns(connection, table)):
        raise InventoryError("required_column_missing")
    return True


def _optional_table(connection: sqlite3.Connection, table: str, required: set[str]) -> bool:
    try:
        found = connection.execute(
            "SELECT type FROM sqlite_master WHERE name=? AND type IN ('table','view')", (table,)
        ).fetchone()
    except sqlite3.Error as exc:
        raise InventoryError("database_schema_unreadable") from exc
    if found is None:
        return False
    if found[0] != "table":
        raise InventoryError("optional_table_must_be_physical_table")
    if not required.issubset(_columns(connection, table)):
        raise InventoryError("optional_table_schema_invalid")
    return True


def _normalize_option(key: str, raw: Any) -> Any:
    if not isinstance(raw, str):
        raise InventoryError("billing_option_value_invalid")
    value = raw.strip()
    if key in INTEGER_OPTIONS:
        if not re.fullmatch(r"[+-]?(0|[1-9][0-9]*)", value):
            raise InventoryError("billing_option_value_invalid")
        return int(value, 10)
    if key in DECIMAL_OPTIONS:
        if len(raw) > 256:
            raise InventoryError("billing_option_value_invalid")
        try:
            parsed = Decimal(value)
        except InvalidOperation as exc:
            raise InventoryError("billing_option_value_invalid") from exc
        if not parsed.is_finite():
            raise InventoryError("billing_option_value_invalid")
        parts = parsed.as_tuple()
        if abs(parts.exponent) > 256 or abs(parsed.adjusted()) > 256:
            raise InventoryError("billing_option_value_invalid")
        # Decimal.normalize() uses the active context precision and can round
        # long historical values. Fixed-point formatting preserves every digit.
        normalized = format(parsed, "f")
        if len(normalized) > 512:
            raise InventoryError("billing_option_value_invalid")
        return "0" if normalized in ("-0", "") else normalized
    if key in BOOLEAN_OPTIONS:
        if value.lower() not in ("true", "false"):
            raise InventoryError("billing_option_value_invalid")
        return value.lower() == "true"
    if key in RATIO_OPTIONS:
        def unique_object(pairs):
            output = {}
            for name, item in pairs:
                if name in output:
                    raise ValueError("duplicate key")
                output[name] = item
            return output

        def reject_constant(_value):
            raise ValueError("invalid JSON constant")

        try:
            parsed = json.loads(
                raw,
                object_pairs_hook=unique_object,
                parse_constant=reject_constant,
            )
        except (json.JSONDecodeError, TypeError, ValueError) as exc:
            raise InventoryError("billing_option_value_invalid") from exc
        if not isinstance(parsed, dict) or any(not isinstance(name, str) for name in parsed):
            raise InventoryError("billing_option_value_invalid")
        return {"entry_count": len(parsed), "raw_sha256": hashlib.sha256(raw.encode("utf-8")).hexdigest()}
    if key == "TopUpLink":
        # A configured URL can contain credentials in its query string.
        return {"configured": bool(value)}
    raise InventoryError("billing_option_not_whitelisted")


def _integer(value: Any, field: str, nullable: bool = False) -> int | None:
    if value is None and nullable:
        return None
    # bool is not accepted as a substitute for an SQLite integer.
    if type(value) is not int:
        raise InventoryError("non_integer_financial_value")
    return value


def _rows(connection: sqlite3.Connection, sql: str, parameters: tuple[Any, ...] = ()) -> list[sqlite3.Row]:
    try:
        return list(connection.execute(sql, parameters))
    except sqlite3.Error as exc:
        raise InventoryError("database_read_failed") from exc


def _build_report(connection: sqlite3.Connection, source_sha256: str, source_bytes: int) -> dict[str, Any]:
    connection.row_factory = sqlite3.Row
    try:
        integrity = connection.execute("PRAGMA integrity_check").fetchone()
        user_version_row = connection.execute("PRAGMA user_version").fetchone()
    except sqlite3.Error as exc:
        raise InventoryError("database_integrity_unreadable") from exc
    if not integrity or integrity[0] != "ok":
        raise InventoryError("database_integrity_failed")
    user_version = _integer(user_version_row[0], "user_version") if user_version_row else None

    _require_table(connection, "users", {"id", "role", "status", "quota", "used_quota", "request_count"})
    _require_table(connection, "tokens", {"id", "user_id", "status", "remain_quota", "used_quota", "unlimited_quota", "expired_time"})
    _require_table(connection, "redemptions", {"id", "user_id", "status", "quota", "created_time", "redeemed_time"})

    users_raw = _rows(connection, "SELECT id, role, status, quota, used_quota, request_count FROM users ORDER BY id")
    users: list[dict[str, int]] = []
    user_ids: set[int] = set()
    anomalies: list[dict[str, Any]] = []
    for row in users_raw:
        item = {field: _integer(row[field], field) for field in ("id", "role", "status", "quota", "used_quota", "request_count")}
        if item["id"] in user_ids:
            raise InventoryError("duplicate_user_id")
        user_ids.add(item["id"])
        for field in ("quota", "used_quota"):
            if item[field] < 0:
                anomalies.append({"kind": "negative_user_financial_value", "record_id": item["id"], "field": field})
        users.append(item)  # type: ignore[arg-type]

    tokens_raw = _rows(connection, "SELECT id, user_id, status, remain_quota, used_quota, unlimited_quota, expired_time FROM tokens ORDER BY id")
    tokens: list[dict[str, Any]] = []
    token_ids: set[int] = set()
    for row in tokens_raw:
        item = {field: _integer(row[field], field) for field in ("id", "user_id", "status", "remain_quota", "used_quota", "expired_time")}
        unlimited = _integer(row["unlimited_quota"], "unlimited_quota")
        if unlimited not in (0, 1):
            raise InventoryError("invalid_boolean_financial_value")
        if item["id"] in token_ids:
            raise InventoryError("duplicate_token_id")
        token_ids.add(item["id"])
        if item["user_id"] not in user_ids:
            anomalies.append({"kind": "token_owner_missing", "record_id": item["id"]})
        for field in ("remain_quota", "used_quota"):
            if item[field] < 0:
                anomalies.append({"kind": "negative_token_quota", "record_id": item["id"], "field": field})
        tokens.append({**item, "unlimited_quota": bool(unlimited)})

    redemption_raw = _rows(connection, "SELECT id, user_id, status, quota, created_time, redeemed_time FROM redemptions ORDER BY id")
    redemptions: list[dict[str, int]] = []
    redemption_ids: set[int] = set()
    enabled_unredeemed_quota = 0
    disabled_unredeemed_quota = 0
    for row in redemption_raw:
        item = {field: _integer(row[field], field) for field in ("id", "user_id", "status", "quota", "created_time", "redeemed_time")}
        if item["id"] in redemption_ids:
            raise InventoryError("duplicate_redemption_id")
        redemption_ids.add(item["id"])
        if item["user_id"] not in user_ids:
            anomalies.append({"kind": "redemption_creator_missing", "record_id": item["id"]})
        if item["quota"] < 0:
            anomalies.append({"kind": "negative_redemption_quota", "record_id": item["id"]})
        if item["status"] == 1:
            enabled_unredeemed_quota += item["quota"]
        elif item["status"] == 2:
            disabled_unredeemed_quota += item["quota"]
        elif item["status"] != 3:
            anomalies.append({"kind": "unknown_redemption_status", "record_id": item["id"]})
        redemptions.append(item)  # type: ignore[arg-type]

    options_exists = _optional_table(connection, "options", {"key", "value"})
    if options_exists:
        placeholders = ",".join("?" for _ in LEGACY_BILLING_OPTIONS)
        option_rows = _rows(connection, f"SELECT key, value FROM options WHERE key IN ({placeholders}) ORDER BY key", LEGACY_BILLING_OPTIONS)
        options: dict[str, Any] = {}
        for row in option_rows:
            key = row["key"]
            if not isinstance(key, str) or key in options:
                raise InventoryError("duplicate_or_invalid_billing_option")
            normalized = _normalize_option(key, row["value"])
            options[key] = normalized
            numeric = normalized if isinstance(normalized, int) else Decimal(normalized) if key in DECIMAL_OPTIONS else None
            if numeric is not None and numeric < 0:
                anomalies.append({"kind": "negative_billing_option", "key": key})
    else:
        options = {}

    if _optional_table(connection, "logs", {"id", "user_id", "type", "quota"}):
        log_rows = _rows(connection, "SELECT id, user_id, type, quota FROM logs ORDER BY id")
        grouped_logs: dict[int, dict[str, int]] = {}
        seen_log_ids: set[int] = set()
        for row in log_rows:
            log_id = _integer(row["id"], "log_id")
            log_user_id = _integer(row["user_id"], "log_user_id")
            log_type = _integer(row["type"], "log_type")
            log_quota = _integer(row["quota"], "log_quota")
            if log_id in seen_log_ids:
                raise InventoryError("duplicate_log_id")
            seen_log_ids.add(log_id)
            if log_user_id not in user_ids:
                anomalies.append({"kind": "log_user_missing", "record_id": log_id})
            aggregate = grouped_logs.setdefault(log_type, {"count": 0, "quota_sum": 0})
            aggregate["count"] += 1
            aggregate["quota_sum"] += log_quota
            if log_quota < 0:
                anomalies.append({"kind": "negative_log_quota", "record_id": log_id})
        logs: dict[str, Any] = {
            "provided": True,
            "by_type": [
                {"type": kind, **totals} for kind, totals in sorted(grouped_logs.items())
            ],
        }
    else:
        logs = {"provided": False, "by_type": None}

    point_accounts_exists = _optional_table(connection, "point_accounts", {"user_id", "available_micro", "held_micro", "spent_micro"})
    if point_accounts_exists:
        account_rows = _rows(connection, "SELECT user_id, available_micro, held_micro, spent_micro FROM point_accounts ORDER BY user_id")
        account_ids: set[int] = set()
        account_totals = {"available_micro": 0, "held_micro": 0, "spent_micro": 0}
        for row in account_rows:
            user_id = _integer(row["user_id"], "point_account_user_id")
            if user_id in account_ids:
                raise InventoryError("duplicate_point_account_user")
            account_ids.add(user_id)
            if user_id not in user_ids:
                anomalies.append({"kind": "point_account_user_missing", "record_id": user_id})
            for field in account_totals:
                value = _integer(row[field], field)
                account_totals[field] += value
                if value < 0:
                    anomalies.append({"kind": "negative_financial_value", "record_id": user_id, "field": field})
        point_accounts: dict[str, Any] = {"exists": True, "count": len(account_rows), **account_totals}
    else:
        point_accounts = {"exists": False, "count": None, "available_micro": None, "held_micro": None, "spent_micro": None}

    point_holds_exists = _optional_table(connection, "point_holds", {"id", "user_id", "state"})
    if point_holds_exists:
        hold_rows = _rows(connection, "SELECT id, user_id, state FROM point_holds ORDER BY id")
        hold_states: dict[str, int] = {}
        hold_ids: set[int] = set()
        for row in hold_rows:
            hold_id = _integer(row["id"], "point_hold_id")
            if hold_id in hold_ids:
                raise InventoryError("duplicate_point_hold_id")
            hold_ids.add(hold_id)
            hold_user_id = _integer(row["user_id"], "point_hold_user_id")
            if hold_user_id not in user_ids:
                anomalies.append({"kind": "point_hold_user_missing", "record_id": hold_id})
            state = row["state"]
            if not isinstance(state, str) or state not in POINT_HOLD_STATES:
                raise InventoryError("invalid_point_hold_state")
            hold_states[state] = hold_states.get(state, 0) + 1
        point_holds: dict[str, Any] = {"exists": True, "count": len(hold_rows), "by_state": hold_states}
    else:
        point_holds = {"exists": False, "count": None, "by_state": None}

    negative_count = sum(1 for anomaly in anomalies if anomaly["kind"].startswith("negative_"))
    return {
        "format_version": FORMAT_VERSION,
        "generated_at_utc": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
        "source": {"bytes": source_bytes, "sha256": source_sha256, "sqlite_user_version": user_version},
        "users": {"count": len(users), "quota_sum": sum(row["quota"] for row in users), "used_quota_sum": sum(row["used_quota"] for row in users), "request_count_sum": sum(row["request_count"] for row in users), "rows": users},
        "tokens": {"count": len(tokens), "rows": tokens, "warning": "token limits are not user assets and are not added to user quota"},
        "redemptions": {
            "count": len(redemptions),
            "enabled_unredeemed_count": sum(1 for row in redemptions if row["status"] == 1),
            "enabled_unredeemed_quota": enabled_unredeemed_quota,
            "disabled_unredeemed_count": sum(1 for row in redemptions if row["status"] == 2),
            "disabled_unredeemed_quota": disabled_unredeemed_quota,
            "used_count": sum(1 for row in redemptions if row["status"] == 3),
            "rows": redemptions,
            "warning": "user_id is the creator; enabled and disabled unredeemed codes are separate potential liabilities, not credited account balance",
        },
        "options": {"persisted": options_exists, "values": options, "missing_keys": [key for key in LEGACY_BILLING_OPTIONS if key not in options]},
        "logs": logs,
        "points": {"accounts": point_accounts, "holds": point_holds, "warning": "point balances are reported separately and are never converted or added to legacy quota"},
        "anomalies": {"negative_financial_findings": negative_count, "records": anomalies},
    }


def create_inventory(input_path: str | os.PathLike[str]) -> tuple[dict[str, Any], Path, str]:
    path = _check_input(Path(input_path))
    digest_before, size_before = _sha256_file(path)
    _check_no_sidecars(path)
    try:
        uri = path.as_uri() + "?mode=ro"
        connection = sqlite3.connect(uri, uri=True, timeout=1.0)
        connection.execute("PRAGMA query_only=ON")
        connection.execute("BEGIN")
        report = _build_report(connection, digest_before, size_before)
    except InventoryError:
        raise
    except (OSError, sqlite3.Error, ValueError) as exc:
        raise InventoryError("database_open_or_read_failed") from exc
    finally:
        if "connection" in locals():
            connection.close()
    _check_no_sidecars(path)
    digest_after, size_after = _sha256_file(path)
    if digest_before != digest_after or size_before != size_after:
        raise InventoryError("input_changed_during_read")
    return report, path, digest_after


def write_report(report: dict[str, Any], input_path: Path, output_path: str | os.PathLike[str]) -> Path:
    output = Path(output_path)
    if output.is_symlink():
        raise InventoryError("output_symlink_rejected")
    try:
        parent = output.parent.resolve(strict=True)
        if not parent.is_dir():
            raise InventoryError("output_parent_invalid")
        candidate = parent / output.name
        if candidate.resolve(strict=False) == input_path or candidate.exists():
            raise InventoryError("output_exists_or_matches_input")
    except InventoryError:
        raise
    except OSError as exc:
        raise InventoryError("output_parent_invalid") from exc

    payload = (json.dumps(report, ensure_ascii=False, sort_keys=True, indent=2) + "\n").encode("utf-8")
    fd = -1
    temp_name: str | None = None
    output_created = False
    try:
        fd, temp_name = tempfile.mkstemp(prefix=".legacy-inventory-", dir=parent)
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "wb") as target:
            fd = -1
            target.write(payload)
            target.flush()
            os.fsync(target.fileno())
        # link() creates the final name atomically and fails if it already exists.
        os.link(temp_name, candidate)
        output_created = True
        os.unlink(temp_name)
        temp_name = None
        return candidate
    except FileExistsError as exc:
        raise InventoryError("output_exists_or_matches_input") from exc
    except OSError as exc:
        if output_created:
            try:
                candidate.unlink()
            except OSError:
                pass
        raise InventoryError("report_write_failed") from exc
    finally:
        if fd >= 0:
            os.close(fd)
        if temp_name is not None:
            try:
                os.unlink(temp_name)
            except OSError:
                pass


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Create a privacy-minimized inventory from an explicit SQLite backup.")
    parser.add_argument("--input", required=True, help="path to an independent, closed SQLite backup")
    parser.add_argument("--output", required=True, help="new JSON report path; existing files are never overwritten")
    args = parser.parse_args(argv)
    try:
        report, input_path, _ = create_inventory(args.input)
        output = write_report(report, input_path, args.output)
    except InventoryError as exc:
        # Fixed codes only; no exception text, path, SQL, or row contents.
        print(json.dumps({"error": exc.code}, sort_keys=True), file=sys.stderr)
        return 2
    print(json.dumps({"status": "created", "format_version": FORMAT_VERSION, "report_file_mode": "0600"}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
