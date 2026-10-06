package main

// Unit tests. tests/test_core.py checks the same cases against the Python
// implementation, so both must keep returning these values.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseTopic(t *testing.T) {
	cases := map[string][]string{
		"CRISPR base editing":               {"CRISPR & base & editing"},
		`"base editing", prime editing`:     {"base editing", "prime & editing"},
		"graph neural networks OR GNN":      {"graph & neural & networks", "GNN"},
		"  single  ":                        {"single"},
		`a; b, "c d" e`:                     {"a", "b", "c d & e"},
		"x & y":                             {"x & y"},
		"":                                  nil,
		"dup, dup":                          {"dup"},
		`"quoted, with comma" OR other one`: {"quoted, with comma", "other & one"},
	}
	for in, want := range cases {
		if got := parseTopic(in); !reflect.DeepEqual(got, want) {
			t.Errorf("parseTopic(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestQueries(t *testing.T) {
	if got := pubmedTerm("CRISPR & base editing"); got != `("CRISPR"[tiab] AND "base editing"[tiab])` {
		t.Errorf("pubmedTerm: %s", got)
	}
	if got := epmcTerm("epigenetic clock"); got != `TITLE_ABS:"epigenetic clock"` {
		t.Errorf("epmcTerm: %s", got)
	}
	if got := arxivTerm("a & b c"); got != `((ti:"a" OR abs:"a") AND (ti:"b c" OR abs:"b c"))` {
		t.Errorf("arxivTerm: %s", got)
	}
	if got := openalexTerm("CRISPR & base editing"); got != `(CRISPR AND "base editing")` {
		t.Errorf("openalexTerm: %s", got)
	}
	if got := githubQuery("graph & neural networks"); got != `"graph" "neural networks"` {
		t.Errorf("githubQuery: %s", got)
	}
}

func TestTermRegex(t *testing.T) {
	re := pyRe(termRegex([]string{"graph & neural & networks", "GNN"}))
	for _, s := range []string{"A graph neural network for X", "Neural networks on a graph", "GNNs explained"} {
		if !reSearch(re, s) {
			t.Errorf("should match: %q", s)
		}
	}
	for _, s := range []string{"neural networks only", "graphene"} {
		if reSearch(re, s) {
			t.Errorf("should not match: %q", s)
		}
	}
	if got := termRegex(nil); got != `(?!)` {
		t.Errorf("empty: %s", got)
	}
	w := wordMatcher([]string{"CRISPR & base editing"})
	if !w("CRISPR-Cas9 base-editing tools") || !w("crispr and base editors... base editing") || w("CRISPR database editing") {
		t.Error("wordMatcher")
	}
	m := termMatcher([]string{"base editing"})
	if !m("Advances in base editing.") || m("base-editing") || m("database editing") {
		t.Error("termMatcher boundaries")
	}
}

func TestTopicSlug(t *testing.T) {
	for in, want := range map[string]string{"CRISPR base editing": "crispr-base-editing",
		`"base editing", prime`: "base-editing-prime", "  ": ""} {
		if got := topicSlug(in); got != want {
			t.Errorf("topicSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseWhen(t *testing.T) {
	ref := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		end  bool
		want string
	}{{"2024", false, "2024-01-01"}, {"2024", true, "2024-12-31"}, {"2024-02", true, "2024-02-29"},
		{"1m", false, "2026-02-28"}, {"1y", false, "2025-03-31"}, {"2w", false, "2026-03-17"},
		{"today", false, "2026-03-31"}, {"2024-03-15", false, "2024-03-15"}}
	for _, c := range cases {
		if got, err := parseWhen(c.in, c.end, ref); err != nil || got != c.want {
			t.Errorf("parseWhen(%q, %v) = %q, %v; want %q", c.in, c.end, got, err, c.want)
		}
	}
	for _, bad := range []string{"2024-13", "5x", "20240315"} {
		if _, err := parseWhen(bad, false, ref); err == nil {
			t.Errorf("parseWhen(%q) should fail", bad)
		}
	}
}

func TestNormDOI(t *testing.T) {
	for in, want := range map[string]string{
		"https://doi.org/10.1186/GB-2013-14-10-R115": "10.1186/gb-2013-14-10-r115",
		"doi: 10.1101/2020.01.01.123456v2":           "10.1101/2020.01.01.123456",
		"not a doi":                                  "",
	} {
		if got := normDOI(in); got != want {
			t.Errorf("normDOI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFlags(t *testing.T) {
	spec := []flagSpec{{"limit", "50", kInt}, {"out", "", kStr}, {"dry-run", "false", kBool}}
	f, err := parseFlags([]string{"--limit=5", "--out", "x.csv", "--dry-run"}, spec, "")
	if err != nil || f.int("limit") != 5 || f["out"] != "x.csv" || !f.bool("dry-run") {
		t.Errorf("parseFlags: %v %v", f, err)
	}
	for _, args := range [][]string{{"--limit", "abc"}, {"--bogus"}, {"stray"}, {"--out"}} {
		if _, err := parseFlags(args, spec, ""); err == nil {
			t.Errorf("parseFlags(%q) should fail", args)
		}
	}
}

func TestCSVQuoting(t *testing.T) {
	var b bytes.Buffer
	w := bufio.NewWriter(&b)
	csvLine(w, []string{"plain", "a,b", `say "hi"`, " lead", "multi\nline"})
	csvLine(w, []string{""})
	w.Flush()
	want := "plain,\"a,b\",\"say \"\"hi\"\"\", lead,\"multi\nline\"\r\n\"\"\r\n"
	if b.String() != want {
		t.Errorf("csv:\n%q\nwant\n%q", b.String(), want)
	}
}

func TestReferences(t *testing.T) {
	r := Row{"uid": "doi:10.1/x", "resource_type": "article", "date": "2022-01-12", "year": "2022",
		"authors": "Belsky DW, Caspi A, Corcoran DL", "title": "DunedinPACE, a DNA methylation biomarker.",
		"venue": "eLife", "volume": "11", "doi": "10.1/x"}
	want := map[string]string{
		"APA": "Belsky, D. W., Caspi, A., & Corcoran, D. L. (2022). DunedinPACE, a DNA methylation biomarker. eLife, 11. https://doi.org/10.1/x\n",
		"VAN": "1. Belsky DW, Caspi A, Corcoran DL. DunedinPACE, a DNA methylation biomarker. eLife. 2022;11. doi:10.1/x\n",
	}
	for code, w := range want {
		if got := renderRefs(code, []Row{r}); got != w {
			t.Errorf("%s:\n%q\nwant\n%q", code, got, w)
		}
	}
	if got := renderRefs("BIB", []Row{r}); !strings.HasPrefix(got, "@article{belsky2022dunedinpace,\n") {
		t.Errorf("BIB key: %q", got)
	}
	if _, err := parseCodes("APA,bibtex,XYZ"); err == nil {
		t.Error("parseCodes should reject XYZ")
	}
}

func TestTopicConfig(t *testing.T) {
	cfg := string(topicConfig("graph neural networks, GNN", "2025-01-01"))
	for _, s := range []string{`topic: "graph neural networks, GNN"`, `start_date: "2025-01-01"`,
		`- "graph & neural & networks"`, `- "GNN"`, "sources: [pubmed, europepmc, arxiv, openalex]"} {
		if !strings.Contains(cfg, s) {
			t.Errorf("topic config misses %q", s)
		}
	}
	if configTopic([]byte(cfg)) != "graph neural networks, GNN" {
		t.Error("configTopic")
	}
	if defaultTopic() == "" {
		t.Error("the embedded default configuration needs a topic")
	}
}

func TestResolveProject(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SOURCELENS_SETTINGS", filepath.Join(dir, "settings.yaml"))
	t.Setenv("SOURCELENS_OUTPUT", filepath.Join(dir, "out"))
	p, err := resolveProject("CRISPR base editing", "", true, "2025-06-01")
	if err != nil || !p.created || p.dir != filepath.Join(dir, "out", "crispr-base-editing") {
		t.Fatalf("resolveProject: %+v %v", p, err)
	}
	if _, err := os.Stat(p.config); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveProject("something else", p.dir, false, ""); err == nil {
		t.Error("a folder holding another topic must be refused")
	}
	if _, err := resolveProject("never created", "", false, ""); err == nil {
		t.Error("a missing catalogue must be reported when not creating")
	}
}

func TestProgressLine(t *testing.T) {
	dir := t.TempDir()
	ft := filepath.Join(dir, "fulltext.log")
	_ = os.WriteFile(ft, []byte("[10:00:00] start\n[10:01:00]   300/1200 ok 280, deferred 3\n"), 0o644)
	oa := filepath.Join(dir, "openalex.log")
	_ = os.WriteFile(oa, []byte("[10:01:00] GET 429 on https://doi.org/10.1101/2020.01.01 (try 1); waiting 4s\n"), 0o644)
	now := time.Now()
	// 2 finished steps plus a quarter of the running one: 2.25 of 4; a DOI is not a count
	got := progressLine(2, 4, now, []activeStep{{"fulltext", ft, now}, {"openalex", oa, now}}, 200)
	want := "[" + strings.Repeat("█", 14) + strings.Repeat("░", 10) + "] 2/4 steps  0:00  fulltext 300/1200, openalex 0:00"
	if got != want {
		t.Errorf("progressLine:\n got %q\nwant %q", got, want)
	}
	if got := progressLine(0, 3, now, nil, 30); got != "["+strings.Repeat("░", 24)+"] 0/" { // cut to the width
		t.Errorf("progressLine cut: %q", got)
	}
}

func TestLongRetryAfterStopsRequestsToThatHost(t *testing.T) {
	var mu sync.Mutex
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.URL.RequestURI())
		mu.Unlock()
		w.Header().Set("Retry-After", "27764")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	reset := func() { rateMu.Lock(); rateLimited = map[string]time.Time{}; rateMu.Unlock() }
	reset()
	defer reset()
	c := NewClient(0, nil, nil)
	t0 := time.Now()
	if r := c.Get(srv.URL+"/works", reqOpts{}); r != nil {
		t.Fatalf("first request: got status %d, want nil", r.Status)
	}
	if r := c.Get(srv.URL+"/works?page=2", reqOpts{}); r != nil { // no second request
		t.Fatalf("second request: got status %d, want nil", r.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	if time.Since(t0) > 5*time.Second || len(hits) != 1 || hits[0] != "/works" {
		t.Errorf("hits %v after %v", hits, time.Since(t0))
	}
	notes := rateLimitNotes()
	if len(notes) != 1 || !strings.HasPrefix(notes[0], "rate limited by "+host+" until ") || !strings.HasSuffix(notes[0], "; run again then") {
		t.Errorf("notes %q", notes)
	}
}

const testJATS = `<?xml version="1.0"?>
<article xmlns:xlink="http://www.w3.org/1999/xlink"><body>
<fig id="f1"><label>Figure 1.</label><caption><title>Clock <italic>accuracy</italic></title>
<p>Error by age.</p></caption><graphic xlink:href="pone.0123456.g001"/></fig>
<table-wrap id="t1"><label>Table 2</label><caption><p>Cohorts</p></caption><graphic xlink:href="tab2.gif"/></table-wrap>
<supplementary-material id="s1"><label>Supplement 1.</label><caption><p>eTable 1. Results</p></caption>
<media xlink:href="jamanetwopen-s001.xlsx"/></supplementary-material>
</body></article>`

func TestAttachmentCaptions(t *testing.T) {
	caps := attCaptions([]byte(testJATS))
	for name, want := range map[string]attDesc{
		"pone.0123456.g001.jpg":  {"figure", "Figure 1.", "Clock accuracy Error by age."},
		"tab2.gif":               {"table", "Table 2", "Cohorts"},
		"jamanetwopen-s001.xlsx": {"supplementary", "Supplement 1.", "eTable 1. Results"},
		"other.png":              {"figure", "", ""},
		"data.csv":               {"supplementary", "", ""},
	} {
		if got := describe(name, caps); got != want {
			t.Errorf("describe(%s) = %+v, want %+v", name, got, want)
		}
	}
	if safeName("a b/../c?.xlsx") != "c_.xlsx" || safeName("...") != "file" {
		t.Errorf("safeName: %q %q", safeName("a b/../c?.xlsx"), safeName("..."))
	}
	rows := []Row{{"file": "a.jpg", "ext": "jpg", "status": "ok"}, {"file": "b.xlsx", "ext": "xlsx", "status": "listed"},
		{"file": "c.jpg", "ext": "jpg", "status": "failed"}}
	if got := attSummary(rows); got != "1/3: jpg 2, xlsx 1" {
		t.Errorf("attSummary = %q", got)
	}
	if attSummary([]Row{{"file": "", "status": "none"}}) != "0/0" || attSummary(nil) != "" {
		t.Error("attSummary of none / nothing")
	}
	if got := summaryExts("1/3: jpg 2, xlsx 1"); !reflect.DeepEqual(got, map[string]bool{"jpg": true, "xlsx": true}) {
		t.Errorf("summaryExts = %v", got)
	}
	if humanBytes("512") != "512 B" || humanBytes("2048") != "2.0 KB" || humanBytes("x") != "" {
		t.Error("humanBytes")
	}
}

func testCatalogue(t *testing.T) project {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SOURCELENS_SETTINGS", filepath.Join(dir, "settings.yaml"))
	p, err := resolveProject("test topic", filepath.Join(dir, "cat"), true, "2025-01-01")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNoFolderWithoutFiles(t *testing.T) {
	p := testCatalogue(t)
	f := &fetcher{lim: NewLimiter(0, nil), blocked: map[string]time.Time{}, log: &Logger{w: io.Discard}}
	rec := Rec{"uid": "url:example.org/paper", "pub_date": "2025-03-01"}
	row := Row{"date": "2025-03-01", "title": "A paper"}
	folder := folderFor(rec, row)
	_ = os.MkdirAll(folder, 0o755)
	_ = os.WriteFile(filepath.Join(folder, "metadata.json"), []byte("{}"), 0o644) // left by an earlier version
	idx, atts, done := f.fetchOne(rec, row, true, []string{"pdf", "md", "txt", "xml"}, []string{"pmc", "unpaywall", "openalex"}, nil)
	if idx["status"] != "none" || idx["reason"] != "no PMC copy" || idx["folder"] != "" || atts != nil || done || fileExists(folder) {
		t.Errorf("fetchOne without files: %v %v %v, folder kept %v", idx, atts, done, fileExists(folder))
	}
	f2 := filepath.Join(p.dir, "fulltext", "2024", "u")
	_ = os.MkdirAll(f2, 0o755)
	_ = os.WriteFile(filepath.Join(f2, "metadata.json"), []byte(`{"fulltext": {"sources_tried": ["pmc_s3", "unpaywall"]}}`), 0o644)
	old := Row{"uid": "u", "status": "none", "reason": "", "folder": "fulltext/2024/u"}
	if n := tidyIndex(map[string]Row{"u": old}, []string{"u"}); n != 1 || fileExists(f2) {
		t.Errorf("tidyIndex removed %d, folder kept %v", n, fileExists(f2))
	}
	if old["folder"] != "" || old["reason"] != "no open-access copy found (tried: pmc_s3, unpaywall)" {
		t.Errorf("tidied row %v", old)
	}
	keep := filepath.Join(p.dir, "fulltext", "2024", "k")
	_ = os.MkdirAll(keep, 0o755)
	_ = os.WriteFile(filepath.Join(keep, "paper.pdf"), []byte("%PDF"), 0o644)
	if n := tidyIndex(map[string]Row{"k": {"uid": "k", "status": "none", "folder": "fulltext/2024/k"}}, []string{"k"}); n != 0 || !fileExists(keep) {
		t.Error("tidyIndex removed a folder with a file")
	}
}

func TestReadUIDsFile(t *testing.T) {
	dir := t.TempDir()
	w := func(name, text string) string {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte(text), 0o644)
		return p
	}
	a := w("a.csv", "date,uid,title\n2025,doi:10.1/x,\"A, b\"\n2025,pmid:1,c\n")
	b := w("b.csv", "doi\nhttps://doi.org/10.1234/Y\nnot-a-doi\n")
	c := w("c.txt", "uid\ndoi:10.1/z\n\npmid:2\n")
	for path, want := range map[string][]string{a: {"doi:10.1/x", "pmid:1"}, b: {"doi:10.1234/y"}, c: {"doi:10.1/z", "pmid:2"}} {
		if got := readUIDsFile(path); !reflect.DeepEqual(got, want) {
			t.Errorf("readUIDsFile(%s) = %v, want %v", filepath.Base(path), got, want)
		}
	}
}

func filterDefaults() flagSet {
	o := flagSet{}
	for _, s := range filterSpec {
		o[s.name] = s.def
	}
	return o
}

func TestQueryNewFilters(t *testing.T) {
	dry := []Row{
		{"date": "2025-01-02", "uid": "doi:10.1/a", "title": "Clock one", "resource_type": "article", "tier": "core",
			"fulltext_status": "ok", "attachments": "2/3: jpg 2, xlsx 1", "summary": "DNA methylation clock", "keywords": "aging"},
		{"date": "2025-02-03", "uid": "doi:10.1/b", "title": "Frailty two", "resource_type": "review", "tier": "related",
			"fulltext_status": "", "attachments": "0/0", "summary": "frailty index", "keywords": "methylation"},
		{"date": "2025-03-04", "uid": "doi:10.1/c", "title": "Proteome", "resource_type": "article", "tier": "core",
			"fulltext_status": "none", "attachments": "", "summary": "proteomic clock", "keywords": ""},
	}
	pick := func(k, v string) []string {
		o := filterDefaults()
		o[k] = v
		rows := make([]Row, len(dry))
		for i, r := range dry {
			rows[i] = Row{}
			for kk, vv := range r {
				rows[i][kk] = vv
			}
		}
		out, _, err := selectRows(o, rows)
		if err != nil {
			t.Fatal(err)
		}
		var uids []string
		for _, r := range out {
			uids = append(uids, r["uid"])
		}
		return uids
	}
	for _, c := range []struct {
		k, v string
		want []string
	}{
		{"summary", "methylation", []string{"doi:10.1/a", "doi:10.1/b"}},
		{"summary", "^(dna|proteomic)", []string{"doi:10.1/a", "doi:10.1/c"}},
		{"ext", "XLSX", []string{"doi:10.1/a"}},
		{"has-attachments", "true", []string{"doi:10.1/a"}},
		{"fulltext-status", "-,none", []string{"doi:10.1/b", "doi:10.1/c"}},
	} {
		if got := pick(c.k, c.v); !reflect.DeepEqual(got, c.want) {
			t.Errorf("--%s %s: %v, want %v", c.k, c.v, got, c.want)
		}
	}
	if attachmentCount("2/3: jpg 2") != 3 || attachmentCount("") != 0 {
		t.Error("attachmentCount")
	}
}

// stdoutOf runs fn and returns what it printed.
func stdoutOf(t *testing.T, fn func() int) (int, string) {
	t.Helper()
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	rc := fn()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	return rc, string(b)
}

func TestFilesMoveAndRecord(t *testing.T) {
	p := testCatalogue(t)
	folder := filepath.Join(p.dir, "fulltext", "2025", "10.1_a")
	_ = os.MkdirAll(filepath.Join(folder, "attachments"), 0o755)
	for name, data := range map[string]string{"paper.pdf": "%PDF-1", "paper.md": "# A", "attachments/s1.xlsx": "xlsx", "attachments/f1.jpg": "jpg"} {
		_ = os.WriteFile(filepath.Join(folder, filepath.FromSlash(name)), []byte(data), 0o644)
	}
	_ = os.WriteFile(filepath.Join(folder, "metadata.json"), []byte(`{"fulltext": {"files": {"paper.pdf": {}, "paper.md": {}}}}`), 0o644)
	_ = writeCSV(filepath.Join(p.dir, "progress.csv"), []Row{{"uid": "doi:10.1/a", "date": "2025-01-02", "title": "Clock one",
		"resource_type": "article", "tier": "core", "fulltext_status": "ok", "fulltext_pdf": "fulltext/2025/10.1_a/paper.pdf",
		"fulltext_md": "fulltext/2025/10.1_a/paper.md", "attachments": "2/2: jpg 1, xlsx 1"}}, progressCols)
	_ = writeCSV(filepath.Join(p.dir, "fulltext", "fulltext_index.csv"), []Row{{"uid": "doi:10.1/a", "status": "ok", "has_pdf": "True",
		"has_md": "True", "attachments": "2/2", "folder": "fulltext/2025/10.1_a"}}, indexCols)
	_ = writeAttIndex(map[string][]Row{"doi:10.1/a": {
		{"uid": "doi:10.1/a", "file": "f1.jpg", "ext": "jpg", "kind": "figure", "label": "Figure 1", "bytes": "3",
			"status": "ok", "path": "fulltext/2025/10.1_a/attachments/f1.jpg"},
		{"uid": "doi:10.1/a", "file": "s1.xlsx", "ext": "xlsx", "kind": "supplementary", "caption": "eTable 1",
			"bytes": "4", "status": "ok", "path": "fulltext/2025/10.1_a/attachments/s1.xlsx"}}})
	d := []string{"--dir", p.dir}
	run := func(args ...string) (int, string) {
		return stdoutOf(t, func() int { return cmdFiles(append(append([]string(nil), d...), args...)) })
	}
	if rc, out := run("--ext", "xlsx,pdf"); rc != 0 || strings.Split(out, "\n")[0] != "2 files (10 B) of 1 matching papers" {
		t.Errorf("list: %d %q", rc, out)
	}
	out := filepath.Join(t.TempDir(), "out")
	if rc, _ := run("--name", "etable", "--move-to", out); rc != 0 {
		t.Fatalf("move: %d", rc)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "10.1_a", "attachments", "s1.xlsx")); string(b) != "xlsx" || fileExists(filepath.Join(folder, "attachments", "s1.xlsx")) {
		t.Error("s1.xlsx not moved")
	}
	rows := readAttIndex()["doi:10.1/a"]
	if rows[0]["status"] != "ok" || rows[1]["status"] != "moved" || !strings.HasSuffix(rows[1]["path"], "/attachments/s1.xlsx") {
		t.Errorf("attachment rows after move: %v", rows)
	}
	if rc, _ := run("--kind", "paper", "--delete"); rc != 1 {
		t.Error("--delete without --yes must not delete")
	}
	if rc, _ := run("--kind", "paper", "--delete", "--yes"); rc != 0 {
		t.Fatal("delete failed")
	}
	idx, _ := readCSV(filepath.Join(p.dir, "fulltext", "fulltext_index.csv"))
	if idx[0]["status"] != "removed" || idx[0]["reason"] != "deleted by sourcelens files" || idx[0]["attachments"] != "1/2" {
		t.Errorf("index after delete: %v", idx[0])
	}
	prog, _ := readCSV(filepath.Join(p.dir, "progress.csv"))
	if prog[0]["fulltext_status"] != "removed" || prog[0]["fulltext_pdf"] != "" || prog[0]["attachments"] != "1/2: jpg 1, xlsx 1" {
		t.Errorf("progress after delete: %v", prog[0])
	}
	_, all := run("--status", "all")
	lines := strings.Split(strings.TrimSpace(all), "\n")
	if !strings.HasPrefix(lines[len(lines)-1], "supplementary | xlsx | 4 B | moved | s1.xlsx") {
		t.Errorf("listing after move: %q", lines[len(lines)-1])
	}
}

func TestReportPage(t *testing.T) {
	d := omap{{"meta", omap{{"title", `run report: "x" <y>`}}}, {"steps", []any{}}, {"columns", []any{"uid"}},
		{"rows", []any{[]any{"u"}}}, {"attachments", []any{}}}
	page, err := reportPage(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page, "<title>run report: &quot;x&quot; &lt;y&gt;</title>") || !strings.Contains(page, `src="data:image/png;base64,`) ||
		strings.Contains(page, "__SL_") {
		t.Error("page placeholders")
	}
	blob := strings.SplitN(strings.SplitN(page, `<script id="sl-data" type="application/octet-stream">`, 2)[1], "</script>", 2)[0]
	raw, _ := base64.StdEncoding.DecodeString(blob)
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(zr)
	var got any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"meta": map[string]any{"title": `run report: "x" <y>`}, "steps": []any{}, "columns": []any{"uid"},
		"rows": []any{[]any{"u"}}, "attachments": []any{}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("report data %v", got)
	}
}

func TestLocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	a, _ := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	b, _ := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	defer a.Close()
	defer b.Close()
	if lockFile(a, false) != nil {
		t.Fatal("first lock")
	}
	if lockFile(b, false) == nil {
		t.Fatal("second lock should fail")
	}
	unlockFile(a)
	if lockFile(b, false) != nil {
		t.Fatal("lock after unlock")
	}
}

func TestSplitPositionals(t *testing.T) {
	spec := []flagSpec{{"types", "", kStr}, {"plan", "false", kBool}, {"dir", "", kStr}}
	pos, rest := splitPositionals([]string{"a.csv", "--types", "pdf", "--plan", "--dir=x"}, spec)
	if !reflect.DeepEqual(pos, []string{"a.csv"}) || !reflect.DeepEqual(rest, []string{"--types", "pdf", "--plan", "--dir=x"}) {
		t.Errorf("%v %v", pos, rest)
	}
	pos, rest = splitPositionals([]string{"--plan", "b.csv"}, spec)
	if !reflect.DeepEqual(pos, []string{"b.csv"}) || !reflect.DeepEqual(rest, []string{"--plan"}) {
		t.Errorf("%v %v", pos, rest)
	}
}
