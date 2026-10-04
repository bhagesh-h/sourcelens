package main

// Unit tests. tests/test_core.py checks the same cases against the Python
// implementation, so both must keep returning these values.

import (
	"bufio"
	"bytes"
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
