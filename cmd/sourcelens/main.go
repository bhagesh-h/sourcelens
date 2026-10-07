// Command sourcelens collects the latest research on a topic: papers and
// preprints, open-access full texts, code repositories, software packages and
// websites, in one chronological, append-only catalogue (progress.csv).
//
// Every pipeline step is part of this binary. A stage runs its steps as child
// processes of the binary ("sourcelens __step NAME ...") so that each step has
// its own log file.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// version is set at build time with -ldflags "-X main.version=..."
var version = "1.0.1"

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
		{"attachments", "figures and supplementary files (spreadsheets, slides, documents) of open-access papers"},
		{"repo", "code repositories and software packages"},
		{"website", "websites, databases, web calculators"},
	}
	typeGroups = map[string][]string{"fulltext": {"pdf", "md", "txt", "xml"}, "repos": {"repo"}, "repository": {"repo"},
		"package": {"repo"}, "packages": {"repo"}, "websites": {"website"}, "paper": {"papers"}, "metadata": {"papers"},
		"attachment": {"attachments"}, "files": {"pdf", "md", "txt", "xml", "attachments"}}
	paperTypes    = []string{"article", "review", "preprint", "report", "thesis", "conference paper", "book chapter"}
	ftFormats     = []string{"pdf", "md", "txt", "xml", "attachments"}
	metadataTypes = []string{"papers", "repo", "website"} // what a dry run fetches
	formatGroups  = map[string][]string{"fulltext": {"pdf", "md", "txt", "xml"}, "files": {"pdf", "md", "txt", "xml", "attachments"},
		"attachment": {"attachments"}}
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
	"dryrun":          stepDryrun,
}

const mainHelp = `sourcelens %s: collect the latest research on any topic

usage
  sourcelens "TOPIC" [options]     search, download and catalogue a topic (same as update --topic)
  sourcelens COMMAND [options]

commands
  update    search, download and rebuild a catalogue (the default topic without --topic)
  download  download the full texts and attachments of the rows in a CSV (a filtered dry run)
  retry     download again the full texts that were deferred, partial or failed
  query     filter a catalogue or a dry-run table
  files     list, copy, move or delete downloaded files by extension, name or paper
  export    write catalogue entries as references (APA AMA MLA CHI HAR VAN IEE NAT BIB RIS ENW CSL)
  report    write the run report (searchable HTML) of a catalogue now
  status    size, full-text coverage, last runs and failures of a catalogue
  list      the catalogues in the output folder
  config    show or change the settings of this machine (output folder, contact email, keys)
  sources   the values --sources, --types, --paper-types and --format accept
  test      check the append-only contract on a throwaway catalogue
  version   print the version

examples
  sourcelens "CRISPR base editing"                 last 12 months, every source
  sourcelens "graph neural networks" --range 3y    a longer window
  sourcelens update                                the default topic
  sourcelens "CRISPR base editing" --dry-run       metadata and a table of what is there; no downloads
  sourcelens query --in reports/dryrun_<stamp>.csv --summary "off-target" --out picked.csv
  sourcelens download exports/picked.csv           download only the picked rows
  sourcelens query --topic "CRISPR base editing" --range 1m
  sourcelens export --topic "CRISPR base editing" --format BIB --out crispr.bib

sourcelens COMMAND --help shows the options of a command.
`

const projectHelp = `catalogue
  --topic TEXT        the topic; words are all required, commas separate alternatives,
                      "double quotes" keep a phrase (default: the default topic)
  --dir DIR           use the catalogue in DIR instead of the output folder
`

const updateHelp = `sourcelens update [options]: search, download and rebuild a catalogue

  sourcelens update                                 the default topic, configured window
  sourcelens "CRISPR base editing"                  a new topic: last 12 months
  sourcelens "CRISPR base editing" --range 5y       a new topic: last 5 years
  sourcelens update --range 1y                      only work published in the last year
  sourcelens update --types papers --range 6m       metadata only, last 6 months
  sourcelens update --from 2020 --to 2022-06        exact window (year, month or day)
  sourcelens update --types pdf,md --sources pmc,unpaywall --range 2y
  sourcelens update --types repo,website            repositories, packages, websites only
  sourcelens update --dry-run                       metadata only, then a table of what could be downloaded
  sourcelens update --types attachments --ext xlsx,csv,pptx   spreadsheets and slides of open papers
  sourcelens update --sources pubmed,arxiv --plan

` + projectHelp + `
options
  --sources LIST      sources or groups (default all; see: sourcelens sources)
  --types LIST        all | papers,pdf,md,txt,xml,attachments,repo,website (default all)
  --paper-types LIST  paper types to download full texts for (default all)
  --range SPAN        span back from --to: 1d 7d 2w 1m 6m 1y 2y 10y
  --from WHEN         YYYY, YYYY-MM, YYYY-MM-DD, today or a span (2y)
  --to WHEN           YYYY, YYYY-MM, YYYY-MM-DD or today (default today)
  --scope S           search groups: focused | broad | all (default all)
  --tiers LIST        catalogue tiers for full texts (default landmark,core,related)
  --ext LIST          attachment file extensions to download, e.g. xlsx,csv,pptx (default all)
  --max-attachment-mb N   larger attachments are listed but not downloaded (default 100, 0: no limit)
  --workers N         parallel full-text downloads (default 12)
  --parallel N        steps run at the same time within a stage (default 5)
  --min-stars N       GitHub search hits need this many stars (default 3)
  --heartbeat SEC     progress lines every SEC seconds when the output is not a
                      terminal; a terminal shows a progress bar (default 120, 0: none)
  --stop-on-error     stop after the first failed stage
  --dry-run           search and fetch metadata only, download nothing, and write
                      reports/dryrun_<stamp>.csv: every row with its summary and what
                      could be downloaded; filter it with query --in, then download
  --plan              print the plan and a full-text estimate, then exit

Every run writes reports/runreport_<stamp>.html: a single page with the run's
numbers and a searchable, filterable table of the catalogue.

A range limits the searches (publication date), the full-text downloads
(publication date) and the GitHub search (creation date). It never removes
anything from progress.csv, which only grows. A new topic starts 12 months
back unless --range or --from says otherwise; asking later for older work
extends its window.
`

const retryHelp = `sourcelens retry [options]: download again the full texts whose last attempt
was deferred (rate limit), partial or failed, then rebuild

` + projectHelp + `
options
  --status LIST   last full-text statuses to try again (default deferred,partial,error)
  --types LIST    formats to keep: pdf,md,txt,xml,attachments (default all)
  --sources LIST  full-text sources (default pmc,biorxiv,europepmc,arxiv,openalex,unpaywall)
  --ext LIST      attachment file extensions to download (default all)
  --workers N     parallel downloads (default 12)
  --plan          count the records and exit
`

const downloadHelp = `sourcelens download FILE [options]: download the full texts and attachments
of the rows in FILE, then rebuild

FILE is a CSV with a uid or doi column: a dry-run table filtered with
sourcelens query --in ... --out FILE, or any query output. Relative paths are
looked up in the current folder, then in the catalogue and its exports/.

  sourcelens "CRISPR base editing" --dry-run
  sourcelens query --topic "CRISPR base editing" --in reports/dryrun_<stamp>.csv --summary prime --out prime.csv
  sourcelens download --topic "CRISPR base editing" exports/prime.csv
  sourcelens download picked.csv --types attachments --ext xlsx,csv

` + projectHelp + `
options
  --types LIST    formats: pdf,md,txt,xml,attachments (default all)
  --sources LIST  full-text sources (default pmc,biorxiv,europepmc,arxiv,openalex,unpaywall)
  --ext LIST      attachment file extensions to download (default all)
  --max-attachment-mb N   larger attachments are listed but not downloaded (default 100, 0: no limit)
  --workers N     parallel downloads (default 12)
  --plan          count the rows and exit
`

const reportHelp = `sourcelens report [--topic TEXT | --dir DIR]: write reports/runreport_<stamp>.html
for the catalogue as it is now (every update, retry, download and dry run writes one too)
`

const statusHelp = `sourcelens status [--topic TEXT | --dir DIR]: size, full-text coverage, last
builds, the last run and its failures, and whether an update is running
`

func fail(msg string) int {
	fmt.Fprintf(os.Stderr, "sourcelens: %s\n", msg)
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
				return nil, fmt.Errorf(`unknown %s "%s" (see: sourcelens sources)`, what, tok)
			}
			if !contains(out, it) {
				out = append(out, it)
			}
		}
	}
	return out, nil
}

var (
	sayMu  sync.Mutex
	status string // the progress bar line kept below the messages (terminal only)
)

func say(format string, a ...any) {
	sayMu.Lock()
	defer sayMu.Unlock()
	clear := ""
	if status != "" {
		clear = "\r\x1b[K"
	}
	fmt.Printf("%s[%s] %s\n%s", clear, time.Now().Format("15:04:05"), fmt.Sprintf(format, a...), status)
}

// setStatus draws the progress bar line below the messages; "" removes it.
func setStatus(line string) {
	sayMu.Lock()
	defer sayMu.Unlock()
	status = line
	fmt.Print("\r\x1b[K" + line)
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
	stopOnError, dryRun, plan              bool
	ext, stamp                             string
	maxAttMB                               int
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
	if o.dryRun {
		ty = keep(metadataTypes, ty)
	}
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
		s3 = append(s3, newStep("fulltext", append([]string{"--formats", strings.Join(formats, ","), "--sources", strings.Join(fts, ","),
			"--types", strings.Join(o.paperTypes, ","), "--tiers", o.tiers, "--workers", strconv.Itoa(o.workers)},
			attachmentArgs(o, formats)...)...))
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
	if o.dryRun {
		stages = append(stages, []step{newStep("dryrun", "--stamp", o.stamp)})
	}
	var out [][]step
	for _, s := range stages {
		if len(s) > 0 {
			out = append(out, s)
		}
	}
	return out
}

// attachmentArgs: --attachment-ext / --max-attachment-mb for the fulltext step,
// when attachments are asked for.
func attachmentArgs(o opts, formats []string) []string {
	if !contains(formats, "attachments") {
		return nil
	}
	var out []string
	if o.ext != "" {
		var exts []string
		for _, e := range strings.Split(o.ext, ",") {
			if e = pyStrip(e); e != "" {
				exts = append(exts, strings.TrimLeft(strings.ToLower(e), "."))
			}
		}
		out = append(out, "--attachment-ext", strings.Join(exts, ","))
	}
	if o.maxAttMB != 100 {
		out = append(out, "--max-attachment-mb", strconv.Itoa(o.maxAttMB))
	}
	return out
}

// runStamp: the stamp of a run's reports, YYYY_MM_DD_HH_MM_SS.
func runStamp(t time.Time) string { return t.Format("2006_01_02_15_04_05") }

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shQuote is python's shlex.quote.
func shQuote(s string) string {
	if s == "" {
		return "''"
	}
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func commandLine() string {
	parts := []string{"sourcelens"}
	for _, a := range os.Args[1:] {
		parts = append(parts, shQuote(a))
	}
	return strings.Join(parts, " ")
}

func logDir(started time.Time) string {
	return rpath("logs", "cli_"+started.Format("2006-01-02_150405"))
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

const barWidth = 24

// "340/1200" in a step's last log line: how far that step is
var countRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_/.])(\d+)/(\d+)(?:[^A-Za-z0-9_/.]|$)`)

func clock(d time.Duration) string {
	s := int(d.Seconds())
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

type activeStep struct {
	name, log string
	start     time.Time
}

// progressLine: [bar] finished/all steps, elapsed time, then each running step with its count or time.
func progressLine(done, total int, t0 time.Time, active []activeStep, width int) string {
	frac := float64(done)
	var parts []string
	for _, a := range active {
		m := countRe.FindStringSubmatch(lastLine(a.log))
		n, of := 0, 0
		if m != nil {
			n, _ = strconv.Atoi(m[1])
			of, _ = strconv.Atoi(m[2])
		}
		if m != nil && of > 0 && n <= of {
			frac += float64(n) / float64(of)
			parts = append(parts, fmt.Sprintf("%s %s/%s", a.name, m[1], m[2]))
		} else {
			parts = append(parts, a.name+" "+clock(time.Since(a.start)))
		}
	}
	filled := barWidth
	if total > 0 {
		filled = min(barWidth, int(float64(barWidth)*frac/float64(total)+0.5))
	}
	line := fmt.Sprintf("[%s%s] %d/%d steps  %s", strings.Repeat("█", filled), strings.Repeat("░", barWidth-filled),
		done, total, clock(time.Since(t0)))
	if len(parts) > 0 {
		line += "  " + strings.Join(parts, ", ")
	}
	return runeCut(line, max(20, width-1))
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

func runPlan(stages [][]step, o opts, label string, runStart time.Time) int {
	logdir := logDir(runStart)
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
		_ = os.WriteFile(filepath.Join(logdir, "sourcelens.yaml"), b, 0o644)
	}
	say("logs in %s", logdir)
	env := os.Environ()
	if o.start != "" || o.end != "" {
		cfgStart, _ := queryWindow()
		env = append(env, "SOURCELENS_SEARCH_START="+orDefault(o.start, cfgStart), "SOURCELENS_SEARCH_END="+orDefault(o.end, today()))
	}

	var mu sync.Mutex
	running := map[string]string{}
	started := map[string]time.Time{}
	var runOrder []string
	finished, total := 0, 0
	for _, st := range stages {
		total += len(st)
	}
	stop, tickerDone := make(chan struct{}), make(chan struct{})
	tAll := time.Now()
	// a terminal gets one progress bar line; a log file gets heartbeat lines
	_, isTerm := stdoutTerminal()
	bar := o.heartbeat > 0 && isTerm && os.Getenv("TERM") != "dumb"
	kick := make(chan struct{}, 1) // redraw the bar now: a step started or ended
	poke := func() {
		select {
		case kick <- struct{}{}:
		default:
		}
	}
	end := func(name string) {
		mu.Lock()
		if _, ok := running[name]; ok {
			delete(running, name)
			finished++
		}
		mu.Unlock()
		poke()
	}
	if o.heartbeat > 0 {
		go func() {
			defer close(tickerDone)
			every := time.Duration(o.heartbeat) * time.Second
			if bar {
				every = time.Second
			}
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-kick:
					if !bar {
						continue
					}
				case <-t.C:
				}
				mu.Lock()
				var cur [][2]string
				var active []activeStep
				for _, n := range runOrder {
					if l, ok := running[n]; ok {
						cur = append(cur, [2]string{n, l})
						if st, ok := started[n]; ok {
							active = append(active, activeStep{n, l, st})
						}
					}
				}
				done := finished
				mu.Unlock()
				if bar {
					width, _ := stdoutTerminal()
					setStatus(progressLine(done, total, tAll, active, width))
					continue
				}
				for _, c := range cur {
					say("  ...  %-15s %s", c[0], lastLine(c[1]))
				}
			}
		}()
	} else {
		close(tickerDone)
	}
	// Ctrl+C reaches the steps too; wait for them, then stop like python's KeyboardInterrupt
	// (an interrupt that the parent process ignores stays ignored, as in python)
	var interrupted atomic.Bool
	sig := make(chan os.Signal, 1)
	if !signal.Ignored(os.Interrupt) {
		signal.Notify(sig, os.Interrupt)
		defer signal.Stop(sig)
	}
	go func() {
		if _, ok := <-sig; ok {
			interrupted.Store(true)
		}
	}()
	stopTicker := func() {
		close(stop)
		<-tickerDone
		if bar {
			setStatus("")
		}
	}
	var results []result
	n, failed := 0, false
	for i, stage := range stages {
		if interrupted.Load() {
			break
		}
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
				mu.Lock()
				started[s.name] = time.Now()
				mu.Unlock()
				poke()
				res[j] = runStep(s, logs[j], env)
				end(s.name) // the heartbeat and the bar report running steps only
			}(j, s)
		}
		wg.Wait()
		for _, r := range res {
			end(r.s.name)
			results = append(results, r)
			failed = failed || r.rc != 0
		}
		if failed && o.stopOnError {
			break
		}
	}
	stopTicker()
	if interrupted.Load() {
		fmt.Fprintln(os.Stderr, "interrupted")
		return 130
	}
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
		flagSpec{"dry-run", "false", kBool}, flagSpec{"plan", "false", kBool}, flagSpec{"ext", "", kStr},
		flagSpec{"max-attachment-mb", "100", kInt})
	f, err := parseFlags(argv, spec, updateHelp)
	if _, ok := err.(errHelp); ok {
		return 0
	}
	if err != nil {
		return fail(err.Error())
	}
	o := opts{scope: f["scope"], tiers: f["tiers"], workers: f.int("workers"), parallel: f.int("parallel"),
		minStars: f.int("min-stars"), heartbeat: f.int("heartbeat"), stopOnError: f.bool("stop-on-error"),
		dryRun: f.bool("dry-run"), plan: f.bool("plan"), ext: f["ext"], maxAttMB: f.int("max-attachment-mb")}
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
	p, err := resolveProject(f["topic"], f["dir"], !o.plan, lo)
	if err != nil {
		if o.plan && !fileExists(p.config) {
			fmt.Printf("sourcelens update (plan)\n  catalogue: %s (not created yet)\n  topic    : %s\n  terms    : %s\n",
				p.dir, orDefault(f["topic"], defaultTopic()), strings.Join(parseTopic(orDefault(f["topic"], defaultTopic())), " | "))
			return 0
		}
		return fail(err.Error())
	}
	if p.created {
		say("new catalogue for %q in %s", p.topic, p.dir)
	}
	if !o.plan {
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
	started := time.Now()
	o.stamp = runStamp(started)
	stages := plan(o)
	shown := o.types
	header := "sourcelens update"
	if o.dryRun {
		shown = keep(metadataTypes, o.types)
		header += " (dry run: metadata only, nothing is downloaded)"
	}
	fmt.Printf("%s\n  topic  : %s\n  folder : %s\n  window : %s .. %s\n  sources: %s\n  types  : %s\n"+
		"  paper types (full text): %s\n  scope  : %s   full-text tiers: %s   workers: %d   parallel steps: %d\n",
		header, p.topic, p.dir, orDefault(o.start, "config start"), orDefault(o.end, "today"), strings.Join(o.sources, ", "),
		strings.Join(shown, ", "), strings.Join(o.paperTypes, ", "), o.scope, o.tiers, o.workers, o.parallel)
	for i, st := range stages {
		nm := make([]string, len(st))
		for j, s := range st {
			nm[j] = s.name
		}
		fmt.Printf("  stage %d: %s\n", i+1, strings.Join(nm, " | "))
	}
	if o.plan {
		for i, st := range stages {
			for _, s := range st {
				fmt.Printf("    %d. %s\n", i+1, s)
			}
		}
		if formats := keep(ftFormats[:4], shown); len(formats) > 0 {
			todo, scope := estimateFulltext(o, formats)
			fmt.Printf("  estimate: %d of %d catalogued papers in scope would be tried for full text "+
				"(plus whatever the searches add)\n", todo, scope)
		}
		return 0
	}
	lock, err := acquirePipelineLock("sourcelens update")
	if err != nil {
		return fail(err.Error())
	}
	label := "update"
	if o.dryRun {
		label = "dry run"
	}
	rc := runPlan(stages, o, label, started)
	lock.Close()
	if rc == 130 { // interrupted
		return rc
	}
	report := writeReport(label, started, rc, p.topic)
	if o.dryRun {
		where := topicFlag(f["topic"])
		if f["dir"] != "" {
			where = " --dir " + f["dir"]
		}
		dryrunSummary(o.stamp, where)
	} else {
		newest(10)
	}
	fmt.Printf("\ncatalogue: %s\n", rpath("progress.csv"))
	if report != "" {
		fmt.Printf("report   : %s\n", report)
	}
	return rc
}

// writeReport: reports/runreport_<stamp>.html for this run; a failed report
// never fails the run.
func writeReport(label string, started time.Time, rc int, topic string) string {
	path, err := writeRunReport(label, runStamp(started), started, logDir(started), commandLine(), topic, rc)
	if err != nil {
		say("run report not written: %v", err)
		return ""
	}
	return path
}

// dryrunSummary: what the dry run found, and how to pick and download from it.
func dryrunSummary(stamp, where string) {
	path := rpath("reports", "dryrun_"+stamp+".csv")
	rows, _ := readCSV(path)
	if len(rows) == 0 {
		fmt.Println("\ndry run: no table written (see the log of the dryrun step)")
		return
	}
	var papers []Row
	pend, summ := map[string]int{}, map[string]int{}
	for _, r := range rows {
		if paperTypeSet[r["resource_type"]] {
			papers = append(papers, r)
		}
		for _, p := range strings.Split(r["pending"], ";") {
			if p = pyStrip(p); p != "" {
				pend[p]++
			}
		}
		summ[orDefault(r["summary_from"], "none")]++
	}
	exts := map[string]int{}
	nfiles := 0
	for _, list := range readAttIndex() {
		for _, a := range list {
			if a["file"] != "" {
				nfiles++
				exts[orDefault(a["ext"], "?")]++
			}
		}
	}
	byCount := func(m map[string]int, limit int) string {
		ks := sortedKeys(m)
		sort.SliceStable(ks, func(i, j int) bool { return m[ks[i]] > m[ks[j]] })
		if limit > 0 && len(ks) > limit {
			ks = ks[:limit]
		}
		parts := make([]string, len(ks))
		for i, k := range ks {
			parts[i] = fmt.Sprintf("%s: %d", k, m[k])
		}
		return strings.Join(parts, ", ")
	}
	fmt.Println("\ndry run: metadata only, nothing was downloaded")
	fmt.Printf("  rows        : %d (%s)\n", len(rows), counts(rows, "resource_type"))
	fmt.Printf("  full text   : %s   (of %d papers; -: not tried)\n", counts(papers, "fulltext_status"), len(papers))
	fmt.Printf("  pending     : %s\n", orDefault(byCount(pend, 0), "nothing"))
	fmt.Printf("  summaries   : %s\n", byCount(summ, 0))
	att := fmt.Sprintf("%d files listed", nfiles)
	if nfiles > 0 {
		att += " (" + byCount(exts, 8) + ")"
	}
	fmt.Printf("  attachments : %s\n", att)
	fmt.Printf("  table       : %s\n", path)
	fmt.Println("next: pick rows, then download only those")
	fmt.Printf("  sourcelens query%s --in \"%s\" --summary \"WORDS\" --out picked.csv\n", where, path)
	fmt.Printf("  sourcelens download%s %s\n", where, rpath("exports", "picked.csv"))
}

func cmdRetry(argv []string) int {
	spec := withProject(flagSpec{"status", "deferred,partial,error", kStr}, flagSpec{"types", strings.Join(ftFormats, ","), kStr},
		flagSpec{"sources", strings.Join(ftSources, ","), kStr}, flagSpec{"ext", "", kStr}, flagSpec{"workers", "12", kInt},
		flagSpec{"plan", "false", kBool}, flagSpec{"dry-run", "false", kBool})
	f, err := parseFlags(argv, spec, retryHelp)
	if _, ok := err.(errHelp); ok {
		return 0
	}
	if err != nil {
		return fail(err.Error())
	}
	formats, err := expand(f["types"], ftFormats, formatGroups, "format")
	if err != nil {
		return fail(err.Error())
	}
	fsrc, err := expand(f["sources"], ftSources, map[string][]string{"fulltext": ftSources}, "full-text source")
	if err != nil {
		return fail(err.Error())
	}
	p, err := resolveProject(f["topic"], f["dir"], false, "")
	if err != nil {
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
	fmt.Printf("sourcelens retry: %d records with status %s\n", n, f["status"])
	if f.bool("plan") || f.bool("dry-run") || n == 0 {
		return 0
	}
	o := opts{sources: fsrc, types: formats, paperTypes: paperTypes, parallel: 1, heartbeat: 120, ext: f["ext"], maxAttMB: 100}
	stages := [][]step{{newStep("fulltext", append([]string{"--only-status", f["status"], "--formats", strings.Join(formats, ","),
		"--sources", strings.Join(fsrc, ","), "--workers", strconv.Itoa(f.int("workers"))}, attachmentArgs(o, formats)...)...)},
		{newStep("catalogue-1")}, {newStep("summary")}}
	lock, err := acquirePipelineLock("sourcelens retry")
	if err != nil {
		return fail("another update is running; try again later")
	}
	started := time.Now()
	rc := runPlan(stages, o, "retry", started)
	lock.Close()
	if rc == 130 {
		return rc
	}
	if report := writeReport("retry", started, rc, p.topic); report != "" {
		fmt.Printf("report   : %s\n", report)
	}
	return rc
}

// splitPositionals: (arguments that are not flags or flag values, the rest).
func splitPositionals(argv []string, spec []flagSpec) ([]string, []string) {
	kinds := map[string]flagKind{}
	for _, s := range spec {
		kinds[s.name] = s.kind
	}
	var pos, rest []string
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			rest = append(rest, a)
			name, _, hasEq := strings.Cut(strings.TrimLeft(a, "-"), "=")
			if k, ok := kinds[name]; ok && !hasEq && k != kBool && i+1 < len(argv) {
				rest = append(rest, argv[i+1])
				i++
			}
		} else {
			pos = append(pos, a)
		}
	}
	return pos, rest
}

// findInput: a file named on the command line, as given, else in the
// catalogue, else in its exports/.
func findInput(name string) string {
	for _, p := range []string{name, rpath(name), rpath("exports", name)} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			if abs, err := filepath.Abs(p); err == nil {
				return abs
			}
			return p
		}
	}
	return ""
}

func cmdDownload(argv []string) int {
	spec := withProject(flagSpec{"types", strings.Join(ftFormats, ","), kStr}, flagSpec{"sources", strings.Join(ftSources, ","), kStr},
		flagSpec{"ext", "", kStr}, flagSpec{"max-attachment-mb", "100", kInt}, flagSpec{"workers", "12", kInt},
		flagSpec{"plan", "false", kBool}, flagSpec{"in", "", kStr})
	pos, rest := splitPositionals(argv, spec)
	f, err := parseFlags(rest, spec, downloadHelp)
	if _, ok := err.(errHelp); ok {
		return 0
	}
	if err != nil {
		return fail(err.Error())
	}
	name := f["in"]
	if name == "" && len(pos) > 0 {
		name = pos[0]
	}
	if len(pos) > 1 || (len(pos) > 0 && f["in"] != "") {
		return fail("download takes one file")
	}
	if name == "" {
		return fail("name the CSV of rows to download (see: sourcelens download --help)")
	}
	formats, err := expand(f["types"], ftFormats, formatGroups, "format")
	if err != nil {
		return fail(err.Error())
	}
	fsrc, err := expand(f["sources"], ftSources, map[string][]string{"fulltext": ftSources}, "full-text source")
	if err != nil {
		return fail(err.Error())
	}
	p, err := resolveProject(f["topic"], f["dir"], false, "")
	if err != nil {
		return fail(err.Error())
	}
	path := findInput(name)
	if path == "" {
		return fail("cannot find " + name)
	}
	uids := map[string]bool{}
	for _, u := range readUIDsFile(path) {
		uids[u] = true
	}
	all, _ := readCSV(rpath("progress.csv"))
	var papers []Row
	for _, r := range all {
		if uids[r["uid"]] && paperTypeSet[r["resource_type"]] {
			papers = append(papers, r)
		}
	}
	fmt.Printf("sourcelens download: %d rows in %s; %d papers in the catalogue\n", len(uids), path, len(papers))
	if f.bool("plan") || len(papers) == 0 {
		return 0
	}
	started := time.Now()
	logdir := logDir(started)
	if err := os.MkdirAll(logdir, 0o755); err != nil {
		return fail(err.Error())
	}
	var sb strings.Builder
	for _, r := range papers {
		sb.WriteString(r["uid"] + "\n")
	}
	picked := filepath.Join(logdir, "selection.txt")
	if err := os.WriteFile(picked, []byte(sb.String()), 0o644); err != nil {
		return fail(err.Error())
	}
	o := opts{sources: fsrc, types: formats, paperTypes: paperTypes, parallel: 1, heartbeat: 120, ext: f["ext"],
		maxAttMB: f.int("max-attachment-mb")}
	stages := [][]step{{newStep("fulltext", append([]string{"--uids-file", picked, "--formats", strings.Join(formats, ","),
		"--sources", strings.Join(fsrc, ","), "--workers", strconv.Itoa(f.int("workers"))}, attachmentArgs(o, formats)...)...)},
		{newStep("catalogue-1")}, {newStep("links")}, {newStep("summary")}}
	lock, err := acquirePipelineLock("sourcelens download")
	if err != nil {
		return fail("another update is running; try again later")
	}
	rc := runPlan(stages, o, "download", started)
	lock.Close()
	if rc == 130 {
		return rc
	}
	if report := writeReport("download", started, rc, p.topic); report != "" {
		fmt.Printf("report   : %s\n", report)
	}
	return rc
}

func cmdReport(argv []string) int {
	f, err := parseFlags(argv, projectSpec, reportHelp)
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
	started := time.Now()
	path, err := writeRunReport("report", runStamp(started), started, "", commandLine(), p.topic, 0)
	if err != nil {
		return fail(err.Error())
	}
	fmt.Printf("report: %s\n", path)
	return 0
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
		fmt.Printf("no progress.csv yet; run: sourcelens update%s\n", topicFlag(f["topic"]))
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
		fmt.Printf("  papers not yet tried for full text: %d   (try deferred/partial again: sourcelens retry)\n", waiting)
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
	fmt.Println("  groups: fulltext = pdf,md,txt,xml; files = pdf,md,txt,xml,attachments; repos/package = repo; " +
		"websites = website")
	fmt.Println("  attachments: figures and supplementary files; --ext picks extensions (xlsx,csv,pptx,...)")
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
	rc := 0
	if err := steps[args[0]](args[1:], log); err != nil {
		log.Printf("ERROR: %v", err)
		rc = 1
	}
	// a server that asked for a long wait: the step is incomplete, the next run tries again
	notes := rateLimitNotes()
	for _, n := range notes {
		log.Printf("%s", n)
	}
	if rc == 0 && len(notes) > 0 {
		rc = 1
	}
	return rc
}

func hasHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "-help" {
			return true
		}
	}
	return false
}

var commands = []string{"update", "download", "retry", "query", "files", "export", "report", "status", "list", "config",
	"sources", "test", "version", "help"}

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
	case "download":
		return cmdDownload(args)
	case "report":
		return cmdReport(args)
	case "files":
		if hasHelp(args) {
			fmt.Print(filesHelp)
			return 0
		}
		return cmdFiles(args)
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
		fmt.Printf("sourcelens %s (go)\n", version)
		return 0
	}
	// anything else that is not an option is a topic: sourcelens "CRISPR base editing" [options]
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
	fmt.Fprintf(os.Stderr, "sourcelens: unknown option \"%s\"\n\n", cmd)
	fmt.Printf(mainHelp, version)
	return 2
}

func main() {
	initPaths()
	os.Exit(run(os.Args[1:]))
}
