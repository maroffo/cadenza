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
    dict(id="yogurt_greco_0", fdc="170894", name_it="Yogurt greco 0% grassi", cat="latticini", allergens=["milk", "lactose"], syn=["yogurt greco magro"]),
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

    # --- Estensione "tutti i giorni" (fdc pinnati): verdura ---
    dict(id="cavolfiore", fdc="169986", name_it="Cavolfiore", cat="verdura", allergens=[]),
    dict(id="finocchio", fdc="169385", name_it="Finocchio", cat="verdura", allergens=[]),
    dict(id="sedano", fdc="169988", name_it="Sedano", cat="verdura", allergens=[]),
    dict(id="asparagi", fdc="168389", name_it="Asparagi", cat="verdura", allergens=[]),
    dict(id="funghi", fdc="169251", name_it="Funghi (champignon)", cat="verdura", allergens=[]),
    dict(id="rucola", fdc="169387", name_it="Rucola", cat="verdura", allergens=[]),
    dict(id="radicchio", fdc="168564", name_it="Radicchio", cat="verdura", allergens=[]),
    dict(id="bietola", fdc="169991", name_it="Bietola", cat="verdura", allergens=[]),
    dict(id="verza", fdc="170388", name_it="Verza", cat="verdura", allergens=[]),
    dict(id="cavolo", fdc="169975", name_it="Cavolo cappuccio", cat="verdura", allergens=[]),
    dict(id="porro", fdc="169246", name_it="Porro", cat="verdura", allergens=[]),
    dict(id="aglio", fdc="169230", name_it="Aglio", cat="verdura", allergens=[]),
    dict(id="mais", fdc="169998", name_it="Mais (chicchi)", cat="verdura", allergens=[]),
    # --- frutta ---
    dict(id="limone", fdc="167746", name_it="Limone", cat="frutta", allergens=[], unit_g=58),
    dict(id="ananas", fdc="169124", name_it="Ananas", cat="frutta", allergens=[]),
    dict(id="pompelmo", fdc="174673", name_it="Pompelmo", cat="frutta", allergens=[]),
    dict(id="melone", fdc="169092", name_it="Melone", cat="frutta", allergens=[]),
    dict(id="ciliegie", fdc="171719", name_it="Ciliegie", cat="frutta", allergens=[]),
    dict(id="prugne", fdc="169949", name_it="Prugne", cat="frutta", allergens=[], unit_g=66),
    dict(id="fichi", fdc="173021", name_it="Fichi", cat="frutta", allergens=[], unit_g=50),
    dict(id="lamponi", fdc="167755", name_it="Lamponi", cat="frutta", allergens=[]),
    dict(id="more", fdc="173946", name_it="More", cat="frutta", allergens=[]),
    dict(id="mango", fdc="169910", name_it="Mango", cat="frutta", allergens=[]),
    dict(id="melograno", fdc="169134", name_it="Melograno", cat="frutta", allergens=[]),
    dict(id="cocco", fdc="170169", name_it="Cocco (polpa)", cat="frutta", allergens=["nuts"]),
    # --- proteine: carne, pesce, legumi ---
    dict(id="vitello", fdc="175274", name_it="Vitello (lonza, cotto)", cat="proteine", allergens=[]),
    dict(id="coniglio", fdc="172522", name_it="Coniglio (cotto)", cat="proteine", allergens=[]),
    dict(id="manzo_bistecca", fdc="168635", name_it="Bistecca di manzo magra (cotta)", cat="proteine", allergens=[]),
    dict(id="pollo_coscia", fdc="172388", name_it="Coscia di pollo (cotta)", cat="proteine", allergens=[]),
    dict(id="sgombro", fdc="175120", name_it="Sgombro (cotto)", cat="proteine", allergens=["fish"]),
    dict(id="branzino", fdc="171989", name_it="Branzino (cotto)", cat="proteine", allergens=["fish"]),
    dict(id="orata", fdc="173694", name_it="Orata/spigola (cotta)", cat="proteine", allergens=["fish"]),
    dict(id="sardine", fdc="175139", name_it="Sardine (sott'olio)", cat="proteine", allergens=["fish"]),
    dict(id="salmone_affumicato", fdc="173687", name_it="Salmone affumicato", cat="proteine", allergens=["fish"]),
    dict(id="calamari", fdc="171982", name_it="Calamari (cotti)", cat="proteine", allergens=["shellfish"]),
    dict(id="wurstel", fdc="174614", name_it="Würstel di manzo", cat="proteine", allergens=[]),
    dict(id="fave", fdc="173753", name_it="Fave (cotte)", cat="proteine", allergens=[]),
    dict(id="edamame", fdc="168411", name_it="Edamame", cat="proteine", allergens=["soy"]),
    # --- latticini e alternative vegetali ---
    dict(id="pecorino", fdc="171249", name_it="Pecorino/Romano", cat="latticini", allergens=["milk"]),
    dict(id="gorgonzola", fdc="172175", name_it="Gorgonzola (erborinato)", cat="latticini", allergens=["milk", "lactose"]),
    dict(id="fontina", fdc="170843", name_it="Fontina", cat="latticini", allergens=["milk", "lactose"]),
    dict(id="provolone", fdc="170850", name_it="Provolone", cat="latticini", allergens=["milk", "lactose"]),
    dict(id="feta", fdc="173420", name_it="Feta", cat="latticini", allergens=["milk", "lactose"]),
    dict(id="formaggio_spalmabile", fdc="173418", name_it="Formaggio spalmabile", cat="latticini", allergens=["milk", "lactose"], syn=["philadelphia"]),
    dict(id="yogurt_magro", fdc="170886", name_it="Yogurt magro bianco", cat="latticini", allergens=["milk", "lactose"]),
    dict(id="latte_soia", fdc="173768", name_it="Latte di soia", cat="latticini", allergens=["soy"], syn=["bevanda di soia"]),
    dict(id="latte_mandorla", fdc="174832", name_it="Latte di mandorla", cat="latticini", allergens=["nuts"], syn=["bevanda di mandorla"]),
    dict(id="latte_riso", fdc="171942", name_it="Latte di riso", cat="latticini", allergens=[], syn=["bevanda di riso"]),
    # --- cereali e derivati ---
    dict(id="quinoa", fdc="168917", name_it="Quinoa (cotta)", cat="cereali", allergens=[]),
    dict(id="farro", fdc="169746", name_it="Farro/spelta (cotto)", cat="cereali", allergens=["gluten"]),
    dict(id="grano_saraceno", fdc="170686", name_it="Grano saraceno (cotto)", cat="cereali", allergens=[]),
    dict(id="pane_segale", fdc="172684", name_it="Pane di segale", cat="cereali", allergens=["gluten"]),
    dict(id="fette_biscottate", fdc="174977", name_it="Fette biscottate", cat="cereali", allergens=["gluten"]),
    dict(id="farina_00", fdc="168894", name_it="Farina di frumento (00)", cat="cereali", allergens=["gluten"]),
    dict(id="pane_pita", fdc="174915", name_it="Pane pita", cat="cereali", allergens=["gluten"]),
    dict(id="grissini", fdc="174929", name_it="Grissini", cat="cereali", allergens=["gluten"]),
    dict(id="crackers", fdc="172746", name_it="Crackers", cat="cereali", allergens=["gluten"]),
    # --- frutta secca e semi ---
    dict(id="pinoli", fdc="170591", name_it="Pinoli", cat="grassi", allergens=["nuts"]),
    dict(id="pistacchi", fdc="170184", name_it="Pistacchi", cat="grassi", allergens=["nuts"]),
    dict(id="anacardi", fdc="170162", name_it="Anacardi", cat="grassi", allergens=["nuts"]),
    dict(id="semi_zucca", fdc="170556", name_it="Semi di zucca", cat="grassi", allergens=[]),
    dict(id="semi_girasole", fdc="170562", name_it="Semi di girasole", cat="grassi", allergens=[]),
    dict(id="semi_chia", fdc="170554", name_it="Semi di chia", cat="grassi", allergens=[]),
    # --- bevande ---
    dict(id="caffe", fdc="171890", name_it="Caffè (filtro)", cat="bevande", allergens=[]),

    # --- aggiunte guidate dalle ricette ---
    dict(id="riso_integrale_crudo", fdc="169703", name_it="Riso integrale (crudo)", cat="cereali", allergens=[]),
    dict(id="tonno_olio", fdc="173708", name_it="Tonno sott'olio (sgocciolato)", cat="proteine", allergens=["fish"]),
    dict(id="mozzarella_delattosata", fdc="170845", name_it="Mozzarella delattosata", cat="latticini", allergens=["milk"], syn=["mozzarella senza lattosio"]),
    dict(id="olive_nere", fdc="169094", name_it="Olive nere", cat="grassi", allergens=[]),
    dict(id="mais_scatola", fdc="169214", name_it="Mais in scatola (sgocciolato)", cat="verdura", allergens=[]),
    dict(id="capperi", fdc="172238", name_it="Capperi (sott'aceto)", cat="condimenti", allergens=[]),
    dict(id="maionese", fdc="171009", name_it="Maionese", cat="condimenti", allergens=["egg"]),
    dict(id="tempeh", fdc="172467", name_it="Tempeh (cotto)", cat="proteine", allergens=["soy"]),
    dict(id="fagioli_cannellini", fdc="175204", name_it="Fagioli cannellini in scatola", cat="proteine", allergens=[], syn=["cannellini"]),
    dict(id="albicocche", fdc="171697", name_it="Albicocche (fresche)", cat="frutta", allergens=[], unit_g=35),
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
    # Lactose-free / plant options absent from SR Legacy (relevant to the family)
    dict(id="latte_avena", name_it="Latte di avena", name_en="Oat milk (unsweetened)", cat="latticini", allergens=[],
         per100=dict(kcal=45, protein_g=0.8, fat_g=1.5, carb_g=6.7, fiber_g=0.8, sugar_g=4.0, sodium_mg=40, caffeine_mg=0),
         source="etichetta", as_of="2026-06", syn=["bevanda di avena"]),
    dict(id="latte_delattosato", name_it="Latte delattosato (PS)", name_en="Lactose-free milk (semi-skimmed)", cat="latticini", allergens=["milk"],
         per100=dict(kcal=47, protein_g=3.4, fat_g=1.6, carb_g=4.8, fiber_g=0, sugar_g=4.8, sodium_mg=44, caffeine_mg=0),
         source="etichetta", as_of="2026-06", syn=["latte senza lattosio", "zymil"]),
    dict(id="prosciutto_cotto", name_it="Prosciutto cotto", name_en="Cooked ham (lean)", cat="proteine", allergens=[],
         per100=dict(kcal=107, protein_g=16.6, fat_g=3.8, carb_g=1.5, fiber_g=0, sugar_g=1.0, sodium_mg=1100, caffeine_mg=0),
         source="etichetta/INRAN", as_of="2026-06"),
    dict(id="cioccolato_fondente", name_it="Cioccolato fondente (70-85%)", name_en="Dark chocolate 70-85%", cat="dolcificanti", allergens=[],
         per100=dict(kcal=598, protein_g=7.8, fat_g=42.6, carb_g=45.9, fiber_g=10.9, sugar_g=24.0, sodium_mg=20, caffeine_mg=80),
         source="USDA FDC (generic)", as_of="2026-06"),
    dict(id="passata", name_it="Passata di pomodoro", name_en="Tomato puree/passata", cat="condimenti", allergens=[],
         per100=dict(kcal=38, protein_g=1.6, fat_g=0.2, carb_g=8.6, fiber_g=1.9, sugar_g=5.0, sodium_mg=18, caffeine_mg=0),
         source="etichetta/INRAN", as_of="2026-06"),
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
