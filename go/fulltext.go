package main

// Full texts ("fulltext" step). Mirrors python/pullliturature/fetch_fulltext.py:
// PMC open-access bucket -> bioRxiv / medRxiv -> Europe PMC XML -> arXiv ->
// Unpaywall; formats pdf / md / txt / xml; metadata.json per record; index.

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
	allFTSources  = []string{"arxiv", "biorxiv", "europepmc", "pmc", "unpaywall"}
	articleTypes  = []string{"article", "book chapter", "conference paper", "preprint", "report", "review", "thesis"}
	indexCols     = []string{"uid", "status", "source", "license", "has_pdf", "has_md", "has_txt", "has_xml", "folder", "pdf_url", "checked_on", "note"}
	preprintHosts = map[string]bool{"www.biorxiv.org": true, "www.medrxiv.org": true}
	hostInterval  = map[string]time.Duration{"pmc-oa-opendata.s3.amazonaws.com": 20 * time.Millisecond,
		"api.unpaywall.org": 100 * time.Millisecond, "api.biorxiv.org": 500 * time.Millisecond,
		"www.ebi.ac.uk": 250 * time.Millisecond, "www.biorxiv.org": 6 * time.Second,
		"www.medrxiv.org": 6 * time.Second, "arxiv.org": 3 * time.Second}
	s3KeyRe = regexp.MustCompile(`<Key>([^<]+)</Key>`)
)

func browserUA() string {
	if contact() == "" {
		return "Mozilla/5.0 (X11; Linux x86_64) litSearch/2.0"
	}
	return "Mozilla/5.0 (X11; Linux x86_64) litSearch/2.0 (mailto:" + contact() + ")"
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

func (x *ftCtx) pmcS3(pmcid string) (map[string]any, bool) {
	r := x.c.Get(s3Base+"/", reqOpts{params: url.Values{"list-type": {"2"}, "prefix": {pmcid + "."}, "max-keys": {"100"}}})
	if r == nil || r.Status != 200 {
		return nil, true // listing failed
	}
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(pmcid) + `\.(\d+)/` + regexp.QuoteMeta(pmcid) + `\.\d+\.json$`)
	best := -1
	for _, m := range s3KeyRe.FindAllStringSubmatch(r.Text(), -1) {
		if mm := re.FindStringSubmatch(m[1]); mm != nil {
			if v, _ := strconv.Atoi(mm[1]); v > best {
				best = v
			}
		}
	}
	if best < 0 {
		return nil, false
	}
	j := x.c.Get(fmt.Sprintf("%s/%s.%d/%s.%d.json", s3Base, pmcid, best, pmcid, best), reqOpts{})
	if j == nil || j.Status != 200 {
		return nil, false
	}
	m := decodeJSON(j.Body)
	if m != nil {
		m["_version"] = best
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
// PDF -> text / Markdown with poppler
// ---------------------------------------------------------------------------

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
		if fileExists(filepath.Join(folder, formatFiles[f])) {
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
	return Row{"uid": uid, "status": jstr(ft, "status"), "source": jstr(ft, "source"), "license": jstr(ft, "license"),
		"has_pdf": has("pdf"), "has_md": has("md"), "has_txt": has("txt"), "has_xml": has("xml"),
		"folder": researchRel(folder), "pdf_url": pdfURL, "checked_on": runeCut(checked, 10), "note": ""}
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

func (f *fetcher) fetchOne(rec Rec, row Row, wantPDF bool, formats, sources []string) Row {
	x := &ftCtx{f: f, c: f.client()}
	uid := str(rec, "uid")
	folder := folderFor(rec, row)
	_ = os.MkdirAll(folder, 0o755)
	files := map[string]any{}
	tried := []any{}
	prov := map[string]any{}
	lic, source := "", ""
	var xmlBytes []byte
	keep := map[string]bool{}
	for _, fm := range formats {
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
			_ = os.WriteFile(pdfTmp, data, 0o644)
			tmpPDF = true
			return
		}
		if !keep[name] {
			return
		}
		_ = os.WriteFile(filepath.Join(folder, name), data, 0o644)
		files[name] = map[string]any{"bytes": len(data), "sha256": sha(data), "url": u, "via": via}
	}
	havePDF := func() bool { _, ok := files["paper.pdf"]; return ok || tmpPDF }

	pmcid := str(rec, "pmcid")
	s3err := false
	if pmcid != "" && src["pmc"] {
		tried = append(tried, "pmc_s3")
		m, failed := x.pmcS3(pmcid)
		s3err = failed
		if m != nil {
			p := map[string]any{}
			for _, k := range []string{"pmcid", "_version", "license_code", "is_pmc_openaccess", "is_manuscript", "is_retracted", "citation"} {
				p[k] = m[k]
			}
			prov["pmc_s3"] = p
			lic = orDefault(jstr(m, "license_code"), lic)
			source = "pmc_s3"
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
		}
	}
	doi := str(rec, "doi")
	if src["biorxiv"] && strings.HasPrefix(doi, "10.1101/") && (xmlBytes == nil || (wantPDF && !havePDF())) {
		tried = append(tried, "biorxiv")
		if v := x.biorxiv(doi); v != nil {
			server := jstr(v, "_server")
			prov["biorxiv"] = map[string]any{"server": v["server"], "version": v["version"], "date": v["date"],
				"license": v["license"], "published": v["published"]}
			lic = orDefault(lic, jstr(v, "license"))
			if j := jstr(v, "jatsxml"); xmlBytes == nil && j != "" {
				if b := x.getBytes(j, false); b != nil && bytes.Contains(b[:min(5000, len(b))], []byte("<article")) {
					xmlBytes = b
					source = orDefault(source, server)
					save("paper.jats.xml", b, j, server)
				}
			}
			pdf := fmt.Sprintf("https://www.%s.org/content/%sv%s.full.pdf", server, doi, orDefault(jstr(v, "version"), "1"))
			if wantPDF && !havePDF() {
				if b := x.getBytes(pdf, true); b != nil {
					save("paper.pdf", b, pdf, server)
					source = orDefault(source, server)
				}
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
		}
	}
	// Unpaywall needs a contact address (contact_email in config/local.yaml)
	if src["unpaywall"] && contact() != "" && wantPDF && !havePDF() && doi != "" {
		tried = append(tried, "unpaywall")
		r := x.c.Get("https://api.unpaywall.org/v2/"+escapeDOIPath(doi), reqOpts{params: url.Values{"email": {contact()}}, tries: 3})
		if r != nil && r.Status == 200 {
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

	if b, err := os.ReadFile(filepath.Join(folder, "metadata.json")); err == nil {
		old := ymap(jmap(decodeJSON(b), "fulltext")["files"])
		for k, v := range old {
			if _, ok := files[k]; !ok && fileExists(filepath.Join(folder, k)) {
				files[k] = v
			}
		}
	}
	present := map[string]bool{}
	for f, name := range formatFiles {
		if _, ok := files[name]; ok {
			present[f] = true
		}
	}
	status := "none"
	if complete(present, formats) {
		status = "ok"
	} else if len(files) > 0 {
		status = "partial"
	}
	if status != "ok" && x.deferred {
		status = "deferred"
	}
	ft := map[string]any{"status": status, "source": source, "license": lic, "files": files,
		"fetched_on": time.Now().Format("2006-01-02T15:04:05"), "sources_tried": tried,
		"formats": sortedCopy(formats), "sources": sortedCopy(sources)}
	// key order as python writes it (nested records keep Go's sorted key order)
	fto := omap{{"status", status}, {"source", source}, {"license", lic}, {"files", files},
		{"fetched_on", ft["fetched_on"]}, {"sources_tried", tried}, {"formats", ft["formats"]}, {"sources", ft["sources"]}}
	for _, k := range []string{"pmc_s3", "biorxiv", "unpaywall", "xml_error"} {
		if v, ok := prov[k]; ok {
			ft[k] = v
			fto = append(fto, okv{k, v})
		}
	}
	for k, v := range prov {
		if _, ok := ft[k]; !ok {
			ft[k] = v
			fto = append(fto, okv{k, v})
		}
	}
	catRow := omap{}
	for _, k := range []string{"tier", "category", "modality", "title", "date"} {
		catRow = append(catRow, okv{k, row[k]})
	}
	_ = writeJSON(filepath.Join(folder, "metadata.json"), omap{{"uid", uid}, {"record", rec}, {"catalogue_row", catRow}, {"fulltext", fto}})
	return indexRow(uid, folder, ft)
}

func sortedCopy(xs []string) []string {
	out := append([]string(nil), xs...)
	sort.Strings(out)
	return out
}

func stepFulltext(args []string, log *Logger) error {
	o := parseStepArgs(args)
	tiers := splitList(orDefault(o["tiers"], "landmark,core,related"))
	workers := atoiSafe(orDefault(o["workers"], "6"))
	retryDays := atoiSafe(orDefault(o["retry-days"], "30"))
	formats := splitList(orDefault(o["formats"], "pdf,md,txt,xml"))
	sources := splitList(orDefault(o["sources"], strings.Join(allFTSources, ",")))
	for _, f := range formats {
		if !contains(allFormats, f) {
			return fmt.Errorf("formats must be within %v", allFormats)
		}
	}
	for _, s := range sources {
		if !contains(allFTSources, s) {
			return fmt.Errorf("sources must be within %v", allFTSources)
		}
	}
	if contains(sources, "unpaywall") && contact() == "" {
		log.Printf("Unpaywall skipped: no contact_email in config/local.yaml")
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
	if o["uids"] != "" {
		want := splitList(o["uids"])
		var keep []Row
		for _, r := range sel {
			if contains(want, r["uid"]) {
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
	isAll := len(formats) == len(allFormats)
	var todo []Row
	for _, r := range sel {
		prev, ok := index[r["uid"]]
		switch {
		case force || o["uids"] != "" || !ok:
			todo = append(todo, r)
		case prev["status"] == "partial" || prev["status"] == "deferred" || prev["status"] == "error" ||
			(prev["status"] == "none" && prev["checked_on"] < cutoff):
			todo = append(todo, r)
		case prev["status"] == "ok" && prev["checked_on"] < cutoff:
			have := map[string]bool{}
			for _, f := range allFormats {
				if prev["has_"+f] == "True" {
					have[f] = true
				}
			}
			missing := false
			for _, f := range formats {
				if !have[f] {
					missing = true
				}
			}
			if !complete(have, formats) || (missing && !isAll) {
				todo = append(todo, r)
			}
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
		rec Rec
		row Row
	}
	var jobs []job
	resumed := 0
	for _, r := range todo {
		rec := store.Get(r["uid"])
		if rec == nil {
			continue
		}
		if !force {
			if prev := fromDisk(rec, r, formats); prev != nil {
				setIndex(r["uid"], prev)
				resumed++
				continue
			}
		}
		jobs = append(jobs, job{rec, r})
	}
	log.Printf("%d already complete on disk; %d to download", resumed, len(jobs))
	writeIndex := func() {
		keys := sortedKeys(index)
		out := make([]Row, len(keys))
		for i, k := range keys {
			out[i] = index[k]
		}
		_ = writeCSV(rpath("fulltext", "fulltext_index.csv"), out, indexCols)
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
				func() {
					defer func() {
						if e := recover(); e != nil {
							res = Row{"uid": str(j.rec, "uid"), "status": "error", "checked_on": today(),
								"note": runeCut(fmt.Sprint(e), 300)}
							log.Printf("error on %s: %v", str(j.rec, "uid"), e)
						}
					}()
					res = f.fetchOne(j.rec, j.row, wantPDF, formats, sources)
				}()
				mu.Lock()
				setIndex(str(j.rec, "uid"), res)
				done++
				if done%25 == 0 {
					writeIndex()
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
	writeIndex()
	log.Printf("done: %s", statusCounts())
	return nil
}
