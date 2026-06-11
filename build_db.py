"""
Assemble the flashcard SQLite database.

Takes the exploded vocabulary (one row per word + POS category, from
``di_base.build_exploded_df``), merges in the per-(word, POS) translations
produced by ``translate_pipeline.py``, drops the bookkeeping columns that are
not useful for studying, and writes the result to a SQLite table.

This is a separate, importable step so the database can be rebuilt at any time
from the checkpoint without re-calling the (paid) translation API.

Run
---
    python build_db.py                       # all levels -> flashcards.db
    python build_db.py --levels Fondamentale
    python build_db.py --csv vocab_di_base.csv --checkpoint translations_by_pos.jsonl \
        --db flashcards.db --table flashcards
"""

from __future__ import annotations

import argparse
import json
import sqlite3
from pathlib import Path

import pandas as pd

from di_base import build_exploded_df

# Columns the user does not want in the study database (matched case-insensitively
# against the stripped column name, so 'Disambiguation'/'disambiguation' both go).
DROP_COLS = {
    "significato",
    "disambiguation",
    "root",
    "group",
    "special note",
    "participio passato irregolare",
    "aus.",
}

# Translation fields that get merged onto each exploded row.
TRANSLATION_COLS = ["english", "spanish_relation", "spanish_word", "spanish_note"]


def load_translations(checkpoint: Path) -> pd.DataFrame:
    """Read the per-(word, POS) translation JSONL into a DataFrame.

    Lines without the new ``pos_category`` key (e.g. an older per-word
    translations.jsonl) are ignored so a stale file can't corrupt the merge.
    """
    rows: list[dict] = []
    if checkpoint.exists():
        with checkpoint.open(encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    rec = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if "italian" not in rec or "pos_category" not in rec:
                    continue  # skip old-schema / partial lines
                rows.append(rec)

    cols = ["italian", "pos_category", *TRANSLATION_COLS]
    if not rows:
        return pd.DataFrame(columns=cols)

    df = pd.DataFrame(rows)
    # english is stored as a list -> flatten to "sense one / sense two".
    df["english"] = df["english"].apply(
        lambda xs: " / ".join(xs) if isinstance(xs, list) else xs
    )
    return df


def build_database(csv_path, checkpoint, db_path, table: str = "flashcards",
                   levels=None) -> pd.DataFrame:
    """Build the merged table and write it to ``db_path`` (table replaced)."""
    csv_path, checkpoint, db_path = Path(csv_path), Path(checkpoint), Path(db_path)

    ex = build_exploded_df(pd.read_csv(csv_path), levels=levels)

    # Drop the bookkeeping columns the user doesn't want to study from.
    to_drop = [c for c in ex.columns if c.strip().lower() in DROP_COLS]
    ex = ex.drop(columns=to_drop)

    tr = load_translations(checkpoint)

    # Case-insensitive join on (word, pos_category) so echo casing can't break it.
    ex["_w"] = ex["word"].astype(str).str.strip().str.lower()
    ex["_p"] = ex["pos_category"].astype(str).str.strip().str.lower()

    if not tr.empty:
        tr = tr.rename(columns={"italian": "word"})
        tr["_w"] = tr["word"].astype(str).str.strip().str.lower()
        tr["_p"] = tr["pos_category"].astype(str).str.strip().str.lower()
        tr = tr[["_w", "_p", *TRANSLATION_COLS]].drop_duplicates(["_w", "_p"])
        merged = ex.merge(tr, on=["_w", "_p"], how="left")
    else:
        for col in TRANSLATION_COLS:
            ex[col] = pd.NA
        merged = ex

    merged = merged.drop(columns=["_w", "_p"])

    conn = sqlite3.connect(db_path)
    try:
        merged.to_sql(table, conn, if_exists="replace", index=False)
    finally:
        conn.close()

    translated = int(merged["english"].notna().sum())
    print(f"Wrote {len(merged)} rows to {db_path} (table '{table}'); "
          f"{translated} have translations, {len(merged) - translated} still pending.")
    return merged


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--csv", default="vocab_di_base.csv")
    ap.add_argument("--checkpoint", default="translations_by_pos.jsonl")
    ap.add_argument("--db", default="flashcards.db")
    ap.add_argument("--table", default="flashcards")
    ap.add_argument("--levels", nargs="*", default=None,
                    help="Limit to these level names (default: all levels).")
    args = ap.parse_args()

    build_database(args.csv, args.checkpoint, args.db,
                   table=args.table, levels=args.levels)


if __name__ == "__main__":
    main()
