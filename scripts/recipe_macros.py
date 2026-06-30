# ABOUTME: Prototype/validator for the nutrition schema: foods -> recipes -> meals.
# ABOUTME: Resolves units to grams and derives macros + allergens at every layer.
#
# /// script
# requires-python = ">=3.11"
# dependencies = ["pyyaml"]
# ///
"""Executable spec for the future Go `recipes` engine.

Three layers, macros/allergens DERIVED (never stored):
  Food (catalog, per 100g) -> Recipe (N foods) -> Meal (N recipes + M foods).
For a family meal the 53/47 adult/child split applies to the meal total.

Usage: uv run scripts/recipe_macros.py [month 1-12]   (month enables seasonal ranking)
"""

import json
import sys
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parent.parent
KEYS = ["kcal", "carb_g", "protein_g", "fat_g", "fiber_g", "sugar_g", "sodium_mg", "caffeine_mg"]
ADULT_SHARE, CHILD_SHARE = 0.265, 0.235  # 53% / 2 adults, 47% / 2 children

SEASON_OF_MONTH = {12: "inverno", 1: "inverno", 2: "inverno",
                   3: "primavera", 4: "primavera", 5: "primavera",
                   6: "estate", 7: "estate", 8: "estate",
                   9: "autunno", 10: "autunno", 11: "autunno"}


def in_season(recipe, season):
    seasons = recipe.get("stagioni")
    return (not seasons) or (season in seasons)


def grams(ing, food, misure):
    unit = ing.get("unita", "g")
    qta = float(ing.get("qta", 0))
    if unit in ("g", "ml"):
        return qta
    if unit == "qb":
        return 0.0
    if unit == "pz":
        ug = food.get("unit_grams", 0)
        if not ug:
            raise ValueError(f"{ing['food']}: unità 'pz' ma manca unit_grams")
        return qta * ug
    m = misure.get(unit)
    if not m:
        raise ValueError(f"unità sconosciuta: {unit}")
    per_food = m.get("per_food", {}).get(ing["food"])
    return qta * (per_food if per_food is not None else m["default_g"])


def add(tot, food, g):
    for k in KEYS:
        tot[k] += food[k] * g / 100


def recipe_totals(r, by, misure):
    """Total macros + allergens for the WHOLE recipe (all its porzioni)."""
    tot = {k: 0.0 for k in KEYS}
    allerg, flags = set(), []
    for ing in r["ingredienti"]:
        food = by.get(ing["food"])
        if not food:
            flags.append(f"MANCA: {ing['food']}")
            continue
        g = grams(ing, food, misure)
        if ing.get("unita") == "qb":
            flags.append(f"{food['name_it']}: q.b. (0 nei macro)")
        add(tot, food, g)
        allerg |= set(food["allergens"])
    return tot, allerg, flags


def meal_totals(m, recipes_by_id, by, misure):
    tot = {k: 0.0 for k in KEYS}
    allerg, flags = set(), []
    for ref in m.get("ricette", []):
        r = recipes_by_id.get(ref["ricetta"])
        if not r:
            flags.append(f"MANCA ricetta: {ref['ricetta']}")
            continue
        rt, ra, rf = recipe_totals(r, by, misure)
        per_serv = {k: rt[k] / r.get("porzioni", 1) for k in KEYS}
        n = float(ref.get("porzioni", r.get("porzioni", 1)))
        for k in KEYS:
            tot[k] += per_serv[k] * n
        allerg |= ra
        flags += rf
    for ing in m.get("cibi", []):
        food = by.get(ing["food"])
        if not food:
            flags.append(f"MANCA cibo: {ing['food']}")
            continue
        add(tot, food, grams(ing, food, misure))
        allerg |= set(food["allergens"])
    return tot, allerg, flags


def fmt(tot, div=1.0):
    t = {k: tot[k] / div for k in KEYS}
    return (f"{t['kcal']:.0f} kcal | carbo {t['carb_g']:.1f}g | prot {t['protein_g']:.1f}g | "
            f"grassi {t['fat_g']:.1f}g | fibra {t['fiber_g']:.1f}g | Na {t['sodium_mg']:.0f}mg")


def main():
    catalog = json.loads((ROOT / "internal/foods/catalog.json").read_text())
    by = {f["id"]: f for f in catalog["foods"]}
    doc = yaml.safe_load((ROOT / "internal/recipes/recipes.yaml").read_text())
    misure = doc.get("misure", {})
    recipes = doc.get("ricette", [])
    recipes_by_id = {r["id"]: r for r in recipes}

    season = None
    if len(sys.argv) > 1 and sys.argv[1].isdigit():
        season = SEASON_OF_MONTH[int(sys.argv[1])]
        print(f"[stagione: {season} -> di stagione prima]")
        recipes = sorted(recipes, key=lambda r: 0 if in_season(r, season) else 1)

    problems = 0
    print("\n### RICETTE ###")
    for r in recipes:
        tot, allerg, flags = recipe_totals(r, by, misure)
        problems += sum(1 for f in flags if f.startswith("MANCA"))
        serv = r.get("porzioni", 1)
        stag = ",".join(r.get("stagioni", [])) or "tutto l'anno"
        mark = ("  [DI STAGIONE]" if in_season(r, season) else "  [fuori stagione]") if season else ""
        print(f"\n== {r['nome']} ({r.get('categoria','?')}, {serv} porz., {stag}){mark} ==")
        print(f"   per porzione: {fmt(tot, serv)}")
        print(f"   allergeni: {sorted(allerg) or 'nessuno'} | lattosio: {'SI' if 'lactose' in allerg else 'no'}")
        for f in flags:
            print(f"   ! {f}")

    print("\n### PASTI ###")
    for m in doc.get("pasti", []):
        tot, allerg, flags = meal_totals(m, recipes_by_id, by, misure)
        problems += sum(1 for f in flags if f.startswith("MANCA"))
        print(f"\n== {m['nome']} ({m.get('tipo','?')}) ==")
        print(f"   totale pasto: {fmt(tot)}")
        print(f"   adulto: {fmt(tot, 1/ADULT_SHARE)}")
        print(f"   bambino: {fmt(tot, 1/CHILD_SHARE)}")
        print(f"   allergeni: {sorted(allerg) or 'nessuno'} | lattosio: {'SI' if 'lactose' in allerg else 'no'}")
        for f in flags:
            print(f"   ! {f}")

    if problems:
        sys.exit(f"\n{problems} riferimenti non risolti")
    print(f"\nOK: {len(recipes)} ricette, {len(doc.get('pasti', []))} pasti, catalogo {catalog['count']} alimenti")


if __name__ == "__main__":
    main()
