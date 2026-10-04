"""Unit tests for the Python implementation.

cmd/sourcelens/sourcelens_test.go checks the same cases against the Go
implementation, so both must keep returning these values.
"""

from __future__ import annotations

import datetime as dt
import re

import pytest

from sourcelens.common import agelit
from sourcelens.common.flags import parse_flags
from sourcelens.common.settings import (
    ProjectError,
    config_topic,
    default_topic,
    resolve_project,
    topic_config,
    topic_slug,
)
from sourcelens.common.terms import (
    arxiv_term,
    epmc_term,
    github_query,
    openalex_term,
    parse_topic,
    pubmed_term,
    term_matcher,
    term_regex,
    word_matcher,
)
from sourcelens.query import refs


@pytest.mark.parametrize("text,want", [
    ("CRISPR base editing", ["CRISPR & base & editing"]),
    ('"base editing", prime editing', ["base editing", "prime & editing"]),
    ("graph neural networks OR GNN", ["graph & neural & networks", "GNN"]),
    ("  single  ", ["single"]),
    ('a; b, "c d" e', ["a", "b", "c d & e"]),
    ("x & y", ["x & y"]),
    ("", []),
    ("dup, dup", ["dup"]),
    ('"quoted, with comma" OR other one', ["quoted, with comma", "other & one"]),
])
def test_parse_topic(text, want):
    assert parse_topic(text) == want


def test_queries():
    assert pubmed_term("CRISPR & base editing") == '("CRISPR"[tiab] AND "base editing"[tiab])'
    assert epmc_term("epigenetic clock") == 'TITLE_ABS:"epigenetic clock"'
    assert arxiv_term("a & b c") == '((ti:"a" OR abs:"a") AND (ti:"b c" OR abs:"b c"))'
    assert openalex_term("CRISPR & base editing") == '(CRISPR AND "base editing")'
    assert github_query("graph & neural networks") == '"graph" "neural networks"'


def test_term_regex():
    rx = re.compile(term_regex(["graph & neural & networks", "GNN"]))
    for s in ("A graph neural network for X", "Neural networks on a graph", "GNNs explained"):
        assert rx.search(s), s
    for s in ("neural networks only", "graphene"):
        assert not rx.search(s), s
    assert term_regex([]) == "(?!)"
    w = word_matcher(["CRISPR & base editing"])
    assert w("CRISPR-Cas9 base-editing tools") and w("crispr and base editors... base editing")
    assert not w("CRISPR database editing")
    m = term_matcher(["base editing"])
    assert m("Advances in base editing.") and not m("base-editing") and not m("database editing")


def test_topic_slug():
    assert topic_slug("CRISPR base editing") == "crispr-base-editing"
    assert topic_slug('"base editing", prime') == "base-editing-prime"
    assert topic_slug("  ") == ""


def test_parse_when():
    today = dt.date.today()
    assert agelit.parse_when("2024") == "2024-01-01"
    assert agelit.parse_when("2024", end=True) == "2024-12-31"
    assert agelit.parse_when("2024-02", end=True) == "2024-02-29"
    assert agelit.parse_when("2024-03-15") == "2024-03-15"
    assert agelit.parse_when("today") == today.isoformat()
    assert agelit.parse_when("2w") == (today - dt.timedelta(days=14)).isoformat()
    for bad in ("2024-13", "5x", "20240315"):
        with pytest.raises(ValueError):
            agelit.parse_when(bad)


def test_norm_doi():
    assert agelit.norm_doi("https://doi.org/10.1186/GB-2013-14-10-R115") == "10.1186/gb-2013-14-10-r115"
    assert agelit.norm_doi("doi: 10.1101/2020.01.01.123456v2") == "10.1101/2020.01.01.123456"
    assert agelit.norm_doi("not a doi") == ""


def test_flags():
    spec = {"limit": (50, int), "out": ("", str), "dry-run": (False, bool)}
    o = parse_flags(["--limit=5", "--out", "x.csv", "--dry-run"], spec, "")
    assert (o.limit, o.out, o.dry_run) == (5, "x.csv", True)
    for args in (["--limit", "abc"], ["--bogus"], ["stray"], ["--out"]):
        with pytest.raises(ValueError):
            parse_flags(args, spec, "")


def test_csv_quoting(tmp_path):
    p = tmp_path / "x.csv"
    agelit.write_csv(p, [{"a": "plain", "b": "a,b", "c": 'say "hi"', "d": " lead", "e": "multi\nline"}],
                     ["a", "b", "c", "d", "e"])
    assert p.read_bytes() == b'a,b,c,d,e\r\nplain,"a,b","say ""hi""", lead,"multi\nline"\r\n'


def test_references():
    r = {"uid": "doi:10.1/x", "resource_type": "article", "date": "2022-01-12", "year": "2022",
         "authors": "Belsky DW, Caspi A, Corcoran DL", "title": "DunedinPACE, a DNA methylation biomarker.",
         "venue": "eLife", "volume": "11", "doi": "10.1/x"}
    assert refs.render("APA", [r]) == ("Belsky, D. W., Caspi, A., & Corcoran, D. L. (2022). DunedinPACE, a DNA "
                                       "methylation biomarker. eLife, 11. https://doi.org/10.1/x\n")
    assert refs.render("VAN", [r]) == ("1. Belsky DW, Caspi A, Corcoran DL. DunedinPACE, a DNA methylation "
                                       "biomarker. eLife. 2022;11. doi:10.1/x\n")
    assert refs.render("BIB", [r]).startswith("@article{belsky2022dunedinpace,\n")
    with pytest.raises(ValueError):
        refs.parse_codes("APA,bibtex,XYZ")


def test_topic_config():
    cfg = topic_config("graph neural networks, GNN", "2025-01-01").decode()
    for s in ('topic: "graph neural networks, GNN"', 'start_date: "2025-01-01"',
              '- "graph & neural & networks"', '- "GNN"', "sources: [pubmed, europepmc, arxiv, openalex]"):
        assert s in cfg
    assert config_topic(cfg) == "graph neural networks, GNN"
    assert default_topic()


def test_resolve_project(tmp_path, monkeypatch):
    monkeypatch.setenv("SOURCELENS_SETTINGS", str(tmp_path / "settings.yaml"))
    monkeypatch.setenv("SOURCELENS_OUTPUT", str(tmp_path / "out"))
    p = resolve_project("CRISPR base editing", "", True, "2025-06-01")
    assert p.created and p.dir == tmp_path / "out" / "crispr-base-editing"
    assert p.config.is_file()
    with pytest.raises(ProjectError):
        resolve_project("something else", str(p.dir), False)
    with pytest.raises(ProjectError):
        resolve_project("never created", "", False)
