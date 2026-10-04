package main

// findings.md ("summary" step): growth per year, data layers per year, origin
// papers of registry entries, first title mention of each named entity, the
// most-cited work, the most-starred repositories, websites, full-text coverage.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/dlclark/regexp2"
	"gopkg.in/yaml.v3"
)

var (
	summaryPapers = map[string]bool{"article": true, "review": true, "preprint": true, "report": true,
		"book chapter": true, "conference paper": true, "thesis": true, "dataset": true}
	registryIDs = regexp.MustCompile(`([^;]*) registry: ([^;]*)`)
	starsRe     = regexp.MustCompile(`stars=(\d+)`)
)

// summaryModalities: classify.summary_modalities, else every modality rule, in order.
func summaryModalities(classify *yaml.Node) []string {
	var out []string
	if n := nodeChild(classify, "summary_modalities"); n != nil && len(n.Content) > 0 {
		for _, x := range n.Content {
			out = append(out, x.Value)
		}
		return out
	}
	for _, kv := range orderedMapping(nodeChild(classify, "modality")) {
		out = append(out, kv.Key)
	}
	return out
}

// pyCounterRepr formats a counter as {'key': n, ...}.
func pyCounterRepr(c *counter) string {
	parts := make([]string, len(c.keys))
	for i, k := range c.keys {
		q := pyListRepr([]string{k})
		parts[i] = q[1:len(q)-1] + ": " + strconv.Itoa(c.n[k])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func mdTable(header []string, rows [][]any) []string {
	out := []string{"| " + strings.Join(header, " | ") + " |", "|" + strings.Repeat("---|", len(header))}
	for _, r := range rows {
		cells := make([]string, len(r))
		for i, x := range r {
			cells[i] = strings.ReplaceAll(strings.ReplaceAll(fmt.Sprint(x), "|", "/"), "\n", " ")
		}
		out = append(out, "| "+strings.Join(cells, " | ")+" |")
	}
	return append(out, "")
}

func cite(r Row) string {
	link := r["url"]
	if r["doi"] != "" {
		link = "[" + r["doi"] + "](https://doi.org/" + r["doi"] + ")"
	}
	first := ""
	if r["authors"] != "" {
		first = strings.SplitN(r["authors"], ",", 2)[0]
	}
	return fmt.Sprintf("%s (%s, %s) %s", runeCut(r["title"], 120), first, r["year"], link)
}

func citedBy(r Row) int {
	n, err := strconv.Atoi(strings.TrimSpace(r["cited_by"]))
	if err != nil {
		return 0
	}
	return n
}

func dateOr9(r Row) string { return orDefault(r["date"], "9999") }

func stepSummary(args []string, log *Logger) error {
	rows, _ := readCSV(rpath("progress.csv"))
	var papers, repos, sites []Row
	for _, r := range rows {
		switch rt := r["resource_type"]; {
		case summaryPapers[rt]:
			papers = append(papers, r)
		case rt == "repository" || rt == "software package":
			repos = append(repos, r)
		case rt == "website" || rt == "database" || rt == "web calculator":
			sites = append(sites, r)
		}
	}
	ys := map[string]bool{}
	for _, r := range papers {
		if r["year"] != "" {
			ys[r["year"]] = true
		}
	}
	years := sortedKeys(ys)
	classify := configSection("classify")
	modalities := summaryModalities(classify)
	clf := LoadClassifier()
	topic := orDefault(configTopic(mustRead(configFile())), defaultTopic())
	L := []string{"# Findings: " + topic, "",
		fmt.Sprintf("_Generated %s by `litsearch` (summary step) from `progress.csv` "+
			"(%d rows: %d papers/preprints/reviews, %d repositories and packages, "+
			"%d websites and databases). Classification is rule-based (classify section of the configuration); "+
			"treat counts as indicative._", today(), len(rows), len(papers), len(repos), len(sites)), ""}

	// growth
	L = append(L, "## Publications per year", "")
	by, tiers, total := map[[2]string]int{}, map[[2]string]int{}, map[string]int{}
	for _, r := range papers {
		by[[2]string{r["year"], r["resource_type"]}]++
		tiers[[2]string{r["year"], r["tier"]}]++
		total[r["year"]]++
	}
	var t1 [][]any
	for _, y := range years {
		row := []any{y}
		for _, t := range []string{"article", "review", "preprint"} {
			row = append(row, by[[2]string{y, t}])
		}
		for _, t := range []string{"landmark", "core", "related"} {
			row = append(row, tiers[[2]string{y, t}])
		}
		t1 = append(t1, append(row, total[y]))
	}
	L = append(L, mdTable([]string{"year", "article", "review", "preprint", "landmark", "core", "related", "total"}, t1)...)

	// modality per year
	if len(modalities) > 0 {
		L = append(L, "## Data layers over time (core + landmark papers)", "",
			"A paper can use several layers, so rows do not sum to the totals above.", "")
		cm := map[[2]string]int{}
		for _, r := range papers {
			if r["tier"] == "core" || r["tier"] == "landmark" {
				for _, m := range strings.Split(r["modality"], ";") {
					if m = pyStrip(m); m != "" {
						cm[[2]string{r["year"], m}]++
					}
				}
			}
		}
		var t2 [][]any
		for _, y := range years {
			row := []any{y}
			for _, m := range modalities {
				row = append(row, cm[[2]string{y, m}])
			}
			t2 = append(t2, row)
		}
		L = append(L, mdTable(append([]string{"year"}, modalities...), t2)...)
	}

	// origin papers of registry entries: the chronology of the field, which
	// title mentions alone cannot give
	var origin []Row
	for _, r := range rows {
		if registryIDs.MatchString(r["entities"]) {
			origin = append(origin, r)
		}
	}
	if len(origin) > 0 {
		L = append(L, "## Origin papers of registry entries, oldest first", "",
			"Papers that introduced an entry of a reference registry; the ids are the registry's identifiers.", "")
		sort.SliceStable(origin, func(i, j int) bool { return dateOr9(origin[i]) < dateOr9(origin[j]) })
		var t [][]any
		for _, r := range origin {
			m := registryIDs.FindStringSubmatch(r["entities"])
			t = append(t, []any{r["date"], runeCut(pyStrip(m[1])+": "+m[2], 90), cite(r)})
		}
		L = append(L, mdTable([]string{"date", "registry ids", "paper"}, t)...)
	}

	// first appearance of named entities in titles
	type firstRow struct {
		date, name string
		n          int
		cite       string
	}
	var first []firstRow
	ents := nodeChild(classify, "entities")
	if ents == nil {
		ents = nodeChild(classify, "clock_names")
	}
	for _, kv := range orderedMapping(ents) {
		pat, opt := kv.Value.Value, regexp2.RegexOptions(0)
		if strings.HasPrefix(pat, "(?i)") {
			pat, opt = pat[4:], regexp2.IgnoreCase
		}
		rx := regexp2.MustCompile(`(?<![\w-])(?:`+pat+`)(?![\w-])`, opt)
		var best Row
		for _, r := range papers {
			if reSearch(rx, r["title"]) && (best == nil || dateOr9(r) < dateOr9(best)) {
				best = r
			}
		}
		if best != nil {
			n := 0
			for _, r := range papers {
				if strings.Contains(r["entities"], kv.Key) {
					n++
				}
			}
			first = append(first, firstRow{best["date"], kv.Key, n, cite(best)})
		}
	}
	sort.SliceStable(first, func(i, j int) bool {
		a, b := first[i], first[j]
		if a.date != b.date {
			return a.date < b.date
		}
		if a.name != b.name {
			return a.name < b.name
		}
		if a.n != b.n {
			return a.n < b.n
		}
		return a.cite < b.cite
	})
	if len(first) > 0 {
		L = append(L, "## When each named entity first appears in a catalogued title", "",
			"Earliest title mention within the catalogue window, not necessarily the origin paper; "+
				"`papers` counts every catalogued paper whose title, abstract or keywords mention it.", "")
		var t3 [][]any
		for _, f := range first {
			t3 = append(t3, []any{f.date, f.name, f.n, f.cite})
		}
		L = append(L, mdTable([]string{"first title mention", "entity", "papers", "earliest paper"}, t3)...)
	}

	// most cited
	byCites := func(rs []Row) []Row {
		out := append([]Row(nil), rs...)
		sort.SliceStable(out, func(i, j int) bool { return citedBy(out[i]) > citedBy(out[j]) })
		return out
	}
	L = append(L, "## Most-cited papers", "")
	top := byCites(papers)
	var t4 [][]any
	for _, r := range top[:min(30, len(top))] {
		t4 = append(t4, []any{citedBy(r), r["resource_type"], r["category"], cite(r)})
	}
	L = append(L, mdTable([]string{"cited by", "type", "category", "paper"}, t4)...)
	if len(modalities) > 0 {
		L = append(L, fmt.Sprintf("## Most-cited %s papers per data layer", clf.originCategory), "")
	}
	for _, m := range modalities {
		var sub []Row
		for _, r := range papers {
			if strings.Contains(r["modality"], m) && r["category"] == clf.originCategory {
				sub = append(sub, r)
			}
		}
		if len(sub) == 0 {
			continue
		}
		L = append(L, fmt.Sprintf("**%s** (%d %s papers)", m, len(sub), clf.originCategory), "")
		s := byCites(sub)
		var t [][]any
		for _, r := range s[:min(5, len(s))] {
			t = append(t, []any{citedBy(r), cite(r)})
		}
		L = append(L, mdTable([]string{"cited by", "paper"}, t)...)
	}

	// repositories
	stars := func(r Row) int {
		if m := starsRe.FindStringSubmatch(r["details"]); m != nil {
			return atoiSafe(m[1])
		}
		return 0
	}
	if len(repos) > 0 {
		L = append(L, "## Most-starred repositories", "")
		s := append([]Row(nil), repos...)
		sort.SliceStable(s, func(i, j int) bool { return stars(s[i]) > stars(s[j]) })
		var t [][]any
		for _, r := range s[:min(30, len(s))] {
			t = append(t, []any{stars(r), r["date"], "[" + runeCut(r["title"], 100) + "](" + r["url"] + ")"})
		}
		L = append(L, mdTable([]string{"stars", "created", "repository"}, t)...)
	}
	if len(sites) > 0 {
		L = append(L, "## Websites, databases and calculators", "")
		s := append([]Row(nil), sites...)
		sort.SliceStable(s, func(i, j int) bool { return dateOr9(s[i]) < dateOr9(s[j]) })
		var t [][]any
		for _, r := range s {
			t = append(t, []any{orDefault(r["date"], "n/a"), r["resource_type"], "[" + runeCut(r["title"], 90) + "](" + r["url"] + ")"})
		}
		L = append(L, mdTable([]string{"first archived", "type", "site"}, t)...)
	}

	// full text
	ft := newCounter()
	ftt := map[[2]string]int{}
	nPDF, nMD, nTXT := 0, 0, 0
	for _, r := range papers {
		st := orDefault(r["fulltext_status"], "not tried")
		ft.add(st, 1)
		ftt[[2]string{r["tier"], st}]++
		if r["fulltext_pdf"] != "" {
			nPDF++
		}
		if r["fulltext_md"] != "" {
			nMD++
		}
		if r["fulltext_txt"] != "" {
			nTXT++
		}
	}
	L = append(L, "## Full-text coverage", "")
	var t5 [][]any
	for _, t := range []string{"landmark", "core", "related"} {
		row := []any{t}
		for _, s := range []string{"ok", "partial", "none", "not tried"} {
			row = append(row, ftt[[2]string{t, s}])
		}
		t5 = append(t5, row)
	}
	L = append(L, mdTable([]string{"tier", "ok (md+txt)", "partial", "none (no open copy)", "not tried"}, t5)...)
	L = append(L, fmt.Sprintf("PDFs on disk: %d; Markdown: %d; plain text: %d.", nPDF, nMD, nTXT), "")

	out := rpath("summary", "findings.md")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(out, []byte(strings.Join(L, "\n")), 0o644); err != nil {
		return err
	}
	log.Printf("wrote %s (%d lines); full text %s", out, len(L), pyCounterRepr(ft))
	return nil
}
