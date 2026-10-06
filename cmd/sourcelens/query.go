package main

// `sourcelens query` and `sourcelens export`: filter a catalogue, print or save
// the rows, or write them as references.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dlclark/regexp2"
)

const filterHelp = `filters (AND between filters, OR within a comma list)
  --text RE           regex over title, entities, venue, matched groups, details
  --title RE          regex over the title
  --doi LIST          DOIs: comma list or @file (one per line)
  --uid LIST          catalogue uids: comma list or @file
  --from WHEN         YYYY, YYYY-MM, YYYY-MM-DD, today or a span (2y)
  --to WHEN           YYYY, YYYY-MM, YYYY-MM-DD or today
  --range SPAN        span back from --to: 1d 7d 2w 1m 6m 1y 10y
  --tier LIST         landmark,core,related
  --type LIST         article,review,preprint,repository,software package,website,...
  --category LIST     e.g. review,benchmark/comparison
  --modality LIST     substrings, e.g. DNA methylation,proteomic
  --entity LIST       substrings of the entities column, e.g. GrimAge,DunedinPACE
  --species LIST      substrings, e.g. mouse
  --added-since DATE  rows whose added_on >= DATE
  --has-fulltext      only rows with a downloaded Markdown full text
  --fulltext RE       regex searched in the downloaded paper.md / paper.txt
  --summary RE        regex over the summary (abstract, start of the full text or a
                      site's description) and the keywords
  --fulltext-status LIST  ok,partial,none,deferred,error,removed; - for not tried
  --ext LIST          rows with attachments of these extensions, e.g. xlsx,csv,pptx
  --has-attachments   only rows with attachments
  --in FILE           filter this CSV (a dry-run table, a query output) instead of
                      progress.csv; looked up as given, then in the catalogue and exports/
  --sort KEY          date | cited_by | title | author (default date)
`

const queryHelp = `sourcelens query [filters] [--limit N] [--out FILE]: filter a catalogue

  sourcelens query --range 1m                                  what was published last month
  sourcelens query --topic "CRISPR base editing" --type review
  sourcelens query --text GrimAge --modality proteomic --from 2022 --tier core
  sourcelens query --fulltext DunedinPACE --type article,preprint
  sourcelens query --doi 10.18632/aging.101414,10.7554/elife.73420
  sourcelens query --title "epigenetic clock" --range 6m --out recent.csv
  sourcelens query --in reports/dryrun_<stamp>.csv --summary "single.cell" --ext xlsx --out picked.csv

` + projectHelp + "\n" + filterHelp + `  --limit N           rows printed (default 50; --out gets all)
  --out FILE          CSV of all matching rows; relative paths go to <catalogue>/exports/
`

const exportHelp = `sourcelens export [filters] --format CODES [--out PATH]: write references

  sourcelens export --from 2024 --type article,review --format APA
  sourcelens export --topic "CRISPR base editing" --range 6m --format BIB --out crispr.bib
  sourcelens export --doi 10.18632/aging.101414,10.7554/elife.73420 --format APA,BIB,RIS
  sourcelens export --title "GrimAge|DunedinPACE" --range 2y --format VAN --out grim
  sourcelens export --doi @dois.txt --format CSL --out refs.json

formats (three-letter codes, comma list)
  APA AMA MLA CHI HAR VAN IEE NAT   text styles, one reference per line
  BIB RIS ENW CSL                   BibTeX, RIS, EndNote tagged, CSL-JSON

` + projectHelp + "\n" + filterHelp + `  --format CODES      default APA
  --limit N           at most N references (default all)
  --out PATH          a file (one format) or a prefix (one file per format);
                      relative paths go to <catalogue>/exports/
`

var (
	filterSpec = []flagSpec{{"text", "", kStr}, {"title", "", kStr}, {"doi", "", kStr}, {"uid", "", kStr},
		{"from", "", kStr}, {"to", "", kStr}, {"range", "", kStr}, {"tier", "", kStr}, {"type", "", kStr},
		{"category", "", kStr}, {"modality", "", kStr}, {"entity", "", kStr}, {"species", "", kStr},
		{"added-since", "", kStr}, {"has-fulltext", "false", kBool}, {"fulltext", "", kStr},
		{"summary", "", kStr}, {"fulltext-status", "", kStr}, {"ext", "", kStr}, {"has-attachments", "false", kBool},
		{"in", "", kStr}, {"sort", "date", kStr}, {"topic", "", kStr}, {"dir", "", kStr}}
	sortKeys = []string{"date", "cited_by", "title", "author"}
	showCols = []string{"date", "resource_type", "tier", "category", "cited_by", "title", "doi", "url"}
)

func listArg(spec string) ([]string, error) {
	if spec == "" {
		return nil, nil
	}
	var out []string
	if strings.HasPrefix(spec, "@") {
		b, err := os.ReadFile(spec[1:])
		if err != nil {
			return nil, fmt.Errorf("cannot read %s", spec[1:])
		}
		for _, l := range splitLinesPy(string(b)) {
			if l = pyStrip(l); l != "" {
				out = append(out, l)
			}
		}
		return out, nil
	}
	for _, x := range strings.Split(spec, ",") {
		if x = pyStrip(x); x != "" {
			out = append(out, x)
		}
	}
	return out, nil
}

// filterWindow: (--from or --range back from --to/today, --to); "" when not given.
func filterWindow(o flagSet) (string, string, error) {
	now := time.Now()
	todayD, _ := time.Parse("2006-01-02", now.Format("2006-01-02"))
	hi, lo := "", ""
	var err error
	if o["to"] != "" {
		if hi, err = parseWhen(o["to"], true, now); err != nil {
			return "", "", err
		}
	}
	if o["range"] != "" {
		ref := todayD
		if hi != "" {
			ref, _ = time.Parse("2006-01-02", hi)
		}
		s, err := parseWhen(o["range"], false, now)
		if err != nil {
			return "", "", err
		}
		d, _ := time.Parse("2006-01-02", s)
		lo = d.Add(ref.Sub(todayD)).Format("2006-01-02")
	}
	if o["from"] != "" {
		if lo, err = parseWhen(o["from"], false, now); err != nil {
			return "", "", err
		}
	}
	return lo, hi, nil
}

// outPath: absolute paths as given, relative ones under <catalogue>/exports/.
func outPath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(Research, "exports", p)
}

// inputPath: --in, as given, else in the catalogue, else in its exports/.
func inputPath(name string) (string, error) {
	for _, p := range []string{name, filepath.Join(Research, name), filepath.Join(Research, "exports", name)} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("cannot find %s", name)
}

// inputRows: rows and columns of --in, or of progress.csv.
func inputRows(o flagSet) ([]Row, []string, error) {
	path := rpath("progress.csv")
	if o["in"] != "" {
		p, err := inputPath(o["in"])
		if err != nil {
			return nil, nil, err
		}
		path = p
	}
	rows, cols := readCSV(path)
	return rows, cols, nil
}

// attachmentCount: files available in an attachments column value ("2/5: ..." -> 5).
func attachmentCount(v string) int {
	head, _, _ := strings.Cut(v, ":")
	if _, n, ok := strings.Cut(head, "/"); ok {
		return atoiSafe(n)
	}
	return 0
}

func firstAuthorKey(r Row) string {
	a := pyStrip(strings.SplitN(r["authors"], ",", 2)[0])
	if a == "" {
		return strings.ToLower(r["title"])
	}
	return strings.ToLower(strings.Split(a, " ")[0])
}

func compileFilter(flag, p string) (*regexp2.Regexp, error) {
	if p == "" {
		return nil, nil
	}
	re, err := regexp2.Compile(p, regexp2.IgnoreCase)
	if err != nil {
		return nil, fmt.Errorf("invalid regular expression for --%s", flag)
	}
	return re, nil
}

// selectRows: rows of progress.csv (or rows) that pass every filter, sorted. hits holds
// the --fulltext context per uid.
func selectRows(o flagSet, rows []Row) ([]Row, map[string]string, error) {
	if !contains(sortKeys, o["sort"]) {
		return nil, nil, fmt.Errorf("--sort must be date, cited_by, title or author")
	}
	if rows == nil {
		var err error
		if rows, _, err = inputRows(o); err != nil {
			return nil, nil, err
		}
	}
	srx, err := compileFilter("summary", o["summary"])
	if err != nil {
		return nil, nil, err
	}
	if srx != nil && len(rows) > 0 {
		if _, ok := rows[0]["summary"]; !ok {
			rows = overviewRows(rows)
		}
	}
	exts := map[string]bool{}
	for _, e := range strings.Split(o["ext"], ",") {
		if e = pyStrip(e); e != "" {
			exts[strings.TrimLeft(strings.ToLower(e), ".")] = true
		}
	}
	anyIn := func(value, wanted string) bool {
		if wanted == "" {
			return true
		}
		for _, w := range strings.Split(wanted, ",") {
			if strings.Contains(strings.ToLower(value), strings.ToLower(pyStrip(w))) {
				return true
			}
		}
		return false
	}
	exactIn := func(value, wanted string) bool {
		if wanted == "" {
			return true
		}
		for _, w := range strings.Split(wanted, ",") {
			if strings.ToLower(value) == strings.ToLower(pyStrip(w)) {
				return true
			}
		}
		return false
	}
	rx, err := compileFilter("text", o["text"])
	if err != nil {
		return nil, nil, err
	}
	trx, err := compileFilter("title", o["title"])
	if err != nil {
		return nil, nil, err
	}
	frx, err := compileFilter("fulltext", o["fulltext"])
	if err != nil {
		return nil, nil, err
	}
	dl, err := listArg(o["doi"])
	if err != nil {
		return nil, nil, err
	}
	dois := map[string]bool{}
	for _, d := range dl {
		if n := normDOI(d); n != "" {
			dois[n] = true
		}
	}
	ul, err := listArg(o["uid"])
	if err != nil {
		return nil, nil, err
	}
	uids := map[string]bool{}
	for _, u := range ul {
		uids[u] = true
	}
	lo, hi, err := filterWindow(o)
	if err != nil {
		return nil, nil, err
	}
	lo, hi = orDefault(lo, "0000"), orDefault(hi, "9999")
	hits := map[string]string{}
	var out []Row
	for _, r := range rows {
		d := orDefault(r["date"], "0000")
		if !(lo <= d && d <= hi) {
			continue
		}
		if len(dois) > 0 && !dois[strings.ToLower(r["doi"])] {
			continue
		}
		if len(uids) > 0 && !uids[r["uid"]] {
			continue
		}
		if !(exactIn(r["tier"], o["tier"]) && exactIn(r["resource_type"], o["type"]) &&
			exactIn(r["category"], o["category"]) && anyIn(r["modality"], o["modality"]) &&
			anyIn(orDefault(r["entities"], r["clocks"]), o["entity"]) && anyIn(r["species"], o["species"])) {
			continue
		}
		if o["added-since"] != "" && r["added_on"] < o["added-since"] {
			continue
		}
		if o.bool("has-fulltext") && r["fulltext_md"] == "" {
			continue
		}
		if o["fulltext-status"] != "" && !exactIn(orDefault(r["fulltext_status"], "-"), o["fulltext-status"]) {
			continue
		}
		if o.bool("has-attachments") && attachmentCount(r["attachments"]) == 0 {
			continue
		}
		if len(exts) > 0 {
			hit := false
			for e := range summaryExts(r["attachments"]) {
				if exts[e] {
					hit = true
				}
			}
			if !hit {
				continue
			}
		}
		if srx != nil && !reSearch(srx, r["summary"]+" "+r["keywords"]) {
			continue
		}
		if trx != nil && !reSearch(trx, r["title"]) {
			continue
		}
		if rx != nil && !reSearch(rx, strings.Join([]string{r["title"], orDefault(r["entities"], r["clocks"]), r["venue"], r["matched_groups"], r["details"]}, " ")) {
			continue
		}
		if frx != nil {
			p := orDefault(r["fulltext_md"], r["fulltext_txt"])
			if p == "" {
				continue
			}
			b, err := os.ReadFile(researchFile(p))
			if err != nil {
				continue
			}
			text := strings.ToValidUTF8(string(b), "")
			m, _ := frx.FindStringMatch(text)
			if m == nil {
				continue
			}
			rs := []rune(text)
			s, e := max(0, m.Index-80), min(len(rs), m.Index+m.Length+80)
			hits[r["uid"]] = strings.Join(pyFields(string(rs[s:e])), " ")
		}
		out = append(out, r)
	}
	switch o["sort"] {
	case "cited_by":
		sort.SliceStable(out, func(i, j int) bool { return atoiSafe(out[i]["cited_by"]) > atoiSafe(out[j]["cited_by"]) })
	case "author":
		sort.SliceStable(out, func(i, j int) bool { return firstAuthorKey(out[i]) < firstAuthorKey(out[j]) })
	default:
		k := o["sort"]
		sort.SliceStable(out, func(i, j int) bool { return out[i][k] < out[j][k] })
	}
	return out, hits, nil
}

func withSpec(extra ...flagSpec) []flagSpec {
	return append(append([]flagSpec(nil), filterSpec...), extra...)
}

func cmdQuery(argv []string) int {
	o, err := parseFlags(argv, withSpec(flagSpec{"limit", "50", kInt}, flagSpec{"out", "", kStr}), queryHelp)
	if _, ok := err.(errHelp); ok {
		return 0
	}
	if err != nil {
		return fail(err.Error())
	}
	if _, err := resolveProject(o["topic"], o["dir"], false, ""); err != nil {
		return fail(err.Error())
	}
	rows, cols, err := inputRows(o)
	if err != nil {
		return fail(err.Error())
	}
	out, hits, err := selectRows(o, rows)
	if err != nil {
		return fail(err.Error())
	}
	fmt.Printf("%d matching rows\n", len(out))
	limit := o.int("limit")
	shown := out
	if limit >= 0 && limit < len(out) {
		shown = out[:limit]
	} else if limit < 0 {
		shown = out[:max(0, len(out)+limit)]
	}
	for _, r := range shown {
		cells := make([]string, len(showCols))
		for i, k := range showCols {
			n := 40
			if k == "title" {
				n = 110
			}
			cells[i] = runeCut(r[k], n)
		}
		fmt.Println(strings.Join(cells, " | "))
		if h := hits[r["uid"]]; h != "" {
			fmt.Printf("      … %s …\n", h)
		}
	}
	if o["out"] != "" {
		path := outPath(o["out"])
		if err := writeCSV(path, out, cols); err != nil {
			return fail(err.Error())
		}
		fmt.Printf("wrote %s\n", path)
	}
	return 0
}

// pySuffix is pathlib's PurePath.suffix.
func pySuffix(p string) string {
	name := filepath.Base(p)
	i := strings.LastIndex(name, ".")
	if 0 < i && i < len(name)-1 {
		return name[i:]
	}
	return ""
}

func cmdExport(argv []string) int {
	o, err := parseFlags(argv, withSpec(flagSpec{"format", "APA", kStr}, flagSpec{"limit", "0", kInt},
		flagSpec{"out", "", kStr}), exportHelp)
	if _, ok := err.(errHelp); ok {
		return 0
	}
	if err != nil {
		return fail(err.Error())
	}
	codes, err := parseCodes(o["format"])
	if err != nil {
		return fail(err.Error())
	}
	if _, err := resolveProject(o["topic"], o["dir"], false, ""); err != nil {
		return fail(err.Error())
	}
	rows, _, err := selectRows(o, nil)
	if err != nil {
		return fail(err.Error())
	}
	if n := o.int("limit"); n > 0 && n < len(rows) {
		rows = rows[:n]
	} else if n < 0 {
		rows = rows[:max(0, len(rows)+n)]
	}
	refRows, _ := readCSV(rpath("corpus", "references.csv"))
	full := map[string]Row{}
	for _, r := range refRows {
		full[r["uid"]] = r
	}
	items := make([]Row, 0, len(rows))
	for _, r := range rows {
		if f, ok := full[r["uid"]]; ok {
			items = append(items, f)
			continue
		}
		it := Row{}
		for _, k := range refColumns {
			it[k] = r[k]
		}
		items = append(items, it)
	}
	fmt.Fprintf(os.Stderr, "%d references\n", len(items))
	if o["out"] == "" {
		for _, c := range codes {
			if len(codes) > 1 {
				fmt.Printf("==> %s <==\n", c)
			}
			fmt.Print(renderRefs(c, items))
		}
		return 0
	}
	out := outPath(o["out"])
	for _, c := range codes {
		path := out
		if !(len(codes) == 1 && pySuffix(out) != "") {
			path = out + refExt[c]
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fail(err.Error())
		}
		if err := os.WriteFile(path, []byte(renderRefs(c, items)), 0o644); err != nil {
			return fail(err.Error())
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", path)
	}
	return 0
}
