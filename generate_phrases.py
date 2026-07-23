"""
Generate ~200 common Italian phrases for a 5-week study abroad, grouped by
travel category, with English translations and Spanish equivalents, using the
Gemini API with structured output.

This mirrors ``translate_pipeline.py``: a Pydantic schema drives structured
output, requests are batched to amortize latency/cost, and progress is
checkpointed to a JSONL file so a rerun resumes instead of regenerating (and
never re-calls the paid API for phrases already collected). A final step writes
the phrases into the ``phrases`` table of ``flashcards.db`` -- the same database
the Go backend reads -- which the app serves as its "Basics" deck.

Setup
-----
    pip install google-genai pydantic pandas
    export GEMINI_API_KEY="...your key from aistudio.google.com/apikey..."

Run
---
    python generate_phrases.py                 # generate all categories, real API
    python generate_phrases.py --dry-run       # offline mock, no key needed
    python generate_phrases.py --build-only    # just (re)write the DB from checkpoint
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
from pathlib import Path

import pandas as pd
from pydantic import BaseModel, Field


# ---------------------------------------------------------------------------
# 1. Categories and how many phrases each should contribute (~200 total)
# ---------------------------------------------------------------------------
# (name, description used to steer the model, target count)
CATEGORIES: list[tuple[str, str, int]] = [
    ("Greetings & Politeness",
     "hello/goodbye, please/thank you, excuse me, introductions, small talk", 25),
    ("Getting Around",
     "trains, buses, taxis, tickets, asking about schedules and stops", 30),
    ("Food & Restaurants",
     "ordering food and drink, the bill, dietary needs, cafes and bars", 35),
    ("Shopping & Money",
     "prices, paying, cards vs cash, sizes, markets and shops", 25),
    ("Accommodation",
     "checking in/out, the room, wifi, keys, problems at the hotel or flat", 20),
    ("Emergencies & Health",
     "pharmacy, feeling ill, help, police, lost items, basic symptoms", 25),
    ("Directions & Places",
     "asking and understanding the way, left/right, near/far, landmarks", 20),
    ("Socializing & Study Abroad",
     "meeting people, university/class, making plans, exchanging contacts", 20),
]


# ---------------------------------------------------------------------------
# 2. Output schema
# ---------------------------------------------------------------------------
class PhraseEntry(BaseModel):
    category: str = Field(
        description="The category this phrase belongs to, copied EXACTLY from the "
                    "requested category name."
    )
    italian: str = Field(description="The natural, commonly-used Italian phrase.")
    english: str = Field(description="A natural English translation of the phrase.")
    spanish: str = Field(
        description="The equivalent everyday Spanish phrase (how a Spanish speaker "
                    "would say the same thing), to leverage the learner's Spanish."
    )


SYSTEM_INSTRUCTION = """\
You help an English speaker who also speaks Spanish prepare for a 5-week study
abroad in Italy. You produce the MOST COMMON, practical Italian phrases a
traveler and student will actually use day to day -- not textbook curiosities.

For each phrase give:
- italian: the natural phrase an Italian would really say (keep it short, spoken
  register, correct and idiomatic).
- english: a natural English translation (not word-for-word if that sounds off).
- spanish: the equivalent everyday phrase in Spanish, so the learner can lean on
  what they already know.

Echo the category name EXACTLY as requested. Return only distinct, useful
phrases; do not repeat any phrase already listed as collected.
"""


# ---------------------------------------------------------------------------
# 3. Checkpoint helpers (JSONL: one PhraseEntry per line)
# ---------------------------------------------------------------------------
def _key(category: str, italian: str) -> tuple[str, str]:
    return category.strip().lower(), italian.strip().lower()


def load_done(checkpoint_path: Path) -> dict[tuple[str, str], dict]:
    done: dict[tuple[str, str], dict] = {}
    if checkpoint_path.exists():
        with checkpoint_path.open(encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    rec = json.loads(line)
                    done[_key(rec["category"], rec["italian"])] = rec
                except (json.JSONDecodeError, KeyError):
                    continue
    return done


def append_records(checkpoint_path: Path, records: list[dict]) -> None:
    with checkpoint_path.open("a", encoding="utf-8") as f:
        for rec in records:
            f.write(json.dumps(rec, ensure_ascii=False) + "\n")


# ---------------------------------------------------------------------------
# 4. Prompt + API calls (real and mock)
# ---------------------------------------------------------------------------
def build_prompt(category: str, description: str, count: int, have: list[str]) -> str:
    lines = [
        f"Category: {category}",
        f"Theme: {description}",
        f"Produce {count} distinct, very common Italian phrases for this category.",
    ]
    if have:
        joined = "\n".join(f"- {p}" for p in have)
        lines.append("Already collected (do NOT repeat these):\n" + joined)
    return "\n\n".join(lines)


def call_gemini(client, model: str, category: str, description: str,
                count: int, have: list[str]) -> list[PhraseEntry]:
    from google.genai import types

    resp = client.models.generate_content(
        model=model,
        contents=build_prompt(category, description, count, have),
        config=types.GenerateContentConfig(
            system_instruction=SYSTEM_INSTRUCTION,
            response_mime_type="application/json",
            response_schema=list[PhraseEntry],
            temperature=0.7,  # some variety so batches don't collide
        ),
    )
    parsed = resp.parsed
    if parsed is None:
        parsed = [PhraseEntry(**d) for d in json.loads(resp.text)]
    return parsed


def call_mock(client, model: str, category: str, description: str,
              count: int, have: list[str]) -> list[PhraseEntry]:
    """Deterministic fake phrases for --dry-run / offline tests."""
    start = len(have)
    out = []
    for i in range(count):
        n = start + i + 1
        out.append(PhraseEntry(
            category=category,
            italian=f"Frase {n} ({category})",
            english=f"Phrase {n} ({category})",
            spanish=f"Frase {n} en español ({category})",
        ))
    return out


# ---------------------------------------------------------------------------
# 5. Driver: fill each category up to its target
# ---------------------------------------------------------------------------
def run(checkpoint: Path, caller, client, model: str,
        batch_size: int = 25, max_rounds: int = 6) -> None:
    done = load_done(checkpoint)

    for category, description, target in CATEGORIES:
        have = [rec["italian"] for k, rec in done.items() if k[0] == category.strip().lower()]
        rounds = 0
        while len(have) < target and rounds < max_rounds:
            rounds += 1
            want = min(batch_size, target - len(have))
            try:
                results = caller(client, model, category, description, want, have)
            except Exception as e:  # transient API error -> backoff & retry
                wait = 2 ** rounds
                print(f"  [{category}] error ({e}); retry {rounds}/{max_rounds} in {wait}s")
                time.sleep(wait)
                continue

            fresh = []
            for r in results:
                r.category = category  # force exact category regardless of echo
                k = _key(category, r.italian)
                if k in done:
                    continue
                done[k] = r.model_dump()
                have.append(r.italian)
                fresh.append(r.model_dump())
            if fresh:
                append_records(checkpoint, fresh)
            print(f"  [{category}] {len(have)}/{target}")

        if len(have) < target:
            print(f"  WARNING: {category} stopped at {len(have)}/{target}")


# ---------------------------------------------------------------------------
# 6. Build the phrases table
# ---------------------------------------------------------------------------
def build_phrases_db(checkpoint: Path, db_path: Path, table: str = "phrases") -> pd.DataFrame:
    done = load_done(checkpoint)
    rows = list(done.values())
    df = pd.DataFrame(rows, columns=["category", "italian", "english", "spanish"])
    # Preserve category grouping in a stable teaching order.
    order = {name: i for i, (name, _d, _t) in enumerate(CATEGORIES)}
    df["_o"] = df["category"].map(lambda c: order.get(c, 999))
    df = df.sort_values(["_o", "italian"]).drop(columns=["_o"]).reset_index(drop=True)

    import sqlite3
    conn = sqlite3.connect(db_path)
    try:
        df.to_sql(table, conn, if_exists="replace", index=False)
    finally:
        conn.close()
    print(f"Wrote {len(df)} phrases to {db_path} (table '{table}') "
          f"across {df['category'].nunique()} categories.")
    return df


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", default="gemini-3.1-flash-lite")
    ap.add_argument("--batch-size", type=int, default=25)
    ap.add_argument("--checkpoint", default="phrases.jsonl")
    ap.add_argument("--db", default="flashcards.db")
    ap.add_argument("--table", default="phrases")
    ap.add_argument("--dry-run", action="store_true", help="use mock, no API/key needed")
    ap.add_argument("--build-only", action="store_true",
                    help="skip generation; just (re)write the DB from the checkpoint")
    args = ap.parse_args()

    checkpoint = Path(args.checkpoint)

    if not args.build_only:
        if args.dry_run:
            run(checkpoint, call_mock, client=None, model=args.model,
                batch_size=args.batch_size)
        else:
            from google import genai
            key = os.environ.get("GEMINI_API_KEY")
            if not key:
                print("Set GEMINI_API_KEY (get one at https://aistudio.google.com/apikey).")
                sys.exit(1)
            client = genai.Client(api_key=key)
            run(checkpoint, call_gemini, client=client, model=args.model,
                batch_size=args.batch_size)

    build_phrases_db(checkpoint, Path(args.db), table=args.table)


if __name__ == "__main__":
    main()
