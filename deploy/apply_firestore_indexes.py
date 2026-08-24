#!/usr/bin/env python3
"""Apply firestore.indexes.json with gcloud and wait until each index is READY."""

from __future__ import annotations

import argparse
import json
import subprocess
import time
from pathlib import Path
from typing import Any, Callable

READY = "READY"
BROKEN_STATES = {"NEEDS_REPAIR", "DELETING"}


def run_json(args: list[str]) -> Any:
    proc = subprocess.run(args, text=True, capture_output=True)
    if proc.returncode != 0:
        detail = (proc.stderr or proc.stdout).strip() or "no command output"
        raise RuntimeError(
            f"{args[0]} command failed with exit {proc.returncode}: {detail}"
        )
    return json.loads(proc.stdout or "[]")


def normalized_fields(
    index: dict[str, Any], *, drop_implicit_name: bool = False
) -> list[tuple[str, str]]:
    fields: list[tuple[str, str]] = []
    for field in index.get("fields", []):
        path = str(field.get("fieldPath", ""))
        if drop_implicit_name and path == "__name__":
            continue
        if "order" in field:
            mode = str(field["order"]).upper()
        else:
            mode = str(field.get("arrayConfig", "")).upper()
        fields.append((path, mode))
    return fields


def normalize_scope(value: str) -> str:
    return value.replace("-", "_").upper()


def list_indexes(project: str, database: str, group: str) -> list[dict[str, Any]]:
    result = run_json(
        [
            "gcloud",
            "firestore",
            "indexes",
            "composite",
            "list",
            f"--project={project}",
            f"--database={database}",
            f"--filter=COLLECTION_GROUP:{group}",
            "--format=json",
        ]
    )
    if not isinstance(result, list):
        raise ValueError("gcloud index list returned a non-list JSON value")
    return result


def matching_indexes(
    project: str, database: str, desired: dict[str, Any]
) -> list[dict[str, Any]]:
    group = str(desired["collectionGroup"])
    desired_fields = normalized_fields(desired)
    desired_has_name = any(path == "__name__" for path, _ in desired_fields)
    desired_scope = normalize_scope(str(desired.get("queryScope", "COLLECTION")))
    matches: list[dict[str, Any]] = []
    for item in list_indexes(project, database, group):
        if normalize_scope(str(item.get("queryScope", "COLLECTION"))) != desired_scope:
            continue
        current_fields = normalized_fields(
            item, drop_implicit_name=not desired_has_name
        )
        if current_fields == desired_fields:
            matches.append(item)
    return matches


def create(project: str, database: str, desired: dict[str, Any]) -> None:
    scope = str(desired.get("queryScope", "COLLECTION")).lower().replace("_", "-")
    args = [
        "gcloud",
        "firestore",
        "indexes",
        "composite",
        "create",
        f"--project={project}",
        f"--database={database}",
        f"--collection-group={desired['collectionGroup']}",
        f"--query-scope={scope}",
        "--quiet",
    ]
    for field in desired.get("fields", []):
        path = field["fieldPath"]
        if "order" in field:
            args.append(f"--field-config=field-path={path},order={field['order'].lower()}")
        elif "arrayConfig" in field:
            args.append(
                f"--field-config=field-path={path},array-config={field['arrayConfig'].lower()}"
            )
        else:
            raise ValueError(f"index field {path!r} has neither order nor arrayConfig")
    proc = subprocess.run(args, text=True, capture_output=True)
    if proc.returncode == 0:
        return
    combined = f"{proc.stdout}\n{proc.stderr}".upper()
    # A concurrent idempotent setup/deploy may win between list and create.
    # Re-listing below is the source of truth, so ALREADY_EXISTS is harmless.
    if "ALREADY_EXISTS" in combined or "ALREADY EXISTS" in combined:
        return
    raise subprocess.CalledProcessError(
        proc.returncode, args, output=proc.stdout, stderr=proc.stderr
    )


def ensure_ready(
    project: str,
    database: str,
    desired: dict[str, Any],
    *,
    timeout: float,
    poll_interval: float,
    monotonic: Callable[[], float] = time.monotonic,
    sleep: Callable[[float], None] = time.sleep,
) -> None:
    deadline = monotonic() + timeout
    create_requested = False
    label = str(desired["collectionGroup"])
    while True:
        matches = matching_indexes(project, database, desired)
        states = {str(item.get("state", "STATE_UNSPECIFIED")).upper() for item in matches}
        if READY in states:
            print(f"firestore index {label}: READY")
            return
        broken = states & BROKEN_STATES
        if broken:
            raise RuntimeError(
                f"firestore index {label} is unusable: {', '.join(sorted(broken))}"
            )
        if not matches and not create_requested:
            print(f"firestore index {label}: creating")
            create(project, database, desired)
            create_requested = True
        elif matches:
            print(
                f"firestore index {label}: waiting for "
                f"{', '.join(sorted(states)) or 'state'}"
            )
        if monotonic() >= deadline:
            state_text = ", ".join(sorted(states)) if states else "not visible"
            raise TimeoutError(
                f"firestore index {label} did not become READY within {timeout:.0f}s "
                f"(last state: {state_text})"
            )
        sleep(poll_interval)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--project", required=True)
    parser.add_argument("--database", default="(default)")
    parser.add_argument(
        "--file",
        type=Path,
        default=Path(__file__).resolve().parents[1] / "firestore.indexes.json",
    )
    parser.add_argument("--wait-timeout", type=float, default=20 * 60)
    parser.add_argument("--poll-interval", type=float, default=10)
    args = parser.parse_args()
    if args.wait_timeout <= 0 or args.poll_interval <= 0:
        parser.error("wait timeout and poll interval must be positive")

    config = json.loads(args.file.read_text(encoding="utf-8"))
    for index in config.get("indexes", []):
        ensure_ready(
            args.project,
            args.database,
            index,
            timeout=args.wait_timeout,
            poll_interval=args.poll_interval,
        )


if __name__ == "__main__":
    main()
