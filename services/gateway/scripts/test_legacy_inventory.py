import hashlib
import importlib.util
import json
import os
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).with_name("legacy-inventory.py")
SPEC = importlib.util.spec_from_file_location("legacy_inventory", SCRIPT)
legacy_inventory = importlib.util.module_from_spec(SPEC)
assert SPEC and SPEC.loader
SPEC.loader.exec_module(legacy_inventory)


def create_fixture(path: Path, *, include_logs: bool = True, include_points: bool = True) -> None:
    db = sqlite3.connect(path)
    db.executescript(
        """
        CREATE TABLE users (
            id INTEGER, username TEXT, password TEXT, display_name TEXT,
            role INTEGER, status INTEGER, email TEXT, quota INTEGER,
            used_quota INTEGER, request_count INTEGER
        );
        CREATE TABLE tokens (
            id INTEGER, user_id INTEGER, `key` TEXT, name TEXT, status INTEGER,
            remain_quota INTEGER, used_quota INTEGER, unlimited_quota INTEGER,
            expired_time INTEGER
        );
        CREATE TABLE redemptions (
            id INTEGER, user_id INTEGER, `key` TEXT, status INTEGER, name TEXT,
            quota INTEGER, created_time INTEGER, redeemed_time INTEGER
        );
        CREATE TABLE options (`key` TEXT, value TEXT);
        INSERT INTO users VALUES
            (1, 'secret-user-name', 'secret-password', 'secret-display', 1, 1, 'secret@example.test', 1200, 700, 3),
            (2, 'deleted-user', 'secret-password-2', 'private-name', 1, 3, 'private@example.test', -4, 9, 1);
        INSERT INTO tokens VALUES
            (10, 1, 'secret-access-key', 'secret-token-name', 1, 500, 80, 0, -1),
            (11, 999, 'another-secret-key', 'hidden-name', 1, 0, 0, 1, 2000000000);
        INSERT INTO redemptions VALUES
            (20, 1, 'secret-redemption-code', 1, 'private-code-name', 250, 100, 0),
            (21, 999, 'redeemed-secret-code', 3, 'private-code-name-2', 10, 101, 102),
            (22, 1, 'disabled-secret-code', 2, 'private-code-name-3', 75, 103, 0);
        INSERT INTO options VALUES
            ('QuotaForNewUser', '1234'),
            ('QuotaPerUnit', '500000'),
            ('DisplayInCurrencyEnabled', 'true'),
            ('TopUpLink', 'https://checkout.invalid/path?token=secret-query-value'),
            ('ModelRatio', '{"private-model-name":1.5}');
        """
    )
    if include_logs:
        db.executescript(
            """
            CREATE TABLE logs (id INTEGER, user_id INTEGER, type INTEGER, quota INTEGER, content TEXT);
            INSERT INTO logs VALUES
                (30, 1, 1, 250, 'private log body'),
                (31, 1, 1, -5, 'private log body 2'),
                (32, 2, 2, 0, 'must-not-hide-null');
            """
        )
    if include_points:
        db.executescript(
            """
            CREATE TABLE point_accounts (
                user_id INTEGER, available_micro INTEGER, held_micro INTEGER, spent_micro INTEGER
            );
            INSERT INTO point_accounts VALUES (1, 2000000, 300000, 900000), (999, -1, 0, 0);
            CREATE TABLE point_holds (id INTEGER, user_id INTEGER, state TEXT, request_body TEXT);
            INSERT INTO point_holds VALUES
                (40, 1, 'held', 'private request'), (41, 999, 'pending', 'secret payload');
            """
        )
    db.execute("PRAGMA user_version=17")
    db.commit()
    db.close()


class LegacyInventoryTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.source = self.root / "backup.sqlite3"
        create_fixture(self.source)

    def tearDown(self) -> None:
        self.temp.cleanup()

    def test_exact_minimized_summary_hash_and_permissions(self) -> None:
        before = hashlib.sha256(self.source.read_bytes()).hexdigest()
        report, input_path, digest = legacy_inventory.create_inventory(self.source)
        self.assertEqual(input_path, self.source.resolve())
        self.assertEqual(digest, before)
        self.assertEqual(report["source"]["bytes"], self.source.stat().st_size)
        self.assertEqual(report["source"]["sqlite_user_version"], 17)
        self.assertEqual(report["users"]["quota_sum"], 1196)
        self.assertEqual(report["users"]["used_quota_sum"], 709)
        self.assertEqual(report["users"]["rows"][1]["status"], 3)
        self.assertEqual(report["tokens"]["count"], 2)
        self.assertFalse(report["tokens"]["rows"][0]["unlimited_quota"])
        self.assertTrue(report["tokens"]["rows"][1]["unlimited_quota"])
        self.assertEqual(report["redemptions"]["enabled_unredeemed_count"], 1)
        self.assertEqual(report["redemptions"]["enabled_unredeemed_quota"], 250)
        self.assertEqual(report["redemptions"]["disabled_unredeemed_count"], 1)
        self.assertEqual(report["redemptions"]["disabled_unredeemed_quota"], 75)
        self.assertEqual(report["redemptions"]["used_count"], 1)
        self.assertEqual(report["logs"]["by_type"], [
            {"type": 1, "count": 2, "quota_sum": 245},
            {"type": 2, "count": 1, "quota_sum": 0},
        ])
        self.assertEqual(report["points"]["accounts"]["available_micro"], 1_999_999)
        self.assertEqual(report["points"]["accounts"]["held_micro"], 300_000)
        self.assertEqual(report["points"]["holds"]["by_state"], {"held": 1, "pending": 1})
        self.assertEqual(report["anomalies"]["negative_financial_findings"], 3)
        self.assertTrue(any(item["kind"] == "token_owner_missing" for item in report["anomalies"]["records"]))
        rendered = json.dumps(report, ensure_ascii=False)
        for secret in ("secret-user-name", "secret-password", "secret-display", "secret@example.test", "secret-access-key", "secret-token-name", "secret-redemption-code", "private log body", "private request", "secret-query-value", "private-model-name"):
            self.assertNotIn(secret, rendered)
        self.assertEqual(hashlib.sha256(self.source.read_bytes()).hexdigest(), before)
        self.assertFalse(any(Path(str(self.source) + suffix).exists() for suffix in legacy_inventory.SIDECAR_SUFFIXES))

        output = self.root / "report.json"
        legacy_inventory.write_report(report, input_path, output)
        self.assertEqual(stat_mode(output), 0o600)
        with self.assertRaises(legacy_inventory.InventoryError):
            legacy_inventory.write_report(report, input_path, output)
        self.assertTrue(output.exists())

    def test_cli_subprocess_writes_private_report_without_exposing_path_or_secrets(self) -> None:
        output = self.root / "cli-report.json"
        result = subprocess.run(
            [sys.executable, str(SCRIPT), "--input", str(self.source), "--output", str(output)],
            check=False,
            capture_output=True,
            text=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn(str(self.source), result.stdout + result.stderr)
        self.assertEqual(stat_mode(output), 0o600)
        content = output.read_text("utf-8")
        for secret in ("secret-password", "secret-access-key", "secret@example.test", "secret-query-value"):
            self.assertNotIn(secret, content)

    def test_optional_tables_are_explicitly_missing(self) -> None:
        source = self.root / "minimal.sqlite3"
        create_fixture(source, include_logs=False, include_points=False)
        report, _, _ = legacy_inventory.create_inventory(source)
        self.assertEqual(report["logs"], {"provided": False, "by_type": None})
        self.assertFalse(report["points"]["accounts"]["exists"])
        self.assertFalse(report["points"]["holds"]["exists"])
        self.assertFalse(report["options"]["missing_keys"] == [])

    def test_bad_schema_values_and_optional_views_fail_without_report(self) -> None:
        db = sqlite3.connect(self.source)
        db.execute("UPDATE options SET value='NaN' WHERE `key`='QuotaPerUnit'")
        db.commit()
        db.close()
        with self.assertRaises(legacy_inventory.InventoryError) as caught:
            legacy_inventory.create_inventory(self.source)
        self.assertEqual(caught.exception.code, "billing_option_value_invalid")

        view_db = self.root / "view.sqlite3"
        db = sqlite3.connect(view_db)
        db.executescript("CREATE TABLE users(id,role,status,quota,used_quota,request_count); CREATE TABLE tokens(id,user_id,status,remain_quota,used_quota,unlimited_quota,expired_time); CREATE TABLE redemptions(id,user_id,status,quota,created_time,redeemed_time); CREATE VIEW logs AS SELECT 1 AS id, 1 AS type, 0 AS quota;")
        db.close()
        with self.assertRaises(legacy_inventory.InventoryError) as caught:
            legacy_inventory.create_inventory(view_db)
        self.assertEqual(caught.exception.code, "optional_table_must_be_physical_table")

    def test_sidecars_wal_and_existing_or_same_output_fail_closed(self) -> None:
        sidecar = Path(str(self.source) + "-wal")
        sidecar.write_bytes(b"fixture")
        with self.assertRaises(legacy_inventory.InventoryError) as caught:
            legacy_inventory.create_inventory(self.source)
        self.assertEqual(caught.exception.code, "sqlite_sidecar_present")
        sidecar.unlink()

        wal_path = self.root / "wal.sqlite3"
        create_fixture(wal_path)
        db = sqlite3.connect(wal_path)
        self.assertEqual(db.execute("PRAGMA journal_mode=WAL").fetchone()[0].lower(), "wal")
        db.execute("PRAGMA wal_checkpoint(TRUNCATE)")
        db.close()
        self.assertFalse(any(Path(str(wal_path) + suffix).exists() for suffix in legacy_inventory.SIDECAR_SUFFIXES))
        self.assertEqual(wal_path.read_bytes()[18:20], b"\x02\x02")
        with self.assertRaises(legacy_inventory.InventoryError) as caught:
            legacy_inventory.create_inventory(wal_path)
        self.assertEqual(caught.exception.code, "sqlite_must_use_rollback_journal")
        self.assertFalse(any(Path(str(wal_path) + suffix).exists() for suffix in legacy_inventory.SIDECAR_SUFFIXES))

        with self.assertRaises(legacy_inventory.InventoryError):
            legacy_inventory.write_report({}, self.source.resolve(), self.source)

    def test_bad_rows_duplicate_ids_and_failure_do_not_write_output(self) -> None:
        db = sqlite3.connect(self.source)
        db.execute("UPDATE users SET quota='not-an-integer' WHERE id=1")
        db.commit()
        db.close()
        with self.assertRaises(legacy_inventory.InventoryError) as caught:
            legacy_inventory.create_inventory(self.source)
        self.assertEqual(caught.exception.code, "non_integer_financial_value")
        output = self.root / "absent.json"
        self.assertFalse(output.exists())
        cli = subprocess.run(
            [sys.executable, str(SCRIPT), "--input", str(self.source), "--output", str(output)],
            check=False,
            capture_output=True,
            text=True,
        )
        self.assertEqual(cli.returncode, 2)
        self.assertIn('"error": "non_integer_financial_value"', cli.stderr)
        self.assertNotIn("not-an-integer", cli.stderr)
        self.assertFalse(output.exists())

        duplicate = self.root / "duplicate.sqlite3"
        db = sqlite3.connect(duplicate)
        db.executescript("CREATE TABLE users(id,role,status,quota,used_quota,request_count); INSERT INTO users VALUES(1,1,1,1,1,1),(1,1,1,2,2,2); CREATE TABLE tokens(id,user_id,status,remain_quota,used_quota,unlimited_quota,expired_time); CREATE TABLE redemptions(id,user_id,status,quota,created_time,redeemed_time);")
        db.close()
        with self.assertRaises(legacy_inventory.InventoryError) as caught:
            legacy_inventory.create_inventory(duplicate)
        self.assertEqual(caught.exception.code, "duplicate_user_id")

    def test_python_integer_totals_can_exceed_sqlite_int64_without_overflow(self) -> None:
        maximum = 2**63 - 1
        db = sqlite3.connect(self.source)
        db.execute("UPDATE users SET quota=? WHERE id=1", (maximum,))
        db.execute("UPDATE users SET quota=? WHERE id=2", (maximum,))
        db.commit()
        db.close()
        report, _, _ = legacy_inventory.create_inventory(self.source)
        self.assertEqual(report["users"]["quota_sum"], maximum * 2)

    def test_input_change_during_read_is_rejected(self) -> None:
        original = legacy_inventory._build_report

        def build_then_mutate(connection, digest, size):
            report = original(connection, digest, size)
            with self.source.open("ab") as changed:
                changed.write(b"mutation")
            return report

        with mock.patch.object(legacy_inventory, "_build_report", side_effect=build_then_mutate):
            with self.assertRaises(legacy_inventory.InventoryError) as caught:
                legacy_inventory.create_inventory(self.source)
        self.assertEqual(caught.exception.code, "input_changed_during_read")

    def test_log_null_or_duplicate_and_unknown_hold_state_fail_closed(self) -> None:
        db = sqlite3.connect(self.source)
        db.execute("UPDATE logs SET quota=NULL WHERE id=30")
        db.commit()
        db.close()
        with self.assertRaises(legacy_inventory.InventoryError) as caught:
            legacy_inventory.create_inventory(self.source)
        self.assertEqual(caught.exception.code, "non_integer_financial_value")

        db = sqlite3.connect(self.source)
        db.execute("UPDATE logs SET id=31, quota=0 WHERE id=30")
        db.commit()
        db.close()
        with self.assertRaises(legacy_inventory.InventoryError) as caught:
            legacy_inventory.create_inventory(self.source)
        self.assertEqual(caught.exception.code, "duplicate_log_id")

        db = sqlite3.connect(self.source)
        db.execute("UPDATE logs SET id=rowid")
        db.execute("UPDATE point_holds SET state='private-secret-state' WHERE id=40")
        db.commit()
        db.close()
        with self.assertRaises(legacy_inventory.InventoryError) as caught:
            legacy_inventory.create_inventory(self.source)
        self.assertEqual(caught.exception.code, "invalid_point_hold_state")

    def test_decimal_option_preserves_digits_beyond_default_precision(self) -> None:
        exact = "1.123456789012345678901234567890123456789"
        db = sqlite3.connect(self.source)
        db.execute("DELETE FROM options WHERE `key`='QuotaPerUnit'")
        db.execute("INSERT INTO options VALUES ('QuotaPerUnit', ?)", (exact,))
        db.commit()
        db.close()
        report, _, _ = legacy_inventory.create_inventory(self.source)
        self.assertEqual(report["options"]["values"]["QuotaPerUnit"], exact)


def stat_mode(path: Path) -> int:
    return os.stat(path).st_mode & 0o777


if __name__ == "__main__":
    unittest.main()
