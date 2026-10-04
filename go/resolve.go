package main

// Seed resolution ("resolve-seeds" step) with Crossref / DataCite lookups.
// Mirrors python/pullliturature/resolve_seeds.py and python/common/crossref.py.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

var crossrefTypes = map[string]string{"journal-article": "article", "posted-content": "preprint",
	"proceedings-article": "conference paper", "book-chapter": "book chapter", "book": "book", "dataset": "dataset",
	"report": "report", "dissertation": "thesis", "peer-review": "peer review", "reference-entry": "reference entry"}

func escapeDOIPath(doi string) string {
	parts := strings.Split(doi, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func crDate(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	dp, ok := m["date-parts"].([]any)
	if !ok || len(dp) == 0 {
		return ""
	}
	p, ok := dp[0].([]any)
	if !ok || len(p) == 0 || p[0] == nil {
		return ""
	}
	y, _ := toInt(p[0])
	mo, d := 1, 1
	if len(p) > 1 {
		mo, _ = toInt(p[1])
	}
	if len(p) > 2 {
		d, _ = toInt(p[2])
	}
	return fmt.Sprintf("%04d-%02d-%02d", y, mo, d)
}

func decodeJSON(b []byte) map[string]any {
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.UseNumber()
	var m map[string]any
	if d.Decode(&m) != nil {
		return nil
	}
	return m
}

func firstStr(v any) string {
	if l, ok := v.([]any); ok && len(l) > 0 {
		if s, ok := l[0].(string); ok {
			return s
		}
	}
	return ""
}

func crossrefLookup(c *Client, doi string) Rec {
	var p url.Values
	if contact() != "" {
		p = url.Values{"mailto": {contact()}}
	}
	r := c.Get("https://api.crossref.org/works/"+escapeDOIPath(doi), reqOpts{params: p})
	if r == nil || r.Status != 200 {
		return nil
	}
	js := decodeJSON(r.Body)
	m := jmap(js, "message")
	var dates []string
	for _, k := range []string{"published-online", "published-print", "posted", "issued", "published"} {
		if d := crDate(m[k]); d != "" {
			dates = append(dates, d)
		}
	}
	var authors []string
	for _, a := range jlist(m["author"]) {
		am := ymap(a)
		if fam := jstr(am, "family"); fam != "" {
			var ini strings.Builder
			for _, x := range pyFields(strings.ReplaceAll(jstr(am, "given"), "-", " ")) {
				ini.WriteString(string([]rune(x)[0]))
			}
			authors = append(authors, strings.TrimSpace(fam+" "+ini.String()))
		} else if n := jstr(am, "name"); n != "" {
			authors = append(authors, n)
		}
	}
	ctype := jstr(m, "type")
	venue := firstStr(m["container-title"])
	if venue == "" {
		switch inst := m["institution"].(type) {
		case []any:
			if len(inst) > 0 {
				venue = jstr(ymap(inst[0]), "name")
			}
		case map[string]any:
			venue = jstr(inst, "name")
		}
	}
	if venue == "" && ctype == "posted-content" {
		venue = orDefault(jstr(m, "publisher"), "preprint")
	}
	lic := ""
	if l := jlist(m["license"]); len(l) > 0 {
		lic = jstr(ymap(l[0]), "URL")
	}
	links := []any{}
	for _, l := range jlist(m["link"]) {
		lm := ymap(l)
		if jstr(lm, "content-type") == "application/pdf" {
			links = append(links, jstr(lm, "URL"))
		}
	}
	sort.Strings(dates)
	date, year := "", ""
	if len(dates) > 0 {
		date, year = dates[0], dates[0][:4]
	}
	typ := ctype
	if t, ok := crossrefTypes[ctype]; ok {
		typ = t
	}
	cited, _ := toInt(m["is-referenced-by-count"])
	rec := Rec{"epmc_id": "", "source_db": "CROSSREF", "doi": normDOI(orDefault(jstr(m, "DOI"), doi)),
		"pmid": "", "pmcid": "", "title": cleanText(firstStr(m["title"])), "authors": strings.Join(authors, ", "),
		"journal": cleanText(venue), "volume": jstr(m, "volume"), "issue": jstr(m, "issue"), "pages": jstr(m, "page"),
		"pub_date": date, "pub_year": year, "pub_types": typ, "is_preprint": ctype == "posted-content",
		"cited_by": cited, "license": lic, "fulltext_urls": links, "abstract": cleanText(jstr(m, "abstract")),
		"publisher": jstr(m, "publisher")}
	rec["uid"] = makeUID(str(rec, "doi"), "", "", "", "")
	return rec
}

func dataciteLookup(c *Client, doi string) Rec {
	r := c.Get("https://api.datacite.org/dois/"+escapeDOIPath(doi), reqOpts{})
	if r == nil || r.Status != 200 {
		return nil
	}
	a := jmap(jmap(decodeJSON(r.Body), "data"), "attributes")
	title := ""
	if t := jlist(a["titles"]); len(t) > 0 {
		title = jstr(ymap(t[0]), "title")
	}
	var creators []string
	for _, cr := range jlist(a["creators"]) {
		creators = append(creators, jstr(ymap(cr), "name"))
	}
	dates := map[string]string{}
	for _, d := range jlist(a["dates"]) {
		dm := ymap(d)
		dates[jstr(dm, "dateType")] = jstr(dm, "date")
	}
	date := orDefault(dates["Issued"], dates["Created"])
	if date == "" {
		date = jstr(a, "publicationYear") + "-01-01"
	}
	var desc []string
	for _, d := range jlist(a["descriptions"]) {
		dm := ymap(d)
		if jstr(dm, "descriptionType") == "Abstract" {
			desc = append(desc, jstr(dm, "description"))
		}
	}
	lic := ""
	if l := jlist(a["rightsList"]); len(l) > 0 {
		lic = jstr(ymap(l[0]), "rightsUri")
	}
	rec := Rec{"epmc_id": "", "source_db": "DATACITE", "doi": normDOI(orDefault(jstr(a, "doi"), doi)),
		"pmid": "", "pmcid": "", "title": cleanText(title), "authors": strings.Join(creators, ", "),
		"journal": jstr(a, "publisher"), "pub_date": runeCut(date, 10), "pub_year": runeCut(date, 4),
		"pub_types": strings.ToLower(jstr(jmap(a, "types"), "resourceTypeGeneral")), "is_preprint": false,
		"abstract": cleanText(strings.Join(desc, " ")), "url": jstr(a, "url"), "license": lic}
	rec["uid"] = makeUID(str(rec, "doi"), "", "", "", "")
	return rec
}

var resolveCols = []string{"doi", "resolved_via", "uid", "pub_date", "title", "roles"}

func stepResolveSeeds(args []string, log *Logger) error {
	seeds, _ := readCSV(rpath("seeds", "local_seeds.csv"))
	roles := map[string]map[string]bool{}
	for _, s := range seeds {
		if roles[s["doi"]] == nil {
			roles[s["doi"]] = map[string]bool{}
		}
		roles[s["doi"]][s["role"]] = true
	}
	store := LoadStore()
	oldRows, _ := readCSV(rpath("seeds", "seed_resolution.csv"))
	old := map[string]Row{}
	for _, r := range oldRows {
		old[r["doi"]] = r
	}
	out := map[string]Row{}
	var todo []string
	for _, doi := range sortedKeys(roles) {
		if uid := store.FindID(doi, "", ""); uid != "" {
			out[doi] = Row{"doi": doi, "resolved_via": orDefault(old[doi]["resolved_via"], "search"), "uid": uid}
		} else {
			todo = append(todo, doi)
		}
	}
	log.Printf("%d seed DOIs; %d already in store; resolving %d", len(roles), len(roles)-len(todo), len(todo))

	pc := pubmedClient(log)
	var pmids []string
	for i := 0; i < len(todo); i += 40 {
		chunk := todo[i:min(i+40, len(todo))]
		q := make([]string, len(chunk))
		for j, d := range chunk {
			q[j] = fmt.Sprintf(`"%s"[doi]`, d)
		}
		_, ids, err := esearch(pc, strings.Join(q, " OR "), 200)
		if err == nil {
			pmids = append(pmids, ids...)
		}
	}
	set := map[string]bool{}
	for _, p := range pmids {
		set[p] = true
	}
	for _, r := range efetch(pc, sortedKeys(set), log) {
		store.Merge(r, "seed:pubmed")
	}
	var todo2 []string
	for _, d := range todo {
		if uid := store.FindID(d, "", ""); uid != "" {
			out[d] = Row{"doi": d, "resolved_via": "pubmed", "uid": uid}
		} else {
			todo2 = append(todo2, d)
		}
	}
	log.Printf("PubMed resolved %d; %d left", len(todo)-len(todo2), len(todo2))

	ec := NewClient(250*time.Millisecond, nil, log)
	for i := 0; i < len(todo2); i += 25 {
		chunk := todo2[i:min(i+25, len(todo2))]
		q := make([]string, len(chunk))
		for j, d := range chunk {
			q[j] = fmt.Sprintf(`DOI:"%s"`, d)
		}
		err := epmcCursor(ec, strings.Join(q, " OR "), "core", 25, 25, log, func(js map[string]any) {
			for _, raw := range jlist(jmap(js, "resultList")["result"]) {
				if m, ok := raw.(map[string]any); ok {
					if rec := epmcNormalise(m); str(rec, "uid") != "" {
						store.Merge(rec, "seed:europepmc")
					}
				}
			}
		})
		if err != nil {
			log.Printf("Europe PMC unavailable (%v); falling through to Crossref", err)
			break
		}
	}
	var todo3 []string
	for _, d := range todo2 {
		if uid := store.FindID(d, "", ""); uid != "" {
			out[d] = Row{"doi": d, "resolved_via": "europepmc", "uid": uid}
		} else {
			todo3 = append(todo3, d)
		}
	}
	log.Printf("Europe PMC resolved %d; %d left", len(todo2)-len(todo3), len(todo3))

	crUA := "litSearch/2.0"
	if contact() != "" {
		crUA += " (mailto:" + contact() + ")"
	}
	cc := NewClient(100*time.Millisecond, map[string]string{"User-Agent": crUA}, log)
	for _, d := range todo3 {
		rec, via := crossrefLookup(cc, d), "crossref"
		if rec == nil {
			rec, via = dataciteLookup(cc, d), "datacite"
		}
		if rec == nil {
			out[d] = Row{"doi": d, "resolved_via": "unresolved", "uid": ""}
			continue
		}
		out[d] = Row{"doi": d, "resolved_via": via, "uid": store.Merge(rec, "seed:"+via)}
	}
	if err := store.Save(); err != nil {
		return err
	}
	var rows []Row
	via := map[string]int{}
	for _, d := range sortedKeys(out) {
		r := out[d]
		if r["uid"] != "" {
			rec := store.Get(r["uid"])
			r["pub_date"], r["title"] = str(rec, "pub_date"), str(rec, "title")
		}
		r["roles"] = strings.Join(sortedKeys(roles[d]), "; ")
		rows = append(rows, r)
		via[r["resolved_via"]]++
	}
	if err := writeCSV(rpath("seeds", "seed_resolution.csv"), rows, resolveCols); err != nil {
		return err
	}
	parts := []string{}
	for _, k := range sortedKeys(via) {
		parts = append(parts, fmt.Sprintf("'%s': %d", k, via[k]))
	}
	log.Printf("seed resolution: {%s}", strings.Join(parts, ", "))
	return nil
}
