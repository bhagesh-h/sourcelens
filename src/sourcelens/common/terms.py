"""Search terms.

A term is a phrase ("base editing"), or several phrases joined with " & "
that must all appear in the title or abstract ("CRISPR & base editing"). A
search group matches a record when any of its terms matches. The Go
implementation (cmd/sourcelens/terms.go) builds the same queries and patterns.
"""

from __future__ import annotations

import json
import re

_GO_SPECIAL = set("\\.+*?()|[]{}^$")


def quote_meta(s: str) -> str:
    """Escape the characters Go's regexp.QuoteMeta escapes, and only those."""
    return "".join("\\" + c if c in _GO_SPECIAL else c for c in s)


def term_parts(t: str) -> list[str]:
    """'CRISPR & base editing' -> ['CRISPR', 'base editing']."""
    return [" ".join(p.split()) for p in t.split("&") if p.split()]


def _split_outside_quotes(s: str, sep: str) -> list[str]:
    out, in_quote, last, i = [], False, 0, 0
    while i < len(s):
        if s[i] == '"':
            in_quote = not in_quote
        if not in_quote and s.startswith(sep, i):
            out.append(s[last:i])
            last = i + len(sep)
            i += len(sep)
            continue
        i += 1
    out.append(s[last:])
    return out


def _topic_term(a: str) -> str:
    """Quoted parts are phrases, other words are required one by one."""
    parts: list[str] = []
    cur: list[str] = []
    in_quote = False

    def push():
        p = " ".join("".join(cur).split())
        if p:
            parts.append(p)
        cur.clear()

    for ch in a:
        if ch == '"':
            push()
            in_quote = not in_quote
        elif not in_quote and ch.isspace():
            push()
        elif ch == "&" and not in_quote:
            push()
        else:
            cur.append(ch)
    push()
    return " & ".join(parts)


def parse_topic(s: str) -> list[str]:
    """Turn what a user types into search terms.

    CRISPR base editing            -> ['CRISPR & base & editing']  (all words required)
    "base editing", prime editing  -> ['base editing', 'prime & editing']
    graph neural networks OR GNN   -> ['graph & neural & networks', 'GNN']

    Commas, semicolons and the word OR separate alternatives; within an
    alternative, double quotes keep a phrase together.
    """
    alts: list[str] = []
    cur: list[str] = []
    in_quote = False

    def flush():
        a = "".join(cur).strip()
        if a:
            alts.append(a)
        cur.clear()

    for ch in s:
        if ch == '"':
            in_quote = not in_quote
            cur.append(ch)
        elif not in_quote and ch in ",;":
            flush()
        else:
            cur.append(ch)
    flush()
    expanded = [x for a in alts for x in _split_outside_quotes(a, " OR ")]
    terms: list[str] = []
    for a in expanded:
        t = _topic_term(a)
        if t and t not in terms:
            terms.append(t)
    return terms


# --- source-specific queries -------------------------------------------------

def _join(parts: list[str], sep: str, f) -> str:
    q = [f(p) for p in parts]
    return q[0] if len(q) == 1 else "(" + sep.join(q) + ")"


def _quote(p: str) -> str:
    return '"' + p.replace('"', "") + '"'


def pubmed_term(t: str) -> str:
    return _join(term_parts(t), " AND ", lambda p: _quote(p) + "[tiab]")


def epmc_term(t: str) -> str:
    return _join(term_parts(t), " AND ", lambda p: "TITLE_ABS:" + _quote(p))


def arxiv_term(t: str) -> str:
    parts = term_parts(t)
    if len(parts) == 1:
        return f"ti:{_quote(parts[0])} OR abs:{_quote(parts[0])}"
    return _join(parts, " AND ", lambda p: f"(ti:{_quote(p)} OR abs:{_quote(p)})")


def openalex_term(t: str) -> str:
    """OpenAlex search syntax; phrases are quoted, single words are not (so
    OpenAlex can stem them); commas and | end a filter value there."""
    def one(p: str) -> str:
        p = " ".join(p.replace(",", " ").replace("|", " ").split())
        return p.replace('"', "") if " " not in p else _quote(p)
    return _join(term_parts(t), " AND ", one)


def github_query(t: str) -> str:
    """GitHub requires every quoted part."""
    return " ".join(_quote(p) for p in term_parts(t))


# --- local matching ------------------------------------------------------------

def term_matcher(terms: list[str]):
    """A function telling whether a text matches any term, every part of a term
    standing on its own (not inside a longer word or hyphenated compound)."""
    res = [[re.compile(r"(?i)(?:^|[^\w-])" + re.escape(p) + r"(?:$|[^\w-])") for p in term_parts(t)]
           for t in terms]

    def match(text: str) -> bool:
        return any(all(r.search(text) for r in rs) for rs in res if rs)
    return match


def word_matcher(terms: list[str]):
    """Looser than term_matcher: words stand on their own, but a hyphen counts as
    a separator ("CRISPR-Cas9" contains CRISPR), the words of a phrase may be
    joined by spaces or hyphens, and a plural "s" is allowed."""
    res = []
    for t in terms:
        rs = []
        for p in term_parts(t):
            words = [re.escape(w) for w in re.split(r"[\s-]+", p) if w]
            rs.append(re.compile(r"(?i)(?:^|[^\w])" + r"[\s-]+".join(words) + r"(?:e?s)?(?:$|[^\w])"))
        res.append(rs)

    def match(text: str) -> bool:
        return any(rs and all(r.search(text) for r in rs) for rs in res)
    return match


def _is_word(ch: str) -> bool:
    return ch == "_" or ch.isalpha() or ch.isdecimal()


def term_regex(terms: list[str]) -> str:
    """A case-insensitive pattern matching a text that contains any term: words
    at word boundaries, a plural "s" allowed, spaces and hyphens interchangeable."""
    def word(p: str) -> str:
        q = re.sub(r"[ -]+", lambda m: r"[\s-]+", quote_meta(p))
        pre = r"\b" if _is_word(p[0]) else ""
        post = ""
        if _is_word(p[-1]):
            post = r"\b"
            if p[-1].isalpha():
                # "networks" also matches "network": drop a plural s, then allow one
                if len(p) > 3 and p[-1] in "sS" and p[-2] not in "sS":
                    q = q[:-1]
                post = r"(?:e?s)?\b"
        return pre + q + post

    alts = []
    for t in terms:
        parts = term_parts(t)
        if not parts:
            continue
        if len(parts) == 1:
            alts.append(word(parts[0]))
        else:
            alts.append("^" + "".join(r"(?=[\s\S]*" + word(p) + ")" for p in parts))
    if not alts:
        return "(?!)"
    return "(?i)" + "|".join(alts)


def yq(s: str) -> str:
    """A double-quoted YAML string (JSON syntax)."""
    return json.dumps(s, ensure_ascii=False)


def ysq(s: str) -> str:
    """A single-quoted YAML string."""
    return "'" + s.replace("'", "''") + "'"
