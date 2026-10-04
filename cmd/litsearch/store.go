package main

// Record store (corpus/records.jsonl.gz) and search-hit log
// (corpus/search_hits.csv). Python twin: src/litsearch/common/store.py and hits.py:
// stable uids, alias index, merge without losing information, saves under a
// file lock that folds in records other processes added meanwhile.

import (
	"sort"
)

var listFields = map[string]bool{"fulltext_urls": true, "sources": true, "epmc_ids": true}
var preferNew = map[string]bool{"cited_by": true, "is_open_access": true, "in_pmc": true, "has_pdf": true, "license": true}

type Store struct {
	path  string
	recs  map[string]Rec
	order []string
	alias map[string]string
}

func storePath() string { return rpath("corpus", "records.jsonl.gz") }

func LoadStore() *Store {
	s := &Store{path: storePath(), recs: map[string]Rec{}, alias: map[string]string{}}
	for _, r := range readJSONL(s.path) {
		uid := str(r, "uid")
		if uid == "" {
			continue
		}
		if _, ok := s.recs[uid]; !ok {
			s.order = append(s.order, uid)
		}
		s.recs[uid] = r
		s.index(r)
	}
	return s
}

func (s *Store) keys(r Rec) []string {
	var ks []string
	if d := normDOI(str(r, "doi")); d != "" {
		ks = append(ks, "doi:"+d)
	}
	if p := normPMID(str(r, "pmid")); p != "" {
		ks = append(ks, "pmid:"+p)
	}
	if p := normPMCID(str(r, "pmcid")); p != "" {
		ks = append(ks, "pmcid:"+p)
	}
	for _, e := range strList(r, "epmc_ids") {
		ks = append(ks, "epmc:"+e)
	}
	return ks
}

func (s *Store) index(r Rec) {
	uid := str(r, "uid")
	for _, k := range s.keys(r) {
		if _, ok := s.alias[k]; !ok {
			s.alias[k] = uid
		}
	}
	if _, ok := s.alias[uid]; !ok {
		s.alias[uid] = uid
	}
}

func (s *Store) Find(r Rec) string {
	if u := str(r, "uid"); u != "" {
		if v, ok := s.alias[u]; ok {
			return v
		}
	}
	for _, k := range s.keys(r) {
		if v, ok := s.alias[k]; ok {
			return v
		}
	}
	return ""
}

func (s *Store) FindID(doi, pmid, pmcid string, epmcIDs ...string) string {
	r := Rec{"doi": doi, "pmid": pmid, "pmcid": pmcid}
	if len(epmcIDs) > 0 {
		l := make([]any, len(epmcIDs))
		for i, e := range epmcIDs {
			l[i] = e
		}
		r["epmc_ids"] = l
	}
	return s.Find(r)
}

func (s *Store) Get(uid string) Rec {
	if v, ok := s.alias[uid]; ok {
		uid = v
	}
	return s.recs[uid]
}

func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case bool:
		return !x
	case []any:
		return len(x) == 0
	case []string:
		return len(x) == 0
	default:
		if n, ok := toInt(v); ok {
			return n == 0
		}
	}
	return false
}

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case float64:
		return int(x), true
	case interface{ Int64() (int64, error) }:
		i, err := x.Int64()
		return int(i), err == nil
	}
	return 0, false
}

func unionList(a, b any) []any {
	set := map[string]bool{}
	for _, l := range []any{a, b} {
		switch x := l.(type) {
		case []any:
			for _, v := range x {
				if sv, ok := v.(string); ok {
					set[sv] = true
				}
			}
		case []string:
			for _, v := range x {
				set[v] = true
			}
		}
	}
	keys := sortedKeys(set)
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = k
	}
	return out
}

// Merge adds or enriches a record and returns the uid it lives under.
func (s *Store) Merge(rec Rec, source string) string {
	r := Rec{}
	for k, v := range rec {
		r[k] = v
	}
	if e, db := str(r, "epmc_id"), str(r, "source_db"); e != "" && db != "" {
		r["epmc_ids"] = []any{db + "/" + e}
	}
	delete(r, "epmc_id")
	r["sources"] = []any{source}
	uid := s.Find(r)
	if uid == "" {
		uid = str(r, "uid")
		s.recs[uid] = r
		s.order = append(s.order, uid)
	} else {
		old := s.recs[uid]
		for k, v := range r {
			if k == "uid" {
				continue
			}
			switch {
			case listFields[k]:
				old[k] = unionList(old[k], v)
			case preferNew[k] && !empty(v):
				if k == "cited_by" {
					a, _ := toInt(old[k])
					b, _ := toInt(v)
					if b > a {
						old[k] = b
					}
				} else {
					old[k] = v
				}
			case empty(old[k]) && !empty(v):
				old[k] = v
			}
		}
		r = old
	}
	r["uid"] = uid
	s.index(r)
	return uid
}

func (s *Store) Len() int { return len(s.recs) }

func (s *Store) Values() []Rec {
	out := make([]Rec, 0, len(s.order))
	for _, u := range s.order {
		out = append(out, s.recs[u])
	}
	return out
}

// Save writes the store, keeping records another process saved meanwhile.
func (s *Store) Save() error {
	return withLock(rpath("corpus", ".records.lock"), func() error {
		for _, r := range readJSONL(s.path) {
			if s.Find(r) == "" {
				uid := str(r, "uid")
				s.recs[uid] = r
				s.order = append(s.order, uid)
				s.index(r)
			}
		}
		return writeJSONL(s.path, s.Values())
	})
}

// ---------------------------------------------------------------------------
// search hits
// ---------------------------------------------------------------------------

var hitCols = []string{"uid", "group", "scope", "source", "first_seen", "last_seen"}

type hitKey struct{ uid, group, source string }

func hitsPath() string { return rpath("corpus", "search_hits.csv") }

func loadHits() map[hitKey]Row {
	rows, _ := readCSV(hitsPath())
	m := make(map[hitKey]Row, len(rows))
	for _, r := range rows {
		m[hitKey{r["uid"], r["group"], r["source"]}] = r
	}
	return m
}

func recordHit(h map[hitKey]Row, uid, group, scope, source, run string) bool {
	k := hitKey{uid, group, source}
	if r, ok := h[k]; ok {
		r["last_seen"] = run
		return false
	}
	h[k] = Row{"uid": uid, "group": group, "scope": scope, "source": source, "first_seen": run, "last_seen": run}
	return true
}

func saveHits(h map[hitKey]Row) error {
	return withLock(rpath("corpus", ".search_hits.lock"), func() error {
		merged := loadHits()
		for k, r := range h {
			if old, ok := merged[k]; ok {
				if r["first_seen"] < old["first_seen"] {
					old["first_seen"] = r["first_seen"]
				}
				if r["last_seen"] > old["last_seen"] {
					old["last_seen"] = r["last_seen"]
				}
			} else {
				merged[k] = r
			}
		}
		rows := make([]Row, 0, len(merged))
		for _, r := range merged {
			rows = append(rows, r)
		}
		sort.Slice(rows, func(i, j int) bool {
			a, b := rows[i], rows[j]
			if a["uid"] != b["uid"] {
				return a["uid"] < b["uid"]
			}
			if a["group"] != b["group"] {
				return a["group"] < b["group"]
			}
			return a["source"] < b["source"]
		})
		return writeCSV(hitsPath(), rows, hitCols)
	})
}
