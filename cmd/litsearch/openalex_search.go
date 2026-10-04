package main

// OpenAlex search ("openalex-search" step). OpenAlex indexes works from every
// field of research, so it is what makes topics outside biomedicine work.
// Each group's terms are matched in title and abstract within the search
// window, newest first, up to search.max_results works per group.

import (
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const openalexWorks = "https://api.openalex.org/works"

var openalexSelect = "id,doi,title,display_name,publication_date,publication_year,type,authorships," +
	"primary_location,ids,abstract_inverted_index,cited_by_count,open_access,biblio,language,best_oa_location"

// openalexQuery: the group's terms (OR), and its require_any terms when given.
func openalexQuery(g Group) string {
	or := func(terms []string) string {
		q := make([]string, len(terms))
		for i, t := range terms {
			q[i] = openalexTerm(t)
		}
		if len(q) == 1 {
			return q[0]
		}
		return "(" + strings.Join(q, " OR ") + ")"
	}
	q := or(g.Terms)
	if len(g.RequireAny) > 0 {
		q += " AND " + or(g.RequireAny)
	}
	return q
}

// openalexAbstract rebuilds an abstract from OpenAlex's inverted index.
func openalexAbstract(idx map[string]any) string {
	type wp struct {
		pos  int
		word string
	}
	var words []wp
	for w, ps := range idx {
		for _, p := range jlist(ps) {
			if n, ok := toInt(p); ok {
				words = append(words, wp{n, w})
			}
		}
	}
	sort.Slice(words, func(i, j int) bool { return words[i].pos < words[j].pos })
	parts := make([]string, len(words))
	for i, w := range words {
		parts[i] = w.word
	}
	return cleanText(strings.Join(parts, " "))
}

// openalexTypes: OpenAlex work types and how the catalogue records them.
var openalexTypes = map[string]string{
	"article": "Journal Article", "review": "Review", "preprint": "preprint", "book-chapter": "book chapter",
	"book": "book chapter", "dissertation": "thesis", "dataset": "dataset", "report": "report",
	"editorial": "Editorial", "letter": "Letter", "erratum": "Erratum", "retraction": "Retraction",
	"standard": "report", "other": "",
}

// openalexWork maps an OpenAlex work to a store record (nil for types that are not research).
func openalexWork(w map[string]any) Rec {
	typ := jstr(w, "type")
	pt, ok := openalexTypes[typ]
	if !ok {
		return nil // paratext, peer-review, grant, libguides, ...
	}
	loc := jmap(w, "primary_location")
	src := jmap(loc, "source")
	if typ == "article" && jstr(src, "type") == "conference" {
		pt = "conference paper"
	}
	ids := jmap(w, "ids")
	var names []string
	for _, a := range jlist(w["authorships"]) {
		if n := jstr(jmap(ymap(a), "author"), "display_name"); n != "" {
			names = append(names, n)
		}
	}
	oa := jmap(w, "open_access")
	var urls []any
	if u := jstr(oa, "oa_url"); u != "" {
		urls = append(urls, u)
	}
	if u := jstr(jmap(w, "best_oa_location"), "pdf_url"); u != "" && (len(urls) == 0 || urls[0] != u) {
		urls = append(urls, u)
	}
	if urls == nil {
		urls = []any{}
	}
	bib := jmap(w, "biblio")
	pages := jstr(bib, "first_page")
	if lp := jstr(bib, "last_page"); lp != "" && lp != pages {
		pages += "-" + lp
	}
	cited, _ := toInt(w["cited_by_count"])
	isOA, _ := oa["is_oa"].(bool)
	date := jstr(w, "publication_date")
	year := jstr(w, "publication_year")
	if year == "" && len(date) >= 4 {
		year = date[:4]
	}
	rec := Rec{
		"source_db": "OPENALEX", "openalex_id": jstr(w, "id"), "doi": normDOI(jstr(w, "doi")),
		"pmid": normPMID(jstr(ids, "pmid")), "pmcid": normPMCID(jstr(ids, "pmcid")),
		"title": cleanText(orDefault(jstr(w, "title"), jstr(w, "display_name"))), "authors": strings.Join(names, ", "),
		"journal": cleanText(jstr(src, "display_name")), "volume": jstr(bib, "volume"), "issue": jstr(bib, "issue"),
		"pages": pages, "pub_date": date, "pub_year": year, "pub_types": pt, "is_preprint": typ == "preprint",
		"is_open_access": isOA, "cited_by": cited, "license": jstr(loc, "license"), "fulltext_urls": urls,
		"language": jstr(w, "language"), "abstract": openalexAbstract(jmap(w, "abstract_inverted_index")),
		"url": orDefault(jstr(loc, "landing_page_url"), jstr(w, "id")),
		"oa_pdf_url": orDefault(jstr(jmap(w, "best_oa_location"), "pdf_url"), func() string {
			if isOA {
				return jstr(loc, "pdf_url")
			}
			return ""
		}()),
	}
	if str(rec, "title") == "" {
		return nil
	}
	link := ""
	if str(rec, "doi") == "" && str(rec, "pmid") == "" && str(rec, "pmcid") == "" {
		link = jstr(w, "id")
	}
	rec["uid"] = makeUID(str(rec, "doi"), str(rec, "pmid"), str(rec, "pmcid"), "", link)
	return rec
}

var workKeyRe = regexp.MustCompile(`[^\pL\pN]+`)

// workKey: works with the same title, venue and year are versions of one item
// (Zenodo concept and version DOIs, figshare .v2, repository copies).
func workKey(r Rec) string {
	t := strings.TrimSpace(workKeyRe.ReplaceAllString(strings.ToLower(str(r, "title")), " "))
	if t == "" {
		return ""
	}
	year := runeCut(orDefault(str(r, "pub_year"), str(r, "pub_date")), 4)
	return t + "|" + strings.ToLower(strings.TrimSpace(str(r, "journal"))) + "|" + year
}

func searchMaxResults() int {
	var s struct {
		MaxResults int `yaml:"max_results"`
	}
	_ = loadConfig("search", &s)
	if s.MaxResults <= 0 {
		return 5000
	}
	return s.MaxResults
}

func stepOpenAlexSearch(args []string, log *Logger) error {
	o := parseStepArgs(args)
	all, _ := loadGroups()
	groups := filterGroups(all, o["groups"], orDefault(o["scope"], "all"))
	start, end := searchWindow()
	limit := searchMaxResults()
	c := NewClient(120*time.Millisecond, nil, log)
	store := LoadStore()
	hits := loadHits()
	run := today()
	log.Printf("record store: %d records; window %s .. %s; at most %d works per group", store.Len(), start, end, limit)
	seen := map[string]string{} // workKey -> uid of the version already stored
	for _, r := range store.Values() {
		if k := workKey(r); k != "" {
			if _, ok := seen[k]; !ok {
				seen[k] = str(r, "uid")
			}
		}
	}
	var counts omap
	for _, g := range groups {
		query := openalexQuery(g)
		// OpenAlex stems words ("base" also finds "based"); keep a work only when the
		// terms appear as whole words in its title or abstract. Works whose abstract
		// OpenAlex does not provide are kept: they matched its full index.
		match := wordMatcher(g.Terms)
		var req func(string) bool
		if len(g.RequireAny) > 0 {
			req = wordMatcher(g.RequireAny)
		}
		dropped, versions := 0, 0
		filter := "title_and_abstract.search:" + query + ",from_publication_date:" + start + ",to_publication_date:" + end
		cursor, total, collected, nNew := "*", 0, 0, 0
		for cursor != "" && collected < limit {
			params := url.Values{"filter": {filter}, "per-page": {"200"}, "cursor": {cursor},
				"sort": {"publication_date:desc"}, "select": {openalexSelect}}
			if contact() != "" {
				params.Set("mailto", contact())
			}
			if k := os.Getenv("OPENALEX_API_KEY"); k != "" {
				params.Set("api_key", k)
			}
			r := c.Get(openalexWorks, reqOpts{params: params, timeout: 120 * time.Second})
			if r == nil || r.Status != 200 {
				status := 0
				if r != nil {
					status = r.Status
				}
				log.Printf("%s: OpenAlex request failed (HTTP %d); keeping %d works", g.Name, status, collected)
				break
			}
			j := decodeJSON(r.Body)
			meta := jmap(j, "meta")
			if n, ok := toInt(meta["count"]); ok {
				total = n
			}
			results := jlist(j["results"])
			for _, x := range results {
				rec := openalexWork(ymap(x))
				if rec == nil {
					continue
				}
				if text := str(rec, "title") + " " + str(rec, "abstract"); str(rec, "abstract") != "" &&
					(!match(text) || (req != nil && !req(text))) {
					dropped++
					continue
				}
				if store.Find(rec) == "" {
					if first, ok := seen[workKey(rec)]; ok && workKey(rec) != "" {
						// another version of a stored work: count the hit for that one
						versions++
						if recordHit(hits, first, g.Name, g.Scope, "openalex", run) {
							nNew++
						}
						continue
					}
				}
				uid := store.Merge(rec, "openalex")
				if k := workKey(rec); k != "" {
					if _, ok := seen[k]; !ok {
						seen[k] = uid
					}
				}
				if recordHit(hits, uid, g.Name, g.Scope, "openalex", run) {
					nNew++
				}
				collected++
			}
			cursor = jstr(meta, "next_cursor")
			if len(results) == 0 {
				break
			}
		}
		if total > collected && collected >= limit {
			log.Printf("%s: OpenAlex has %d works, kept the newest %d (search.max_results)", g.Name, total, collected)
		}
		log.Printf("%s: OpenAlex count %d, collected %d, %d new hit rows, %d dropped (terms not in title or abstract), "+
			"%d other versions of stored works", g.Name, total, collected, nNew, dropped, versions)
		counts = append(counts, okv{g.Name, omap{{"count", total}, {"collected", collected}, {"new_hits", nNew},
			{"query", query}}})
		if err := store.Save(); err != nil {
			return err
		}
		if err := saveHits(hits); err != nil {
			return err
		}
	}
	_ = writeJSON(rpath("raw", "openalex", run, "counts.json"), omap{{"window", []string{start, end}}, {"groups", counts},
		{"max_results", strconv.Itoa(limit)}})
	log.Printf("done: store %d records", store.Len())
	return nil
}
