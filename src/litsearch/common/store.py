"""The record store: one metadata record per publication, keyed by uid.

<catalogue>/corpus/records.jsonl.gz is shared by every harvester (Europe PMC,
PubMed, seed resolution). A record keeps the uid it was first stored under
for good, so rows in <catalogue>/progress.csv stay addressable across runs even
when a later source reveals a DOI for a record first seen by PMID only.

Merging never discards information: an empty field is filled by a later
source, a non-empty one is kept, lists and source sets are unioned.
"""

from __future__ import annotations

import fcntl
from pathlib import Path

from litsearch.common.agelit import CORPUS, norm_doi, norm_pmcid, norm_pmid, read_jsonl, write_jsonl

STORE = CORPUS / "records.jsonl.gz"
LIST_FIELDS = ("fulltext_urls", "sources", "epmc_ids")
# fields where a later, better source should win over an earlier value
PREFER_NEW = {"cited_by", "is_open_access", "in_pmc", "has_pdf", "license"}


def _empty(v) -> bool:
    return v is None or v == "" or v == [] or v is False or v == 0


class Store:
    def __init__(self, path: Path = STORE):
        self.path = Path(path)
        self.recs: dict[str, dict] = {}
        self.alias: dict[str, str] = {}
        for r in read_jsonl(self.path):
            self.recs[r["uid"]] = r
            self._index(r)

    def _keys(self, r: dict) -> list[str]:
        keys = []
        if norm_doi(r.get("doi")):
            keys.append("doi:" + norm_doi(r["doi"]))
        if norm_pmid(r.get("pmid")):
            keys.append("pmid:" + norm_pmid(r["pmid"]))
        if norm_pmcid(r.get("pmcid")):
            keys.append("pmcid:" + norm_pmcid(r["pmcid"]))
        for e in r.get("epmc_ids") or []:
            keys.append("epmc:" + e)
        return keys

    def _index(self, r: dict) -> None:
        for k in self._keys(r):
            self.alias.setdefault(k, r["uid"])
        self.alias.setdefault(r["uid"], r["uid"])

    def find(self, r: dict) -> str | None:
        for k in [r.get("uid", "")] + self._keys(r):
            if k and k in self.alias:
                return self.alias[k]
        return None

    def get(self, uid: str) -> dict | None:
        return self.recs.get(self.alias.get(uid, uid))

    def merge(self, rec: dict, source: str) -> str:
        """Add or enrich a record; return the uid it lives under."""
        rec = dict(rec)
        if rec.get("epmc_id") and rec.get("source_db"):
            rec["epmc_ids"] = [f"{rec['source_db']}/{rec['epmc_id']}"]
        rec.pop("epmc_id", None)
        rec["sources"] = [source]
        uid = self.find(rec)
        if uid is None:
            uid = rec["uid"]
            self.recs[uid] = rec
        else:
            old = self.recs[uid]
            for k, v in rec.items():
                if k == "uid":
                    continue
                if k in LIST_FIELDS:
                    old[k] = sorted(set(old.get(k) or []) | set(v or []))
                elif k in PREFER_NEW and not _empty(v):
                    old[k] = max(old.get(k) or 0, v) if k == "cited_by" else v
                elif _empty(old.get(k)) and not _empty(v):
                    old[k] = v
            rec = old
        rec["uid"] = uid
        self._index(rec)
        return uid

    def save(self) -> int:
        """Write the store, keeping records another process saved meanwhile.

        Two harvesters may run at once (e.g. a search and the seed
        resolver). Under an exclusive lock the file on disk is re-read and
        any record this process does not know is folded in before writing,
        so neither writer drops the other's additions.
        """
        self.path.parent.mkdir(parents=True, exist_ok=True)
        with open(self.path.with_name(".records.lock"), "w") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            for r in read_jsonl(self.path):
                if self.find(r) is None:
                    self.recs[r["uid"]] = r
                    self._index(r)
            return write_jsonl(self.path, self.recs.values())

    def __len__(self) -> int:
        return len(self.recs)

    def values(self):
        return self.recs.values()
