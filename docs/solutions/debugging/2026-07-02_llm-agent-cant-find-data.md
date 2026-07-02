# ABOUTME: Debugging playbook for "the LLM agent can't find data that is definitely there".
# ABOUTME: Symptom is tool-surface/filter/routing logic, almost never the storage layer.

# Problem

An LLM-with-tools agent (here: the Cadenza coach) repeatedly reported that data
"wasn't there" or "wasn't synced" — a recipe, the user's breakfast, the full
recipe list — and the user concluded the app couldn't read its database. It could.
The data was embedded, deployed, and Load-validated every time.

Three distinct root causes, all in the tool layer:
1. A safety filter (family lactose exclusion) was applied too broadly and hid a
   valid item (the athlete's own recipe).
2. The only lookup tool filtered by category/season — no by-name search — so a
   direct "do you have dish X?" couldn't be answered.
3. The suggestion tool capped at a season-ranked top-N, so a "list everything"
   request silently omitted off-season items.

# Solution

1. **Prove the data is present in seconds** — a deterministic audit (grep the
   embedded asset; run the loader's validation) settles "is it there?" before
   touching anything. Don't trust the agent's narration ("non risulta
   sincronizzato") — that's a rationalization, not a diagnosis.
2. **Fix the tool surface, not the storage:**
   - add a by-name lookup (`query`) that searches the whole dataset and bypasses
     "safe-suggestion" filters (a direct request should surface the item + its
     labels, not hide it);
   - add a no-limit `list_*` tool for "show me everything," distinct from the
     ranked/capped "suggest" tool;
   - scope safety filters to the audience they protect (family-meal exclusion ≠
     the user's personal items).
3. **Tell the model when to use which tool** in the system prompt ("for 'is X in
   the book?' always use query; for 'the list' use list_*"). Tool existence isn't
   enough; routing must be taught.

# Why It Works

An LLM agent's "not found" is a function of *which tool it called and how that
tool filtered*, not of whether the bytes exist. Capped, ranked, and safety-
filtered "suggest" tools are the right default for open-ended asks but the wrong
tool for exact lookups and full listings — so you need distinct tools with clear
routing, and a fast deterministic audit to keep the debugging honest.
