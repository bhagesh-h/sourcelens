package main

// Shared helpers: paths, logging, dates, identifiers, CSV / JSONL files,
// file locks. Python twin: src/sourcelens/common/agelit.py.

import (
	"bufio"
	"compress/gzip"
	"crypto/sha1"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// paths
// ---------------------------------------------------------------------------

// Research is the catalogue folder of the running command or step. The
// command resolves it (see resolveProject) and passes it to its steps as
// SOURCELENS_PROJECT.
var Research string

func initPaths() {
	Research = os.Getenv("SOURCELENS_PROJECT")
	if Research == "" {
		Research, _ = os.Getwd()
	}
}

func rpath(parts ...string) string {
	return filepath.Join(append([]string{Research}, parts...)...)
}

// researchRel: path inside the output folder as stored in the catalogue.
func researchRel(p string) string {
	r, err := filepath.Rel(Research, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(r)
}

// researchFile: catalogue path -> file (accepts the older research/... form).
func researchFile(rel string) string {
	rel = strings.TrimPrefix(rel, "research/")
	return filepath.Join(Research, filepath.FromSlash(rel))
}

// ---------------------------------------------------------------------------
// logging: every step writes "[HH:MM:SS] message" lines to its own writer
// ---------------------------------------------------------------------------

type Logger struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *Logger) Printf(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, "[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, a...))
}

func today() string { return time.Now().Format("2006-01-02") }

// mustRead: a file's bytes, or nil.
func mustRead(path string) []byte { b, _ := os.ReadFile(path); return b }

// ---------------------------------------------------------------------------
// YAML
// ---------------------------------------------------------------------------

func loadYAML(path string, out any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(b, out)
}

// queryWindow: the catalogue window (search section of the configuration).
func queryWindow() (string, string) {
	var q struct {
		Start string `yaml:"start_date"`
		End   string `yaml:"end_date"`
	}
	_ = loadConfig("search", &q)
	if q.Start == "" {
		q.Start = "2011-01-01"
	}
	if q.End == "" || q.End == "today" {
		q.End = today()
	}
	return q.Start, q.End
}

// searchWindow: narrowed by SOURCELENS_SEARCH_START / SOURCELENS_SEARCH_END (set by update).
func searchWindow() (string, string) {
	s, e := queryWindow()
	if v := os.Getenv("SOURCELENS_SEARCH_START"); v != "" {
		s = v
	}
	if v := os.Getenv("SOURCELENS_SEARCH_END"); v != "" {
		e = v
	}
	return s, e
}

func searchWindowOverridden() bool {
	return os.Getenv("SOURCELENS_SEARCH_START") != "" || os.Getenv("SOURCELENS_SEARCH_END") != ""
}

// ---------------------------------------------------------------------------
// dates: 1d 7d 2w 1m 6m 1y 10y | 2024 | 2024-03 | 2024-03-15 | today
// ---------------------------------------------------------------------------

var relRe = regexp.MustCompile(`^([0-9]+)\s*([dwmy])$`)

func lastDay(y int, m time.Month) int { return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day() }

func parseWhen(spec string, end bool, ref time.Time) (string, error) {
	s := strings.ToLower(strings.TrimSpace(spec))
	if s == "" || s == "today" || s == "now" {
		return ref.Format("2006-01-02"), nil
	}
	if m := relRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		var d time.Time
		switch m[2] {
		case "d":
			d = ref.AddDate(0, 0, -n)
		case "w":
			d = ref.AddDate(0, 0, -7*n)
		default:
			months := n
			if m[2] == "y" {
				months = 12 * n
			}
			total := ref.Year()*12 + int(ref.Month()) - 1 - months
			y, mo := total/12, time.Month(total%12+1)
			day := ref.Day()
			if ld := lastDay(y, mo); day > ld {
				day = ld
			}
			d = time.Date(y, mo, day, 0, 0, 0, 0, time.UTC)
		}
		return d.Format("2006-01-02"), nil
	}
	if regexp.MustCompile(`^\d{4}$`).MatchString(s) {
		if end {
			return s + "-12-31", nil
		}
		return s + "-01-01", nil
	}
	bad := fmt.Errorf(`cannot read date "%s" (use 7d, 1m, 1y, 2024, 2024-03 or 2024-03-15)`, spec)
	if regexp.MustCompile(`^\d{4}-\d{2}$`).MatchString(s) {
		t, err := time.Parse("2006-01", s)
		if err != nil {
			return "", bad
		}
		if end {
			return fmt.Sprintf("%s-%02d", s, lastDay(t.Year(), t.Month())), nil
		}
		return s + "-01", nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil || !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(s) {
		return "", bad
	}
	return t.Format("2006-01-02"), nil
}

// ---------------------------------------------------------------------------
// identifiers (src/sourcelens/common/agelit.py)
// ---------------------------------------------------------------------------

var (
	doiRe      = regexp.MustCompile("(?i)\\b10\\.\\d{4,9}/[^\\s\"'`<>|\\]\\[{},;]+")
	doiPrefix  = regexp.MustCompile(`(?i)^(https?://)?(dx\.)?doi\.org/`)
	doiLabel   = regexp.MustCompile(`(?i)^doi:\s*`)
	doiSuffix  = regexp.MustCompile(`(?i)/(suppl_file|suppl|full|abstract|pdf|epdf|html|figures|tables)(/.*)?$`)
	biorxivVer = regexp.MustCompile(`(?i)v\d+(\.full)?(\.pdf|\.txt|\.html)?$|\.full(\.pdf)?$`)
	nonDigit   = regexp.MustCompile(`\D`)
	pmcRe      = regexp.MustCompile(`^PMC\d+$`)
	slugBad    = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	wsRe       = regexp.MustCompile(pyWS + `+`) // python \s is unicode-aware
	tagRe      = regexp.MustCompile(`<[^>]+>`)
)

func normDOI(s string) string {
	d := strings.TrimSpace(s)
	d = doiPrefix.ReplaceAllString(d, "")
	d = doiLabel.ReplaceAllString(d, "")
	m := doiRe.FindString(d)
	if m == "" {
		return ""
	}
	d = strings.TrimRight(strings.FieldsFunc(m, func(r rune) bool { return r == ',' || r == ';' })[0], ".:)")
	for strings.HasSuffix(d, ")") && strings.Count(d, "(") < strings.Count(d, ")") {
		d = d[:len(d)-1]
	}
	if i := strings.Index(d, "/"); i > 0 {
		pre := d[:i]
		if strings.HasPrefix(d[i+1:], pre+"/") {
			d = d[i+1:]
		}
	}
	d = doiSuffix.ReplaceAllString(d, "")
	if strings.HasPrefix(strings.ToLower(d), "10.1101/") {
		d = biorxivVer.ReplaceAllString(d, "")
	}
	return strings.ToLower(d)
}

func normPMID(s string) string {
	d := nonDigit.ReplaceAllString(s, "")
	if d == "" || d == "0" {
		return ""
	}
	return d
}

func normPMCID(s string) string {
	t := strings.ToUpper(strings.TrimSpace(s))
	if t == "" {
		return ""
	}
	if !strings.HasPrefix(t, "PMC") {
		t = "PMC" + nonDigit.ReplaceAllString(t, "")
	}
	if pmcRe.MatchString(t) {
		return t
	}
	return ""
}

func makeUID(doi, pmid, pmcid, epmc, url string) string {
	if d := normDOI(doi); d != "" {
		return "doi:" + d
	}
	if p := normPMID(pmid); p != "" {
		return "pmid:" + p
	}
	if p := normPMCID(pmcid); p != "" {
		return "pmcid:" + p
	}
	if epmc != "" {
		return "epmc:" + strings.ToUpper(epmc)
	}
	if url != "" {
		u := regexp.MustCompile(`(?i)^https?://(www\.)?`).ReplaceAllString(strings.TrimRight(strings.TrimSpace(url), "/"), "")
		return "url:" + strings.ToLower(u)
	}
	return ""
}

func slug(uid string, maxlen int) string {
	parts := strings.SplitN(uid, ":", 2)
	s := strings.Trim(slugBad.ReplaceAllString(parts[len(parts)-1], "_"), "_")
	if len(s) > maxlen {
		h := sha1.Sum([]byte(uid))
		s = s[:maxlen-9] + "_" + hex.EncodeToString(h[:])[:8]
	}
	return s
}

func cleanText(s string) string {
	return pyStrip(wsRe.ReplaceAllString(tagRe.ReplaceAllString(s, ""), " "))
}

// ---------------------------------------------------------------------------
// CSV, written like Python's csv module (CRLF, minimal quoting)
// ---------------------------------------------------------------------------

type Row = map[string]string

func readCSV(path string) ([]Row, []string) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	r := csv.NewReader(bufio.NewReaderSize(f, 1<<20))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	head, err := r.Read()
	if err != nil {
		return nil, nil
	}
	if len(head) > 0 {
		head[0] = strings.TrimPrefix(head[0], "\ufeff")
	}
	var rows []Row
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		m := make(Row, len(head))
		for i, h := range head {
			if i < len(rec) {
				m[h] = rec[i]
			}
		}
		rows = append(rows, m)
	}
	return rows, head
}

// csvField quotes like python's csv module (excel dialect, QUOTE_MINIMAL);
// encoding/csv also quotes leading spaces and rewrites newlines, which python does not.
func csvField(s string) string {
	if strings.ContainsAny(s, ",\"\r\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

func csvLine(w *bufio.Writer, fields []string) {
	if len(fields) == 1 && fields[0] == "" {
		w.WriteString("\"\"\r\n")
		return
	}
	for i, f := range fields {
		if i > 0 {
			w.WriteByte(',')
		}
		w.WriteString(csvField(f))
	}
	w.WriteString("\r\n")
}

func writeCSV(path string, rows []Row, cols []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	csvLine(w, cols)
	rec := make([]string, len(cols))
	for _, r := range rows {
		for i, c := range cols {
			rec[i] = r[c]
		}
		csvLine(w, rec)
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---------------------------------------------------------------------------
// JSON / JSONL(.gz)
// ---------------------------------------------------------------------------

type Rec = map[string]any

func openRead(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(path, ".gz") {
		g, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		return struct {
			io.Reader
			io.Closer
		}{g, f}, nil
	}
	return f, nil
}

func readJSONL(path string) []Rec {
	rc, err := openRead(path)
	if err != nil {
		return nil
	}
	defer rc.Close()
	var out []Rec
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		d := json.NewDecoder(strings.NewReader(line))
		d.UseNumber()
		var m Rec
		if d.Decode(&m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func writeJSONL(path string, recs []Rec) error {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	tmp := strings.TrimSuffix(path, ".gz") + ".tmp"
	if strings.HasSuffix(path, ".gz") {
		tmp += ".gz"
	}
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	var w io.Writer = f
	var gz *gzip.Writer
	if strings.HasSuffix(path, ".gz") {
		gz = gzip.NewWriter(f)
		w = gz
	}
	bw := bufio.NewWriterSize(w, 1<<20)
	for _, r := range recs {
		b, err := jsonMarshal(r)
		if err != nil {
			continue
		}
		bw.Write(b)
		bw.WriteByte('\n')
	}
	bw.Flush()
	if gz != nil {
		gz.Close()
	}
	f.Close()
	return os.Rename(tmp, path)
}

// jsonMarshal: compact JSON without HTML escaping (like ensure_ascii=False).
func jsonMarshal(v any) ([]byte, error) {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(pyUnescape(strings.TrimRight(sb.String(), "\n"))), nil
}

// pyUnescape: python's json.dumps(ensure_ascii=False) writes U+2028 / U+2029 as they are.
func pyUnescape(s string) string {
	if !strings.Contains(s, `\u202`) {
		return s
	}
	return strings.NewReplacer(`\u2028`, "\u2028", `\u2029`, "\u2029").Replace(s)
}

func writeJSON(path string, v any) error {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(pyUnescape(strings.TrimRight(sb.String(), "\n"))), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// field accessors for decoded JSON records
func str(r Rec, k string) string {
	switch v := r[k].(type) {
	case nil:
		return ""
	case string:
		return v
	case json.Number:
		return v.String()
	case bool:
		if v {
			return "True"
		}
		return "False"
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

func num(r Rec, k string) int {
	switch v := r[k].(type) {
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	case float64:
		return int(v)
	case int:
		return v
	case string:
		i, _ := strconv.Atoi(v)
		return i
	}
	return 0
}

func boolean(r Rec, k string) bool {
	switch v := r[k].(type) {
	case bool:
		return v
	case string:
		return v == "True" || v == "true"
	}
	return false
}

func strList(r Rec, k string) []string {
	var out []string
	switch v := r[k].(type) {
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	case []string:
		out = append(out, v...)
	}
	return out
}

// ---------------------------------------------------------------------------
// locks
// ---------------------------------------------------------------------------

// withLock runs fn while holding an exclusive lock on path (lock_unix.go, lock_windows.go).
func withLock(path string, fn func() error) error {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := lockFile(f, true); err != nil {
		return err
	}
	defer unlockFile(f)
	return fn()
}

// acquirePipelineLock: the run lock shared with the Python implementation and update_all.sh.
func acquirePipelineLock(label string) (*os.File, error) {
	path := rpath(".pipeline.lock")
	_ = os.MkdirAll(Research, 0o755)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f, false); err != nil {
		held, _ := os.ReadFile(path)
		f.Close()
		return nil, fmt.Errorf("another update is running (%s); try again later", strings.TrimSpace(string(held)))
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("pid %d since %s (%s)\n", os.Getpid(),
		time.Now().Format("2006-01-02T15:04:05"), label)), 0)
	return f, nil
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func splitList(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// runeCut: first n characters (Python s[:n]).
func runeCut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
