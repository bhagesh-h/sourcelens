package main

// Full texts ("fulltext" step). Python twin: src/sourcelens/pullliturature/fetch_fulltext.py:
// PMC open-access bucket -> bioRxiv / medRxiv -> Europe PMC XML -> arXiv ->
// Unpaywall -> OpenAlex; formats pdf / md / txt / xml and the paper's
// attachments; metadata.json per record that has a file; an index row with
// the status and the reason for every record tried.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"net/url"
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

const (
	s3Base = "https://pmc-oa-opendata.s3.amazonaws.com"
	epmcFT = "https://www.ebi.ac.uk/europepmc/webservices/rest"
	maxPDF = 150_000_000
)

var (
	formatFiles   = map[string]string{"pdf": "paper.pdf", "md": "paper.md", "txt": "paper.txt", "xml": "paper.jats.xml"}
	allFormats    = []string{"md", "pdf", "txt", "xml"}
	allFTSources  = []string{"arxiv", "biorxiv", "europepmc", "openalex", "pmc", "unpaywall"}
	articleTypes  = []string{"article", "book chapter", "conference paper", "preprint", "report", "review", "thesis"}
	indexCols     = []string{"uid", "status", "reason", "source", "license", "has_pdf", "has_md", "has_txt", "has_xml", "attachments", "folder", "pdf_url", "checked_on", "note"}
	preprintHosts = map[string]bool{"www.biorxiv.org": true, "www.medrxiv.org": true}
	hostInterval  = map[string]time.Duration{"pmc-oa-opendata.s3.amazonaws.com": 20 * time.Millisecond,
		"api.unpaywall.org": 100 * time.Millisecond, "api.biorxiv.org": 500 * time.Millisecond,
		"www.ebi.ac.uk": 250 * time.Millisecond, "www.biorxiv.org": 6 * time.Second,
		"www.medrxiv.org": 6 * time.Second, "arxiv.org": 3 * time.Second}
	s3KeyRe      = regexp.MustCompile(`<Key>([^<]+)</Key>`)
	s3ContentsRe = regexp.MustCompile(`(?s)<Contents>(.*?)</Contents>`)
	s3SizeRe     = regexp.MustCompile(`<Size>(\d+)</Size>`)
)

func browserUA() string {
	if contact() == "" {
		return "Mozilla/5.0 (X11; Linux x86_64) sourcelens/" + version
	}
	return "Mozilla/5.0 (X11; Linux x86_64) sourcelens/" + version + " (mailto:" + contact() + ")"
}

// complete: requested text formats present (default md+txt), else the requested ones.
func complete(present map[string]bool, formats []string) bool {
	var core []string
	for _, f := range formats {
		if f == "md" || f == "txt" {
			core = append(core, f)
		}
	}
	if len(core) == 0 {
		core = formats
	}
	for _, f := range core {
		if !present[f] {
			return false
		}
	}
	return true
}

type fetcher struct {
	lim       *Limiter
	mu        sync.Mutex
	blocked   map[string]time.Time
	log       *Logger
	clientsMu sync.Mutex
}

func (f *fetcher) client() *Client {
	return NewClient(0, map[string]string{"User-Agent": browserUA()}, f.log).WithLimiter(f.lim)
}

type ftCtx struct {
	f        *fetcher
	c        *Client
	deferred bool
}

func (x *ftCtx) getBytes(u string, wantPDF bool) []byte {
	pu, err := url.Parse(u)
	if err != nil {
		return nil
	}
	host := strings.ToLower(pu.Host)
	var r *Resp
	switch {
	case preprintHosts[host]:
		x.f.mu.Lock()
		until := x.f.blocked[host]
		x.f.mu.Unlock()
		if time.Now().Before(until) {
			x.deferred = true
			return nil
		}
		r = x.c.Get(u, reqOpts{tries: 1, raw: true, timeout: 180 * time.Second})
		if r != nil && r.Status == 429 {
			wait := 300 * time.Second
			if s, err := strconv.Atoi(r.Header.Get("Retry-After")); err == nil {
				wait = time.Duration(s) * time.Second
			}
			x.f.mu.Lock()
			x.f.blocked[host] = time.Now().Add(wait)
			x.f.mu.Unlock()
			x.deferred = true
			x.f.log.Printf("%s rate-limited; deferring its downloads for %.0fs", host, wait.Seconds())
			return nil
		}
	case host == "pmc-oa-opendata.s3.amazonaws.com":
		r = x.c.Get(u, reqOpts{tries: 3, timeout: 180 * time.Second})
	default:
		r = x.c.Get(u, reqOpts{tries: 2, timeout: 60 * time.Second})
	}
	if r == nil || r.Status != 200 {
		return nil
	}
	b := r.Body
	if wantPDF && (!bytes.HasPrefix(b, []byte("%PDF")) || len(b) > maxPDF) {
		return nil
	}
	return b
}

type mediaFile struct {
	name string
	size int
}

type pmcList struct {
	version int
	names   []string // listing order
	sizes   map[string]int
}

// pmcListing: the latest version of an article in the PMC bucket with its
// files and sizes; nil when it is not in the open-access subset; failed when
// the bucket could not be asked.
func (x *ftCtx) pmcListing(pmcid string) (lst *pmcList, failed bool) {
	r := x.c.Get(s3Base+"/", reqOpts{params: url.Values{"list-type": {"2"}, "prefix": {pmcid + "."}, "max-keys": {"1000"}}})
	if r == nil || r.Status != 200 {
		return nil, true
	}
	pat := regexp.MustCompile(`^` + regexp.QuoteMeta(pmcid) + `\.(\d+)/(.+)$`)
	versions := map[int]*pmcList{}
	for _, c := range s3ContentsRe.FindAllStringSubmatch(r.Text(), -1) {
		k := s3KeyRe.FindStringSubmatch(c[1])
		if k == nil {
			continue
		}
		m := pat.FindStringSubmatch(xmlUnescape(k[1]))
		if m == nil {
			continue
		}
		v, _ := strconv.Atoi(m[1])
		l := versions[v]
		if l == nil {
			l = &pmcList{version: v, sizes: map[string]int{}}
			versions[v] = l
		}
		size := -1
		if sz := s3SizeRe.FindStringSubmatch(c[1]); sz != nil {
			size, _ = strconv.Atoi(sz[1])
		}
		if _, seen := l.sizes[m[2]]; !seen {
			l.names = append(l.names, m[2])
		}
		l.sizes[m[2]] = size
	}
	best := -1
	for v, l := range versions {
		if _, ok := l.sizes[fmt.Sprintf("%s.%d.json", pmcid, v)]; ok && v > best {
			best = v
		}
	}
	if best < 0 {
		return nil, false
	}
	return versions[best], false
}

// pmcMedia: the article's attachments, every file but its own XML / text / PDF / JSON.
func pmcMedia(pmcid string, l *pmcList) []mediaFile {
	own := fmt.Sprintf("%s.%d.", pmcid, l.version)
	var out []mediaFile
	for _, n := range l.names {
		if !strings.HasPrefix(n, own) && !strings.Contains(n, "/") {
			out = append(out, mediaFile{n, l.sizes[n]})
		}
	}
	return out
}

// pmcS3: the latest version's JSON record with "_version" and "_media"; nil
// when not in the open-access subset; failed when the bucket could not be read.
func (x *ftCtx) pmcS3(pmcid string) (map[string]any, bool) {
	l, failed := x.pmcListing(pmcid)
	if l == nil {
		return nil, failed
	}
	j := x.c.Get(fmt.Sprintf("%s/%s.%d/%s.%d.json", s3Base, pmcid, l.version, pmcid, l.version), reqOpts{})
	if j == nil || j.Status != 200 {
		return nil, false
	}
	m := decodeJSON(j.Body)
	if m != nil {
		m["_version"] = l.version
		m["_media"] = pmcMedia(pmcid, l)
	}
	return m, false
}

func s3URL(v string) string {
	if v == "" {
		return ""
	}
	p := strings.SplitN(strings.TrimPrefix(v, "s3://pmc-oa-opendata/"), "?", 2)[0]
	return s3Base + "/" + p
}

func (x *ftCtx) biorxiv(doi string) map[string]any {
	for _, server := range []string{"biorxiv", "medrxiv"} {
		r := x.c.Get("https://api.biorxiv.org/details/"+server+"/"+doi, reqOpts{tries: 3})
		if r == nil || r.Status != 200 {
			continue
		}
		coll := jlist(decodeJSON(r.Body)["collection"])
		if len(coll) == 0 {
			continue
		}
		best, bv := map[string]any{}, -1
		for _, c := range coll {
			cm := ymap(c)
			if v := atoiSafe(jstr(cm, "version")); v > bv {
				best, bv = cm, v
			}
		}
		best["_server"] = server
		return best
	}
	return nil
}

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ---------------------------------------------------------------------------
// PDF -> text / Markdown with poppler (pdftotext, pdftohtml); optional: without
// them PDFs are kept but not converted
// ---------------------------------------------------------------------------

func pdfToolsStatus() string {
	var missing []string
	for _, t := range []string{"pdftotext", "pdftohtml"} {
		if _, err := exec.LookPath(t); err != nil {
			missing = append(missing, t)
		}
	}
	if len(missing) == 0 {
		return "poppler (pdftotext, pdftohtml)"
	}
	return "unavailable: install poppler-utils (" + strings.Join(missing, ", ") + " not found)"
}

func pdfToText(path string) string {
	out, err := exec.Command("pdftotext", "-enc", "UTF-8", path, "-").Output()
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		out, _ = exec.Command("pdftotext", "-layout", "-enc", "UTF-8", path, "-").Output()
	}
	return strings.ToValidUTF8(string(out), "")
}

type phText struct {
	Top    float64 `xml:"top,attr"`
	Height float64 `xml:"height,attr"`
	Font   string  `xml:"font,attr"`
	Inner  string  `xml:",innerxml"`
}

type phPage struct {
	Fonts []struct {
		ID   string  `xml:"id,attr"`
		Size float64 `xml:"size,attr"`
	} `xml:"fontspec"`
	Texts []phText `xml:"text"`
}

// pdfToMarkdown: headings from font size, bold from <b>, paragraphs from
// vertical gaps (poppler's pdftohtml -xml).
func pdfToMarkdown(path string) string {
	out, err := exec.Command("pdftohtml", "-xml", "-i", "-q", "-stdout", "-enc", "UTF-8", path).Output()
	if err != nil {
		return ""
	}
	d := xml.NewDecoder(bytes.NewReader(out))
	d.Strict = false
	d.Entity = xml.HTMLEntity
	var doc struct {
		Pages []phPage `xml:"page"`
	}
	if d.Decode(&doc) != nil {
		return ""
	}
	sizes := map[string]float64{}
	weight := map[float64]int{}
	for _, p := range doc.Pages {
		for _, f := range p.Fonts {
			sizes[f.ID] = f.Size
		}
		for _, t := range p.Texts {
			weight[sizes[t.Font]] += len(t.Inner)
		}
	}
	body, bw := 0.0, -1
	for s, w := range weight {
		if w > bw || (w == bw && s < body) {
			body, bw = s, w
		}
	}
	var lines []string
	var para []string
	flush := func() {
		if len(para) > 0 {
			txt := strings.Join(para, " ")
			txt = regexp.MustCompile(`(\w)- (\w)`).ReplaceAllString(txt, "$1$2")
			lines = append(lines, txt, "")
			para = nil
		}
	}
	bold := regexp.MustCompile(`(?s)</?b>`)
	for _, p := range doc.Pages {
		lastTop, lastH := -1.0, 0.0
		for _, t := range p.Texts {
			raw := t.Inner
			isBold := strings.Contains(raw, "<b>")
			txt := strings.TrimSpace(xmlUnescape(bold.ReplaceAllString(raw, "")))
			if txt == "" {
				continue
			}
			size := sizes[t.Font]
			switch {
			case body > 0 && size >= body*1.6:
				flush()
				lines = append(lines, "# "+txt, "")
			case body > 0 && size >= body*1.2:
				flush()
				lines = append(lines, "## "+txt, "")
			default:
				if lastTop >= 0 && (t.Top-lastTop > 1.8*max(lastH, 1) || t.Top < lastTop) {
					flush()
				}
				if isBold && len(para) == 0 && len([]rune(txt)) < 120 {
					txt = "**" + txt + "**"
				}
				para = append(para, txt)
			}
			lastTop, lastH = t.Top, t.Height
		}
		flush()
	}
	return pyStrip(nl3.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")) + "\n"
}

// ---------------------------------------------------------------------------
// one record
// ---------------------------------------------------------------------------

func folderFor(rec Rec, row Row) string {
	year := runeCut(orDefault(str(rec, "pub_date"), orDefault(row["date"], "0000")), 4)
	if year == "" {
		year = "0000"
	}
	return rpath("fulltext", year, slug(str(rec, "uid"), 120))
}

func indexRow(uid, folder string, ft map[string]any) Row {
	has := func(f string) string {
		if folder != "" && fileExists(filepath.Join(folder, formatFiles[f])) {
			return "True"
		}
		return "False"
	}
	files := ymap(ft["files"])
	pdfURL := jstr(ymap(files["paper.pdf"]), "url")
	checked := jstr(ft, "fetched_on")
	if checked == "" {
		checked = today()
	}
	rel := ""
	if folder != "" {
		rel = researchRel(folder)
	}
	return Row{"uid": uid, "status": jstr(ft, "status"), "reason": jstr(ft, "reason"), "source": jstr(ft, "source"),
		"license": jstr(ft, "license"), "has_pdf": has("pdf"), "has_md": has("md"), "has_txt": has("txt"),
		"has_xml": has("xml"), "attachments": "", "folder": rel, "pdf_url": pdfURL,
		"checked_on": runeCut(checked, 10), "note": ""}
}

// tidy removes a record folder that holds nothing but its metadata.json (an
// attempt that found no file).
func tidy(folder string) bool {
	ents, err := os.ReadDir(folder)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if e.Name() != "metadata.json" && e.Name() != ".paper.tmp.pdf" {
			return false
		}
	}
	for _, e := range ents {
		_ = os.Remove(filepath.Join(folder, e.Name()))
	}
	return os.Remove(folder) == nil
}

func fromDisk(rec Rec, row Row, formats []string) Row {
	folder := folderFor(rec, row)
	b, err := os.ReadFile(filepath.Join(folder, "metadata.json"))
	if err != nil {
		return nil
	}
	m := decodeJSON(b)
	ft := jmap(m, "fulltext")
	files := ymap(ft["files"])
	present := map[string]bool{}
	for f, name := range formatFiles {
		if fileExists(filepath.Join(folder, name)) {
			present[f] = true
		}
	}
	for name := range files {
		if !fileExists(filepath.Join(folder, name)) {
			return nil
		}
	}
	if jstr(ft, "status") != "ok" || !complete(present, formats) {
		return nil
	}
	return indexRow(str(rec, "uid"), folder, ft)
}

type attOpts struct {
	exts     map[string]bool // nil: every extension
	maxBytes int             // 0: no limit
	prev     []Row
}

// fetchAttachments downloads the attachments of one record; one row per file
// (or one "none" row). Files on disk are kept; files moved or deleted with
// `sourcelens files` are not downloaded again.
func (x *ftCtx) fetchAttachments(folder, uid, pmcid string, version int, media []mediaFile, xmlBytes []byte, att *attOpts) []Row {
	caps := attCaptions(xmlBytes)
	prev := map[string]Row{}
	for _, r := range att.prev {
		if r["file"] != "" {
			prev[r["file"]] = r
		}
	}
	adir := filepath.Join(folder, "attachments")
	var rows []Row
	for _, mf := range media {
		fname := safeName(mf.name)
		ext := fileExt(fname)
		d := describe(mf.name, caps)
		u := fmt.Sprintf("%s/%s.%d/%s", s3Base, pmcid, version, urlQuote(mf.name))
		size := ""
		if mf.size >= 0 {
			size = strconv.Itoa(mf.size)
		}
		row := Row{"uid": uid, "file": fname, "ext": ext, "kind": d.kind, "label": d.label, "caption": d.caption,
			"bytes": size, "status": "listed", "path": "", "url": u, "checked_on": today(), "reason": ""}
		old, hasOld := prev[fname]
		target := filepath.Join(adir, fname)
		st, statErr := os.Stat(target)
		switch {
		case hasOld && (old["status"] == "moved" || old["status"] == "deleted"):
			row["status"], row["path"], row["reason"] = old["status"], old["path"], old["reason"]
		case statErr == nil && (mf.size < 0 || st.Size() == int64(mf.size)):
			row["status"], row["path"], row["bytes"] = "ok", researchRel(target), strconv.FormatInt(st.Size(), 10)
		case att.exts != nil && !att.exts[ext]:
			row["reason"] = "extension not selected"
		case att.maxBytes > 0 && mf.size > att.maxBytes:
			row["status"], row["reason"] = "skipped", fmt.Sprintf("larger than %d MB", att.maxBytes/1_000_000)
		default:
			b := x.getBytes(u, false)
			if b == nil {
				row["status"], row["reason"] = "failed", "download failed"
			} else {
				_ = os.MkdirAll(adir, 0o755)
				_ = os.WriteFile(target, b, 0o644)
				row["status"], row["path"], row["bytes"] = "ok", researchRel(target), strconv.Itoa(len(b))
			}
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		rows = append(rows, noneRow(uid, "no attachments"))
	}
	return rows
}

// attachmentsOnly fetches the attachments of a record whose text formats are
// complete on disk: index columns attachments and folder, the rows, and ok
// false when the PMC bucket could not be read.
func (f *fetcher) attachmentsOnly(rec Rec, row Row, att *attOpts) (Row, []Row, bool) {
	x := &ftCtx{f: f, c: f.client()}
	uid, pmcid := str(rec, "uid"), str(rec, "pmcid")
	folder := folderFor(rec, row)
	var m map[string]any
	failed := false
	if pmcid != "" {
		m, failed = x.pmcS3(pmcid)
	}
	if failed {
		return Row{"attachments": "", "folder": ""}, nil, false
	}
	var rows []Row
	if m == nil {
		reason := "no PMC id"
		if pmcid != "" {
			reason = "not in the PMC open-access subset"
		}
		rows = []Row{noneRow(uid, reason)}
	} else {
		xmlBytes, _ := os.ReadFile(filepath.Join(folder, "paper.jats.xml"))
		media, _ := m["_media"].([]mediaFile)
		rows = x.fetchAttachments(folder, uid, pmcid, m["_version"].(int), media, xmlBytes, att)
	}
	metaPath := filepath.Join(folder, "metadata.json")
	if b, err := os.ReadFile(metaPath); err == nil {
		meta := decodeJSON(b)
		if meta == nil {
			meta = map[string]any{}
		}
		ft := jmap(meta, "fulltext")
		if ft == nil {
			ft = map[string]any{}
		}
		ft["attachments"] = attMeta(rows)
		meta["fulltext"] = ft
		_ = writeJSON(metaPath, meta)
	} else {
		for _, r := range rows {
			if r["status"] == "ok" {
				_ = writeJSON(metaPath, omap{{"uid", uid}, {"record", rec}, {"fulltext", omap{{"attachments", attMeta(rows)}}}})
				break
			}
		}
	}
	rel := ""
	if st, err := os.Stat(folder); err == nil && st.IsDir() {
		rel = researchRel(folder)
	}
	return Row{"attachments": attColumn(rows), "folder": rel}, rows, true
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// fetchOne downloads one record: its fulltext_index.csv row and, when att is
// not nil, its attachment rows (attDone false: attachments not decided now).
func (f *fetcher) fetchOne(rec Rec, row Row, wantPDF bool, formats, sources []string, att *attOpts) (Row, []Row, bool) {
	x := &ftCtx{f: f, c: f.client()}
	uid := str(rec, "uid")
	folder := folderFor(rec, row)
	files := map[string]any{}
	tried := []any{}
	prov := map[string]any{}
	var notes []string
	lic, source := "", ""
	var xmlBytes []byte
	var textFormats []string
	for _, fm := range formats {
		if _, ok := formatFiles[fm]; ok {
			textFormats = append(textFormats, fm)
		}
	}
	keep := map[string]bool{}
	for _, fm := range textFormats {
		keep[formatFiles[fm]] = true
	}
	src := map[string]bool{}
	for _, s := range sources {
		src[s] = true
	}
	wantPDF = wantPDF && (keep["paper.pdf"] || keep["paper.md"] || keep["paper.txt"])
	pdfTmp := filepath.Join(folder, ".paper.tmp.pdf")
	tmpPDF := false
	save := func(name string, data []byte, u, via string) {
		if name == "paper.pdf" && !keep[name] {
			_ = os.MkdirAll(folder, 0o755)
			_ = os.WriteFile(pdfTmp, data, 0o644)
			tmpPDF = true
			return
		}
		if !keep[name] {
			return
		}
		_ = os.MkdirAll(folder, 0o755)
		_ = os.WriteFile(filepath.Join(folder, name), data, 0o644)
		files[name] = map[string]any{"bytes": len(data), "sha256": sha(data), "url": u, "via": via}
	}
	havePDF := func() bool { _, ok := files["paper.pdf"]; return ok || tmpPDF }

	pmcid := str(rec, "pmcid")
	s3err := false
	var pmc map[string]any
	if pmcid != "" && src["pmc"] {
		tried = append(tried, "pmc_s3")
		m, failed := x.pmcS3(pmcid)
		s3err = failed
		switch {
		case failed:
			notes = append(notes, "PMC bucket could not be read")
		case m == nil:
			notes = append(notes, "not in the PMC open-access subset")
		default:
			pmc = m
		}
		if pmc != nil {
			p := map[string]any{}
			for _, k := range []string{"pmcid", "_version", "license_code", "is_pmc_openaccess", "is_manuscript", "is_retracted", "citation"} {
				p[k] = m[k]
			}
			prov["pmc_s3"] = p
			lic = orDefault(jstr(m, "license_code"), lic)
			source = "pmc_s3"
			n0 := len(files)
			if u := s3URL(jstr(m, "xml_url")); u != "" {
				if b := x.getBytes(u, false); b != nil {
					xmlBytes = b
					save("paper.jats.xml", b, u, "pmc_s3")
				}
			}
			if u := s3URL(jstr(m, "text_url")); u != "" {
				if b := x.getBytes(u, false); b != nil {
					save("paper.txt", b, u, "pmc_s3")
				}
			}
			if u := s3URL(jstr(m, "pdf_url")); wantPDF && u != "" {
				if b := x.getBytes(u, true); b != nil {
					save("paper.pdf", b, u, "pmc_s3")
				}
			}
			if len(files) == n0 && xmlBytes == nil {
				notes = append(notes, "PMC files did not download")
			}
		}
	} else if src["pmc"] {
		notes = append(notes, "no PMC copy")
	}
	doi := str(rec, "doi")
	if src["biorxiv"] && strings.HasPrefix(doi, "10.1101/") && (xmlBytes == nil || (wantPDF && !havePDF())) {
		tried = append(tried, "biorxiv")
		if v := x.biorxiv(doi); v == nil {
			notes = append(notes, "not found in the bioRxiv / medRxiv API")
		} else {
			server := jstr(v, "_server")
			prov["biorxiv"] = map[string]any{"server": v["server"], "version": v["version"], "date": v["date"],
				"license": v["license"], "published": v["published"]}
			lic = orDefault(lic, jstr(v, "license"))
			got := false
			if j := jstr(v, "jatsxml"); xmlBytes == nil && j != "" {
				if b := x.getBytes(j, false); b != nil && bytes.Contains(b[:min(5000, len(b))], []byte("<article")) {
					xmlBytes = b
					got = true
					source = orDefault(source, server)
					save("paper.jats.xml", b, j, server)
				}
			}
			pdf := fmt.Sprintf("https://www.%s.org/content/%sv%s.full.pdf", server, doi, orDefault(jstr(v, "version"), "1"))
			if wantPDF && !havePDF() {
				if b := x.getBytes(pdf, true); b != nil {
					save("paper.pdf", b, pdf, server)
					got = true
					source = orDefault(source, server)
				}
			}
			if !got && !x.deferred {
				notes = append(notes, server+" files did not download")
			}
		}
	}
	if xmlBytes == nil && src["europepmc"] {
		ppr := ""
		for _, e := range strList(rec, "epmc_ids") {
			if strings.HasPrefix(e, "PPR/") {
				ppr = strings.SplitN(e, "/", 2)[1]
				break
			}
		}
		ext := ppr
		if ext == "" && pmcid != "" && (s3err || !src["pmc"]) {
			ext = pmcid
		}
		if ext != "" {
			tried = append(tried, "europepmc_xml")
			r := x.c.Get(epmcFT+"/"+ext+"/fullTextXML", reqOpts{tries: 2})
			if r != nil && r.Status == 200 && bytes.Contains(r.Body, []byte("<body")) {
				xmlBytes = r.Body
				source = orDefault(source, "europepmc")
				save("paper.jats.xml", r.Body, epmcFT+"/"+ext+"/fullTextXML", "europepmc")
			} else {
				notes = append(notes, "no full text in Europe PMC")
			}
		}
	}
	if src["arxiv"] && wantPDF && !havePDF() && strings.HasPrefix(doi, "10.48550/arxiv.") {
		tried = append(tried, "arxiv")
		u := "https://arxiv.org/pdf/" + strings.SplitN(doi, "arxiv.", 2)[1]
		if b := x.getBytes(u, true); b != nil {
			save("paper.pdf", b, u, "arxiv")
			lic = orDefault(lic, "arXiv (see abstract page for licence)")
			source = orDefault(source, "arxiv")
		} else {
			notes = append(notes, "arXiv PDF did not download")
		}
	}
	// Unpaywall needs a contact address (sourcelens config set contact_email ...)
	if src["unpaywall"] && wantPDF && !havePDF() && doi != "" && contact() == "" {
		notes = append(notes, "Unpaywall skipped: no contact email")
	}
	if src["unpaywall"] && contact() != "" && wantPDF && !havePDF() && doi != "" {
		tried = append(tried, "unpaywall")
		r := x.c.Get("https://api.unpaywall.org/v2/"+escapeDOIPath(doi), reqOpts{params: url.Values{"email": {contact()}}, tries: 3})
		if r == nil || r.Status != 200 {
			notes = append(notes, "Unpaywall: DOI unknown or no answer")
		} else {
			up := decodeJSON(r.Body)
			best := jmap(up, "best_oa_location")
			prov["unpaywall"] = map[string]any{"is_oa": up["is_oa"], "oa_status": up["oa_status"], "best_oa_url": best["url"]}
			locs := append([]any{best}, jlist(up["oa_locations"])...)
			seen := map[string]bool{}
			for _, l := range locs {
				lm := ymap(l)
				u := jstr(lm, "url_for_pdf")
				if u == "" || seen[u] {
					continue
				}
				seen[u] = true
				if b := x.getBytes(u, true); b != nil {
					save("paper.pdf", b, u, "unpaywall:"+jstr(lm, "host_type"))
					lic = orDefault(lic, jstr(lm, "license"))
					source = orDefault(source, "unpaywall")
					break
				}
			}
			if !havePDF() {
				isOA, _ := up["is_oa"].(bool)
				switch {
				case !isOA:
					notes = append(notes, "Unpaywall: not open access")
				case len(seen) == 0:
					notes = append(notes, "Unpaywall: open access as a web page only, no PDF link")
				default:
					plural := "s"
					if len(seen) == 1 {
						plural = ""
					}
					notes = append(notes, fmt.Sprintf("Unpaywall: %d PDF link%s failed (blocked or not a PDF)", len(seen), plural))
				}
			}
		}
	}
	// OpenAlex's open-access PDF link (works from any field; no contact address needed)
	if src["openalex"] && wantPDF && !havePDF() {
		if u := str(rec, "oa_pdf_url"); u != "" {
			tried = append(tried, "openalex")
			if b := x.getBytes(u, true); b != nil {
				save("paper.pdf", b, u, "openalex")
				lic = orDefault(lic, str(rec, "license"))
				source = orDefault(source, "openalex")
			} else {
				notes = append(notes, "OpenAlex open-access link failed (blocked or not a PDF)")
			}
		}
	}
	if xmlBytes != nil && (keep["paper.md"] || keep["paper.txt"]) {
		md, fm := jatsConvert(xmlBytes)
		if len(md) > 1500 {
			save("paper.md", []byte(md), "", "jats->md")
			if _, ok := files["paper.txt"]; !ok {
				save("paper.txt", []byte(mdToText(md)), "", "jats->txt")
			}
			lic = orDefault(lic, fm.License)
		}
	}
	_, hasMD := files["paper.md"]
	_, hasTXT := files["paper.txt"]
	if havePDF() && ((keep["paper.md"] && !hasMD) || (keep["paper.txt"] && !hasTXT)) {
		p := pdfTmp
		if _, ok := files["paper.pdf"]; ok {
			p = filepath.Join(folder, "paper.pdf")
		}
		if keep["paper.md"] && !hasMD {
			if md := pdfToMarkdown(p); strings.TrimSpace(md) != "" {
				save("paper.md", []byte(md), "", "pdf->md (poppler)")
			}
		}
		if keep["paper.txt"] && !hasTXT {
			if txt := pdfToText(p); strings.TrimSpace(txt) != "" {
				save("paper.txt", []byte(txt), "", "pdf->txt (poppler)")
			}
		}
	}
	_ = os.Remove(pdfTmp)

	var oldFT map[string]any
	if b, err := os.ReadFile(filepath.Join(folder, "metadata.json")); err == nil {
		oldFT = jmap(decodeJSON(b), "fulltext")
		for k, v := range ymap(oldFT["files"]) {
			if _, ok := files[k]; !ok && fileExists(filepath.Join(folder, k)) {
				files[k] = v
			}
		}
	}
	present := map[string]bool{}
	for fm, name := range formatFiles {
		if _, ok := files[name]; ok {
			present[fm] = true
		}
	}
	status := "none"
	if complete(present, textFormats) {
		status = "ok"
	} else if len(files) > 0 {
		status = "partial"
	}
	if status != "ok" && x.deferred {
		status = "deferred"
		notes = append(notes, "bioRxiv / medRxiv rate limit; tried again on the next run")
	}
	if status == "partial" {
		var core, missing []string
		for _, fm := range textFormats {
			if fm == "md" || fm == "txt" {
				core = append(core, fm)
			}
		}
		if len(core) == 0 {
			core = textFormats
		}
		for _, fm := range sortedCopy(core) {
			if !present[fm] {
				missing = append(missing, fm)
			}
		}
		notes = append([]string{"missing " + strings.Join(missing, ", ")}, notes...)
	}
	if status != "ok" && len(notes) == 0 {
		if len(tried) == 0 {
			notes = append(notes, "no PMC id, DOI or open-access link to try")
		} else {
			notes = append(notes, "no open-access copy found")
		}
	}
	reason := ""
	if status != "ok" {
		reason = strings.Join(dedupe(notes), "; ")
	}

	var attRows []Row
	attDone := false
	if att != nil {
		switch {
		case pmc != nil:
			xb := xmlBytes
			if xb == nil {
				xb, _ = os.ReadFile(filepath.Join(folder, "paper.jats.xml"))
			}
			media, _ := pmc["_media"].([]mediaFile)
			attRows, attDone = x.fetchAttachments(folder, uid, pmcid, pmc["_version"].(int), media, xb, att), true
		case !s3err:
			r := "no PMC id"
			if pmcid != "" {
				r = "not in the PMC open-access subset"
			}
			attRows, attDone = []Row{noneRow(uid, r)}, true
		}
	}
	hasFiles := len(files) > 0
	for _, r := range attRows {
		if r["status"] == "ok" {
			hasFiles = true
		}
	}
	if st, err := os.Stat(filepath.Join(folder, "attachments")); err == nil && st.IsDir() {
		hasFiles = true
	}
	fetched := time.Now().Format("2006-01-02T15:04:05")
	ft := map[string]any{"status": status, "reason": reason, "source": source, "license": lic, "files": files,
		"fetched_on": fetched, "sources_tried": tried, "formats": sortedCopy(formats), "sources": sortedCopy(sources)}
	// key order as python writes it (nested records keep Go's sorted key order)
	fto := omap{{"status", status}, {"reason", reason}, {"source", source}, {"license", lic}, {"files", files},
		{"fetched_on", fetched}, {"sources_tried", tried}, {"formats", ft["formats"]}, {"sources", ft["sources"]}}
	for _, k := range []string{"pmc_s3", "biorxiv", "unpaywall", "xml_error"} {
		if v, ok := prov[k]; ok {
			ft[k] = v
			fto = append(fto, okv{k, v})
		}
	}
	if attDone {
		fto = append(fto, okv{"attachments", attMeta(attRows)})
	} else if old, ok := oldFT["attachments"]; ok && len(jlist(old)) > 0 {
		fto = append(fto, okv{"attachments", old})
	}
	var out Row
	if hasFiles {
		catRow := omap{}
		for _, k := range []string{"tier", "category", "modality", "title", "date"} {
			catRow = append(catRow, okv{k, row[k]})
		}
		_ = writeJSON(filepath.Join(folder, "metadata.json"), omap{{"uid", uid}, {"record", rec}, {"catalogue_row", catRow}, {"fulltext", fto}})
		out = indexRow(uid, folder, ft)
	} else {
		// nothing on disk for this record: no folder, only its index row
		tidy(folder)
		out = indexRow(uid, "", ft)
	}
	if attDone {
		out["attachments"] = attColumn(attRows)
	}
	return out, attRows, attDone
}

func sortedCopy(xs []string) []string {
	out := append([]string(nil), xs...)
	sort.Strings(out)
	return out
}

// readUIDsFile: uids from a CSV with a uid (or doi) column, or one uid per line.
func readUIDsFile(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	text := strings.TrimPrefix(string(b), "\ufeff")
	first, _, _ := strings.Cut(text, "\n")
	if strings.Contains(first, ",") || strings.TrimSpace(first) == "uid" || strings.TrimSpace(first) == "doi" {
		rows, cols := readCSV(path)
		var out []string
		switch {
		case contains(cols, "uid"):
			for _, r := range rows {
				if r["uid"] != "" {
					out = append(out, r["uid"])
				}
			}
			return out
		case contains(cols, "doi"):
			for _, r := range rows {
				if d := normDOI(r["doi"]); d != "" {
					out = append(out, "doi:"+d)
				}
			}
			return out
		}
	}
	var out []string
	for _, l := range splitLinesPy(text) {
		if t := strings.TrimSpace(l); t != "" && t != "uid" && t != "doi" {
			out = append(out, t)
		}
	}
	return out
}

// tidyIndex: records without a file keep no folder; removes the folders
// earlier versions left behind and gives their rows a reason.
func tidyIndex(index map[string]Row, order []string) int {
	removed := 0
	for _, uid := range order {
		r := index[uid]
		if r["status"] == "ok" || r["status"] == "partial" || r["folder"] == "" {
			continue
		}
		folder := researchFile(r["folder"])
		var tried []string
		if b, err := os.ReadFile(filepath.Join(folder, "metadata.json")); err == nil {
			for _, t := range jlist(jmap(decodeJSON(b), "fulltext")["sources_tried"]) {
				if s, ok := t.(string); ok {
					tried = append(tried, s)
				}
			}
		}
		if tidy(folder) || !fileExists(folder) {
			removed++
			r["folder"] = ""
			if r["reason"] == "" {
				if len(tried) > 0 {
					r["reason"] = "no open-access copy found (tried: " + strings.Join(tried, ", ") + ")"
				} else {
					r["reason"] = "no open-access copy found"
				}
			}
		}
	}
	return removed
}

func stepFulltext(args []string, log *Logger) error {
	o := parseStepArgs(args)
	tiers := splitList(orDefault(o["tiers"], "landmark,core,related"))
	workers := atoiSafe(orDefault(o["workers"], "6"))
	retryDays := atoiSafe(orDefault(o["retry-days"], "30"))
	formats := splitList(orDefault(o["formats"], "pdf,md,txt,xml"))
	sources := splitList(orDefault(o["sources"], strings.Join(allFTSources, ",")))
	for _, f := range formats {
		if !contains(allFormats, f) && f != "attachments" {
			return fmt.Errorf("formats must be within %v", append(append([]string(nil), allFormats...), "attachments"))
		}
	}
	for _, s := range sources {
		if !contains(allFTSources, s) {
			return fmt.Errorf("sources must be within %v", allFTSources)
		}
	}
	var textFormats []string
	for _, f := range formats {
		if f != "attachments" {
			textFormats = append(textFormats, f)
		}
	}
	wantAtt := contains(formats, "attachments") && contains(sources, "pmc")
	var exts map[string]bool
	for _, e := range splitList(o["attachment-ext"]) {
		if exts == nil {
			exts = map[string]bool{}
		}
		exts[strings.TrimPrefix(strings.ToLower(e), ".")] = true
	}
	maxBytes := max(0, atoiSafe(orDefault(o["max-attachment-mb"], "100"))) * 1_000_000
	if contains(sources, "unpaywall") && contact() == "" && len(textFormats) > 0 {
		log.Printf("Unpaywall skipped: no contact email (sourcelens config set contact_email you@example.org)")
	}
	if s := pdfToolsStatus(); strings.HasPrefix(s, "unavailable") && len(textFormats) > 0 {
		log.Printf("PDF to text/Markdown %s; PDFs are kept", s)
	}
	rtypes := map[string]bool{}
	for _, t := range splitList(orDefault(o["types"], strings.Join(articleTypes, ","))) {
		if contains(articleTypes, t) {
			rtypes[t] = true
		}
	}
	dateFrom, dateTo := o["from"], o["to"]
	if dateFrom == "" && dateTo == "" && searchWindowOverridden() {
		dateFrom, dateTo = searchWindow()
	}
	force := o["force"] != ""
	rows, _ := readCSV(rpath("progress.csv"))
	var sel []Row
	for _, r := range rows {
		if rtypes[r["resource_type"]] && contains(tiers, r["tier"]) {
			sel = append(sel, r)
		}
	}
	var explicit map[string]bool
	if o["uids"] != "" || o["uids-file"] != "" {
		explicit = map[string]bool{}
		for _, u := range splitList(o["uids"]) {
			explicit[u] = true
		}
		if o["uids-file"] != "" {
			for _, u := range readUIDsFile(o["uids-file"]) {
				explicit[u] = true
			}
		}
		var keep []Row
		for _, r := range sel {
			if explicit[r["uid"]] {
				keep = append(keep, r)
			}
		}
		sel = keep
	}
	if dateFrom != "" || dateTo != "" {
		lo, hi := orDefault(dateFrom, "0000"), orDefault(dateTo, "9999")
		var keep []Row
		for _, r := range sel {
			if d := orDefault(r["date"], "0000"); lo <= d && d <= hi {
				keep = append(keep, r)
			}
		}
		sel = keep
	}
	order := map[string]int{"landmark": 0, "core": 1, "related": 2}
	sort.SliceStable(sel, func(i, j int) bool {
		a, b := sel[i], sel[j]
		oa, ob := order[a["tier"]], order[b["tier"]]
		if oa != ob {
			return oa < ob
		}
		return atoiSafe(a["cited_by"]) > atoiSafe(b["cited_by"])
	})
	idxRows, _ := readCSV(rpath("fulltext", "fulltext_index.csv"))
	index := map[string]Row{}
	var indexOrder []string // python dict order: file order, then new uids as they finish
	setIndex := func(uid string, r Row) {
		if _, ok := index[uid]; !ok {
			indexOrder = append(indexOrder, uid)
		}
		index[uid] = r
	}
	for _, r := range idxRows {
		setIndex(r["uid"], r)
	}
	if n := tidyIndex(index, indexOrder); n > 0 {
		log.Printf("removed %d folders of records without any file (their index rows keep the reason)", n)
	}
	attIndex := readAttIndex()
	statusCounts := func() string {
		c := newCounter()
		for _, u := range indexOrder {
			c.add(index[u]["status"], 1)
		}
		return pyCounterRepr(c)
	}
	if o["only-status"] != "" {
		want := splitList(o["only-status"])
		var keep []Row
		for _, r := range sel {
			if contains(want, index[r["uid"]]["status"]) {
				keep = append(keep, r)
			}
		}
		sel = keep
		force = true
	}
	cutoff := time.Now().AddDate(0, 0, -retryDays).Format("2006-01-02")
	isAll := len(textFormats) == len(allFormats)
	textDue := func(r Row) bool {
		prev, ok := index[r["uid"]]
		switch {
		case len(textFormats) == 0:
			return false
		case force || explicit != nil || !ok:
			return true
		case prev["status"] == "partial" || prev["status"] == "deferred" || prev["status"] == "error" ||
			(prev["status"] == "none" && prev["checked_on"] < cutoff):
			return true
		case prev["status"] == "ok" && prev["checked_on"] < cutoff:
			have := map[string]bool{}
			for _, f := range allFormats {
				if prev["has_"+f] == "True" {
					have[f] = true
				}
			}
			missing := false
			for _, f := range textFormats {
				if !have[f] {
					missing = true
				}
			}
			return !complete(have, textFormats) || (missing && !isAll)
		}
		return false
	}
	attDue := func(r Row) bool {
		if !wantAtt || r["pmcid"] == "" {
			return false
		}
		if explicit != nil {
			return true
		}
		prev := index[r["uid"]]
		if prev["status"] == "removed" || (prev["status"] == "none" && !textDue(r)) {
			return false
		}
		if prev["attachments"] == "" {
			return true
		}
		for _, a := range attIndex[r["uid"]] {
			if a["status"] == "failed" {
				return true
			}
		}
		return false
	}
	type item struct {
		row        Row
		text, want bool
	}
	var todo []item
	for _, r := range sel {
		t, a := textDue(r), attDue(r)
		if t || a {
			todo = append(todo, item{r, t, a})
		}
	}
	if n := atoiSafe(o["limit"]); n > 0 && len(todo) > n {
		todo = todo[:n]
	}
	store := LoadStore()
	extra := ""
	if dateFrom != "" || dateTo != "" {
		extra = fmt.Sprintf("; dates %s .. %s", orDefault(dateFrom, "…"), orDefault(dateTo, "…"))
	}
	log.Printf("%d article rows in tiers %v; %d to fetch; formats %v; sources %v%s",
		len(sel), tiers, len(todo), sortedCopy(formats), sortedCopy(sources), extra)

	f := &fetcher{lim: NewLimiter(time.Second, hostInterval), blocked: map[string]time.Time{}, log: log}
	type job struct {
		rec       Rec
		row       Row
		text      bool
		att       *attOpts
		uid       string
		prevAttCo string
	}
	var jobs []job
	resumed := 0
	for _, it := range todo {
		uid := it.row["uid"]
		rec := store.Get(uid)
		if rec == nil {
			continue
		}
		text := it.text
		if text && !force {
			if prev := fromDisk(rec, it.row, textFormats); prev != nil {
				prev["attachments"] = index[uid]["attachments"]
				setIndex(uid, prev)
				resumed++
				text = false
			}
		}
		if !text && !it.want {
			continue
		}
		var att *attOpts
		if it.want {
			att = &attOpts{exts: exts, maxBytes: maxBytes, prev: attIndex[uid]}
		}
		jobs = append(jobs, job{rec, it.row, text, att, uid, index[uid]["attachments"]})
	}
	log.Printf("%d already complete on disk; %d to download", resumed, len(jobs))
	writeIndexes := func() {
		keys := sortedKeys(index)
		out := make([]Row, len(keys))
		for i, k := range keys {
			out[i] = index[k]
		}
		_ = writeCSV(rpath("fulltext", "fulltext_index.csv"), out, indexCols)
		if wantAtt {
			_ = writeAttIndex(attIndex)
		}
	}
	var mu sync.Mutex
	done := 0
	ch := make(chan job)
	var wg sync.WaitGroup
	wantPDF := o["no-pdf"] == ""
	for w := 0; w < max(1, workers); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				var res Row
				var attRows []Row
				var attDone, failed bool
				func() {
					defer func() {
						if e := recover(); e != nil {
							failed = true
							res = Row{"uid": j.uid, "status": "error", "reason": "error while downloading", "checked_on": today(),
								"note": runeCut(fmt.Sprint(e), 300)}
							log.Printf("error on %s: %v", j.uid, e)
						}
					}()
					if j.text {
						res, attRows, attDone = f.fetchOne(j.rec, j.row, wantPDF, formats, sources, j.att)
					} else {
						res, attRows, attDone = f.attachmentsOnly(j.rec, j.row, j.att)
					}
				}()
				mu.Lock()
				switch {
				case failed:
					if _, ok := index[j.uid]; j.text || !ok {
						setIndex(j.uid, res)
					}
				case j.text:
					if !attDone {
						res["attachments"] = j.prevAttCo
					}
					setIndex(j.uid, res)
				default:
					prev := index[j.uid]
					if prev == nil {
						prev = Row{"uid": j.uid}
					}
					if res["attachments"] != "" {
						prev["attachments"] = res["attachments"]
					}
					if prev["folder"] == "" && res["folder"] != "" {
						prev["folder"] = res["folder"]
					}
					setIndex(j.uid, prev)
				}
				if attDone && len(attRows) > 0 {
					attIndex[j.uid] = attRows
				}
				done++
				if done%25 == 0 {
					writeIndexes()
					log.Printf("  %d/%d %s", done, len(jobs), statusCounts())
				}
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		ch <- j
	}
	close(ch)
	wg.Wait()
	writeIndexes()
	log.Printf("done: %s", statusCounts())
	return nil
}
