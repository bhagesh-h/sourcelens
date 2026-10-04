package main

// Reference formatting for `litSearch export`. Mirrors python/query/refs.py rule
// for rule; for the same rows both print byte-identical text.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/dlclark/regexp2"
)

var (
	refCodes        = []string{"APA", "AMA", "MLA", "CHI", "HAR", "VAN", "IEE", "NAT", "BIB", "RIS", "ENW", "CSL"}
	refDescriptions = map[string]string{
		"APA": "APA 7th edition", "AMA": "AMA Manual of Style 11th edition", "MLA": "MLA 9th edition",
		"CHI": "Chicago 17th, author-date", "HAR": "Harvard (Cite Them Right)", "VAN": "Vancouver / ICMJE (NLM)",
		"IEE": "IEEE", "NAT": "Nature", "BIB": "BibTeX", "RIS": "RIS (Zotero, Mendeley, EndNote, RefWorks)",
		"ENW": "EndNote tagged (.enw)", "CSL": "CSL-JSON (Zotero, pandoc, citeproc)",
	}
	refAliases = map[string]string{"IEEE": "IEE", "VANCOUVER": "VAN", "BIBTEX": "BIB", "ENDNOTE": "ENW",
		"CSL-JSON": "CSL", "JSON": "CSL", "CHICAGO": "CHI", "HARVARD": "HAR", "NATURE": "NAT"}
	refExt = map[string]string{"APA": ".apa.txt", "AMA": ".ama.txt", "MLA": ".mla.txt", "CHI": ".chi.txt",
		"HAR": ".har.txt", "VAN": ".van.txt", "IEE": ".iee.txt", "NAT": ".nat.txt", "BIB": ".bib", "RIS": ".ris",
		"ENW": ".enw", "CSL": ".json"}
	collectiveRe = regexp2.MustCompile(`\b(consortium|group|study|investigators|collaboration|network|initiative|`+
		`team|committee|society|project|cohort|alliance|working)\b`, regexp2.IgnoreCase)
	initialsRe = regexp.MustCompile(`^(.+?) ([A-Z]{1,4})$`)
	nonAlnum   = regexp.MustCompile(`[^A-Za-z0-9]`)
	pageSplit  = regexp.MustCompile(`[-–]`)
	isoDay     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	risType    = map[string]string{"article": "JOUR", "preprint": "UNPB", "software": "COMP", "web": "ELEC", "data": "DATA", "other": "GEN"}
	enwType    = map[string]string{"article": "Journal Article", "preprint": "Unpublished Work", "software": "Computer Program",
		"web": "Web Page", "data": "Dataset", "other": "Generic"}
	cslType = map[string]string{"article": "article-journal", "preprint": "article", "software": "software", "web": "webpage",
		"data": "dataset", "other": "document"}
	refColumns = []string{"uid", "resource_type", "date", "year", "authors", "title", "venue", "doi", "pmid", "pmcid", "url"}
)

// pyQuote is python's repr() of a plain string.
func pyQuote(s string) string { q := pyListRepr([]string{s}); return q[1 : len(q)-1] }

func parseCodes(spec string) ([]string, error) {
	var out []string
	for _, tok := range strings.Split(spec, ",") {
		t := strings.ToUpper(pyStrip(tok))
		if t == "" {
			continue
		}
		if a, ok := refAliases[t]; ok {
			t = a
		}
		if !contains(refCodes, t) {
			return nil, fmt.Errorf("unknown format %s; use %s", pyQuote(pyStrip(tok)), strings.Join(refCodes, ", "))
		}
		if !contains(out, t) {
			out = append(out, t)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// authors
// ---------------------------------------------------------------------------

type author struct {
	literal  string
	isLit    bool
	family   string
	initials []string
}

func pyFields(s string) []string { return strings.FieldsFunc(s, pyIsSpace) }

func parseAuthors(s string) []author {
	var out []author
	for _, tok := range strings.Split(strings.TrimRight(pyStrip(s), "."), ",") {
		t := strings.Join(pyFields(tok), " ")
		if t == "" || strings.ToLower(t) == "et al" || strings.ToLower(t) == "et al." {
			continue
		}
		if reSearch(collectiveRe, t) {
			out = append(out, author{literal: t, isLit: true})
			continue
		}
		if m := initialsRe.FindStringSubmatch(t); m != nil {
			var in []string
			for _, c := range m[2] {
				in = append(in, string(c))
			}
			out = append(out, author{family: m[1], initials: in})
			continue
		}
		parts := strings.Split(t, " ")
		if len(parts) == 1 {
			out = append(out, author{literal: t, isLit: true})
			continue
		}
		var in []string
		for _, g := range parts[:len(parts)-1] {
			for _, piece := range strings.Split(g, "-") {
				if c := strings.Trim(piece, "."); c != "" {
					r := []rune(c)[0]
					in = append(in, strings.ToUpper(string(r)))
				}
			}
		}
		out = append(out, author{family: parts[len(parts)-1], initials: in})
	}
	return out
}

func dots(a author, sep string) string {
	xs := make([]string, len(a.initials))
	for i, c := range a.initials {
		xs[i] = c + "."
	}
	return strings.Join(xs, sep)
}

func nInverted(a author) string { // Family, A. A.
	if a.isLit {
		return a.literal
	}
	if len(a.initials) > 0 {
		return a.family + ", " + dots(a, " ")
	}
	return a.family
}

func nHarvard(a author) string { // Family, A.A.
	if a.isLit {
		return a.literal
	}
	if len(a.initials) > 0 {
		return a.family + ", " + dots(a, "")
	}
	return a.family
}

func nCompact(a author) string { // Family AA
	if a.isLit {
		return a.literal
	}
	if len(a.initials) > 0 {
		return a.family + " " + strings.Join(a.initials, "")
	}
	return a.family
}

func nForward(a author) string { // A. A. Family
	if a.isLit {
		return a.literal
	}
	if len(a.initials) > 0 {
		return dots(a, " ") + " " + a.family
	}
	return a.family
}

func mapNames(au []author, f func(author) string) []string {
	n := make([]string, len(au))
	for i, a := range au {
		n[i] = f(a)
	}
	return n
}

func listAPA(au []author) string {
	n := mapNames(au, nInverted)
	switch {
	case len(n) == 0:
		return ""
	case len(n) == 1:
		return n[0]
	case len(n) == 2:
		return n[0] + ", & " + n[1]
	case len(n) <= 20:
		return strings.Join(n[:len(n)-1], ", ") + ", & " + n[len(n)-1]
	}
	return strings.Join(n[:19], ", ") + ", . . . " + n[len(n)-1]
}

func listAMA(au []author) string {
	n := mapNames(au, nCompact)
	if len(n) <= 6 {
		return strings.Join(n, ", ")
	}
	return strings.Join(n[:3], ", ") + ", et al"
}

func listVAN(au []author) string {
	n := mapNames(au, nCompact)
	if len(n) <= 6 {
		return strings.Join(n, ", ")
	}
	return strings.Join(n[:6], ", ") + ", et al."
}

func listMLA(au []author) string {
	switch len(au) {
	case 0:
		return ""
	case 1:
		return nInverted(au[0])
	case 2:
		return nInverted(au[0]) + ", and " + nForward(au[1])
	}
	return nInverted(au[0]) + ", et al."
}

func listHAR(au []author) string {
	n := mapNames(au, nHarvard)
	switch {
	case len(n) == 0:
		return ""
	case len(n) == 1:
		return n[0]
	case len(n) <= 3:
		return strings.Join(n[:len(n)-1], ", ") + " and " + n[len(n)-1]
	}
	return n[0] + " et al."
}

func listCHI(au []author) string {
	if len(au) == 0 {
		return ""
	}
	n := append([]string{nInverted(au[0])}, mapNames(au[1:], nForward)...)
	switch {
	case len(n) == 1:
		return n[0]
	case len(n) == 2:
		return n[0] + ", and " + n[1]
	case len(n) <= 10:
		return strings.Join(n[:len(n)-1], ", ") + ", and " + n[len(n)-1]
	}
	return strings.Join(n[:7], ", ") + ", et al."
}

func listIEE(au []author) string {
	n := mapNames(au, nForward)
	switch {
	case len(n) == 0:
		return ""
	case len(n) == 1:
		return n[0]
	case len(n) == 2:
		return n[0] + " and " + n[1]
	case len(n) <= 6:
		return strings.Join(n[:len(n)-1], ", ") + ", and " + n[len(n)-1]
	}
	return n[0] + " et al."
}

func listNAT(au []author) string {
	n := mapNames(au, nInverted)
	switch {
	case len(n) == 0:
		return ""
	case len(n) == 1:
		return n[0]
	case len(n) == 2:
		return n[0] + " & " + n[1]
	case len(n) <= 5:
		return strings.Join(n[:len(n)-1], ", ") + " & " + n[len(n)-1]
	}
	return n[0] + " et al."
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func bare(t string) string { // title without a trailing full stop
	t = strings.Join(pyFields(t), " ")
	return strings.TrimSuffix(t, ".")
}

func sentence(t string) string { // text ending in . ? or !
	t = strings.Join(pyFields(t), " ")
	if t == "" {
		return ""
	}
	if strings.ContainsAny(t[len(t)-1:], ".?!") {
		return t
	}
	return t + "."
}

func withDot(t string) string {
	if strings.HasSuffix(t, ".") {
		return t
	}
	return t + "."
}

func refLink(r Row) string {
	if r["doi"] != "" {
		return "https://doi.org/" + r["doi"]
	}
	return r["url"]
}

func refYear(r Row) string { return orDefault(r["year"], "n.d.") }

func joinParts(parts ...string) string { return joinNonEmpty(" ", parts...) }

// ---------------------------------------------------------------------------
// text styles: one line per reference
// ---------------------------------------------------------------------------

func fmtAPA(r Row, au []author) string {
	a := listAPA(au)
	src := r["venue"]
	if r["volume"] != "" {
		src += ", " + r["volume"]
	}
	if r["issue"] != "" {
		src += "(" + r["issue"] + ")"
	}
	if r["pages"] != "" {
		src += ", " + r["pages"]
	}
	var parts []string
	if a != "" {
		parts = []string{a + " (" + refYear(r) + ").", sentence(r["title"])}
	} else {
		parts = []string{sentence(r["title"]), "(" + refYear(r) + ")."}
	}
	if src != "" {
		parts = append(parts, withDot(strings.TrimLeft(src, ", ")))
	}
	if l := refLink(r); l != "" {
		parts = append(parts, l)
	}
	return joinParts(parts...)
}

func amaLike(r Row, a string) string {
	var parts []string
	if a != "" {
		parts = append(parts, withDot(a))
	}
	parts = append(parts, sentence(r["title"]))
	if r["venue"] != "" {
		parts = append(parts, sentence(r["venue"]))
	}
	yv := refYear(r)
	if r["volume"] != "" {
		yv += ";" + r["volume"]
	}
	if r["issue"] != "" {
		yv += "(" + r["issue"] + ")"
	}
	if r["pages"] != "" {
		yv += ":" + r["pages"]
	}
	parts = append(parts, yv+".")
	if r["doi"] != "" {
		parts = append(parts, "doi:"+r["doi"])
	} else if r["url"] != "" {
		parts = append(parts, r["url"])
	}
	return joinParts(parts...)
}

func fmtMLA(r Row, au []author) string {
	var parts []string
	if a := listMLA(au); a != "" {
		parts = append(parts, withDot(a))
	}
	parts = append(parts, `"`+sentence(r["title"])+`"`)
	src := r["venue"]
	if r["volume"] != "" {
		src += ", vol. " + r["volume"]
	}
	if r["issue"] != "" {
		src += ", no. " + r["issue"]
	}
	if src != "" {
		src += ", " + refYear(r)
	} else {
		src = refYear(r)
	}
	if r["pages"] != "" {
		src += ", pp. " + r["pages"]
	}
	parts = append(parts, src+".")
	if l := refLink(r); l != "" {
		parts = append(parts, l+".")
	}
	return strings.Join(parts, " ")
}

func fmtHAR(r Row, au []author) string {
	a := listHAR(au)
	t := bare(r["title"])
	head := t + " (" + refYear(r) + ")"
	if a != "" {
		head = a + " (" + refYear(r) + ") '" + t + "'"
	}
	src := r["venue"]
	if r["volume"] != "" {
		src += ", " + r["volume"]
	}
	if r["issue"] != "" {
		src += "(" + r["issue"] + ")"
	}
	if r["pages"] != "" {
		src += ", pp. " + r["pages"]
	}
	out := head + "."
	if src != "" {
		out = head + ", " + strings.TrimLeft(src, ", ") + "."
	}
	if r["doi"] != "" {
		out += " doi:" + r["doi"] + "."
	} else if r["url"] != "" {
		out += " Available at: " + r["url"] + "."
	}
	return out
}

func fmtCHI(r Row, au []author) string {
	var parts []string
	if a := listCHI(au); a != "" {
		parts = append(parts, withDot(a))
	}
	parts = append(parts, refYear(r)+".", `"`+sentence(r["title"])+`"`)
	src := r["venue"]
	if r["volume"] != "" {
		src += " " + r["volume"]
	}
	if r["issue"] != "" {
		src += " (" + r["issue"] + ")"
	}
	if r["pages"] != "" {
		src += ": " + r["pages"]
	}
	if src = pyStrip(src); src != "" {
		parts = append(parts, src+".")
	}
	if l := refLink(r); l != "" {
		parts = append(parts, l+".")
	}
	return strings.Join(parts, " ")
}

func fmtIEE(r Row, au []author, n int) string {
	s := "[" + strconv.Itoa(n) + "] "
	if a := listIEE(au); a != "" {
		s += a + ", "
	}
	s += `"` + bare(r["title"]) + `,"`
	src := []string{r["venue"]}
	if r["volume"] != "" {
		src = append(src, "vol. "+r["volume"])
	}
	if r["issue"] != "" {
		src = append(src, "no. "+r["issue"])
	}
	if r["pages"] != "" {
		src = append(src, "pp. "+r["pages"])
	}
	src = append(src, refYear(r))
	s += " " + joinNonEmpty(", ", src...)
	if r["doi"] != "" {
		s += ", doi: " + r["doi"]
	} else if r["url"] != "" {
		s += ". [Online]. Available: " + r["url"]
	}
	return s + "."
}

func fmtNAT(r Row, au []author, n int) string {
	s := strconv.Itoa(n) + ". "
	if a := listNAT(au); a != "" {
		s += a + " "
	}
	s += sentence(r["title"])
	src := r["venue"]
	if r["volume"] != "" {
		src += " " + r["volume"]
	}
	if r["pages"] != "" {
		src += ", " + r["pages"]
	}
	if src = pyStrip(src); src != "" {
		s += " " + src + " (" + refYear(r) + ")."
	} else {
		s += " (" + refYear(r) + ")."
	}
	if l := refLink(r); l != "" {
		s += " " + l
	}
	return s
}

// ---------------------------------------------------------------------------
// exchange formats
// ---------------------------------------------------------------------------

func refKind(r Row) string {
	switch r["resource_type"] {
	case "article", "review":
		return "article"
	case "preprint":
		return "preprint"
	case "repository", "software package", "software":
		return "software"
	case "website", "database", "web calculator":
		return "web"
	case "dataset", "archive":
		return "data"
	}
	return "other"
}

func asciiWord(s string) string { return strings.ToLower(nonAlnum.ReplaceAllString(s, "")) }

func bibKey(r Row, au []author, used map[string]int) string {
	fam := ""
	if len(au) > 0 {
		f := au[0].family
		if f == "" {
			f = strings.Split(au[0].literal, " ")[0]
		}
		fam = asciiWord(f)
	}
	first := ""
	for _, x := range pyFields(r["title"]) {
		if w := asciiWord(x); w != "" {
			first = w
			break
		}
	}
	key := orDefault(fam, "ref") + orDefault(r["year"], "nd") + first
	n := used[key]
	used[key] = n + 1
	if n == 0 {
		return key
	}
	return key + string("abcdefghijklmnopqrstuvwxyz"[min(n-1, 25)])
}

func bibEscape(s string) string {
	return strings.NewReplacer("&", `\&`, "%", `\%`, "#", `\#`, "_", `\_`).Replace(s)
}

func fmtBIB(r Row, au []author, used map[string]int) string {
	typ := "misc"
	if refKind(r) == "article" {
		typ = "article"
	}
	type field struct{ k, v string }
	var fields []field
	if len(au) > 0 {
		names := make([]string, len(au))
		for i, a := range au {
			if a.isLit {
				names[i] = "{" + a.literal + "}"
			} else {
				names[i] = nInverted(a)
			}
		}
		fields = append(fields, field{"author", strings.Join(names, " and ")})
	}
	fields = append(fields, field{"title", "{" + bibEscape(bare(r["title"])) + "}"})
	if r["venue"] != "" {
		k := "howpublished"
		if typ == "article" {
			k = "journal"
		}
		fields = append(fields, field{k, bibEscape(r["venue"])})
	}
	if r["year"] != "" {
		fields = append(fields, field{"year", r["year"]})
	}
	if r["volume"] != "" {
		fields = append(fields, field{"volume", r["volume"]})
	}
	if r["issue"] != "" {
		fields = append(fields, field{"number", r["issue"]})
	}
	if r["pages"] != "" {
		fields = append(fields, field{"pages", strings.ReplaceAll(r["pages"], "-", "--")})
	}
	if r["doi"] != "" {
		fields = append(fields, field{"doi", r["doi"]})
	}
	if l := refLink(r); l != "" {
		fields = append(fields, field{"url", l})
	}
	if r["pmid"] != "" {
		fields = append(fields, field{"pmid", r["pmid"]})
	}
	if typ == "misc" && r["resource_type"] != "" {
		fields = append(fields, field{"note", r["resource_type"]})
	}
	body := make([]string, len(fields))
	for i, f := range fields {
		body[i] = "  " + f.k + " = {" + f.v + "}"
	}
	return "@" + typ + "{" + bibKey(r, au, used) + ",\n" + strings.Join(body, ",\n") + "\n}"
}

func splitPages(p string) (string, string) {
	var bits []string
	for _, b := range pageSplit.Split(p, -1) {
		if pyStrip(b) != "" {
			bits = append(bits, b)
		}
	}
	if len(bits) == 0 {
		return "", ""
	}
	if len(bits) > 1 {
		return pyStrip(bits[0]), pyStrip(bits[len(bits)-1])
	}
	return pyStrip(bits[0]), ""
}

func fmtRIS(r Row, au []author) string {
	lines := []string{"TY  - " + risType[refKind(r)]}
	for _, a := range au {
		lines = append(lines, "AU  - "+nInverted(a))
	}
	lines = append(lines, "TI  - "+bare(r["title"]))
	if r["venue"] != "" {
		lines = append(lines, "T2  - "+r["venue"])
	}
	if r["year"] != "" {
		lines = append(lines, "PY  - "+r["year"])
	}
	if isoDay.MatchString(r["date"]) {
		lines = append(lines, "DA  - "+strings.ReplaceAll(r["date"], "-", "/"))
	}
	if r["volume"] != "" {
		lines = append(lines, "VL  - "+r["volume"])
	}
	if r["issue"] != "" {
		lines = append(lines, "IS  - "+r["issue"])
	}
	sp, ep := splitPages(r["pages"])
	if sp != "" {
		lines = append(lines, "SP  - "+sp)
	}
	if ep != "" {
		lines = append(lines, "EP  - "+ep)
	}
	if r["doi"] != "" {
		lines = append(lines, "DO  - "+r["doi"])
	}
	if l := refLink(r); l != "" {
		lines = append(lines, "UR  - "+l)
	}
	if r["pmid"] != "" {
		lines = append(lines, "AN  - "+r["pmid"])
	}
	lines = append(lines, "ER  - ")
	return strings.Join(lines, "\n")
}

func fmtENW(r Row, au []author) string {
	k := refKind(r)
	lines := []string{"%0 " + enwType[k]}
	for _, a := range au {
		lines = append(lines, "%A "+nInverted(a))
	}
	lines = append(lines, "%T "+bare(r["title"]))
	if r["venue"] != "" {
		if k == "article" {
			lines = append(lines, "%J "+r["venue"])
		} else {
			lines = append(lines, "%B "+r["venue"])
		}
	}
	if r["year"] != "" {
		lines = append(lines, "%D "+r["year"])
	}
	for _, tf := range [][2]string{{"%V", "volume"}, {"%N", "issue"}, {"%P", "pages"}, {"%R", "doi"}} {
		if r[tf[1]] != "" {
			lines = append(lines, tf[0]+" "+r[tf[1]])
		}
	}
	if l := refLink(r); l != "" {
		lines = append(lines, "%U "+l)
	}
	if r["pmid"] != "" {
		lines = append(lines, "%M "+r["pmid"])
	}
	return strings.Join(lines, "\n")
}

func cslItem(r Row, au []author) map[string]any {
	it := map[string]any{"id": r["uid"], "type": cslType[refKind(r)], "title": bare(r["title"])}
	if len(au) > 0 {
		var as []map[string]any
		for _, a := range au {
			switch {
			case a.isLit:
				as = append(as, map[string]any{"literal": a.literal})
			case len(a.initials) > 0:
				as = append(as, map[string]any{"family": a.family, "given": dots(a, " ")})
			default:
				as = append(as, map[string]any{"family": a.family})
			}
		}
		it["author"] = as
	}
	if r["venue"] != "" {
		it["container-title"] = r["venue"]
	}
	d := r["date"]
	if isoDay.MatchString(d) {
		it["issued"] = map[string]any{"date-parts": [][]int{{atoiSafe(d[:4]), atoiSafe(d[5:7]), atoiSafe(d[8:10])}}}
	} else if y := r["year"]; y != "" && strings.IndexFunc(y, func(c rune) bool { return !unicode.IsDigit(c) }) < 0 {
		it["issued"] = map[string]any{"date-parts": [][]int{{atoiSafe(y)}}}
	}
	for _, sd := range [][2]string{{"volume", "volume"}, {"issue", "issue"}, {"pages", "page"}, {"doi", "DOI"}, {"pmid", "PMID"}} {
		if r[sd[0]] != "" {
			it[sd[1]] = r[sd[0]]
		}
	}
	if l := refLink(r); l != "" {
		it["URL"] = l
	}
	return it
}

// pyJSONIndent matches json.dumps(v, indent=2, ensure_ascii=False, sort_keys=True).
func pyJSONIndent(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	s := strings.TrimSuffix(buf.String(), "\n")
	return strings.NewReplacer(` `, " ", ` `, " ").Replace(s)
}

// renderRefs: refs are rows of references.csv in output order; the text ends in a newline.
func renderRefs(code string, refs []Row) string {
	if code == "CSL" {
		items := make([]map[string]any, 0, len(refs))
		for _, r := range refs {
			items = append(items, cslItem(r, parseAuthors(r["authors"])))
		}
		return pyJSONIndent(items) + "\n"
	}
	used := map[string]int{}
	var out []string
	for i, r := range refs {
		n := i + 1
		au := parseAuthors(r["authors"])
		switch code {
		case "APA":
			out = append(out, fmtAPA(r, au))
		case "AMA":
			out = append(out, strconv.Itoa(n)+". "+amaLike(r, listAMA(au)))
		case "VAN":
			out = append(out, strconv.Itoa(n)+". "+amaLike(r, listVAN(au)))
		case "MLA":
			out = append(out, fmtMLA(r, au))
		case "HAR":
			out = append(out, fmtHAR(r, au))
		case "CHI":
			out = append(out, fmtCHI(r, au))
		case "IEE":
			out = append(out, fmtIEE(r, au, n))
		case "NAT":
			out = append(out, fmtNAT(r, au, n))
		case "BIB":
			out = append(out, fmtBIB(r, au, used))
		case "RIS":
			out = append(out, fmtRIS(r, au))
		case "ENW":
			out = append(out, fmtENW(r, au))
		}
	}
	if len(out) == 0 {
		return ""
	}
	sep := "\n"
	if code == "BIB" || code == "RIS" || code == "ENW" {
		sep = "\n\n"
	}
	return strings.Join(out, sep) + "\n"
}
