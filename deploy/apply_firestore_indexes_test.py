#!/usr/bin/env python3
"""Unit tests for the fail-closed Firestore index provisioner."""

from __future__ import annotations

import importlib.util
import unittest
from pathlib import Path
from unittest import mock

SCRIPT = Path(__file__).with_name("apply_firestore_indexes.py")
SPEC = importlib.util.spec_from_file_location("apply_firestore_indexes", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
indexes = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(indexes)


class IndexProvisionerTest(unittest.TestCase):
    def setUp(self) -> None:
        self.desired = {
            "collectionGroup": "events_written",
            "queryScope": "COLLECTION",
            "fields": [
                {"fieldPath": "external_id", "order": "ASCENDING"},
                {"fieldPath": "created_at", "order": "DESCENDING"},
            ],
        }

    def test_matching_ignores_only_implicit_name_field(self) -> None:
        current = {
            **self.desired,
            "state": "READY",
            "fields": self.desired["fields"]
            + [{"fieldPath": "__name__", "order": "DESCENDING"}],
        }
        with mock.patch.object(indexes, "list_indexes", return_value=[current]):
            self.assertEqual(
                indexes.matching_indexes("project", "(default)", self.desired),
                [current],
            )

        explicit = {
            **self.desired,
            "fields": self.desired["fields"]
            + [{"fieldPath": "__name__", "order": "ASCENDING"}],
        }
        with mock.patch.object(indexes, "list_indexes", return_value=[current]):
            self.assertEqual(
                indexes.matching_indexes("project", "(default)", explicit), []
            )

    def test_ensure_waits_for_ready_instead_of_accepting_creating(self) -> None:
        states = iter([[{"state": "CREATING"}], [{"state": "READY"}]])
        clock = [0.0]

        def sleep(seconds: float) -> None:
            clock[0] += seconds

        with mock.patch.object(indexes, "matching_indexes", side_effect=lambda *_: next(states)), mock.patch.object(
            indexes, "create"
        ) as create:
            indexes.ensure_ready(
                "project",
                "(default)",
                self.desired,
                timeout=30,
                poll_interval=1,
                monotonic=lambda: clock[0],
                sleep=sleep,
            )
            create.assert_not_called()

    def test_ensure_creates_missing_then_requires_ready(self) -> None:
        states = iter([[], [{"state": "CREATING"}], [{"state": "READY"}]])
        clock = [0.0]

        def sleep(seconds: float) -> None:
            clock[0] += seconds

        with mock.patch.object(indexes, "matching_indexes", side_effect=lambda *_: next(states)), mock.patch.object(
            indexes, "create"
        ) as create:
            indexes.ensure_ready(
                "project",
                "(default)",
                self.desired,
                timeout=30,
                poll_interval=1,
                monotonic=lambda: clock[0],
                sleep=sleep,
            )
            create.assert_called_once()

    def test_ensure_fails_on_needs_repair(self) -> None:
        with mock.patch.object(
            indexes, "matching_indexes", return_value=[{"state": "NEEDS_REPAIR"}]
        ):
            with self.assertRaisesRegex(RuntimeError, "NEEDS_REPAIR"):
                indexes.ensure_ready(
                    "project",
                    "(default)",
                    self.desired,
                    timeout=30,
                    poll_interval=1,
                )


if __name__ == "__main__":
    unittest.main()
