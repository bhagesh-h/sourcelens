"""Unit tests for the Python implementation.

cmd/sourcelens/sourcelens_test.go checks the same cases against the Go
implementation, so both must keep returning these values.
"""

from __future__ import annotations

import datetime as dt
import re
import time

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


def test_progress_line(tmp_path):
    from sourcelens.cli.main import progress_line
    ft = tmp_path / "fulltext.log"
    ft.write_text("[10:00:00] start\n[10:01:00]   300/1200 ok 280, deferred 3\n")
    oa = tmp_path / "openalex.log"
    oa.write_text("[10:01:00] GET 429 on https://doi.org/10.1101/2020.01.01 (try 1); waiting 4s\n")
    now = time.time()
    line = progress_line(2, 4, now, [("fulltext", ft, now), ("openalex", oa, now)], 200)
    # 2 finished steps plus a quarter of the running one: 2.25 of 4; a DOI is not a count
    assert line == "[" + "█" * 14 + "░" * 10 + "] 2/4 steps  0:00  fulltext 300/1200, openalex 0:00"
    assert progress_line(0, 3, now, [], 30) == "[" + "░" * 24 + "] 0/"  # cut to the width


def test_long_retry_after_stops_requests_to_that_host():
    import http.server
    import threading

    hits = []

    class TooMany(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            hits.append(self.path)
            self.send_response(429)
            self.send_header("Retry-After", "27764")
            self.end_headers()

        def log_message(self, *args):
            pass

    srv = http.server.HTTPServer(("127.0.0.1", 0), TooMany)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    host = f"127.0.0.1:{srv.server_port}"
    agelit.RATE_LIMITED.clear()
    try:
        h = agelit.Http(min_interval=0)
        t0 = time.time()
        assert h.get(f"http://{host}/works") is None
        assert h.get(f"http://{host}/works?page=2") is None  # no second request
        assert time.time() - t0 < 5 and hits == ["/works"]
        notes = agelit.rate_limit_notes()
        assert len(notes) == 1 and notes[0].startswith(f"rate limited by {host} until ")
        assert notes[0].endswith("; run again then")
    finally:
        srv.shutdown()
        agelit.RATE_LIMITED.clear()


JATS = b"""<?xml version="1.0"?>
<article xmlns:xlink="http://www.w3.org/1999/xlink"><body>
<fig id="f1"><label>Figure 1.</label><caption><title>Clock <italic>accuracy</italic></title>
<p>Error by age.</p></caption><graphic xlink:href="pone.0123456.g001"/></fig>
<table-wrap id="t1"><label>Table 2</label><caption><p>Cohorts</p></caption><graphic xlink:href="tab2.gif"/></table-wrap>
<supplementary-material id="s1"><label>Supplement 1.</label><caption><p>eTable 1. Results</p></caption>
<media xlink:href="jamanetwopen-s001.xlsx"/></supplementary-material>
</body></article>"""


def test_attachment_captions():
    from sourcelens.pullliturature import attachments as at
    caps = at.captions(JATS)
    assert at.describe("pone.0123456.g001.jpg", caps) == ("figure", "Figure 1.", "Clock accuracy Error by age.")
    assert at.describe("tab2.gif", caps) == ("table", "Table 2", "Cohorts")
    assert at.describe("jamanetwopen-s001.xlsx", caps) == ("supplementary", "Supplement 1.", "eTable 1. Results")
    assert at.describe("other.png", caps) == ("figure", "", "")
    assert at.describe("data.csv", caps) == ("supplementary", "", "")
    assert at.safe_name("a b/../c?.xlsx") == "c_.xlsx" and at.safe_name("...") == "file"
    rows = [{"file": "a.jpg", "ext": "jpg", "status": "ok"}, {"file": "b.xlsx", "ext": "xlsx", "status": "listed"},
            {"file": "c.jpg", "ext": "jpg", "status": "failed"}]
    assert at.summary(rows) == "1/3: jpg 2, xlsx 1"
    assert at.summary([{"file": "", "status": "none"}]) == "0/0" and at.summary([]) == ""
    assert at.summary_exts("1/3: jpg 2, xlsx 1") == {"jpg", "xlsx"}
    assert at.human_bytes(512) == "512 B" and at.human_bytes(2048) == "2.0 KB" and at.human_bytes("x") == ""


def make_catalogue(tmp_path, monkeypatch):
    """A catalogue folder with its configuration; the fulltext module pointed at it."""
    from sourcelens.pullliturature import fetch_fulltext as ft
    monkeypatch.setenv("SOURCELENS_SETTINGS", str(tmp_path / "settings.yaml"))
    p = resolve_project("test topic", str(tmp_path / "cat"), True, "2025-01-01")
    monkeypatch.setattr(ft, "FULLTEXT", p.dir / "fulltext")
    monkeypatch.setattr(ft, "INDEX", p.dir / "fulltext" / "fulltext_index.csv")
    return p, ft


def test_no_folder_without_files(tmp_path, monkeypatch):
    p, ft = make_catalogue(tmp_path, monkeypatch)
    rec = {"uid": "url:example.org/paper", "pub_date": "2025-03-01"}
    row = {"date": "2025-03-01", "title": "A paper"}
    folder = ft.folder_for(rec, row)
    folder.mkdir(parents=True)
    (folder / "metadata.json").write_text("{}", encoding="utf-8")  # left by an earlier version
    idx, atts = ft.fetch_one(rec, row, True, ft.ALL_FORMATS, {"pmc", "unpaywall", "openalex"})
    assert idx["status"] == "none" and idx["reason"] == "no PMC copy" and idx["folder"] == ""
    assert atts is None and not folder.exists()
    old = {"uid": "u", "status": "none", "reason": "", "folder": "fulltext/2024/u"}
    f2 = p.dir / "fulltext" / "2024" / "u"
    f2.mkdir(parents=True)
    (f2 / "metadata.json").write_text('{"fulltext": {"sources_tried": ["pmc_s3", "unpaywall"]}}', encoding="utf-8")
    assert ft.tidy_index({"u": old}) == 1 and not f2.exists()
    assert old["folder"] == "" and old["reason"] == "no open-access copy found (tried: pmc_s3, unpaywall)"
    keep = p.dir / "fulltext" / "2024" / "k"
    keep.mkdir(parents=True)
    (keep / "paper.pdf").write_bytes(b"%PDF")
    assert ft.tidy_index({"k": {"uid": "k", "status": "none", "folder": "fulltext/2024/k"}}) == 0 and keep.exists()


def test_read_uids_file(tmp_path):
    from sourcelens.pullliturature.fetch_fulltext import read_uids_file
    a = tmp_path / "a.csv"
    a.write_text("date,uid,title\n2025,doi:10.1/x,\"A, b\"\n2025,pmid:1,c\n", encoding="utf-8")
    b = tmp_path / "b.csv"
    b.write_text("doi\nhttps://doi.org/10.1234/Y\nnot-a-doi\n", encoding="utf-8")
    c = tmp_path / "c.txt"
    c.write_text("uid\ndoi:10.1/z\n\npmid:2\n", encoding="utf-8")
    assert read_uids_file(str(a)) == ["doi:10.1/x", "pmid:1"]
    assert read_uids_file(str(b)) == ["doi:10.1234/y"]
    assert read_uids_file(str(c)) == ["doi:10.1/z", "pmid:2"]


DRY = [
    {"date": "2025-01-02", "uid": "doi:10.1/a", "title": "Clock one", "resource_type": "article", "tier": "core",
     "fulltext_status": "ok", "attachments": "2/3: jpg 2, xlsx 1", "summary": "DNA methylation clock", "keywords": "aging"},
    {"date": "2025-02-03", "uid": "doi:10.1/b", "title": "Frailty two", "resource_type": "review", "tier": "related",
     "fulltext_status": "", "attachments": "0/0", "summary": "frailty index", "keywords": "methylation"},
    {"date": "2025-03-04", "uid": "doi:10.1/c", "title": "Proteome", "resource_type": "article", "tier": "core",
     "fulltext_status": "none", "attachments": "", "summary": "proteomic clock", "keywords": ""},
]


def test_query_new_filters():
    from sourcelens.query import catalog

    def pick(**kw):
        o = parse_flags([], catalog.FILTER_SPEC, "")
        for k, v in kw.items():
            setattr(o, k, v)
        return [r["uid"] for r in catalog.select(o, [dict(r) for r in DRY])[0]]

    assert pick(summary="methylation") == ["doi:10.1/a", "doi:10.1/b"]  # keywords count too
    assert pick(summary="^(dna|proteomic)") == ["doi:10.1/a", "doi:10.1/c"]
    assert pick(ext="XLSX") == ["doi:10.1/a"]
    assert pick(has_attachments=True) == ["doi:10.1/a"]
    assert pick(fulltext_status="-,none") == ["doi:10.1/b", "doi:10.1/c"]
    assert catalog.attachment_count("2/3: jpg 2") == 3 and catalog.attachment_count("") == 0


def test_files_move_and_record(tmp_path, monkeypatch, capsys):
    from sourcelens.buildcatalog.build_progress import COLUMNS
    from sourcelens.common.agelit import read_csv, write_csv
    from sourcelens.pullliturature import attachments as at
    from sourcelens.query import files
    p, ft = make_catalogue(tmp_path, monkeypatch)
    folder = p.dir / "fulltext" / "2025" / "10.1_a"
    (folder / "attachments").mkdir(parents=True)
    for name, data in (("paper.pdf", b"%PDF-1"), ("paper.md", b"# A"), ("attachments/s1.xlsx", b"xlsx"),
                       ("attachments/f1.jpg", b"jpg")):
        (folder / name).write_bytes(data)
    (folder / "metadata.json").write_text('{"fulltext": {"files": {"paper.pdf": {}, "paper.md": {}}}}', encoding="utf-8")
    write_csv(p.dir / "progress.csv", [{"uid": "doi:10.1/a", "date": "2025-01-02", "title": "Clock one",
                                        "resource_type": "article", "tier": "core", "fulltext_status": "ok",
                                        "fulltext_pdf": "fulltext/2025/10.1_a/paper.pdf",
                                        "fulltext_md": "fulltext/2025/10.1_a/paper.md", "attachments": "2/2: jpg 1, xlsx 1"}],
              COLUMNS)
    write_csv(p.dir / "fulltext" / "fulltext_index.csv", [{"uid": "doi:10.1/a", "status": "ok", "has_pdf": True,
                                                            "has_md": True, "attachments": "2/2",
                                                            "folder": "fulltext/2025/10.1_a"}], ft.INDEX_COLUMNS)
    at.write_index({"doi:10.1/a": [
        {"uid": "doi:10.1/a", "file": "f1.jpg", "ext": "jpg", "kind": "figure", "label": "Figure 1", "bytes": 3,
         "status": "ok", "path": "fulltext/2025/10.1_a/attachments/f1.jpg"},
        {"uid": "doi:10.1/a", "file": "s1.xlsx", "ext": "xlsx", "kind": "supplementary", "caption": "eTable 1",
         "bytes": 4, "status": "ok", "path": "fulltext/2025/10.1_a/attachments/s1.xlsx"}]})
    d = ["--dir", str(p.dir)]
    assert files.run(d + ["--ext", "xlsx,pdf"]) == 0
    assert capsys.readouterr().out.splitlines()[0] == "2 files (10 B) of 1 matching papers"
    out = tmp_path / "out"
    assert files.run(d + ["--name", "etable", "--move-to", str(out)]) == 0
    assert (out / "10.1_a" / "attachments" / "s1.xlsx").read_bytes() == b"xlsx"
    assert not (folder / "attachments" / "s1.xlsx").exists()
    rows = at.read_index()["doi:10.1/a"]
    assert [r["status"] for r in rows] == ["ok", "moved"] and rows[1]["path"].endswith("/attachments/s1.xlsx")
    assert files.run(d + ["--kind", "paper", "--delete"]) == 1  # needs --yes
    assert files.run(d + ["--kind", "paper", "--delete", "--yes"]) == 0
    idx = read_csv(p.dir / "fulltext" / "fulltext_index.csv")[0]
    assert idx["status"] == "removed" and idx["reason"] == "deleted by sourcelens files" and idx["attachments"] == "1/2"
    prog = read_csv(p.dir / "progress.csv")[0]
    assert prog["fulltext_status"] == "removed" and prog["fulltext_pdf"] == "" and prog["attachments"] == "1/2: jpg 1, xlsx 1"
    assert files.run(d + ["--status", "all"]) == 0
    assert capsys.readouterr().out.splitlines()[-1].startswith("supplementary | xlsx | 4 B | moved | s1.xlsx")


def test_report_page():
    import base64
    import gzip
    import json

    from sourcelens.buildcatalog import report
    d = {"meta": {"title": 'run report: "x" <y>'}, "steps": [], "columns": ["uid"], "rows": [["u"]], "attachments": []}
    html = report.page(d)
    assert "<title>run report: &quot;x&quot; &lt;y&gt;</title>" in html
    assert 'src="data:image/png;base64,' in html and "__SL_" not in html
    blob = html.split('<script id="sl-data" type="application/octet-stream">', 1)[1].split("</script>", 1)[0]
    assert json.loads(gzip.decompress(base64.b64decode(blob))) == d


def test_locks(tmp_path):
    from sourcelens.common import locks
    path = tmp_path / "x.lock"
    with open(path, "w") as a, open(path, "w") as b:
        assert locks.try_lock(a)
        assert not locks.try_lock(b)
        locks.unlock(a)
        assert locks.try_lock(b)


def test_split_positionals():
    from sourcelens.cli.main import split_positionals
    spec = {"types": ("", str), "plan": (False, bool), "dir": ("", str)}
    assert split_positionals(["a.csv", "--types", "pdf", "--plan", "--dir=x"], spec) == \
        (["a.csv"], ["--types", "pdf", "--plan", "--dir=x"])
    assert split_positionals(["--plan", "b.csv"], spec) == (["b.csv"], ["--plan"])
