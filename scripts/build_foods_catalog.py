# ABOUTME: Builds the embedded food-macros catalog (internal/foods/catalog.json)
# ABOUTME: from USDA FoodData Central SR Legacy (CC0) plus hand-curated IT foods and sports products.
#
# /// script
# requires-python = ">=3.11"
# ///
"""Generate internal/foods/catalog.json for the family-nutrition module.

Source of NUMBERS: USDA FoodData Central, SR Legacy release 2018-04, which is
CC0 1.0 (public domain) — the cleanest license available, redistribution and
embedding explicitly fine. Italian names/synonyms and allergen tags are
hand-authored (food names are not the licensed asset; the composition numbers
are, and those come from CC0 FDC). Sports products and a few Italian staples
absent from SR Legacy are hand-entered from manufacturer labels (label numbers
are facts), each stamped with source + as_of.

All macros are normalized to PER 100 g. Go owns ALL portion arithmetic; a food
that is naturally counted (banana, egg) carries unit_grams so the app can scale
by units without the model ever converting a portion itself.

Usage:
    uv run scripts/build_foods_catalog.py [path-to-sr-legacy-csv-dir]
"""

import csv
import sys
from pathlib import Path
import json

REPO_ROOT = Path(__file__).resolve().parent.parent
OUT_PATH = REPO_ROOT / "internal" / "foods" / "catalog.json"
SR_RELEASE = "2018-04"
DEFAULT_SR_DIR = Path("/tmp/sr_legacy/FoodData_Central_sr_legacy_food_csv_2018-04")
AS_OF = "2026-06"  # passed in, since Date.now() equivalents are avoided here

# FDC nutrient ids (the id column, referenced by food_nutrient.nutrient_id).
NUTRIENT = {
    "kcal": "1008",
    "protein_g": "1003",
    "fat_g": "1004",
    "carb_g": "1005",
    "fiber_g": "1079",
    "sugar_g": "2000",
    "sodium_mg": "1093",
    "caffeine_mg": "1057",
}

# Descriptions we never want the picker to choose: branded, restaurant, prepared
# mixes, baby food, fortified oddities. Keeps matches on canonical generic foods.
GLOBAL_AVOID = [
    "restaurant", "babyfood", "infant", "includes foods for usda",
    "low sodium", "reduced fat", "fat-free", "fat free", "dietetic",
]

# Curated foods sourced from SR Legacy. Each: a stable slug id, the words that
# must ALL appear in the description, words that must NOT, optional explicit fdc
# (pins the row when matching is ambiguous), the Italian name, category,
# allergen tags, optional synonyms, and unit_grams for countable foods.
#
# Allergen vocab: gluten, lactose, milk, egg, nuts, peanuts, soy, fish, shellfish, sesame.
# Lactose nuance: high-lactose dairy carries [milk, lactose]; aged cheese / butter
# carry [milk] only (negligible lactose) so a "lactose"-excluding profile keeps them.
SR_FOODS = [
    # --- Cereali e derivati ---
    dict(id="pasta_secca", fdc="169736", name_it="Pasta di semola (secca)", cat="cereali", allergens=["gluten"], syn=["pasta cruda"]),
    dict(id="pasta_cotta", match="pasta cooked enriched", avoid=["spinach", "corn", "with added salt", "whole"], name_it="Pasta (cotta)", cat="cereali", allergens=["gluten"]),
    dict(id="riso_bianco_crudo", match="rice white long-grain raw enriched", avoid=["instant", "parboiled", "precooked", "salt"], name_it="Riso bianco (crudo)", cat="cereali", allergens=[]),
    dict(id="riso_bianco_cotto", fdc="168878", name_it="Riso bianco (cotto)", cat="cereali", allergens=[]),
    dict(id="riso_integrale_cotto", fdc="169704", name_it="Riso integrale (cotto)", cat="cereali", allergens=[]),
    dict(id="pane_bianco", fdc="174924", name_it="Pane bianco", cat="cereali", allergens=["gluten"]),
    dict(id="pane_integrale", match="bread whole-wheat commercially prepared", avoid=["toasted"], name_it="Pane integrale", cat="cereali", allergens=["gluten"]),
    dict(id="avena", match="oats regular quick not fortified dry", avoid=["cooked"], name_it="Fiocchi d'avena", cat="cereali", allergens=[], syn=["porridge"]),
    dict(id="patate_crude", fdc="170026", name_it="Patate (crude)", cat="cereali", allergens=[]),
    dict(id="patate_lesse", match="potatoes boiled cooked without skin flesh without salt", avoid=["microwave"], name_it="Patate lesse", cat="cereali", allergens=[]),
    dict(id="polenta", match="cornmeal whole-grain yellow", avoid=["degermed", "self-rising"], name_it="Farina di mais (polenta)", cat="cereali", allergens=[]),
    dict(id="couscous_cotto", match="couscous cooked", avoid=[], name_it="Couscous (cotto)", cat="cereali", allergens=["gluten"]),
    dict(id="orzo_perlato_cotto", match="barley pearled cooked", avoid=[], name_it="Orzo perlato (cotto)", cat="cereali", allergens=["gluten"]),
    # --- Frutta ---
    dict(id="banana", fdc="173944", name_it="Banana", cat="frutta", allergens=[], unit_g=118),
    dict(id="mela", match="apples raw with skin", avoid=["dried", "canned", "juice"], name_it="Mela", cat="frutta", allergens=[], unit_g=182),
    dict(id="arancia", match="oranges raw all commercial varieties", avoid=["juice"], name_it="Arancia", cat="frutta", allergens=[], unit_g=131),
    dict(id="pera", match="pears raw", avoid=["dried", "canned", "asian"], name_it="Pera", cat="frutta", allergens=[], unit_g=178),
    dict(id="pesca", match="peaches raw", avoid=["dried", "canned", "frozen"], name_it="Pesca", cat="frutta", allergens=[], unit_g=150),
    dict(id="fragole", match="strawberries raw", avoid=["frozen"], name_it="Fragole", cat="frutta", allergens=[]),
    dict(id="uva", match="grapes red green raw", avoid=["juice", "leaves"], name_it="Uva", cat="frutta", allergens=[]),
    dict(id="kiwi", match="kiwifruit green raw", avoid=["gold"], name_it="Kiwi", cat="frutta", allergens=[], unit_g=69),
    dict(id="anguria", match="watermelon raw", avoid=[], name_it="Anguria", cat="frutta", allergens=[]),
    dict(id="mirtilli", match="blueberries raw", avoid=["frozen", "wild", "canned"], name_it="Mirtilli", cat="frutta", allergens=[]),
    dict(id="datteri", match="dates medjool", avoid=["deglet"], name_it="Datteri (medjool)", cat="frutta", allergens=[], unit_g=24),
    dict(id="uvetta", fdc="168165", name_it="Uvetta", cat="frutta", allergens=[]),  # seedless: has sugar (seeded row is sugar-missing in SR)
    dict(id="albicocche_secche", match="apricots dried sulfured uncooked", avoid=[], name_it="Albicocche secche", cat="frutta", allergens=[]),
    # --- Verdura ---
    dict(id="pomodoro", match="tomatoes red ripe raw year round average", avoid=[], name_it="Pomodoro", cat="verdura", allergens=[]),
    dict(id="carota", match="carrots raw", avoid=["baby"], name_it="Carota", cat="verdura", allergens=[]),
    dict(id="zucchina", match="squash summer zucchini includes skin raw", avoid=[], name_it="Zucchina", cat="verdura", allergens=[]),
    dict(id="spinaci", match="spinach raw", avoid=[], name_it="Spinaci", cat="verdura", allergens=[]),
    dict(id="broccoli", match="broccoli raw", avoid=["chinese", "raab", "leaves"], name_it="Broccoli", cat="verdura", allergens=[]),
    dict(id="fagiolini", match="beans snap green raw", avoid=[], name_it="Fagiolini", cat="verdura", allergens=[]),
    dict(id="peperone", match="peppers sweet red raw", avoid=["freeze-dried"], name_it="Peperone rosso", cat="verdura", allergens=[]),
    dict(id="lattuga", match="lettuce cos or romaine raw", avoid=[], name_it="Lattuga (romana)", cat="verdura", allergens=[]),
    dict(id="cipolla", match="onions raw", avoid=["young", "spring", "welsh", "dehydrated"], name_it="Cipolla", cat="verdura", allergens=[]),
    dict(id="melanzana", match="eggplant raw", avoid=[], name_it="Melanzana", cat="verdura", allergens=[]),
    dict(id="zucca", match="pumpkin raw", avoid=["flowers", "leaves", "canned"], name_it="Zucca", cat="verdura", allergens=[]),
    dict(id="cetriolo", match="cucumber with peel raw", avoid=[], name_it="Cetriolo", cat="verdura", allergens=[]),
    # --- Proteine: carne, pesce, uova, legumi ---
    dict(id="petto_pollo", fdc="171477", name_it="Petto di pollo (cotto)", cat="proteine", allergens=[]),
    dict(id="tacchino", match="turkey breast meat only roasted", avoid=["prepackaged", "sliced"], name_it="Petto di tacchino (cotto)", cat="proteine", allergens=[]),
    dict(id="manzo_macinato", match="beef ground 90% lean meat 10% fat cooked", avoid=["patty"], name_it="Manzo macinato magro (cotto)", cat="proteine", allergens=[]),
    dict(id="maiale_lonza", match="pork fresh loin center rib roasted", avoid=["enhanced", "brine"], name_it="Lonza di maiale (cotta)", cat="proteine", allergens=[]),
    dict(id="uovo", fdc="171287", name_it="Uovo (crudo)", cat="proteine", allergens=["egg"], unit_g=50),
    dict(id="uovo_sodo", match="egg whole cooked hard-boiled", avoid=[], name_it="Uovo sodo", cat="proteine", allergens=["egg"], unit_g=50),
    dict(id="tonno_scatola", fdc="171986", name_it="Tonno in scatola (al naturale)", cat="proteine", allergens=["fish"]),
    dict(id="salmone", match="salmon atlantic farmed cooked dry heat", avoid=[], name_it="Salmone (cotto)", cat="proteine", allergens=["fish"]),
    dict(id="merluzzo", match="cod atlantic cooked dry heat", avoid=["pacific"], name_it="Merluzzo (cotto)", cat="proteine", allergens=["fish"]),
    dict(id="gamberetti", match="crustaceans shrimp cooked", avoid=["imitation", "breaded", "canned"], name_it="Gamberetti (cotti)", cat="proteine", allergens=["shellfish"]),
    dict(id="lenticchie", fdc="172421", name_it="Lenticchie (cotte)", cat="proteine", allergens=[]),
    dict(id="ceci", match="chickpeas garbanzo cooked boiled without salt", avoid=[], name_it="Ceci (cotti)", cat="proteine", allergens=[]),
    dict(id="fagioli", match="beans white mature seeds cooked boiled without salt", avoid=[], name_it="Fagioli bianchi (cotti)", cat="proteine", allergens=[]),
    dict(id="piselli", match="peas green cooked boiled without salt", avoid=["split", "edible-podded"], name_it="Piselli (cotti)", cat="proteine", allergens=[]),
    dict(id="tofu", match="tofu raw firm prepared with calcium sulfate", avoid=["fried", "salted"], name_it="Tofu", cat="proteine", allergens=["soy"]),
    # --- Latticini ---
    dict(id="latte_intero", match="milk whole 3.25% milkfat with added vitamin d", avoid=["chocolate", "dry", "buttermilk"], name_it="Latte intero", cat="latticini", allergens=["milk", "lactose"]),
    dict(id="latte_ps", match="milk lowfat fluid 1% milkfat with added vitamin a and vitamin d", avoid=["chocolate", "protein"], name_it="Latte parzialmente scremato", cat="latticini", allergens=["milk", "lactose"]),
    dict(id="yogurt_bianco", match="yogurt plain whole milk", avoid=["greek", "low", "vanilla"], name_it="Yogurt bianco intero", cat="latticini", allergens=["milk", "lactose"]),
    dict(id="yogurt_greco", fdc="171304", name_it="Yogurt greco intero", cat="latticini", allergens=["milk", "lactose"]),
    dict(id="parmigiano", match="cheese parmesan grated", avoid=["cracker", "chicken", "goldfish", "grill", "imitation", "refrigerated"], name_it="Parmigiano", cat="latticini", allergens=["milk"], syn=["grana", "grana padano"]),
    dict(id="mozzarella", match="cheese mozzarella whole milk", avoid=["low", "part-skim", "string"], name_it="Mozzarella", cat="latticini", allergens=["milk", "lactose"]),
    dict(id="ricotta", match="cheese ricotta whole milk", avoid=["part skim"], name_it="Ricotta", cat="latticini", allergens=["milk", "lactose"]),
    dict(id="burro", match="butter without salt", avoid=["whipped", "oil", "light", "powder"], name_it="Burro", cat="grassi", allergens=["milk"]),
    # --- Grassi e frutta secca ---
    dict(id="olio_oliva", fdc="171413", name_it="Olio extravergine d'oliva", cat="grassi", allergens=[]),
    dict(id="mandorle", match="nuts almonds", avoid=["honey", "roasted", "salt", "oil", "chocolate", "blanched", "paste", "butter", "milk", "smoke"], name_it="Mandorle", cat="grassi", allergens=["nuts"]),
    dict(id="noci", match="nuts walnuts english", avoid=["black", "candied", "glazed"], name_it="Noci", cat="grassi", allergens=["nuts"]),
    dict(id="nocciole", match="nuts hazelnuts or filberts", avoid=["roasted", "blanched"], name_it="Nocciole", cat="grassi", allergens=["nuts"]),
    dict(id="burro_arachidi", match="peanut butter smooth style without salt", avoid=["reduced", "chocolate", "low sodium"], name_it="Burro d'arachidi", cat="grassi", allergens=["peanuts"]),
    dict(id="avocado", match="avocados raw all commercial varieties", avoid=["california", "florida"], name_it="Avocado", cat="grassi", allergens=[], unit_g=150),
    # --- Dolcificanti e condimenti ---
    dict(id="miele", match="honey", avoid=["roasted"], name_it="Miele", cat="dolcificanti", allergens=[]),
    dict(id="zucchero", match="sugars granulated", avoid=["brown", "powdered", "maple"], name_it="Zucchero", cat="dolcificanti", allergens=[]),
    dict(id="marmellata", match="jams and preserves", avoid=["apricot", "dietetic", "reduced", "sodium", "home"], name_it="Marmellata", cat="dolcificanti", allergens=[]),
    dict(id="cola", match="beverages carbonated cola regular", avoid=["low", "diet", "without", "pepper"], name_it="Cola (bevanda)", cat="sport", allergens=[]),
]

# Foods absent from SR Legacy or better entered by hand. Nutrients given PER 100 g
# unless 'serving_g' is set, in which case the per-serving values are normalized to
# 100 g and serving_g becomes unit_grams. source/as_of stamped per row.
MANUAL_FOODS = [
    # Italian staples
    dict(id="bresaola", name_it="Bresaola", name_en="Bresaola (air-dried beef)", cat="proteine", allergens=[],
         per100=dict(kcal=151, protein_g=32.0, fat_g=2.6, carb_g=0.4, fiber_g=0, sugar_g=0, sodium_mg=1600, caffeine_mg=0),
         source="etichetta/INRAN", as_of="2026-06"),
    dict(id="prosciutto_crudo", name_it="Prosciutto crudo", name_en="Prosciutto (dry-cured ham)", cat="proteine", allergens=[],
         per100=dict(kcal=224, protein_g=25.9, fat_g=13.0, carb_g=0.3, fiber_g=0, sugar_g=0, sodium_mg=2570, caffeine_mg=0),
         source="etichetta/INRAN", as_of="2026-06"),
    dict(id="grana_padano", name_it="Grana Padano", name_en="Grana Padano cheese", cat="latticini", allergens=["milk"],
         per100=dict(kcal=398, protein_g=33.0, fat_g=29.0, carb_g=0, fiber_g=0, sugar_g=0, sodium_mg=600, caffeine_mg=0),
         source="etichetta", as_of="2026-06"),
    # Sports nutrition (per serving from manufacturer labels; refreshable, stamp as_of)
    dict(id="gel_sis_go", name_it="Gel SiS GO Isotonic", name_en="SiS GO Isotonic Energy Gel", cat="sport", allergens=[],
         serving_g=60, unit_g=60, serving=dict(kcal=87, protein_g=0, fat_g=0, carb_g=22, fiber_g=0, sugar_g=0.6, sodium_mg=10, caffeine_mg=0),
         source="etichetta SiS", as_of="2026-06", syn=["gel isotonico"]),
    dict(id="gel_maurten_100", name_it="Gel Maurten 100", name_en="Maurten Gel 100", cat="sport", allergens=[],
         serving_g=40, unit_g=40, serving=dict(kcal=100, protein_g=0, fat_g=0, carb_g=25, fiber_g=0, sugar_g=10, sodium_mg=21, caffeine_mg=0),
         source="etichetta Maurten", as_of="2026-06"),
    dict(id="gel_maurten_caf", name_it="Gel Maurten 100 CAF 100", name_en="Maurten Gel 100 CAF 100", cat="sport", allergens=[],
         serving_g=40, unit_g=40, serving=dict(kcal=100, protein_g=0, fat_g=0, carb_g=25, fiber_g=0, sugar_g=10, sodium_mg=21, caffeine_mg=100),
         source="etichetta Maurten", as_of="2026-06"),
    dict(id="gel_gu", name_it="Gel GU Energy", name_en="GU Energy Gel", cat="sport", allergens=[],
         serving_g=32, unit_g=32, serving=dict(kcal=100, protein_g=0, fat_g=0, carb_g=22, fiber_g=0, sugar_g=5, sodium_mg=60, caffeine_mg=20),
         source="etichetta GU", as_of="2026-06"),
    dict(id="gel_enervit", name_it="Gel Enervit Carbo", name_en="Enervit Carbo Gel", cat="sport", allergens=[],
         serving_g=25, unit_g=25, serving=dict(kcal=65, protein_g=0, fat_g=0, carb_g=16, fiber_g=0, sugar_g=6, sodium_mg=40, caffeine_mg=0),
         source="etichetta Enervit", as_of="2026-06"),
    dict(id="barretta_clif", name_it="Barretta Clif Bar", name_en="Clif Bar energy bar", cat="sport", allergens=["soy"],
         serving_g=68, unit_g=68, serving=dict(kcal=250, protein_g=9, fat_g=6, carb_g=44, fiber_g=4, sugar_g=21, sodium_mg=150, caffeine_mg=0),
         source="etichetta Clif", as_of="2026-06", syn=["barretta energetica"]),
    dict(id="maltodestrine", name_it="Maltodestrine (polvere)", name_en="Maltodextrin powder", cat="sport", allergens=[],
         per100=dict(kcal=380, protein_g=0, fat_g=0, carb_g=95, fiber_g=0, sugar_g=2, sodium_mg=0, caffeine_mg=0),
         source="etichetta", as_of="2026-06"),
    dict(id="bevanda_isotonica", name_it="Bevanda isotonica (pronta)", name_en="Isotonic sports drink (ready)", cat="sport", allergens=[],
         per100=dict(kcal=26, protein_g=0, fat_g=0, carb_g=6.4, fiber_g=0, sugar_g=4, sodium_mg=46, caffeine_mg=0),
         source="etichetta", as_of="2026-06", syn=["gatorade", "sport drink"]),
]


def load_sr(sr_dir):
    """Return (foods_by_id, nutrients_by_food). foods: fdc_id -> description."""
    foods = {}
    with open(sr_dir / "food.csv", encoding="utf-8") as f:
        for r in csv.DictReader(f):
            foods[r["fdc_id"]] = r["description"]
    wanted_ids = set(NUTRIENT.values())
    nut = {}
    with open(sr_dir / "food_nutrient.csv", encoding="utf-8") as f:
        for r in csv.DictReader(f):
            if r["nutrient_id"] in wanted_ids:
                nut.setdefault(r["fdc_id"], {})[r["nutrient_id"]] = r["amount"]
    return foods, nut


def pick(foods, spec):
    """Resolve a SR food spec to (fdc_id, description). Explicit fdc wins; else
    pick the shortest description containing all match words and no avoid words."""
    if spec.get("fdc"):
        fid = spec["fdc"]
        if fid not in foods:
            raise SystemExit(f"{spec['id']}: pinned fdc {fid} not in SR Legacy")
        return fid, foods[fid]
    must = spec["match"].lower().split()
    avoid = [a.lower() for a in spec.get("avoid", [])] + GLOBAL_AVOID
    cands = []
    for fid, desc in foods.items():
        d = desc.lower()
        if all(w in d for w in must) and not any(a in d for a in avoid):
            cands.append((len(desc), fid, desc))
    if not cands:
        raise SystemExit(f"{spec['id']}: no SR match for {must!r}")
    cands.sort(key=lambda c: (c[0], c[1]))
    return cands[0][1], cands[0][2]


def num(v):
    try:
        return round(float(v), 2)
    except (TypeError, ValueError):
        return 0.0


def row_from_sr(spec, fid, desc, nutrients):
    n = nutrients.get(fid, {})
    out = dict(
        id=spec["id"], name_en=desc, name_it=spec["name_it"],
        synonyms=spec.get("syn", []), category=spec["cat"],
        allergens=spec.get("allergens", []),
        source="USDA FDC SR Legacy " + SR_RELEASE, source_id="fdc:" + fid, as_of=AS_OF,
    )
    for key, nid in NUTRIENT.items():
        out[key] = num(n.get(nid, 0))
    if spec.get("unit_g"):
        out["unit_grams"] = spec["unit_g"]
    return out


def row_from_manual(spec):
    out = dict(
        id=spec["id"], name_en=spec["name_en"], name_it=spec["name_it"],
        synonyms=spec.get("syn", []), category=spec["cat"],
        allergens=spec.get("allergens", []),
        source=spec["source"], source_id="manual", as_of=spec["as_of"],
    )
    if "per100" in spec:
        for key in NUTRIENT:
            out[key] = num(spec["per100"].get(key, 0))
    else:
        sg = spec["serving_g"]
        for key in NUTRIENT:
            out[key] = round(spec["serving"].get(key, 0) / sg * 100, 2)
    if spec.get("unit_g"):
        out["unit_grams"] = spec["unit_g"]
    return out


def main():
    sr_dir = Path(sys.argv[1]) if len(sys.argv) > 1 else DEFAULT_SR_DIR
    if not (sr_dir / "food.csv").exists():
        sys.exit(f"SR Legacy CSV dir not found at {sr_dir}")
    foods, nutrients = load_sr(sr_dir)

    rows = []
    seen = set()
    for spec in SR_FOODS:
        fid, desc = pick(foods, spec)
        row = row_from_sr(spec, fid, desc, nutrients)
        # Energy must be present, or the row is useless for fueling math.
        if row["kcal"] == 0 and row["carb_g"] == 0 and row["protein_g"] == 0:
            raise SystemExit(f"{spec['id']}: no macros for fdc {fid} ({desc})")
        # Sugar is structurally present for fruit and sweeteners; a zero there
        # means SR left the field empty and num() silently zero-filled it (the
        # "Raisins, seeded" trap). Fail loudly so we pick a populated row.
        if spec["cat"] in ("frutta", "dolcificanti") and row["carb_g"] > 10 and row["sugar_g"] == 0:
            raise SystemExit(f"{spec['id']}: sugar missing for fdc {fid} ({desc}); pick a populated row")
        rows.append(row)
        seen.add(spec["id"])
    for spec in MANUAL_FOODS:
        if spec["id"] in seen:
            raise SystemExit(f"duplicate id {spec['id']}")
        rows.append(row_from_manual(spec))
        seen.add(spec["id"])

    rows.sort(key=lambda r: (r["category"], r["id"]))
    catalog = dict(
        source="USDA FoodData Central SR Legacy " + SR_RELEASE + " (CC0) + hand-curated",
        sr_release=SR_RELEASE, count=len(rows), foods=rows,
    )
    OUT_PATH.parent.mkdir(parents=True, exist_ok=True)
    OUT_PATH.write_text(json.dumps(catalog, ensure_ascii=False, separators=(",", ":")) + "\n", encoding="utf-8")

    size_kb = OUT_PATH.stat().st_size / 1024
    print(f"wrote {OUT_PATH.relative_to(REPO_ROOT)}: {len(rows)} foods, {size_kb:.1f} KB")
    from collections import Counter
    for cat, c in sorted(Counter(r["category"] for r in rows).items()):
        print(f"  {cat:14s} {c}")


if __name__ == "__main__":
    main()
