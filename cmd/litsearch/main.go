// Command litsearch collects the latest research on a topic: papers and
// preprints, open-access full texts, code repositories, software packages and
// websites, in one chronological, append-only catalogue (progress.csv).
//
// Every pipeline step is part of this binary. A stage runs its steps as child
// processes of the binary ("litsearch __step NAME ...") so that each step has
// its own log file.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// version is set at build time with -ldflags "-X main.version=..."
var version = "1.0.0"

// ---------------------------------------------------------------------------
// vocabulary
// ---------------------------------------------------------------------------

type entry struct{ name, desc string }

var (
	sources = []entry{
		{"pubmed", "PubMed / MEDLINE search (NCBI E-utilities)"},
		{"europepmc", "Europe PMC search (preprints, PMC-only records) and Europe PMC full-text XML"},
		{"arxiv", "arXiv search and arXiv PDFs"},
		{"openalex", "OpenAlex search (all fields of research), citation counts, exact dates, open-access PDF links"},
		{"local", "DOIs and links in the reference folders listed in the configuration"},
		{"pmc", "PMC open-access bucket on AWS (XML, text, PDF)"},
		{"biorxiv", "bioRxiv / medRxiv JATS and PDF, and preprint to journal links"},
		{"unpaywall", "open-access PDFs from publishers and repositories (needs contact_email)"},
		{"github", "GitHub repository search and metadata"},
		{"cran", "CRAN packages"},
		{"bioconductor", "Bioconductor packages"},
		{"pypi", "PyPI packages"},
		{"zenodo", "Zenodo records linked from papers"},
		{"websites", "curated websites, databases, calculators; sites cited by papers"},
	}
	sourceGroups = map[string][]string{
		"literature": {"pubmed", "europepmc", "arxiv", "openalex", "local"},
		"fulltext":   {"pmc", "biorxiv", "europepmc", "arxiv", "openalex", "unpaywall"},
		"repos":      {"github", "cran", "bioconductor", "pypi", "zenodo"},
		"packages":   {"cran", "bioconductor", "pypi"},
	}
	types = []entry{
		{"papers", "literature metadata: articles, reviews, preprints (search and enrichment)"},
		{"pdf", "full-text PDF files"},
		{"md", "full-text Markdown"},
		{"txt", "full-text plain text"},
		{"xml", "full-text source XML (JATS)"},
		{"repo", "code repositories and software packages"},
		{"website", "websites, databases, web calculators"},
	}
	typeGroups = map[string][]string{"fulltext": {"pdf", "md", "txt", "xml"}, "repos": {"repo"}, "repository": {"repo"},
		"package": {"repo"}, "packages": {"repo"}, "websites": {"website"}, "paper": {"papers"}, "metadata": {"papers"}}
	paperTypes     = []string{"article", "review", "preprint", "report", "thesis", "conference paper", "book chapter"}
	ftFormats      = []string{"pdf", "md", "txt", "xml"}
	ftSources      = []string{"pmc", "biorxiv", "europepmc", "arxiv", "openalex", "unpaywall"}
	cliRepoSrcs    = []string{"github", "cran", "bioconductor", "pypi", "zenodo"}
	searchSrcNames = []string{"pubmed", "europepmc", "arxiv", "openalex"}
	paperTypeSet   = map[string]bool{}
)

func init() {
	for _, t := range paperTypes {
		paperTypeSet[t] = true
	}
}

// step name -> implementation
var steps = map[string]func([]string, *Logger) error{
	"seeds":           stepSeeds,
	"pubmed":          stepPubmed,
	"europepmc":       stepEuropePMC,
	"arxiv":           stepArxiv,
	"openalex-search": stepOpenAlexSearch,
	"github-search":   stepGithubSearch,
	"resolve-seeds":   stepResolveSeeds,
	"catalogue":       stepCatalogue,
	"openalex":        stepOpenAlex,
	"preprint-links":  stepPreprintLinks,
	"fulltext":        stepFulltext,
	"links":           stepLinks,
	"repositories":    stepRepositories,
	"websites":        stepWebsites,
	"summary":         stepSummary,
}

const mainHelp = `litsearch %s: collect the latest research on any topic

usage
  litsearch "TOPIC" [options]     search, download and catalogue a topic (same as update --topic)
  litsearch COMMAND [options]

commands
  update    search, download and rebuild a catalogue (the default topic without --topic)
  retry     download again the full texts that were deferred, partial or failed
  query     filter a catalogue
  export    write catalogue entries as references (APA AMA MLA CHI HAR VAN IEE NAT BIB RIS ENW CSL)
  status    size, full-text coverage, last runs and failures of a catalogue
  list      the catalogues in the output folder
  config    show or change the settings of this machine (output folder, contact email, keys)
  sources   the values --sources, --types, --paper-types and --format accept
  test      check the append-only contract on a throwaway catalogue
  version   print the version

examples
  litsearch "CRISPR base editing"                 last 12 months, every source
  litsearch "graph neural networks" --range 3y    a longer window
  litsearch update                                the default topic
  litsearch query --topic "CRISPR base editing" --range 1m
  litsearch export --topic "CRISPR base editing" --format BIB --out crispr.bib

litsearch COMMAND --help shows the options of a command.
`

const projectHelp = `catalogue
  --topic TEXT        the topic; words are all required, commas separate alternatives,
                      "double quotes" keep a phrase (default: the default topic)
  --dir DIR           use the catalogue in DIR instead of the output folder
`

const updateHelp = `litsearch update [options]: search, download and rebuild a catalogue

  litsearch update                                 the default topic, configured window
  litsearch "CRISPR base editing"                  a new topic: last 12 months
  litsearch "CRISPR base editing" --range 5y       a new topic: last 5 years
  litsearch update --range 1y                      only work published in the last year
  litsearch update --types papers --range 6m       metadata only, last 6 months
  litsearch update --from 2020 --to 2022-06        exact window (year, month or day)
  litsearch update --types pdf,md --sources pmc,unpaywall --range 2y
  litsearch update --types repo,website            repositories, packages, websites only
  litsearch update --sources pubmed,arxiv --dry-run

` + projectHelp + `
options
  --sources LIST      sources or groups (default all; see: litsearch sources)
  --types LIST        all | papers,pdf,md,txt,xml,repo,website (default all)
  --paper-types LIST  paper types to download full texts for (default all)
  --range SPAN        span back from --to: 1d 7d 2w 1m 6m 1y 2y 10y
  --from WHEN         YYYY, YYYY-MM, YYYY-MM-DD, today or a span (2y)
  --to WHEN           YYYY, YYYY-MM, YYYY-MM-DD or today (default today)
  --scope S           search groups: focused | broad | all (default all)
  --tiers LIST        catalogue tiers for full texts (default landmark,core,related)
  --workers N         parallel full-text downloads (default 12)
  --parallel N        steps run at the same time within a stage (default 5)
  --min-stars N       GitHub search hits need this many stars (default 3)
  --heartbeat SEC     seconds between progress lines (default 120)
  --stop-on-error     stop after the first failed stage
  --dry-run           print the plan and a full-text estimate, then exit

A range limits the searches (publication date), the full-text downloads
(publication date) and the GitHub search (creation date). It never removes
anything from progress.csv, which only grows. A new topic starts 12 months
back unless --range or --from says otherwise; asking later for older work
extends its window.
`

const retryHelp = `litsearch retry [options]: download again the full texts whose last attempt
was deferred (rate limit), partial or failed, then rebuild

` + projectHelp + `
options
  --status LIST   last full-text statuses to try again (default deferred,partial,error)
  --types LIST    formats to keep: pdf,md,txt,xml (default all four)
  --sources LIST  full-text sources (default pmc,biorxiv,europepmc,arxiv,openalex,unpaywall)
  --workers N     parallel downloads (default 12)
  --dry-run       count the records and exit
`

const statusHelp = `litsearch status [--topic TEXT | --dir DIR]: size, full-text coverage, last
builds, the last run and its failures, and whether an update is running
`

func fail(msg string) int {
	fmt.Fprintf(os.Stderr, "litsearch: %s\n", msg)
	return 2
}

func names(es []entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.name
	}
	return out
}

func expand(spec string, vocab []string, groups map[string][]string, what string) ([]string, error) {
	var out []string
	for _, tok := range strings.Split(spec, ",") {
		tok = strings.ToLower(pyStrip(tok))
		if tok == "" {
			continue
		}
		items := groups[tok]
		if tok == "all" {
			items = vocab
		} else if items == nil {
			items = []string{tok}
		}
		for _, it := range items {
			if !contains(vocab, it) {
				return nil, fmt.Errorf(`unknown %s "%s" (see: litsearch sources)`, what, tok)
			}
			if !contains(out, it) {
				out = append(out, it)
			}
		}
	}
	return out, nil
}

var sayMu sync.Mutex

func say(format string, a ...any) {
	sayMu.Lock()
	defer sayMu.Unlock()
	fmt.Printf("[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, a...))
}

func counts(rows []Row, col string) string {
	c := newCounter()
	for _, r := range rows {
		c.add(orDefault(r[col], "-"), 1)
	}
	ks := append([]string(nil), c.keys...)
	sort.SliceStable(ks, func(i, j int) bool {
		if c.n[ks[i]] != c.n[ks[j]] {
			return c.n[ks[i]] > c.n[ks[j]]
		}
		return ks[i] < ks[j]
	})
	parts := make([]string, len(ks))
	for i, k := range ks {
		parts[i] = fmt.Sprintf("%s: %d", k, c.n[k])
	}
	return strings.Join(parts, ", ")
}

// ---------------------------------------------------------------------------
// plan
// ---------------------------------------------------------------------------

type step struct {
	name string
	args []string
}

var stepSuffix = regexp.MustCompile(`-\d+$`)

func newStep(name string, args ...string) step {
	var a []string
	for _, x := range args {
		if x != "" {
			a = append(a, x)
		}
	}
	return step{name, a}
}

func (s step) String() string { return strings.Join(append([]string{s.name}, s.args...), " ") }
func (s step) kind() string {
	if s.name == "openalex-search" {
		return s.name
	}
	return stepSuffix.ReplaceAllString(s.name, "")
}

type opts struct {
	sources, types, paperTypes             []string
	searchSources                          []string // literature sources enabled in the configuration
	start, end, scope, tiers               string
	workers, parallel, minStars, heartbeat int
	stopOnError, dryRun                    bool
}

func keep(vocab []string, in []string) []string {
	var out []string
	for _, v := range vocab {
		if contains(in, v) {
			out = append(out, v)
		}
	}
	return out
}

// configSearchSources: search.sources of the configuration (pubmed, europepmc
// and arxiv when the key is absent).
func configSearchSources() []string {
	var s struct {
		Sources []string `yaml:"sources"`
	}
	_ = loadConfig("search", &s)
	if len(s.Sources) == 0 {
		return []string{"pubmed", "europepmc", "arxiv"}
	}
	return keep(searchSrcNames, s.Sources)
}

func plan(o opts) [][]step {
	src, ty := o.sources, o.types
	papers := contains(ty, "papers")
	search := func(name string) bool { return papers && contains(src, name) && contains(o.searchSources, name) }
	formats := keep(ftFormats, ty)
	fts := keep(ftSources, src)
	rs := keep(cliRepoSrcs, src)
	wantRepo := contains(ty, "repo") && len(rs) > 0
	wantWeb := contains(ty, "website") && contains(src, "websites")
	var stages [][]step
	var s1 []step
	if papers && contains(src, "local") {
		s1 = append(s1, newStep("seeds"))
	}
	if search("pubmed") {
		s1 = append(s1, newStep("pubmed", "--scope", o.scope))
	}
	if search("europepmc") {
		s1 = append(s1, newStep("europepmc", "--scope", o.scope))
	}
	if search("arxiv") && o.scope != "broad" {
		s1 = append(s1, newStep("arxiv"))
	}
	if search("openalex") {
		s1 = append(s1, newStep("openalex-search", "--scope", o.scope))
	}
	if wantRepo && contains(src, "github") {
		s1 = append(s1, newStep("github-search"))
	}
	stages = append(stages, s1)
	if papers && contains(src, "local") {
		stages = append(stages, []step{newStep("resolve-seeds")})
	}
	stages = append(stages, []step{newStep("catalogue-1")})
	var s3 []step
	if papers && contains(src, "openalex") {
		s3 = append(s3, newStep("openalex"))
	}
	if papers && contains(src, "biorxiv") {
		s3 = append(s3, newStep("preprint-links"))
	}
	if len(formats) > 0 && len(fts) > 0 {
		s3 = append(s3, newStep("fulltext", "--formats", strings.Join(formats, ","), "--sources", strings.Join(fts, ","),
			"--types", strings.Join(o.paperTypes, ","), "--tiers", o.tiers, "--workers", strconv.Itoa(o.workers)))
	}
	stages = append(stages, s3)
	if len(s3) > 0 {
		stages = append(stages, []step{newStep("catalogue-2")})
	}
	if wantRepo || wantWeb {
		stages = append(stages, []step{newStep("links")})
	}
	var s5 []step
	if wantRepo {
		s5 = append(s5, newStep("repositories", "--sources", strings.Join(rs, ","), "--min-stars", strconv.Itoa(o.minStars)))
	}
	if wantWeb {
		s5 = append(s5, newStep("websites"))
	}
	stages = append(stages, s5)
	if len(s5) > 0 {
		stages = append(stages, []step{newStep("catalogue-3")})
	}
	stages = append(stages, []step{newStep("summary")})
	var out [][]step
	for _, s := range stages {
		if len(s) > 0 {
			out = append(out, s)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// run
// ---------------------------------------------------------------------------

func lastLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	off := max(0, st.Size()-4000)
	b := make([]byte, st.Size()-off)
	n, _ := f.ReadAt(b, off)
	var lines []string
	for _, l := range splitLinesPy(strings.ToValidUTF8(string(b[:n]), "�")) {
		if pyStrip(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return runeCut(pyStrip(lines[len(lines)-1]), 150)
}

type result struct {
	s   step
	rc  int
	dur time.Duration
	log string
}

func runStep(s step, logPath string, env []string) result {
	t0 := time.Now()
	say("start  %-15s %s", s.name, s)
	rc := 0
	fh, err := os.Create(logPath)
	if err != nil {
		rc = 1
	} else {
		self, _ := os.Executable()
		cmd := exec.Command(self, append([]string{"__step", s.kind()}, s.args...)...)
		cmd.Stdout, cmd.Stderr, cmd.Env, cmd.Dir = fh, fh, env, Research
		if err := cmd.Run(); err != nil {
			rc = 1
			if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() > 0 {
				rc = ee.ExitCode()
			}
		}
		fh.Close()
	}
	dur := time.Since(t0)
	mark := "done "
	if rc != 0 {
		mark = "FAILED"
	}
	say("%s %-15s %5.1f min  %s", mark, s.name, dur.Minutes(), lastLine(logPath))
	return result{s, rc, dur, logPath}
}

func runPlan(stages [][]step, o opts, label string) int {
	stamp := time.Now().Format("2006-01-02_150405")
	logdir := rpath("logs", "cli_"+stamp)
	if err := os.MkdirAll(logdir, 0o755); err != nil {
		return fail(err.Error())
	}
	lines := []string{"command " + label, "version " + version,
		"window " + orDefault(o.start, "config start") + " .. " + orDefault(o.end, "today"),
		"sources " + strings.Join(o.sources, ","), "types " + strings.Join(o.types, ","),
		"paper-types " + strings.Join(o.paperTypes, ",")}
	for i, st := range stages {
		parts := make([]string, len(st))
		for j, s := range st {
			parts[j] = s.String()
		}
		lines = append(lines, fmt.Sprintf("stage %d: %s", i+1, strings.Join(parts, " | ")))
	}
	_ = os.WriteFile(filepath.Join(logdir, "plan.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	// the configuration this run used, kept with its logs
	if b, err := os.ReadFile(configFile()); err == nil {
		_ = os.WriteFile(filepath.Join(logdir, "litsearch.yaml"), b, 0o644)
	}
	say("logs in %s", logdir)
	env := os.Environ()
	if o.start != "" || o.end != "" {
		cfgStart, _ := queryWindow()
		env = append(env, "LITSEARCH_SEARCH_START="+orDefault(o.start, cfgStart), "LITSEARCH_SEARCH_END="+orDefault(o.end, today()))
	}

	var mu sync.Mutex
	running := map[string]string{}
	var runOrder []string
	stop := make(chan struct{})
	if o.heartbeat > 0 {
		go func() {
			t := time.NewTicker(time.Duration(o.heartbeat) * time.Second)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					mu.Lock()
					var cur [][2]string
					for _, n := range runOrder {
						if l, ok := running[n]; ok {
							cur = append(cur, [2]string{n, l})
						}
					}
					mu.Unlock()
					for _, c := range cur {
						say("  ...  %-15s %s", c[0], lastLine(c[1]))
					}
				}
			}
		}()
	}
	var results []result
	n, tAll, failed := 0, time.Now(), false
	for i, stage := range stages {
		nm := make([]string, len(stage))
		for j, s := range stage {
			nm[j] = s.name
		}
		say("== stage %d/%d: %s", i+1, len(stages), strings.Join(nm, ", "))
		res := make([]result, len(stage))
		sem := make(chan struct{}, max(1, o.parallel))
		var wg sync.WaitGroup
		logs := make([]string, len(stage))
		for j, s := range stage {
			n++
			logs[j] = filepath.Join(logdir, fmt.Sprintf("%02d_%s.log", n, s.name))
			mu.Lock()
			running[s.name] = logs[j]
			runOrder = append(runOrder, s.name)
			mu.Unlock()
		}
		for j, s := range stage {
			wg.Add(1)
			sem <- struct{}{} // steps start in plan order
			go func(j int, s step) {
				defer wg.Done()
				defer func() { <-sem }()
				res[j] = runStep(s, logs[j], env)
				mu.Lock()
				delete(running, s.name) // the heartbeat reports running steps only
				mu.Unlock()
			}(j, s)
		}
		wg.Wait()
		for _, r := range res {
			mu.Lock()
			delete(running, r.s.name)
			mu.Unlock()
			results = append(results, r)
			failed = failed || r.rc != 0
		}
		if failed && o.stopOnError {
			break
		}
	}
	close(stop)
	var sb strings.Builder
	for _, r := range results {
		fmt.Fprintf(&sb, "%s\trc=%d\t%.1f min\t%s\n", r.s.name, r.rc, r.dur.Minutes(), filepath.Base(r.log))
	}
	_ = os.WriteFile(filepath.Join(logdir, "summary.txt"), []byte(sb.String()), 0o644)
	say("finished in %.1f min", time.Since(tAll).Minutes())
	td := today()
	if added := rpath("changelog", "added_"+td+".csv"); fileExists(added) {
		rows, _ := readCSV(added)
		say("rows added today: %d (changelog/added_%s.csv)", len(rows), td)
	}
	for _, r := range results {
		if r.rc != 0 {
			say("FAILED step %s: see %s", r.s.name, r.log)
		}
	}
	if failed {
		return 1
	}
	return 0
}

// newest prints the most recent rows added today: what a run found.
func newest(limit int) {
	rows, _ := readCSV(rpath("changelog", "added_"+today()+".csv"))
	if len(rows) == 0 {
		return
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i]["date"] > rows[j]["date"] })
	fmt.Printf("\nnewest additions (%d of %d added today):\n", min(limit, len(rows)), len(rows))
	for _, r := range rows[:min(limit, len(rows))] {
		fmt.Printf("  %-10s %-16s %s\n", r["date"], runeCut(r["resource_type"], 16), runeCut(r["title"], 100))
	}
}

// ---------------------------------------------------------------------------
// estimate
// ---------------------------------------------------------------------------

func estimateFulltext(o opts, formats []string) (int, int) {
	rows, _ := readCSV(rpath("progress.csv"))
	idxRows, _ := readCSV(rpath("fulltext", "fulltext_index.csv"))
	idx := map[string]Row{}
	for _, r := range idxRows {
		idx[r["uid"]] = r
	}
	tiers := strings.Split(o.tiers, ",")
	cutoff := time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	lo, hi := orDefault(o.start, "0000"), orDefault(o.end, "9999")
	todo, scope := 0, 0
	for _, r := range rows {
		if !paperTypeSet[r["resource_type"]] || !contains(o.paperTypes, r["resource_type"]) || !contains(tiers, r["tier"]) {
			continue
		}
		if d := orDefault(r["date"], "0000"); (o.start != "" || o.end != "") && !(lo <= d && d <= hi) {
			continue
		}
		scope++
		p, ok := idx[r["uid"]]
		switch {
		case !ok || p["status"] == "partial" || p["status"] == "deferred" || p["status"] == "error":
			todo++
		case p["status"] == "none" && p["checked_on"] < cutoff:
			todo++
		case p["status"] == "ok" && p["checked_on"] < cutoff:
			for _, f := range formats {
				if p["has_"+f] != "True" {
					todo++
					break
				}
			}
		}
	}
	return todo, scope
}

// ---------------------------------------------------------------------------
// commands
// ---------------------------------------------------------------------------

var projectSpec = []flagSpec{{"topic", "", kStr}, {"dir", "", kStr}}

func withProject(spec ...flagSpec) []flagSpec {
	return append(append([]flagSpec(nil), projectSpec...), spec...)
}

func cmdUpdate(argv []string) int {
	spec := withProject(flagSpec{"sources", "all", kStr}, flagSpec{"types", "all", kStr},
		flagSpec{"paper-types", strings.Join(paperTypes, ","), kStr},
		flagSpec{"range", "", kStr}, flagSpec{"from", "", kStr}, flagSpec{"to", "", kStr}, flagSpec{"scope", "all", kStr},
		flagSpec{"tiers", "landmark,core,related", kStr}, flagSpec{"workers", "12", kInt}, flagSpec{"parallel", "5", kInt},
		flagSpec{"min-stars", "3", kInt}, flagSpec{"heartbeat", "120", kInt}, flagSpec{"stop-on-error", "false", kBool},
		flagSpec{"dry-run", "false", kBool})
	f, err := parseFlags(argv, spec, updateHelp)
	if _, ok := err.(errHelp); ok {
		return 0
	}
	if err != nil {
		return fail(err.Error())
	}
	o := opts{scope: f["scope"], tiers: f["tiers"], workers: f.int("workers"), parallel: f.int("parallel"),
		minStars: f.int("min-stars"), heartbeat: f.int("heartbeat"), stopOnError: f.bool("stop-on-error"),
		dryRun: f.bool("dry-run")}
	if o.sources, err = expand(f["sources"], names(sources), sourceGroups, "source"); err != nil {
		return fail(err.Error())
	}
	if o.types, err = expand(f["types"], names(types), typeGroups, "type"); err != nil {
		return fail(err.Error())
	}
	for _, t := range strings.Split(f["paper-types"], ",") {
		if t = pyStrip(t); t != "" {
			o.paperTypes = append(o.paperTypes, t)
		}
	}
	for _, t := range o.paperTypes {
		if !paperTypeSet[t] {
			return fail(fmt.Sprintf(`unknown paper type "%s" (one of: %s)`, t, strings.Join(paperTypes, ", ")))
		}
	}
	if o.scope != "focused" && o.scope != "broad" && o.scope != "all" {
		return fail("--scope must be focused, broad or all")
	}
	if f["topic"] != "" && len(parseTopic(f["topic"])) == 0 {
		return fail("--topic needs at least one word")
	}
	lo, hi, err := filterWindow(flagSet{"from": f["from"], "to": f["to"], "range": f["range"]})
	if err != nil {
		return fail(err.Error())
	}
	p, err := resolveProject(f["topic"], f["dir"], !o.dryRun, lo)
	if err != nil {
		if o.dryRun && !fileExists(p.config) {
			fmt.Printf("litsearch update (dry run)\n  catalogue: %s (not created yet)\n  topic    : %s\n  terms    : %s\n",
				p.dir, orDefault(f["topic"], defaultTopic()), strings.Join(parseTopic(orDefault(f["topic"], defaultTopic())), " | "))
			return 0
		}
		return fail(err.Error())
	}
	if p.created {
		say("new catalogue for %q in %s", p.topic, p.dir)
	}
	if !o.dryRun {
		if moved, err := extendStart(p, lo); err != nil {
			return fail(err.Error())
		} else if moved {
			say("catalogue window now starts %s", lo)
		}
	}
	o.searchSources = configSearchSources()
	o.start, o.end = lo, hi
	if o.start != "" && o.end == "" {
		o.end = today()
	}
	if o.end != "" && o.start == "" {
		o.start, _ = queryWindow() // the configured start date
	}
	if contains(o.sources, "github") {
		githubTokenFromCLI()
	}
	stages := plan(o)
	fmt.Printf("litsearch update\n  topic  : %s\n  folder : %s\n  window : %s .. %s\n  sources: %s\n  types  : %s\n"+
		"  paper types (full text): %s\n  scope  : %s   full-text tiers: %s   workers: %d   parallel steps: %d\n",
		p.topic, p.dir, orDefault(o.start, "config start"), orDefault(o.end, "today"), strings.Join(o.sources, ", "),
		strings.Join(o.types, ", "), strings.Join(o.paperTypes, ", "), o.scope, o.tiers, o.workers, o.parallel)
	for i, st := range stages {
		nm := make([]string, len(st))
		for j, s := range st {
			nm[j] = s.name
		}
		fmt.Printf("  stage %d: %s\n", i+1, strings.Join(nm, " | "))
	}
	if o.dryRun {
		for i, st := range stages {
			for _, s := range st {
				fmt.Printf("    %d. %s\n", i+1, s)
			}
		}
		if formats := keep(ftFormats, o.types); len(formats) > 0 {
			todo, scope := estimateFulltext(o, formats)
			fmt.Printf("  estimate: %d of %d catalogued papers in scope would be tried for full text "+
				"(plus whatever the searches add)\n", todo, scope)
		}
		return 0
	}
	lock, err := acquirePipelineLock("litsearch update")
	if err != nil {
		return fail(err.Error())
	}
	defer lock.Close()
	rc := runPlan(stages, o, "update")
	newest(10)
	fmt.Printf("\ncatalogue: %s\n", rpath("progress.csv"))
	return rc
}

func cmdRetry(argv []string) int {
	spec := withProject(flagSpec{"status", "deferred,partial,error", kStr}, flagSpec{"types", "pdf,md,txt,xml", kStr},
		flagSpec{"sources", strings.Join(ftSources, ","), kStr}, flagSpec{"workers", "12", kInt},
		flagSpec{"dry-run", "false", kBool})
	f, err := parseFlags(argv, spec, retryHelp)
	if _, ok := err.(errHelp); ok {
		return 0
	}
	if err != nil {
		return fail(err.Error())
	}
	formats, err := expand(f["types"], ftFormats, map[string][]string{"fulltext": ftFormats}, "format")
	if err != nil {
		return fail(err.Error())
	}
	fsrc, err := expand(f["sources"], ftSources, map[string][]string{"fulltext": ftSources}, "full-text source")
	if err != nil {
		return fail(err.Error())
	}
	if _, err := resolveProject(f["topic"], f["dir"], false, ""); err != nil {
		return fail(err.Error())
	}
	want := strings.Split(f["status"], ",")
	idx, _ := readCSV(rpath("fulltext", "fulltext_index.csv"))
	n := 0
	for _, r := range idx {
		if contains(want, r["status"]) {
			n++
		}
	}
	fmt.Printf("litsearch retry: %d records with status %s\n", n, f["status"])
	if f.bool("dry-run") || n == 0 {
		return 0
	}
	o := opts{sources: fsrc, types: formats, paperTypes: paperTypes, parallel: 1, heartbeat: 120}
	stages := [][]step{{newStep("fulltext", "--only-status", f["status"], "--formats", strings.Join(formats, ","),
		"--sources", strings.Join(fsrc, ","), "--workers", strconv.Itoa(f.int("workers")))},
		{newStep("catalogue-1")}, {newStep("summary")}}
	lock, err := acquirePipelineLock("litsearch retry")
	if err != nil {
		return fail("another update is running; try again later")
	}
	defer lock.Close()
	return runPlan(stages, o, "retry")
}

func cmdStatus(argv []string) int {
	f, err := parseFlags(argv, projectSpec, statusHelp)
	if _, ok := err.(errHelp); ok {
		return 0
	}
	if err != nil {
		return fail(err.Error())
	}
	p, err := resolveProject(f["topic"], f["dir"], false, "")
	if err != nil {
		return fail(err.Error())
	}
	fmt.Printf("topic: %s\nfolder: %s\n", p.topic, p.dir)
	rows, _ := readCSV(rpath("progress.csv"))
	if len(rows) == 0 {
		fmt.Printf("no progress.csv yet; run: litsearch update%s\n", topicFlag(f["topic"]))
		return 0
	}
	fmt.Printf("progress.csv: %d rows, %s .. %s\n", len(rows), rows[0]["date"], rows[len(rows)-1]["date"])
	for _, col := range []string{"resource_type", "tier", "fulltext_status"} {
		fmt.Printf("  %-16s%s\n", col, counts(rows, col))
	}
	idx, _ := readCSV(rpath("fulltext", "fulltext_index.csv"))
	if len(idx) > 0 {
		fmt.Printf("full-text index  %s\n", counts(idx, "status"))
		seen := map[string]bool{}
		for _, r := range idx {
			seen[r["uid"]] = true
		}
		waiting := 0
		for _, r := range rows {
			if paperTypeSet[r["resource_type"]] && !seen[r["uid"]] {
				waiting++
			}
		}
		fmt.Printf("  papers not yet tried for full text: %d   (try deferred/partial again: litsearch retry)\n", waiting)
	}
	runs, _ := readCSV(rpath("changelog", "runs.csv"))
	if len(runs) > 0 {
		fmt.Println("last builds:")
		for _, r := range runs[max(0, len(runs)-5):] {
			fmt.Printf("  %s: %s rows, +%s added\n", r["run_date"], r["rows_total"], r["rows_added"])
		}
	}
	dirs, _ := filepath.Glob(rpath("logs", "cli_*"))
	sort.Strings(dirs)
	if len(dirs) > 0 {
		last := dirs[len(dirs)-1]
		fmt.Printf("last run: %s\n", researchRel(last))
		if b, err := os.ReadFile(filepath.Join(last, "plan.txt")); err == nil {
			for _, l := range splitLinesPy(string(b)) {
				if strings.HasPrefix(l, "command") || strings.HasPrefix(l, "window") {
					fmt.Println("  " + l)
				}
			}
		}
		if b, err := os.ReadFile(filepath.Join(last, "summary.txt")); err == nil {
			s := pyStrip(string(b))
			if s != "" {
				for _, l := range splitLinesPy(s) {
					mark := "FAILED"
					if strings.Contains(l, "rc=0") {
						mark = "ok    "
					}
					fmt.Printf("  %s %s\n", mark, strings.ReplaceAll(l, "\t", "  "))
				}
			}
		} else {
			fmt.Println("  (no summary.txt: still running or interrupted)")
		}
	}
	if lf := rpath(".pipeline.lock"); fileExists(lf) {
		held, err := acquirePipelineLock("status probe")
		if err != nil {
			b, _ := os.ReadFile(lf)
			fmt.Printf("update running now: %s\n", pyStrip(string(b)))
		} else {
			held.Close()
		}
	}
	return 0
}

func cmdSources() int {
	fmt.Println("--sources (comma list; default all)")
	for _, e := range sources {
		fmt.Printf("  %-13s %s\n", e.name, e.desc)
	}
	fmt.Println("  groups: literature = pubmed,europepmc,arxiv,openalex,local; fulltext = pmc,biorxiv,europepmc,arxiv,openalex,unpaywall;")
	fmt.Println("          repos = github,cran,bioconductor,pypi,zenodo; packages = cran,bioconductor,pypi")
	fmt.Println("  The literature sources searched for a topic are also limited by search.sources in its configuration.")
	fmt.Println("\n--types (comma list; default all)")
	for _, e := range types {
		fmt.Printf("  %-13s %s\n", e.name, e.desc)
	}
	fmt.Println("  groups: fulltext = pdf,md,txt,xml; repos/package = repo; websites = website")
	fmt.Println("\n--paper-types (full-text downloads; default all): " + strings.Join(paperTypes, ", "))
	fmt.Println("\n--format (export; comma list; default APA)")
	for _, c := range refCodes {
		fmt.Printf("  %s  %s\n", c, refDescriptions[c])
	}
	fmt.Println("\n--range: 1d 7d 2w 1m 6m 1y 2y 10y ...   --from/--to: 2024 | 2024-03 | 2024-03-15 | today")
	return 0
}

// runStepNow runs one step in this process (the child side of runStep).
func runStepNow(args []string) int {
	log := &Logger{w: os.Stderr}
	if len(args) == 0 || steps[args[0]] == nil {
		fmt.Fprintf(os.Stderr, "unknown step %v\n", args)
		return 2
	}
	if err := steps[args[0]](args[1:], log); err != nil {
		log.Printf("ERROR: %v", err)
		return 1
	}
	return 0
}

func hasHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "-help" {
			return true
		}
	}
	return false
}

var commands = []string{"update", "retry", "query", "export", "status", "list", "config", "sources", "test",
	"version", "help"}

func run(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "-h" || argv[0] == "--help" {
		if len(argv) == 2 && contains(commands, argv[1]) && argv[1] != "help" {
			return run([]string{argv[1], "--help"})
		}
		fmt.Printf(mainHelp, version)
		return 0
	}
	cmd, args := argv[0], argv[1:]
	switch cmd {
	case "__step":
		return runStepNow(args)
	case "update":
		return cmdUpdate(args)
	case "retry":
		return cmdRetry(args)
	case "query", "export":
		if hasHelp(args) {
			if cmd == "query" {
				fmt.Print(queryHelp)
			} else {
				fmt.Print(exportHelp)
			}
			return 0
		}
		if cmd == "query" {
			return cmdQuery(args)
		}
		return cmdExport(args)
	case "status":
		return cmdStatus(args)
	case "list":
		return cmdList(args)
	case "config":
		return cmdConfig(args)
	case "sources":
		return cmdSources()
	case "test":
		return cmdTest()
	case "version", "--version", "-v":
		fmt.Printf("litsearch %s (go)\n", version)
		return 0
	}
	// anything else that is not an option is a topic: litsearch "CRISPR base editing" [options]
	if !strings.HasPrefix(cmd, "-") {
		var words, rest []string
		for i, a := range argv {
			if strings.HasPrefix(a, "-") {
				rest = argv[i:]
				break
			}
			words = append(words, a)
		}
		return cmdUpdate(append([]string{"--topic", strings.Join(words, " ")}, rest...))
	}
	fmt.Fprintf(os.Stderr, "litsearch: unknown option \"%s\"\n\n", cmd)
	fmt.Printf(mainHelp, version)
	return 2
}

func main() {
	initPaths()
	os.Exit(run(os.Args[1:]))
}
