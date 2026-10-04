package main

// Europe PMC search ("europepmc" step). Python twin: src/litsearch/pullliturature/search_europepmc.py.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const epmcAPI = "https://www.ebi.ac.uk/europepmc/webservices/rest/searchPOST"

func epmcPhrase(terms []string) string {
	q := make([]string, len(terms))
	for i, t := range terms {
		q[i] = epmcTerm(t)
	}
	return "(" + strings.Join(q, " OR ") + ")"
}

func epmcQuery(g Group, start, end, sources string) string {
	q := epmcPhrase(g.Terms)
	if len(g.RequireAny) > 0 {
		q += " AND " + epmcPhrase(g.RequireAny)
	}
	q = fmt.Sprintf("%s AND FIRST_PDATE:[%s TO %s]", q, start, end)
	if sources == "nonmed" {
		q += " AND NOT SRC:MED"
	}
	return q
}

func jstr(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func jmap(m map[string]any, k string) map[string]any {
	if v, ok := m[k].(map[string]any); ok {
		return v
	}
	return map[string]any{}
}

func jlist(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case nil:
		return nil
	default:
		return []any{x}
	}
}

func epmcNormalise(r map[string]any) Rec {
	ji := jmap(r, "journalInfo")
	journal := jstr(jmap(ji, "journal"), "title")
	source := jstr(r, "source")
	if source == "PPR" && journal == "" {
		journal = orDefault(jstr(jmap(r, "bookOrReportDetails"), "publisher"), "preprint")
	}
	var pubtypes []string
	for _, p := range jlist(jmap(r, "pubTypeList")["pubType"]) {
		if s, ok := p.(string); ok {
			pubtypes = append(pubtypes, s)
		}
	}
	var urls []any
	for _, u := range jlist(jmap(r, "fullTextUrlList")["fullTextUrl"]) {
		if um, ok := u.(map[string]any); ok && jstr(um, "url") != "" {
			urls = append(urls, jstr(um, "url"))
		}
	}
	var kws, mesh []string
	for _, k := range jlist(jmap(r, "keywordList")["keyword"]) {
		if s, ok := k.(string); ok {
			kws = append(kws, s)
		}
	}
	for _, m := range jlist(jmap(r, "meshHeadingList")["meshHeading"]) {
		if mm, ok := m.(map[string]any); ok {
			mesh = append(mesh, jstr(mm, "descriptorName"))
		}
	}
	fund := map[string]bool{}
	for _, g := range jlist(jmap(r, "grantsList")["grant"]) {
		if gm, ok := g.(map[string]any); ok && jstr(gm, "agency") != "" {
			fund[jstr(gm, "agency")] = true
		}
	}
	cited := 0
	if n, ok := toInt(r["citedByCount"]); ok {
		cited = n
	}
	if urls == nil {
		urls = []any{}
	}
	rec := Rec{
		"epmc_id": jstr(r, "id"), "source_db": source, "doi": normDOI(jstr(r, "doi")),
		"pmid": normPMID(jstr(r, "pmid")), "pmcid": normPMCID(jstr(r, "pmcid")),
		"title": cleanText(jstr(r, "title")), "authors": cleanText(jstr(r, "authorString")),
		"journal": cleanText(journal), "volume": jstr(ji, "volume"), "issue": jstr(ji, "issue"),
		"pages": jstr(r, "pageInfo"), "pub_date": jstr(r, "firstPublicationDate"), "pub_year": jstr(r, "pubYear"),
		"pub_types": strings.Join(pubtypes, "; "), "is_preprint": source == "PPR",
		"is_open_access": jstr(r, "isOpenAccess") == "Y", "in_pmc": jstr(r, "inPMC") == "Y",
		"has_pdf": jstr(r, "hasPDF") == "Y", "cited_by": cited, "license": jstr(r, "license"),
		"fulltext_urls": urls, "language": jstr(r, "language"), "abstract": cleanText(jstr(r, "abstractText")),
		"keywords": strings.Join(kws, "; "), "mesh": strings.Join(mesh, "; "),
		"funders": strings.Join(sortedKeys(fund), "; "),
	}
	rec["uid"] = makeUID(str(rec, "doi"), str(rec, "pmid"), str(rec, "pmcid"), str(rec, "epmc_id"), "")
	return rec
}

type epmcUnavailable struct{ msg string }

func (e epmcUnavailable) Error() string { return e.msg }

// epmcCursor calls fn for every page, shrinking pages on timeouts (500) and
// backing off on outages (502/503/504/429), as the Python version does.
func epmcCursor(c *Client, query, resultType string, size, maxSize int, log *Logger, fn func(map[string]any)) error {
	cursor, shrinks, ceiling, clean, waited, backoff := "*", 0, maxSize+1, 0, 0, 30
	for {
		r := c.Post(epmcAPI, url.Values{"query": {query}, "format": {"json"}, "pageSize": {strconv.Itoa(size)},
			"resultType": {resultType}, "cursorMark": {cursor}, "synonym": {"false"}},
			reqOpts{tries: 1, raw: true, timeout: 75 * time.Second})
		var js map[string]any
		status := 0
		if r != nil {
			status = r.Status
			if r.Status == 200 {
				d := json.NewDecoder(strings.NewReader(string(r.Body)))
				d.UseNumber()
				if d.Decode(&js) != nil {
					js, status = nil, 0
				}
			}
		}
		if js == nil {
			if status == 502 || status == 503 || status == 504 || status == 429 {
				if waited > 1800 {
					return epmcUnavailable{fmt.Sprintf("HTTP %d for %ds", status, waited)}
				}
				log.Printf("  Europe PMC HTTP %d; waiting %ds", status, backoff)
				time.Sleep(time.Duration(backoff) * time.Second)
				waited += backoff
				backoff = min(backoff*2, 480)
				continue
			}
			shrinks++
			if shrinks > 12 {
				return epmcUnavailable{fmt.Sprintf("pages keep failing (last HTTP %d)", status)}
			}
			ceiling, size, clean = size, max(10, size/2), 0
			log.Printf("  page failed (HTTP %d); page size -> %d", status, size)
			continue
		}
		shrinks, waited, backoff = 0, 0, 30
		clean++
		fn(js)
		results := jlist(jmap(js, "resultList")["result"])
		nxt := jstr(js, "nextCursorMark")
		if len(results) == 0 || nxt == "" || nxt == cursor {
			return nil
		}
		cursor = nxt
		if clean >= 25 {
			ceiling, clean = maxSize+1, 0
		}
		if size*2 < ceiling {
			size = min(maxSize, size*2)
		}
	}
}

func epmcIDKey(store *Store, r map[string]any) string {
	return store.FindID(jstr(r, "doi"), jstr(r, "pmid"), jstr(r, "pmcid"), jstr(r, "source")+"/"+jstr(r, "id"))
}

func epmcFetchCore(c *Client, source string, ids []string, store *Store, log *Logger) {
	for i := 0; i < len(ids); i += 100 {
		chunk := ids[i:min(i+100, len(ids))]
		parts := make([]string, len(chunk))
		for j, x := range chunk {
			parts[j] = "EXT_ID:" + x
		}
		q := "(" + strings.Join(parts, " OR ") + ") AND SRC:" + source
		err := epmcCursor(c, q, "core", 100, 100, log, func(js map[string]any) {
			for _, raw := range jlist(jmap(js, "resultList")["result"]) {
				if m, ok := raw.(map[string]any); ok {
					rec := epmcNormalise(m)
					if str(rec, "uid") != "" {
						store.Merge(rec, "europepmc")
					}
				}
			}
		})
		if err != nil {
			log.Printf("  core %s: chunk skipped (%v); next run retries it", source, err)
		}
		log.Printf("  core %s: %d/%d", source, min(i+100, len(ids)), len(ids))
	}
}

func stepEuropePMC(args []string, log *Logger) error {
	o := parseStepArgs(args)
	all, sources := loadGroups()
	if o["sources"] != "" {
		sources = o["sources"]
	}
	groups := filterGroups(all, o["groups"], orDefault(o["scope"], "all"))
	start, end := searchWindow()
	store := LoadStore()
	log.Printf("record store: %d records; window %s .. %s", store.Len(), start, end)
	hits := loadHits()
	run := today()
	var counts omap // python dicts keep insertion order
	c := NewClient(250*time.Millisecond, nil, log)
	pc := pubmedClient(log)
	for _, g := range groups {
		query := epmcQuery(g, start, end, sources)
		total := -1
		var ids []map[string]any
		err := epmcCursor(c, query, "idlist", 250, 1000, log, func(js map[string]any) {
			if total < 0 {
				total, _ = toInt(js["hitCount"])
			}
			for _, x := range jlist(jmap(js, "resultList")["result"]) {
				if m, ok := x.(map[string]any); ok {
					ids = append(ids, m)
				}
			}
		})
		if err != nil {
			log.Printf("%s: SKIPPED, Europe PMC unavailable (%v)", g.Name, err)
			counts = append(counts, okv{g.Name, omap{{"error", err.Error()}, {"query", query}}})
			continue
		}
		if total < 0 {
			total = 0
		}
		log.Printf("%s: %d hits, %d ids", g.Name, total, len(ids))
		var missing []map[string]any
		for _, r := range ids {
			if epmcIDKey(store, r) == "" {
				missing = append(missing, r)
			}
		}
		medSet := map[string]bool{}
		var otherKeys []string
		other := map[string][]string{}
		for _, r := range missing {
			if jstr(r, "source") == "MED" {
				if p := jstr(r, "pmid"); p != "" {
					medSet[p] = true
				} else {
					medSet[jstr(r, "id")] = true
				}
			} else {
				s := jstr(r, "source")
				if _, ok := other[s]; !ok {
					otherKeys = append(otherKeys, s)
				}
				other[s] = append(other[s], jstr(r, "id"))
			}
		}
		med := sortedKeys(medSet)
		nOther := 0
		for _, v := range other {
			nOther += len(v)
		}
		log.Printf("  new: %d MEDLINE -> efetch, %d other -> core", len(med), nOther)
		for _, rec := range efetch(pc, med, log) {
			store.Merge(rec, "pubmed")
		}
		for _, r := range missing {
			if jstr(r, "source") == "MED" && epmcIDKey(store, r) == "" {
				if _, ok := other["MED"]; !ok {
					otherKeys = append(otherKeys, "MED")
				}
				other["MED"] = append(other["MED"], jstr(r, "id"))
			}
		}
		for _, s := range otherKeys {
			epmcFetchCore(c, s, other[s], store, log)
		}
		nNew := 0
		for _, r := range ids {
			uid := epmcIDKey(store, r)
			if uid == "" {
				uid = makeUID(jstr(r, "doi"), jstr(r, "pmid"), jstr(r, "pmcid"), jstr(r, "source")+jstr(r, "id"), "")
				if uid == "" {
					continue
				}
			}
			if recordHit(hits, uid, g.Name, g.Scope, "europepmc", run) {
				nNew++
			}
		}
		counts = append(counts, okv{g.Name, omap{{"hitCount", total}, {"ids", len(ids)}, {"new_hits", nNew}, {"query", query}}})
		log.Printf("  %d new hit rows; store now %d", nNew, store.Len())
		if err := store.Save(); err != nil {
			return err
		}
		if err := saveHits(hits); err != nil {
			return err
		}
	}
	_ = writeJSON(rpath("raw", "europepmc", run, "counts.json"), omap{{"window", []string{start, end}}, {"groups", counts}})
	log.Printf("done: store %d records, %d hit rows", store.Len(), len(hits))
	return nil
}
