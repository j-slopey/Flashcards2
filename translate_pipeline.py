"""
Generate English translations + Spanish cognate/false-friend flags for the
Nuovo Vocabolario di Base word list, using the Gemini API with structured output.

Granularity
-----------
Translations are keyed per (word, broad POS category), NOT per word. The source
list explodes via ``di_base.build_exploded_df`` so that e.g. "abbandonato"
(tagged "p.pass., agg., s.m.") is translated separately as a participle, as an
adjective, and as a noun -- the same spelling can need a different English gloss
depending on the part of speech.

Pipeline features
-----------------
- Pydantic schema -> Gemini returns typed objects (no fragile JSON parsing).
- Batched (default 25 pairs/call) to amortize latency/cost.
- Checkpointed to a JSONL file: rerun = resume, already-done pairs are skipped.
- Self-healing: any pair the model drops/renames in a batch is re-queued.
- Final step merges the translations into the exploded table and writes a
  SQLite database (see build_db.py).

Setup
-----
    pip install google-genai pydantic pandas
    export GEMINI_API_KEY="...your key from aistudio.google.com/apikey..."

Run
---
    python translate_pipeline.py                 # all levels, real API
    python translate_pipeline.py --dry-run       # offline mock, no key needed
    python translate_pipeline.py --levels Fondamentale "Alto Uso"
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
from enum import Enum
from pathlib import Path

import pandas as pd
from pydantic import BaseModel, Field

from di_base import build_exploded_df

# google-genai is only imported inside the API path so the offline/mock tests
# (and --dry-run) work without the package or a key present.


# ---------------------------------------------------------------------------
# 1. Output schema
# ---------------------------------------------------------------------------
class SpanishRelation(str, Enum):
    cognate = "cognate"          # Spanish look-alike with a similar meaning (helpful)
    false_friend = "false_friend"  # Spanish look-alike with a different meaning (warn!)
    none = "none"                # no useful resemblance -> card shows nothing


class WordEntry(BaseModel):
    # Both fields are echoed back verbatim so we can match each result to its
    # input pair via (italian, pos_category).
    italian: str = Field(description="The Italian headword, copied exactly from input.")
    pos_category: str = Field(
        description="The broad part-of-speech category, copied exactly from input "
                    "(e.g. 'noun', 'verb', 'adjective')."
    )

    english: list[str] = Field(
        description="1-3 common English translations/senses appropriate to THIS part "
                    "of speech, most frequent first. Lowercase unless a proper noun."
    )

    spanish_relation: SpanishRelation = Field(
        description="How the Italian word relates to a similar-looking Spanish word."
    )
    spanish_word: str = Field(
        default="",
        description="The relevant Spanish word when relation is cognate or false_friend; "
                    "empty string when relation is none."
    )
    spanish_note: str = Field(
        default="",
        description="Short learner note. For false_friend, state the Spanish meaning that "
                    "differs. For cognate, optional mnemonic. Empty when relation is none."
    )


SYSTEM_INSTRUCTION = """\
You are a bilingual Italian lexicographer who also knows Spanish well.

For each Italian headword you are given, you are told the specific PART OF SPEECH
to translate it as (plus the raw dictionary tag for context). Produce:

1. english: 1-3 of the MOST COMMON English translations for the Italian word USED
   AS THAT PART OF SPEECH, ordered most-frequent first. Use the part of speech to
   pick the right sense (e.g. "abbandonato" as a noun = "abandoned person/foundling",
   as an adjective = "abandoned, deserted", as a past participle = "abandoned").
   For verbs, give the English infinitive ("to ..."). Keep them short and
   lowercase unless a proper noun.

2. A Spanish relationship judged against the way a Spanish speaker would read
   the Italian word's FORM:
   - "cognate": a similar-looking Spanish word exists AND shares the core meaning.
     This includes regular correspondences (-zione/-cion, -ta/-dad, etc.).
   - "false_friend": a similar-looking Spanish word exists BUT means something
     notably different (e.g. it. "burro" butter vs es. "burro" donkey;
     it. "salire" to go up vs es. "salir" to leave). Be strict: only flag real,
     well-known false friends a learner would actually confuse.
   - "none": no useful resemblance, OR the resemblance is too weak to help.

3. spanish_word: the relevant Spanish word for cognate/false_friend, else "".
4. spanish_note: for false_friend, briefly give the Spanish meaning that differs.
   For cognate, an optional one-line mnemonic. For none, "".

Echo the italian and pos_category fields back EXACTLY as given so results can be
matched. Translate every pair in the list. Do not add or omit entries.
"""


# ---------------------------------------------------------------------------
# 2. Checkpoint helpers (JSONL: one WordEntry per line)
# ---------------------------------------------------------------------------
def _key(italian: str, pos_category: str) -> tuple[str, str]:
    return italian.strip().lower(), pos_category.strip().lower()


def load_done(checkpoint_path: Path) -> dict[tuple[str, str], dict]:
    """Return {(italian_lower, pos_category_lower): record} for saved pairs."""
    done: dict[tuple[str, str], dict] = {}
    if checkpoint_path.exists():
        with checkpoint_path.open(encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    rec = json.loads(line)
                    done[_key(rec["italian"], rec["pos_category"])] = rec
                except (json.JSONDecodeError, KeyError):
                    continue  # skip a corrupt/partial/old-schema line
    return done


def append_records(checkpoint_path: Path, records: list[dict]) -> None:
    with checkpoint_path.open("a", encoding="utf-8") as f:
        for rec in records:
            f.write(json.dumps(rec, ensure_ascii=False) + "\n")


# ---------------------------------------------------------------------------
# 3. Build the (word, pos_category) work list from the exploded vocabulary
# ---------------------------------------------------------------------------
def build_work_items(csv_path: str, levels=None) -> list[dict]:
    """One dict per unique (word, pos_category): {'italian','pos_category','raw_pos'}.

    Rows whose category is missing or UNKNOWN (an unmapped POS tag) are skipped
    -- there is nothing reliable to translate them as.
    """
    df = pd.read_csv(csv_path)
    ex = build_exploded_df(df, levels=levels)

    items: list[dict] = []
    seen: set[tuple[str, str]] = set()
    for _, r in ex.iterrows():
        cat = r["pos_category"]
        if pd.isna(cat) or str(cat).startswith("UNKNOWN"):
            continue
        word = str(r["word"])
        k = _key(word, str(cat))
        if k in seen:
            continue
        seen.add(k)
        items.append({
            "italian": word,
            "pos_category": str(cat),
            "raw_pos": "" if pd.isna(r["part of speech"]) else str(r["part of speech"]),
        })
    return items


# ---------------------------------------------------------------------------
# 4. Batching
# ---------------------------------------------------------------------------
def chunked(seq, size):
    for i in range(0, len(seq), size):
        yield seq[i:i + size]


def build_prompt(batch: list[dict]) -> str:
    """batch: list of {'italian','pos_category','raw_pos'}. Schema is NOT included."""
    lines = [
        f"{i+1}. {it['italian']}  —  translate as {it['pos_category']}"
        f"  (dictionary tag: {it['raw_pos'] or 'n/a'})"
        for i, it in enumerate(batch)
    ]
    return (
        "Translate the following Italian headwords. Each line gives the part of "
        "speech to translate the word AS.\n\n" + "\n".join(lines)
    )


# ---------------------------------------------------------------------------
# 5. The Gemini call (real) and a mock (for offline testing)
# ---------------------------------------------------------------------------
def call_gemini(client, model: str, batch: list[dict]) -> list[WordEntry]:
    from google.genai import types

    resp = client.models.generate_content(
        model=model,
        contents=build_prompt(batch),
        config=types.GenerateContentConfig(
            system_instruction=SYSTEM_INSTRUCTION,
            response_mime_type="application/json",
            response_schema=list[WordEntry],
            temperature=0.2,
        ),
    )
    # When response_schema is a pydantic type, .parsed gives typed objects.
    parsed = resp.parsed
    if parsed is None:
        # Fall back to manual parse if .parsed didn't populate.
        parsed = [WordEntry(**d) for d in json.loads(resp.text)]
    return parsed


def call_mock(client, model: str, batch: list[dict]) -> list[WordEntry]:
    """Deterministic fake used by --dry-run and the offline test below."""
    out = []
    for item in batch:
        w = item["italian"].lower()
        cat = item["pos_category"]
        if w == "burro":
            out.append(WordEntry(italian=item["italian"], pos_category=cat,
                                  english=["butter"], spanish_relation=SpanishRelation.false_friend,
                                  spanish_word="burro", spanish_note="Spanish 'burro' means donkey."))
        elif w == "nazione":
            out.append(WordEntry(italian=item["italian"], pos_category=cat,
                                  english=["nation"], spanish_relation=SpanishRelation.cognate,
                                  spanish_word="nación", spanish_note="-zione <-> -cion."))
        else:
            out.append(WordEntry(italian=item["italian"], pos_category=cat,
                                  english=[f"<{w}:{cat}>"], spanish_relation=SpanishRelation.none))
    return out


# ---------------------------------------------------------------------------
# 6. Main driver
# ---------------------------------------------------------------------------
def run(items: list[dict], checkpoint: Path, caller, client, model: str,
        batch_size: int = 25, max_retries: int = 4) -> None:
    done = load_done(checkpoint)
    todo = [it for it in items if _key(it["italian"], it["pos_category"]) not in done]
    print(f"{len(done)} already done, {len(todo)} to process.")

    for batch in chunked(todo, batch_size):
        wanted = {_key(it["italian"], it["pos_category"]) for it in batch}
        remaining = list(batch)
        attempt = 0

        while remaining and attempt < max_retries:
            attempt += 1
            try:
                results = caller(client, model, remaining)
            except Exception as e:  # transient API errors -> backoff & retry
                wait = 2 ** attempt
                print(f"  batch error ({e}); retry {attempt}/{max_retries} in {wait}s")
                time.sleep(wait)
                continue

            got = {_key(r.italian, r.pos_category): r for r in results}
            matched = [got[k] for k in wanted if k in got]
            if matched:
                append_records(checkpoint, [m.model_dump() for m in matched])
                done.update({k: got[k].model_dump() for k in got})

            # Re-queue anything the model dropped or renamed.
            remaining = [it for it in remaining
                         if _key(it["italian"], it["pos_category"]) not in done]
            if remaining:
                print(f"  {len(remaining)} unmatched in batch; retry {attempt}")

        if remaining:
            print(f"  WARNING: gave up on "
                  f"{[(it['italian'], it['pos_category']) for it in remaining]}")

        print(f"  progress: {len(done)}/{len(items)}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--csv", default="vocab_di_base.csv")
    ap.add_argument("--levels", nargs="*", default=None,
                    help="Limit to these level names (default: all levels).")
    ap.add_argument("--model", default="gemini-3.1-flash-lite")
    ap.add_argument("--batch-size", type=int, default=30)
    ap.add_argument("--checkpoint", default="translations_by_pos.jsonl")
    ap.add_argument("--db", default="flashcards.db")
    ap.add_argument("--table", default="flashcards")
    ap.add_argument("--limit", type=int, default=0, help="process only first N pairs (testing)")
    ap.add_argument("--dry-run", action="store_true", help="use mock, no API/key needed")
    ap.add_argument("--no-db", action="store_true",
                    help="skip building the SQLite DB after translating")
    args = ap.parse_args()

    items = build_work_items(args.csv, levels=args.levels)
    if args.limit:
        items = items[:args.limit]

    checkpoint = Path(args.checkpoint)

    if args.dry_run:
        run(items, checkpoint, call_mock, client=None, model=args.model,
            batch_size=args.batch_size)
    else:
        from google import genai
        key = os.environ.get("GEMINI_API_KEY")
        if not key:
            print("Set GEMINI_API_KEY (get one at https://aistudio.google.com/apikey).")
            sys.exit(1)
        client = genai.Client(api_key=key)
        run(items, checkpoint, call_gemini, client=client, model=args.model,
            batch_size=args.batch_size)

    if not args.no_db:
        from build_db import build_database
        build_database(args.csv, checkpoint, Path(args.db),
                       table=args.table, levels=args.levels)


if __name__ == "__main__":
    main()
