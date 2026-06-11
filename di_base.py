"""
Classify the messy 'part of speech' tags from the Nuovo Vocabolario di Base
CSV into a small set of broad part-of-speech categories.

Each cell can contain MULTIPLE tags separated by commas, because many Italian
words function as more than one part of speech (e.g. "poco" = adj/adv/noun).

Usage:
    df = pd.read_csv('vocab_di_base.csv')
    df = add_pos_columns(df, source_col='part of speech')
"""

import re
import pandas as pd

# ---------------------------------------------------------------------------
# 1. Normalization
# ---------------------------------------------------------------------------
def normalize_token(token: str) -> str:
    """
    Clean up a single tag fragment so variant spacings collapse to one form.
    e.g. "agg. indef." -> "agg.indef."
         "pron. poss. di prima pers.sing." -> "pron.poss.di prima pers.sing."
         " s.m"  -> "s.m"
    """
    token = token.strip()
    # Collapse "<word>. <word>" -> "<word>.<word>" so "agg. indef." == "agg.indef."
    token = re.sub(r'\.\s+', '.', token)
    return token


# ---------------------------------------------------------------------------
# 2. Classification patterns
#
# IMPORTANT: order matters. Patterns are tried top-to-bottom and the first
# match wins. Each pattern is anchored with ^ so e.g. "avv." (adverb) can
# never accidentally match a "verb" pattern looking for "v.".
# ---------------------------------------------------------------------------
PATTERNS = [
    (r'^v\.',          'verb'),                  # v.tr., v.intr., v.pronom.intr., ...
    (r'^p\.pres',      'participle (present)'),  # p.pres.
    (r'^p\.pass',      'participle (past)'),     # p.pass.
    (r'^s\.',          'noun'),                  # s.m., s.f., s.m.inv., s.m. e f., ...
    (r'^agg\.',        'adjective'),             # agg., agg.indef., agg.poss., agg.f., ...
    (r'^avv\.',        'adverb'),
    (r'^pron\.',       'pronoun'),               # pron.pers., pron.dimostr., pron.poss., ...
    (r'^art\.',        'article'),               # art.det., art.indet.
    (r'^prep\.',       'preposition'),
    (r'^cong\.',       'conjunction'),
    (r'^inter\.',      'interjection'),
    (r'^loc\.',        'idiom / set phrase'),    # loc. di comando
    (r'^sigla',        'abbreviation'),
    (r'^lat\.',        'latin'),
    (r'^simb\.',       'symbol'),
]


def classify_token(token: str) -> str:
    norm = normalize_token(token)
    for pattern, category in PATTERNS:
        if re.match(pattern, norm):
            return category
    return f'UNKNOWN: {token!r}'


# ---------------------------------------------------------------------------
# 3. Apply to a full "part of speech" cell (which may list several tags)
# ---------------------------------------------------------------------------
def classify_pos_string(pos_string: str):
    """
    Returns an ordered list of unique broad categories for one cell,
    e.g. "p.pres., agg., s.m. e f." -> ['participle (present)', 'adjective', 'noun']
    """
    if pd.isna(pos_string):
        return []

    categories = []
    for fragment in pos_string.split(','):
        fragment = fragment.strip()
        if not fragment:
            continue
        cat = classify_token(fragment)
        if cat not in categories:
            categories.append(cat)
    return categories


def add_pos_columns(df: pd.DataFrame, source_col: str = 'part of speech') -> pd.DataFrame:
    """
    Adds:
      - pos_categories: list of all broad categories that apply to the entry
      - pos_primary:    the first/dominant category (useful for simple filtering)
    """
    df = df.copy()
    df['pos_categories'] = df[source_col].apply(classify_pos_string)
    df['pos_primary'] = df['pos_categories'].apply(lambda c: c[0] if c else 'UNKNOWN')
    return df


def build_exploded_df(df: pd.DataFrame, levels=None,
                      source_col: str = 'part of speech') -> pd.DataFrame:
    """
    Produce one row per (word, broad POS category).

    A word like "abbandonato" tagged "p.pass., agg., s.m." becomes three rows:
    one for the participle, one for the adjective, one for the noun. This is the
    grain the translation pipeline and the flashcard DB operate on, because the
    same spelling can need a different English gloss per part of speech.

    Parameters
    ----------
    levels : optional iterable of level names (e.g. ['Fondamentale']) to keep.
             None (default) keeps every level; the `level` column is preserved
             either way so downstream consumers can filter on it.

    The exploded category lands in a singular `pos_category` column.
    """
    if levels is not None:
        df = df[df['level'].isin(levels)]
    df = add_pos_columns(df, source_col=source_col)
    df = df.explode('pos_categories').reset_index(drop=True)
    df = df.rename(columns={'pos_categories': 'pos_category'})
    return df


# ---------------------------------------------------------------------------
# 4. Sanity check: run this on your unique() list to catch unmapped patterns
# ---------------------------------------------------------------------------
def find_unmapped(unique_tags) -> set:
    """
    Returns the set of raw fragments that didn't match any pattern.
    Run this against your df['part of speech'].unique() before trusting
    the output on the full dataset.
    """
    unmapped = set()
    for tag in unique_tags:
        if pd.isna(tag):
            continue
        for fragment in tag.split(','):
            fragment = fragment.strip()
            if not fragment:
                continue
            cat = classify_token(fragment)
            if cat.startswith('UNKNOWN'):
                unmapped.add(fragment)
    return unmapped


if __name__ == '__main__':
    # Example usage
    df = pd.read_csv('vocab_di_base.csv')

    fondamentale_mask = df['level'] == 'Fondamentale'
    df = df[fondamentale_mask]
    # 1. Check coverage first
    unmapped = find_unmapped(df['part of speech'].unique())
    if unmapped:
        print(f"WARNING: {len(unmapped)} unmapped fragments found:")
        for u in sorted(unmapped):
            print(f"  {u!r}")
    else:
        print("All fragments mapped successfully.")

    # 2. Add the new columns
    df = add_pos_columns(df)

    # 3. Quick summary
    print("\nDistribution of primary POS:")
    print(df['pos_primary'].value_counts())

    # 4. If you want one row per (word, category) pair for filtering/flashcards:
    df_exploded = df.explode('pos_categories')
    print(f"\nExploded rows: {len(df_exploded)} (from {len(df)} original entries)")