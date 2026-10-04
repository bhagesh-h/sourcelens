"""Shared helpers: catalogue paths, configuration, a polite HTTP session with
retries, identifier normalisation, dates, CSV and JSON files, and locks.

The stable record key (``uid``) is what progress.csv is merged on: a record
whose uid is already in the catalogue is never added twice, which is what
makes re-runs incremental.
"""

from __future__ import annotations

import csv
import datetime as dt
import gzip
import hashlib
import json
import os
import re
import sys
import time
from pathlib import Path
from typing import Iterable, Iterator
from urllib.parse import quote

import requests
import yaml

from litsearch import __version__

# The catalogue folder of the running command or step. The command resolves it
# (see settings.resolve_project) and passes it to its steps as LITSEARCH_PROJECT.
RESEARCH = Path(os.environ.get("LITSEARCH_PROJECT") or os.getcwd())
# the catalogue's configuration file
CONFIG_FILE = Path(os.environ.get("LITSEARCH_CONFIG") or RESEARCH / "config" / "litsearch.yaml")
CORPUS = RESEARCH / "corpus"
RAW = RESEARCH / "raw"
SEEDS = RESEARCH / "seeds"
FULLTEXT = RESEARCH / "fulltext"
REPOS = RESEARCH / "repos"
WEBSITES = RESEARCH / "websites"

# contact email (settings or LITSEARCH_EMAIL / CONTACT_EMAIL); empty: no
# polite-pool address, and Unpaywall is skipped
CONTACT = os.environ.get("CONTACT_EMAIL", "").strip()
UA = f"litsearch/{__version__} (research literature catalogue" + (f"; mailto:{CONTACT})" if CONTACT else ")")


def set_project(directory: Path, config_file: Path) -> None:
    """Point this process (and the steps it starts) at a catalogue folder.

    Modules that import the path constants must be imported after this call.
    """
    global RESEARCH, CONFIG_FILE, CORPUS, RAW, SEEDS, FULLTEXT, REPOS, WEBSITES, _CONFIG
    os.environ["LITSEARCH_PROJECT"] = str(directory)
    os.environ["LITSEARCH_CONFIG"] = str(config_file)
    RESEARCH, CONFIG_FILE = Path(directory), Path(config_file)
    CORPUS, RAW, SEEDS = RESEARCH / "corpus", RESEARCH / "raw", RESEARCH / "seeds"
    FULLTEXT, REPOS, WEBSITES = RESEARCH / "fulltext", RESEARCH / "repos", RESEARCH / "websites"
    _CONFIG = None


def set_contact(email: str) -> None:
    global CONTACT, UA
    CONTACT = (email or "").strip()
    UA = f"litsearch/{__version__} (research literature catalogue" + (f"; mailto:{CONTACT})" if CONTACT else ")")

# ',' and ';' end a DOI here so "doi1;doi2" lists yield both (see norm_doi)
DOI_RE = re.compile(r"\b10\.\d{4,9}/[^\s\"'`<>|\]\[{},;]+", re.I)


def research_rel(path) -> str:
    """Path inside the output folder as stored in the catalogue (e.g. fulltext/2013/x/paper.md)."""
    return Path(path).relative_to(RESEARCH).as_posix()


def research_file(rel: str) -> Path:
    """Catalogue path -> file. Accepts the older workspace-relative form research/..."""
    rel = str(rel)
    if rel.startswith("research/"):
        rel = rel[len("research/"):]
    return RESEARCH / rel


def today() -> str:
    return dt.date.today().isoformat()


def log(*args) -> None:
    print(f"[{dt.datetime.now():%H:%M:%S}]", *args, file=sys.stderr, flush=True)


def load_yaml(path: Path) -> dict:
    with open(path, encoding="utf-8") as fh:
        return yaml.safe_load(fh)


_CONFIG: dict | None = None
SECTIONS = ("search", "classify", "repos", "websites")


def config_root() -> dict:
    """The whole configuration document."""
    global _CONFIG
    if _CONFIG is None:
        if not CONFIG_FILE.is_file():
            sys.stderr.write(f"litsearch: no configuration at {CONFIG_FILE} "
                             f"(run litsearch update to create the catalogue)\n")
            raise SystemExit(1)
        _CONFIG = load_yaml(CONFIG_FILE) or {}
    return _CONFIG


def config(section: str) -> dict:
    """One section of the configuration file: search, classify, repos or websites."""
    root = config_root()
    if section not in root:
        sys.stderr.write(f"litsearch: section {section!r} missing from {CONFIG_FILE}\n")
        raise SystemExit(1)
    return root[section] or {}


def query_window() -> tuple[str, str]:
    """The catalogue's window (search section of the configuration). The catalogue
    step uses this, so a narrow update never drops older rows from the catalogue."""
    cfg = config("search")
    end = cfg.get("end_date", "today")
    return cfg["start_date"], today() if end == "today" else end


_REL = re.compile(r"^([0-9]+)\s*([dwmy])$")


def parse_when(spec: str, *, end: bool = False) -> str:
    """Date spec -> ISO date.

    Accepts an ISO date (2024-03-01), a month (2024-03), a year (2024), the
    word "today", or a span back from today: 1d, 7d, 2w, 1m, 6m, 1y, 2y, 10y.
    A bare year or month means its first day, or its last day when end=True.
    """
    s = str(spec).strip().lower()
    t = dt.date.today()
    if s in ("", "today", "now"):
        return t.isoformat()
    m = _REL.match(s)
    if m:
        n, unit = int(m.group(1)), m.group(2)
        if unit == "d":
            d = t - dt.timedelta(days=n)
        elif unit == "w":
            d = t - dt.timedelta(weeks=n)
        else:
            months = n * (12 if unit == "y" else 1)
            y, mo = divmod(t.year * 12 + (t.month - 1) - months, 12)
            mo += 1
            last = (dt.date(y + (mo == 12), mo % 12 + 1, 1) - dt.timedelta(days=1)).day
            d = dt.date(y, mo, min(t.day, last))
        return d.isoformat()
    bad = ValueError(f'cannot read date "{spec}" (use 7d, 1m, 1y, 2024, 2024-03 or 2024-03-15)')
    if re.fullmatch(r"[0-9]{4}", s):
        return f"{s}-12-31" if end else f"{s}-01-01"
    if re.fullmatch(r"[0-9]{4}-[0-9]{2}", s):
        y, mo = map(int, s.split("-"))
        if not 1 <= mo <= 12:
            raise bad
        if end:
            return (dt.date(y + (mo == 12), mo % 12 + 1, 1) - dt.timedelta(days=1)).isoformat()
        return f"{s}-01"
    if not re.fullmatch(r"[0-9]{4}-[0-9]{2}-[0-9]{2}", s):
        raise bad
    try:
        return dt.date.fromisoformat(s).isoformat()
    except ValueError:
        raise bad from None


def search_window() -> tuple[str, str]:
    """Window for searches and downloads.

    `litsearch update` narrows it with LITSEARCH_SEARCH_START /
    LITSEARCH_SEARCH_END; without them it is the catalogue window.
    """
    start, end = query_window()
    return (os.environ.get("LITSEARCH_SEARCH_START") or start, os.environ.get("LITSEARCH_SEARCH_END") or end)


def search_window_overridden() -> bool:
    return bool(os.environ.get("LITSEARCH_SEARCH_START") or os.environ.get("LITSEARCH_SEARCH_END"))


def pipeline_lock_path() -> Path:
    return RESEARCH / ".pipeline.lock"


def acquire_pipeline_lock(label: str = "update"):
    """Hold <catalogue>/.pipeline.lock for a whole update, or return None if taken.

    Both implementations use the same file, so two updates never rebuild
    progress.csv at the same time. The lock is released when the returned file
    object is closed or the process exits.
    """
    import fcntl
    lock = pipeline_lock_path()
    lock.parent.mkdir(parents=True, exist_ok=True)
    fh = open(lock, "a+")
    try:
        fcntl.flock(fh, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        fh.close()
        return None
    fh.seek(0)
    fh.truncate()
    fh.write(f"pid {os.getpid()} since {dt.datetime.now().isoformat(timespec='seconds')} ({label})\n")
    fh.flush()
    return fh


# --------------------------------------------------------------------------
# HTTP
# --------------------------------------------------------------------------

class _Session(requests.Session):
    """A Location header that is not UTF-8 (seen on some institutional
    repositories) is followed with its raw bytes percent-encoded, as browsers
    and Go's client do, instead of failing with UnicodeDecodeError."""

    def get_redirect_target(self, resp):
        if not resp.is_redirect:
            return None
        raw = resp.headers["location"].encode("latin1")
        try:
            return raw.decode("utf8")
        except UnicodeDecodeError:
            return quote(raw, safe="/:?&=;%#+,@!$'()*~[]")


class Http:
    """requests.Session with retry/backoff and a minimum interval per host."""

    def __init__(self, min_interval: float = 0.34, headers: dict | None = None):
        self.s = _Session()
        self.s.headers.update({"User-Agent": UA})
        if headers:
            self.s.headers.update(headers)
        self.min_interval = min_interval
        self._last: dict[str, float] = {}

    def _wait(self, url: str) -> None:
        host = url.split("/")[2]
        gap = time.time() - self._last.get(host, 0)
        if gap < self.min_interval:
            time.sleep(self.min_interval - gap)
        self._last[host] = time.time()

    def get(self, url: str, *, params=None, headers=None, timeout=90,
            tries=6, stream=False, ok=(200,), allow_404=True) -> requests.Response | None:
        delay = 2.0
        for attempt in range(1, tries + 1):
            self._wait(url)
            try:
                r = self.s.get(url, params=params, headers=headers,
                               timeout=timeout, stream=stream, allow_redirects=True)
            # UnicodeError / ValueError: an unreadable URL in a redirect
            except (requests.RequestException, UnicodeError, ValueError) as exc:
                log(f"GET error {exc.__class__.__name__} on {url} (try {attempt})")
                time.sleep(delay)
                delay = min(delay * 2, 120)
                continue
            if r.status_code in ok:
                return r
            if r.status_code == 404 and allow_404:
                return None
            if r.status_code in (403, 401, 404, 410, 451):  # final answers; retrying will not help
                return r
            if attempt == tries:  # no point sleeping after the last try
                break
            retry_after = r.headers.get("Retry-After")
            # some servers send "Retry-After: 0" with a 429; never retry faster
            # than our own back-off
            wait = max(delay, float(retry_after)) if retry_after and retry_after.isdigit() else delay
            log(f"GET {r.status_code} on {url[:140]} (try {attempt}); waiting {wait:.0f}s")
            time.sleep(wait)
            delay = min(delay * 2, 120)
        return None

    def post(self, url: str, *, data=None, timeout=90, tries=6) -> requests.Response | None:
        delay = 2.0
        for attempt in range(1, tries + 1):
            self._wait(url)
            try:
                r = self.s.post(url, data=data, timeout=timeout)
            except requests.RequestException as exc:
                log(f"POST error {exc.__class__.__name__} on {url} (try {attempt})")
                time.sleep(delay)
                delay = min(delay * 2, 120)
                continue
            if r.status_code == 200:
                return r
            log(f"POST {r.status_code} on {url} (try {attempt})")
            time.sleep(delay)
            delay = min(delay * 2, 120)
        return None


# --------------------------------------------------------------------------
# Identifiers
# --------------------------------------------------------------------------

def norm_doi(doi: str | None) -> str:
    """Lower-case bare DOI, or '' if the input does not contain one."""
    if not doi:
        return ""
    d = str(doi).strip()
    d = re.sub(r"^(https?://)?(dx\.)?doi\.org/", "", d, flags=re.I)
    d = re.sub(r"^doi:\s*", "", d, flags=re.I)
    m = DOI_RE.search(d)
    if not m:
        return ""
    # ',' and ';' are legal in DOIs but only in pre-2000 SICI forms; in the
    # sources here they are CSV / list separators ("10.x/y,doi,not")
    d = re.split(r"[,;]", m.group(0))[0].rstrip(".:)")
    # unbalanced trailing ')' left by prose such as "(doi:10.x/y)"
    while d.endswith(")") and d.count("(") < d.count(")"):
        d = d[:-1]
    # prefix typed twice: 10.1186/10.1186/gb-2013-14-10-r115
    d = re.sub(r"^(10\.\d{4,9})/\1/", r"\1/", d)
    # landing-page suffixes copied from URLs
    d = re.sub(r"/(suppl_file|suppl|full|abstract|pdf|epdf|html|figures|tables)(/.*)?$", "", d, flags=re.I)
    if d.lower().startswith("10.1101/"):  # bioRxiv / medRxiv version and format
        d = re.sub(r"v\d+(\.full)?(\.pdf|\.txt|\.html)?$|\.full(\.pdf)?$", "", d, flags=re.I)
    return d.lower()


def norm_pmid(pmid) -> str:
    s = re.sub(r"\D", "", str(pmid or ""))
    return s if s and s != "0" else ""


def norm_pmcid(pmcid) -> str:
    s = str(pmcid or "").strip().upper()
    if not s:
        return ""
    if not s.startswith("PMC"):
        s = "PMC" + re.sub(r"\D", "", s)
    return s if re.fullmatch(r"PMC\d+", s) else ""


def make_uid(doi="", pmid="", pmcid="", epmc_id="", url="") -> str:
    """Stable key used to merge records across sources and runs.

    Order of preference: DOI, PMID, PMCID, Europe PMC preprint id, URL.
    """
    if norm_doi(doi):
        return "doi:" + norm_doi(doi)
    if norm_pmid(pmid):
        return "pmid:" + norm_pmid(pmid)
    if norm_pmcid(pmcid):
        return "pmcid:" + norm_pmcid(pmcid)
    if epmc_id:
        return "epmc:" + str(epmc_id).upper()
    if url:
        u = re.sub(r"^https?://(www\.)?", "", url.strip().rstrip("/")).lower()
        return "url:" + u
    raise ValueError("record has no identifier")


def slug(uid: str, maxlen: int = 120) -> str:
    """Filesystem-safe folder name for a uid."""
    s = re.sub(r"[^A-Za-z0-9._-]+", "_", uid.split(":", 1)[-1]).strip("_")
    if len(s) > maxlen:
        s = s[:maxlen - 9] + "_" + hashlib.sha1(uid.encode()).hexdigest()[:8]
    return s


# --------------------------------------------------------------------------
# Files
# --------------------------------------------------------------------------

def open_any(path: Path, mode: str = "rt"):
    path = Path(path)
    if path.suffix == ".gz":
        return gzip.open(path, mode, encoding=None if "b" in mode else "utf-8")
    return open(path, mode, encoding=None if "b" in mode else "utf-8")


def read_jsonl(path: Path) -> Iterator[dict]:
    if not Path(path).exists():
        return
    with open_any(path) as fh:
        for line in fh:
            line = line.strip()
            if line:
                yield json.loads(line)


def write_jsonl(path: Path, rows: Iterable[dict]) -> int:
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    # keep the real suffix last so open_any compresses a .gz temp file too
    tmp = path.with_name(path.stem + ".tmp" + path.suffix)
    n = 0
    with open_any(tmp, "wt") as fh:
        for r in rows:
            fh.write(json.dumps(r, ensure_ascii=False) + "\n")
            n += 1
    tmp.replace(path)
    return n


def read_csv(path: Path) -> list[dict]:
    if not Path(path).exists():
        return []
    with open(path, newline="", encoding="utf-8") as fh:
        return list(csv.DictReader(fh))


def write_csv(path: Path, rows: list[dict], columns: list[str]) -> None:
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + ".tmp")
    with open(tmp, "w", newline="", encoding="utf-8") as fh:
        w = csv.DictWriter(fh, fieldnames=columns, extrasaction="ignore")
        w.writeheader()
        for r in rows:
            w.writerow({c: ("" if r.get(c) is None else r.get(c)) for c in columns})
    tmp.replace(path)


def write_json(path: Path, obj) -> None:
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + ".tmp")
    with open(tmp, "w", encoding="utf-8") as fh:
        json.dump(obj, fh, ensure_ascii=False, indent=2)
    tmp.replace(path)


def clean_text(s) -> str:
    if s is None:
        return ""
    s = re.sub(r"<[^>]+>", "", str(s))
    return re.sub(r"\s+", " ", s).strip()
