package main

// Catalogue build ("catalogue" step): progress.csv, changelog, side files,
// references.csv and summary stats.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var progressCols = []string{"added_on", "date", "year", "resource_type", "tier", "category", "modality",
	"entities", "species", "title", "authors", "venue", "doi", "pmid", "pmcid",
	"url", "code_links", "related", "open_access", "license", "fulltext_status",
	"fulltext_pdf", "fulltext_md", "fulltext_txt", "metadata_file", "cited_by",
	"details", "matched_groups", "found_by", "local_refs", "status", "uid", "notes", "user_tags"}

var keepOnUpdate = map[string]bool{"added_on": true, "notes": true, "user_tags": true}
var tierOrder = map[string]int{"landmark": 0, "core": 1, "related": 2}
var refCols = []string{"uid", "resource_type", "date", "year", "authors", "title", "venue", "volume", "issue",
	"pages", "doi", "pmid", "pmcid", "url"}

var (
	fullDate     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	yearOnly     = regexp.MustCompile(`^\d{4}$`)
	registryNote = regexp.MustCompile(`^[^;]* registry: [^;]*;?`)
)

func resourceType(rec Rec, category string) string {
	pt := strings.ToLower(str(rec, "pub_types"))
	if boolean(rec, "is_preprint") || str(rec, "source_db") == "PPR" || strings.Contains(pt, "preprint") {
		return "preprint"
	}
	if category == "review" {
		return "review"
	}
	for _, k := range []string{"dataset", "software", "book chapter", "conference paper", "thesis", "report"} {
		if strings.Contains(pt, k) {
			return k
		}
	}
	return "article"
}

func bestDate(rec Rec) string {
	d := strings.TrimSpace(str(rec, "pub_date"))
	if fullDate.MatchString(d) {
		return d
	}
	y := str(rec, "pub_year")
	if y == "" && len(d) >= 4 {
		y = d[:4]
	}
	y = strings.TrimSpace(y)
	if yearOnly.MatchString(y) {
		return y + "-01-01"
	}
	return ""
}

var serverNames = [][2]string{{"biorxiv", "bioRxiv"}, {"medrxiv", "medRxiv"}, {"research square", "Research Square"},
	{"preprints.org", "Preprints.org"}, {"ssrn", "SSRN"}, {"psyarxiv", "PsyArXiv"}}

func venueName(v string) string {
	low := strings.ToLower(strings.TrimSpace(v))
	for _, kv := range serverNames {
		if strings.HasPrefix(low, kv[0]) {
			return kv[1]
		}
	}
	return v
}

func firstAuthors(a string, n int) string {
	var parts []string
	for _, p := range strings.Split(strings.TrimRight(a, "."), ",") {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) <= n {
		return strings.Join(parts, ", ")
	}
	return strings.Join(parts[:n], ", ") + " et al."
}

type seedInfo struct {
	roles       map[string]bool
	refs        string
	registryIDs string
}

func loadSeedInfo() map[string]*seedInfo {
	rows, _ := readCSV(rpath("seeds", "local_seeds.csv"))
	type acc struct {
		roles, refs map[string]bool
		ids         map[string]map[string]bool // repo -> registry ids
	}
	tmp := map[string]*acc{}
	for _, s := range rows {
		d := tmp[s["doi"]]
		if d == nil {
			d = &acc{map[string]bool{}, map[string]bool{}, map[string]map[string]bool{}}
			tmp[s["doi"]] = d
		}
		role := strings.Replace(s["role"], "clock_", "registry_", 1) // seeds written before 0.0.1
		d.roles[role] = true
		d.refs[s["source_repo"]+":"+role] = true
		if id := orDefault(s["registry_id"], s["clock_name"]); id != "" {
			if d.ids[s["source_repo"]] == nil {
				d.ids[s["source_repo"]] = map[string]bool{}
			}
			d.ids[s["source_repo"]][id] = true
		}
	}
	out := map[string]*seedInfo{}
	for k, d := range tmp {
		si := &seedInfo{roles: d.roles, refs: strings.Join(sortedKeys(d.refs), "; ")}
		var notes []string
		for _, repo := range sortedKeys(d.ids) {
			notes = append(notes, repo+" registry: "+strings.Join(sortedKeys(d.ids[repo]), ", "))
		}
		si.registryIDs = strings.Join(notes, "; ")
		out[k] = si
	}
	return out
}

func joinNonEmpty(sep string, xs ...string) string {
	var out []string
	for _, x := range xs {
		if x != "" {
			out = append(out, x)
		}
	}
	return strings.Join(out, sep)
}

func atoiSafe(s string) int {
	i, _ := strconv.Atoi(strings.TrimSpace(s))
	return i
}

// orderedRows keeps Python dict semantics: first insertion fixes the position.
type orderedRows struct {
	keys []string
	m    map[string]Row
}

func newOrdered() *orderedRows { return &orderedRows{m: map[string]Row{}} }
func (o *orderedRows) set(k string, r Row) {
	if _, ok := o.m[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.m[k] = r
}
func (o *orderedRows) values() []Row {
	out := make([]Row, len(o.keys))
	for i, k := range o.keys {
		out[i] = o.m[k]
	}
	return out
}

func catalogueSortKey(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		da, db := orDefault(a["date"], "9999"), orDefault(b["date"], "9999")
		if da != db {
			return da < db
		}
		ta, ok := tierOrder[a["tier"]]
		if !ok {
			ta = 9
		}
		tb, ok := tierOrder[b["tier"]]
		if !ok {
			tb = 9
		}
		if ta != tb {
			return ta < tb
		}
		return strings.ToLower(a["title"]) < strings.ToLower(b["title"])
	})
}

func stepCatalogue(args []string, log *Logger) error {
	clf := LoadClassifier()
	start, end := queryWindow()
	store := LoadStore()
	log.Printf("store: %d records; window %s .. %s", store.Len(), start, end)

	groups, scopes, found := map[string]map[string]bool{}, map[string]map[string]bool{}, map[string]map[string]bool{}
	add := func(m map[string]map[string]bool, k, v string) {
		if m[k] == nil {
			m[k] = map[string]bool{}
		}
		m[k][v] = true
	}
	hitRows, _ := readCSV(hitsPath())
	for _, h := range hitRows {
		uid := store.Find(Rec{"uid": h["uid"]})
		if uid == "" {
			uid = h["uid"]
		}
		add(groups, uid, h["group"])
		add(scopes, uid, h["scope"])
		add(found, uid, h["source"])
	}
	seeds := loadSeedInfo()
	ftRows, _ := readCSV(rpath("fulltext", "fulltext_index.csv"))
	ft := map[string]Row{}
	for _, r := range ftRows {
		ft[r["uid"]] = r
	}
	links := map[string][]string{}
	plRows, _ := readCSV(rpath("repos", "paper_links.csv"))
	for _, r := range plRows {
		if !contains(links[r["uid"]], r["url"]) {
			links[r["uid"]] = append(links[r["uid"]], r["url"])
		}
	}
	oaRows, _ := readCSV(rpath("corpus", "citations.csv"))
	openalex := map[string]Row{}
	for _, r := range oaRows {
		openalex[r["uid"]] = r
	}
	ppLinks := map[string]string{}
	ppRows, _ := readCSV(rpath("corpus", "preprint_links.csv"))
	for _, r := range ppRows {
		if r["published_doi"] != "" {
			ppLinks[r["preprint_doi"]] = "published as " + r["published_doi"]
			ppLinks[r["published_doi"]] = "preprint " + r["preprint_doi"]
		}
	}

	recs := store.Values()
	anns := clf.AnnotateAll(recs)
	rows := newOrdered()
	var broad, pre, offtopic []Row
	for i, rec := range recs {
		uid := str(rec, "uid")
		ann := anns[i]
		doi := str(rec, "doi")
		var sd *seedInfo
		if doi != "" {
			sd = seeds[doi]
		}
		roles := map[string]bool{}
		if sd != nil {
			roles = sd.roles
		}
		isLandmark := false
		for r := range roles {
			if clf.landmarkRoles[r] {
				isLandmark = true
			}
		}
		focused := scopes[uid]["focused"]
		fb := map[string]bool{}
		for k := range found[uid] {
			fb[k] = true
		}
		if sd != nil {
			fb["local seed"] = true
		}
		if roles["registry_origin"] {
			ann.Category = clf.originCategory
		}
		f := ft[uid]
		folder := f["folder"]
		fp := func(name, flag string) string {
			if folder != "" && f[flag] == "True" {
				return folder + "/" + name
			}
			return ""
		}
		url := ""
		if doi != "" {
			url = "https://doi.org/" + doi
		} else if p := str(rec, "pmid"); p != "" {
			url = "https://pubmed.ncbi.nlm.nih.gov/" + p + "/"
		} else {
			url = str(rec, "url")
		}
		cited := num(rec, "cited_by")
		if c := atoiSafe(openalex[uid]["cited_by"]); c > cited {
			cited = c
		}
		oa := "no"
		if boolean(rec, "is_open_access") || f["status"] == "ok" || f["status"] == "partial" {
			oa = "yes"
		} else if f == nil {
			oa = "unknown"
		}
		date := bestDate(rec)
		registryIDs := ""
		refs := ""
		if sd != nil {
			registryIDs, refs = sd.registryIDs, sd.refs
		}
		citedS := ""
		if cited != 0 {
			citedS = strconv.Itoa(cited)
		}
		meta := ""
		if folder != "" {
			meta = folder + "/metadata.json"
		}
		row := Row{"date": date, "year": runeCut(date, 4), "resource_type": resourceType(rec, ann.Category),
			"category": ann.Category, "modality": ann.Modality, "entities": joinNonEmpty("; ", registryIDs, ann.Entities),
			"species": ann.Species, "title": str(rec, "title"), "authors": firstAuthors(str(rec, "authors"), 3),
			"venue": venueName(str(rec, "journal")), "doi": doi, "pmid": str(rec, "pmid"), "pmcid": str(rec, "pmcid"),
			"url": url, "code_links": strings.Join(links[uid], "; "), "related": "", "open_access": oa,
			"license": orDefault(f["license"], str(rec, "license")), "fulltext_status": f["status"],
			"fulltext_pdf": fp("paper.pdf", "has_pdf"), "fulltext_md": fp("paper.md", "has_md"),
			"fulltext_txt": fp("paper.txt", "has_txt"), "metadata_file": meta, "cited_by": citedS,
			"matched_groups": strings.Join(sortedKeys(groups[uid]), "; "),
			"found_by":       strings.Join(sortedKeys(fb), "; "), "local_refs": refs, "uid": uid}
		oaDate := openalex[uid]["publication_date"]
		if strings.HasSuffix(row["date"], "-01-01") && runeCut(oaDate, 4) == runeCut(row["date"], 4) && oaDate != row["date"] {
			row["date"] = oaDate
		}
		if v, ok := ppLinks[doi]; ok {
			row["related"] = v
		} else if rd := str(rec, "related_doi"); rd != "" {
			row["related"] = "published as " + rd
		}
		if row["date"] == "" {
			row["status"] = "undated"
		}
		inWindow := row["date"] == "" || (start <= row["date"] && row["date"] <= end)
		switch {
		case isLandmark:
			row["tier"] = "landmark"
		case focused && (ann.CoreTitle || contains(clf.coreCategories, ann.Category)):
			row["tier"] = "core"
		case focused:
			row["tier"] = "related"
		case sd != nil && ann.CoreTitle:
			row["tier"] = "related"
		default:
			row["tier"] = ""
		}
		switch {
		case row["tier"] != "" && !inWindow:
			pre = append(pre, row)
		case row["tier"] != "":
			rows.set(uid, row)
		case sd != nil:
			offtopic = append(offtopic, row)
		case scopes[uid]["broad"]:
			broad = append(broad, row)
		}
	}
	for _, p := range []string{rpath("repos", "repositories_catalogue.csv"), rpath("websites", "websites_catalogue.csv")} {
		extra, _ := readCSV(p)
		for _, r := range extra {
			if r["uid"] == "" {
				continue
			}
			n := Row{}
			for _, c := range progressCols {
				if !keepOnUpdate[c] {
					n[c] = r[c]
				}
			}
			rows.set(r["uid"], n)
		}
	}

	existingRows, _ := readCSV(rpath("progress.csv"))
	existing := newOrdered()
	for _, r := range existingRows {
		existing.set(r["uid"], r)
	}
	run := today()
	var added []Row
	merged := newOrdered()
	for _, uid := range rows.keys {
		n := rows.m[uid]
		if old, ok := existing.m[uid]; !ok {
			n["added_on"] = run
			added = append(added, n)
		} else {
			for k := range keepOnUpdate {
				n[k] = old[k]
			}
		}
		if _, ok := n["status"]; !ok {
			n["status"] = ""
		}
		merged.set(uid, n)
	}
	kept := 0
	for _, uid := range existing.keys {
		if _, ok := merged.m[uid]; !ok {
			old := existing.m[uid]
			old["status"] = "no longer matched by pipeline (kept)"
			merged.set(uid, old)
			kept++
		}
	}
	final := merged.values()
	catalogueSortKey(final)
	if err := writeCSV(rpath("progress.csv"), final, progressCols); err != nil {
		return err
	}
	_ = os.MkdirAll(rpath("changelog"), 0o755)
	if len(added) > 0 {
		dayFile := rpath("changelog", "added_"+run+".csv")
		prev, _ := readCSV(dayFile)
		day := newOrdered()
		for _, r := range prev {
			day.set(r["uid"], r)
		}
		for _, r := range added {
			day.set(r["uid"], r)
		}
		dv := day.values()
		catalogueSortKey(dv)
		_ = writeCSV(dayFile, dv, progressCols)
	}
	runs, runCols := readCSV(rpath("changelog", "runs.csv"))
	newRun := Row{"run_date": run, "window_end": end, "rows_total": strconv.Itoa(len(final)),
		"rows_added": strconv.Itoa(len(added)), "rows_kept_unmatched": strconv.Itoa(kept),
		"broad_only": strconv.Itoa(len(broad)), "store_records": strconv.Itoa(store.Len())}
	runs = append(runs, newRun)
	runCols = []string{"run_date", "window_end", "rows_total", "rows_added", "rows_kept_unmatched", "broad_only", "store_records"}
	_ = writeCSV(rpath("changelog", "runs.csv"), runs, runCols)

	writeReferences(final, store)
	for _, x := range []struct {
		name string
		rows []Row
	}{{"broad_hits.csv", broad}, {"before_window.csv", pre}, {"offtopic_seeds.csv", offtopic}} {
		catalogueSortKey(x.rows)
		_ = writeCSV(rpath("corpus", x.name), x.rows, progressCols)
	}
	summaryStats(final)
	log.Printf("progress.csv: %d rows (%d added this run, %d kept though unmatched); broad-only %d, before the window %d, off-topic seeds %d",
		len(final), len(added), kept, len(broad), len(pre), len(offtopic))
	return nil
}

func writeReferences(rows []Row, store *Store) {
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		rec := store.Get(r["uid"])
		authors := r["authors"]
		if rec != nil && str(rec, "authors") != "" {
			authors = str(rec, "authors")
		}
		out = append(out, Row{"uid": r["uid"], "resource_type": r["resource_type"], "date": r["date"], "year": r["year"],
			"authors": strings.TrimRight(strings.TrimSpace(authors), "."), "title": r["title"], "venue": r["venue"],
			"volume": str(rec, "volume"), "issue": str(rec, "issue"), "pages": str(rec, "pages"), "doi": r["doi"],
			"pmid": r["pmid"], "pmcid": r["pmcid"], "url": r["url"]})
	}
	_ = writeCSV(rpath("corpus", "references.csv"), out, refCols)
}

// ---------------------------------------------------------------------------
// summary/by_year_type.csv, by_year_modality.csv, stats.json
// ---------------------------------------------------------------------------

type counter struct {
	keys []string
	n    map[string]int
}

func newCounter() *counter { return &counter{n: map[string]int{}} }
func (c *counter) add(k string, v int) {
	if _, ok := c.n[k]; !ok {
		c.keys = append(c.keys, k)
	}
	c.n[k] += v
}

// mostCommon: count descending, ties in first-seen order (Counter.most_common).
func (c *counter) mostCommon(limit int) []string {
	ks := append([]string(nil), c.keys...)
	sort.SliceStable(ks, func(i, j int) bool { return c.n[ks[i]] > c.n[ks[j]] })
	if limit > 0 && len(ks) > limit {
		ks = ks[:limit]
	}
	return ks
}

type okv struct {
	k string
	v any
}
type omap []okv

func (m omap) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, kv := range m {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := jsonMarshal(kv.k)
		vb, err := jsonMarshal(kv.v)
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func counterMap(c *counter, keys []string) omap {
	out := omap{}
	for _, k := range keys {
		out = append(out, okv{k, c.n[k]})
	}
	return out
}

func summaryStats(rows []Row) {
	_ = os.MkdirAll(rpath("summary"), 0o755)
	byYear := map[[2]string]int{}
	years, types := map[string]bool{}, map[string]bool{}
	for _, r := range rows {
		byYear[[2]string{r["year"], r["resource_type"]}]++
		years[r["year"]] = true
		types[r["resource_type"]] = true
	}
	ys, ts := sortedKeys(years), sortedKeys(types)
	var out []Row
	for _, y := range ys {
		row := Row{"year": y}
		for _, t := range ts {
			row[t] = strconv.Itoa(byYear[[2]string{y, t}])
		}
		out = append(out, row)
	}
	_ = writeCSV(rpath("summary", "by_year_type.csv"), out, append([]string{"year"}, ts...))
	mods := newCounter()
	ymods := map[[2]string]int{}
	for _, r := range rows {
		for _, m := range strings.Split(r["modality"], ";") {
			if m = strings.TrimSpace(m); m != "" {
				mods.add(m, 1)
				ymods[[2]string{r["year"], m}]++
			}
		}
	}
	mlist := mods.mostCommon(0)
	out = nil
	for _, y := range ys {
		row := Row{"year": y}
		for _, m := range mlist {
			row[m] = strconv.Itoa(ymods[[2]string{y, m}])
		}
		out = append(out, row)
	}
	_ = writeCSV(rpath("summary", "by_year_modality.csv"), out, append([]string{"year"}, mlist...))
	cats, tiers, ents, ftc, rt := newCounter(), newCounter(), newCounter(), newCounter(), newCounter()
	for _, r := range rows {
		cats.add(r["category"], 1)
		tiers.add(r["tier"], 1)
		for _, c := range strings.Split(registryNote.ReplaceAllString(r["entities"], ""), ";") {
			if c = strings.TrimSpace(c); c != "" {
				ents.add(c, 1)
			}
		}
		ftc.add(orDefault(r["fulltext_status"], "not tried"), 1)
		rt.add(r["resource_type"], 1)
	}
	stats := omap{{"rows", len(rows)}, {"tiers", counterMap(tiers, tiers.keys)},
		{"categories", counterMap(cats, cats.mostCommon(0))}, {"modalities", counterMap(mods, mlist)},
		{"entity_mentions", counterMap(ents, ents.mostCommon(60))}, {"fulltext", counterMap(ftc, ftc.keys)},
		{"resource_types", counterMap(rt, rt.keys)}}
	b, _ := jsonMarshal(stats)
	var ind bytes.Buffer
	_ = json.Indent(&ind, b, "", "  ")
	_ = os.WriteFile(filepath.Join(rpath("summary"), "stats.json"), ind.Bytes(), 0o644)
	_ = fmt.Sprint
}
