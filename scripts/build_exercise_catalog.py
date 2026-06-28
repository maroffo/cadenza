# ABOUTME: Builds the embedded exercise catalog (internal/exercises/catalog.json)
# ABOUTME: from the upstream hasaneyldrm/exercises-dataset, filtered to home equipment.
#
# /// script
# requires-python = ">=3.11"
# ///
"""Generate internal/exercises/catalog.json from the upstream dataset.

Usage:
    uv run scripts/build_exercise_catalog.py [path-to-upstream-repo]

The upstream repo is hasaneyldrm/exercises-dataset (clone it, then point this
script at it). We pin the upstream commit SHA so the embedded GIF URLs are
stable: a rebuild from a newer upstream must be a deliberate SHA bump.
"""

import json
import subprocess
import sys
from pathlib import Path

# Equipment available at the athlete's home (decision 3, see the plan). Anything
# outside this set is dropped: it cannot be performed and would only mislead the
# coach into prescribing it.
HOME_EQUIPMENT = {
    "body weight",
    "band",
    "resistance band",
    "dumbbell",
    "kettlebell",
    "stability ball",
    "medicine ball",
    "roller",
    "bosu ball",
    "wheel roller",
}

REPO_ROOT = Path(__file__).resolve().parent.parent
OUT_PATH = REPO_ROOT / "internal" / "exercises" / "catalog.json"


def upstream_sha(repo: Path) -> str:
    return subprocess.run(
        ["git", "-C", str(repo), "rev-parse", "HEAD"],
        capture_output=True, text=True, check=True,
    ).stdout.strip()


def build(repo: Path) -> dict:
    raw = json.loads((repo / "data" / "exercises.json").read_text(encoding="utf-8"))
    sha = upstream_sha(repo)

    exercises = []
    dropped_equipment = 0
    dropped_empty_it = 0
    for e in raw:
        if e.get("equipment") not in HOME_EQUIPMENT:
            dropped_equipment += 1
            continue
        it = (e.get("instructions") or {}).get("it", "").strip()
        steps_it = [s.strip() for s in (e.get("instruction_steps") or {}).get("it", []) if s.strip()]
        # Validate at build time: a record with no Italian instructions is
        # useless to the coach and is dropped rather than discovered empty at
        # prescription time.
        if not it:
            dropped_empty_it += 1
            continue
        # gif_url is a repo-relative path like "videos/0001-2gPfomN.gif"; we keep
        # the basename and rebuild the raw URL at the pinned SHA in Go.
        gif = Path(e["gif_url"]).name
        exercises.append({
            "id": e["id"],
            "name": e["name"],
            "equipment": e["equipment"],
            "target": e["target"],
            "body_part": e["body_part"],
            "secondary": e.get("secondary_muscles") or [],
            "it": it,
            "steps_it": steps_it,
            "gif": gif,
        })

    # Stable ordering by id so a rebuild is a no-op diff unless the data changed,
    # and so the catalog never depends on upstream array position.
    exercises.sort(key=lambda x: x["id"])

    return {
        "source_repo": "hasaneyldrm/exercises-dataset",
        "source_sha": sha,
        "count": len(exercises),
        "exercises": exercises,
        "_dropped_equipment": dropped_equipment,
        "_dropped_empty_it": dropped_empty_it,
    }


def main() -> None:
    repo = Path(sys.argv[1]) if len(sys.argv) > 1 else Path("/tmp/exercises-dataset")
    if not (repo / "data" / "exercises.json").exists():
        sys.exit(f"upstream dataset not found at {repo} (clone hasaneyldrm/exercises-dataset there)")

    catalog = build(repo)
    dropped_eq = catalog.pop("_dropped_equipment")
    dropped_it = catalog.pop("_dropped_empty_it")

    OUT_PATH.parent.mkdir(parents=True, exist_ok=True)
    OUT_PATH.write_text(
        json.dumps(catalog, ensure_ascii=False, indent=None, separators=(",", ":")) + "\n",
        encoding="utf-8",
    )

    size_kb = OUT_PATH.stat().st_size / 1024
    print(f"wrote {OUT_PATH.relative_to(REPO_ROOT)}")
    print(f"  source_sha: {catalog['source_sha']}")
    print(f"  exercises:  {catalog['count']}")
    print(f"  dropped:    {dropped_eq} (equipment) + {dropped_it} (empty IT)")
    print(f"  size:       {size_kb:.1f} KB")


if __name__ == "__main__":
    main()
