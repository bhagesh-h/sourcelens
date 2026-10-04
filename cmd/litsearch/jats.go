package main

// JATS XML -> Markdown and plain text. A line-by-line port of
// src/litsearch/pullliturature/jats.py on a small DOM with lxml-like text / tail.

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/dlclark/regexp2"
)

type xnode struct {
	name     string // local name
	attrs    []xml.Attr
	kids     []*xnode
	text     string // text before the first child (lxml .text)
	tail     string // text after this element (lxml .tail)
	isRefTag bool
}

func (n *xnode) attr(local string) string {
	for _, a := range n.attrs {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

func (n *xnode) xlinkHref() string {
	for _, a := range n.attrs {
		if a.Name.Local == "href" && (strings.Contains(a.Name.Space, "xlink") || a.Name.Space == "xlink") {
			return a.Value
		}
	}
	return ""
}

func parseXML(b []byte) *xnode {
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = false
	d.AutoClose = xml.HTMLAutoClose
	d.Entity = xml.HTMLEntity
	root := &xnode{name: "#document"}
	stack := []*xnode{root}
	for {
		tok, err := d.Token()
		if err == io.EOF || err != nil {
			break
		}
		top := stack[len(stack)-1]
		switch t := tok.(type) {
		case xml.StartElement:
			n := &xnode{name: t.Name.Local, attrs: append([]xml.Attr(nil), t.Attr...)}
			top.kids = append(top.kids, n)
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(top.kids) == 0 {
				top.text += string(t)
			} else {
				top.kids[len(top.kids)-1].tail += string(t)
			}
		}
	}
	return root
}

// child: first direct child with the name (lxml el.find("name"))
func (n *xnode) child(name string) *xnode {
	if n == nil {
		return nil
	}
	for _, k := range n.kids {
		if k.name == name {
			return k
		}
	}
	return nil
}

func (n *xnode) children(name string) []*xnode {
	var out []*xnode
	if n == nil {
		return out
	}
	for _, k := range n.kids {
		if k.name == name {
			out = append(out, k)
		}
	}
	return out
}

// iter: self and all descendants in document order (lxml el.iter())
func (n *xnode) iter(fn func(*xnode)) {
	if n == nil {
		return
	}
	fn(n)
	for _, k := range n.kids {
		k.iter(fn)
	}
}

// find: first descendant (not self) with the name (lxml el.find(".//name"))
func (n *xnode) find(name string) *xnode {
	if n == nil {
		return nil
	}
	for _, k := range n.kids {
		if k.name == name {
			return k
		}
		if f := k.find(name); f != nil {
			return f
		}
	}
	return nil
}

func (n *xnode) findAll(name string) []*xnode {
	var out []*xnode
	if n == nil {
		return out
	}
	for _, k := range n.kids {
		k.iter(func(x *xnode) {
			if x.name == name {
				out = append(out, x)
			}
		})
	}
	return out
}

// findtext: text of the first direct child (lxml findtext: "" if empty, absent -> ok=false)
func (n *xnode) findtext(name string) (string, bool) {
	c := n.child(name)
	if c == nil {
		return "", false
	}
	return c.text, true
}

var jatsBlock = map[string]bool{"p": true, "sec": true, "list": true, "fig": true, "table-wrap": true,
	"disp-formula": true, "boxed-text": true, "def-list": true, "disp-quote": true, "statement": true,
	"supplementary-material": true, "ref-list": true, "fn-group": true, "ack": true, "app": true,
	"app-group": true, "glossary": true}

func inline(el *xnode) string {
	if el == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(el.text)
	for _, ch := range el.kids {
		inner := inline(ch)
		switch name := ch.name; {
		case name == "bold":
			if t := pyStrip(inner); t != "" {
				b.WriteString("**" + t + "**")
			}
		case name == "italic":
			if t := pyStrip(inner); t != "" {
				b.WriteString("*" + t + "*")
			}
		case name == "sup":
			if inner != "" {
				b.WriteString("^" + inner + "^")
			}
		case name == "sub":
			if inner != "" {
				b.WriteString("~" + inner + "~")
			}
		case name == "ext-link":
			href := ch.xlinkHref()
			txt := pyStrip(inner)
			if txt == "" {
				txt = href
			}
			if href != "" && href != txt {
				b.WriteString("[" + txt + "](" + href + ")")
			} else {
				b.WriteString(txt)
			}
		case name == "uri":
			href := ch.xlinkHref()
			if href == "" {
				href = pyStrip(inner)
			}
			b.WriteString(href)
		case name == "xref":
			b.WriteString(inner)
		case name == "inline-formula" || name == "math" || name == "tex-math":
			b.WriteString(" " + strings.Join(pyFields(inner), " ") + " ")
		case name == "break":
			b.WriteString(" ")
		case jatsBlock[name]:
			b.WriteString(" " + inner + " ")
		default:
			b.WriteString(inner)
		}
		b.WriteString(ch.tail)
	}
	return b.String()
}

// Python's \s (Unicode) and str.strip()
const pyWS = `[\t\n\v\f\r \x1c-\x1f\x85\p{Z}]`

var (
	cleanNL  = regexp.MustCompile(pyWS + `*\n` + pyWS + `*`)
	cleanSp  = regexp.MustCompile(`[ \t\r\f\v]+`)
	nl3      = regexp.MustCompile(`\n{3,}`)
	mdHead   = regexp2.MustCompile(`^#+\s*`, regexp2.Multiline)
	mdLink   = regexp.MustCompile(`\[([^\]]*)\]\(([^)]*)\)`)
	mdMarks  = regexp2.MustCompile(`\*\*|__|(?<!\w)\*(?!\s)|(?<!\s)\*(?!\w)|\^|~`, regexp2.None)
	mdTblSep = regexp2.MustCompile(`^\|?-{3,}.*$`, regexp2.Multiline)
)

func pyIsSpace(r rune) bool   { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }
func pyStrip(s string) string { return strings.TrimFunc(s, pyIsSpace) }

func jclean(s string) string {
	return pyStrip(cleanSp.ReplaceAllString(cleanNL.ReplaceAllString(s, " "), " "))
}

func joinInline(nodes []*xnode) string {
	parts := make([]string, len(nodes))
	for i, n := range nodes {
		parts[i] = inline(n)
	}
	return strings.Join(parts, " ")
}

func jtable(tw *xnode) []string {
	var out []string
	label := ""
	if l := tw.child("label"); l != nil {
		label = jclean(inline(l))
	}
	captxt := ""
	if c := tw.child("caption"); c != nil {
		captxt = jclean(joinInline(c.kids))
	}
	if label != "" || captxt != "" {
		out = append(out, strings.TrimSpace(fmt.Sprintf("**%s** %s", label, captxt)))
	} else {
		out = append(out, "")
	}
	if tbl := tw.find("table"); tbl != nil {
		var rows [][]string
		tbl.iter(func(tr *xnode) {
			if tr.name != "tr" {
				return
			}
			var cells []string
			for _, c := range tr.kids {
				if c.name == "td" || c.name == "th" {
					cells = append(cells, strings.ReplaceAll(jclean(inline(c)), "|", "\\|"))
				}
			}
			if len(cells) > 0 {
				rows = append(rows, cells)
			}
		})
		if len(rows) > 0 {
			w := 0
			for _, r := range rows {
				w = max(w, len(r))
			}
			for i := range rows {
				for len(rows[i]) < w {
					rows[i] = append(rows[i], "")
				}
			}
			out = append(out, "", "| "+strings.Join(rows[0], " | ")+" |", "|"+strings.Repeat("---|", w))
			for _, r := range rows[1:] {
				out = append(out, "| "+strings.Join(r, " | ")+" |")
			}
		}
	}
	for _, foot := range tw.findAll("table-wrap-foot") {
		for _, p := range foot.findAll("p") {
			out = append(out, "", "_"+jclean(inline(p))+"_")
		}
	}
	return out
}

func jblock(el *xnode, depth int, lines *[]string) {
	name := el.name
	switch {
	case name == "sec" || name == "ack" || name == "app" || name == "glossary" || name == "boxed-text":
		var hs []string
		for _, x := range []*xnode{el.child("label"), el.child("title")} {
			if x != nil {
				hs = append(hs, jclean(inline(x)))
			}
		}
		head := pyStrip(strings.Join(hs, " "))
		if head == "" && name == "ack" {
			head = "Acknowledgements"
		}
		if head != "" {
			*lines = append(*lines, "", strings.Repeat("#", min(depth+1, 6))+" "+head, "")
		}
		for _, ch := range el.kids {
			if ch.name != "title" && ch.name != "label" {
				jblock(ch, depth+1, lines)
			}
		}
	case name == "p":
		if t := jclean(inline(el)); t != "" {
			*lines = append(*lines, t, "")
		}
	case name == "list":
		lt := el.attr("list-type")
		ordered := lt == "order" || lt == "number" || lt == "arabic" || lt == "alpha-lower" || lt == "roman-lower"
		for i, it := range el.children("list-item") {
			if t := jclean(joinInline(it.kids)); t != "" {
				if ordered {
					*lines = append(*lines, fmt.Sprintf("%d. %s", i+1, t))
				} else {
					*lines = append(*lines, "- "+t)
				}
			}
		}
		*lines = append(*lines, "")
	case name == "fig":
		label := "Figure"
		if l := el.child("label"); l != nil {
			label = jclean(inline(l))
		}
		captxt := ""
		if c := el.child("caption"); c != nil {
			captxt = jclean(joinInline(c.kids))
		}
		*lines = append(*lines, strings.TrimSpace(fmt.Sprintf("**%s.** %s", label, captxt)), "")
	case name == "table-wrap":
		*lines = append(*lines, jtable(el)...)
		*lines = append(*lines, "")
	case name == "disp-formula":
		*lines = append(*lines, "", "    "+jclean(inline(el)), "")
	case name == "supplementary-material":
		label := "Supplementary material"
		if l := el.child("label"); l != nil {
			label = jclean(inline(l))
		}
		captxt := ""
		if c := el.child("caption"); c != nil {
			captxt = jclean(joinInline(c.kids))
		}
		*lines = append(*lines, strings.TrimSpace(fmt.Sprintf("*%s* %s", label, captxt)), "")
	case name == "fn-group":
		el.iter(func(fn *xnode) {
			if fn.name == "p" {
				if t := jclean(inline(fn)); t != "" {
					*lines = append(*lines, t, "")
				}
			}
		})
	case name == "title" || name == "label" || name == "object-id":
		return
	default:
		hasBlock := false
		for _, c := range el.kids {
			if jatsBlock[c.name] {
				hasBlock = true
				break
			}
		}
		if hasBlock {
			for _, ch := range el.kids {
				jblock(ch, depth, lines)
			}
		} else if t := jclean(inline(el)); t != "" {
			*lines = append(*lines, t, "")
		}
	}
}

func jrefs(back *xnode) []string {
	var out []string
	if back == nil {
		return out
	}
	i := 0
	back.iter(func(ref *xnode) {
		if ref.name != "ref" {
			return
		}
		i++
		var cit *xnode
		for _, tag := range []string{"mixed-citation", "element-citation", "citation", "nlm-citation"} {
			if cit = ref.find(tag); cit != nil {
				break
			}
		}
		if cit == nil {
			return
		}
		glue := pyStrip(cit.text) != ""
		for _, c := range cit.kids {
			if pyStrip(c.tail) != "" {
				glue = true
			}
		}
		var txt string
		if cit.name == "element-citation" || !glue {
			var names []string
			cit.iter(func(n *xnode) {
				if n.name == "name" {
					s, _ := n.findtext("surname")
					g, _ := n.findtext("given-names")
					names = append(names, strings.TrimSpace(s+" "+g))
				}
			})
			cit.iter(func(c *xnode) {
				if c.name == "collab" {
					names = append(names, jclean(inline(c)))
				}
			})
			who := strings.Join(names[:min(6, len(names))], ", ")
			if len(names) > 6 {
				who += " et al."
			}
			bits := []string{who}
			for _, tag := range []string{"article-title", "chapter-title", "source", "year", "volume"} {
				if v := cit.child(tag); v != nil {
					bits = append(bits, jclean(inline(v)))
				}
			}
			fp, _ := cit.findtext("fpage")
			lp, _ := cit.findtext("lpage")
			if fp != "" {
				if lp != "" {
					bits = append(bits, fp+"-"+lp)
				} else {
					bits = append(bits, fp)
				}
			}
			var keep []string
			for _, b := range bits {
				if b != "" {
					keep = append(keep, b)
				}
			}
			txt = strings.Join(keep, ". ")
			for _, p := range cit.children("pub-id") {
				if p.attr("pub-id-type") == "pmid" {
					if p.text != "" {
						txt += " PMID:" + strings.TrimSpace(p.text)
					}
					break
				}
			}
		} else {
			txt = jclean(inline(cit))
		}
		for _, p := range cit.children("pub-id") {
			if p.attr("pub-id-type") == "doi" {
				if p.text != "" && !strings.Contains(txt, p.text) {
					txt += " doi:" + strings.TrimSpace(p.text)
				}
				break
			}
		}
		label, ok := ref.findtext("label")
		if !ok || label == "" {
			label = strconv.Itoa(i)
		}
		out = append(out, label+". "+txt)
	})
	return out
}

type jatsMeta struct {
	Title, Journal, License, DOI, PMCID string
	Authors, Keywords                   []string
}

// jatsConvert returns (markdown, metadata) for a JATS article.
func jatsConvert(b []byte) (string, jatsMeta) {
	doc := parseXML(b)
	var art *xnode
	doc.iter(func(n *xnode) {
		if art == nil && n.name == "article" {
			art = n
		}
	})
	if art == nil {
		art = doc
	}
	meta := jatsMeta{}
	ids := map[string]string{}
	var lines []string
	front := art.child("front")
	am := front.child("article-meta")
	if am != nil {
		if t := am.find("article-title"); t != nil {
			meta.Title = jclean(inline(t))
		}
		for _, c := range am.findAll("contrib") {
			if c.attr("contrib-type") != "author" {
				continue
			}
			if n := c.find("name"); n != nil {
				g, _ := n.findtext("given-names")
				s, _ := n.findtext("surname")
				meta.Authors = append(meta.Authors, strings.TrimSpace(g+" "+s))
			} else if cl := c.child("collab"); cl != nil {
				meta.Authors = append(meta.Authors, jclean(inline(cl)))
			}
		}
		for _, aid := range am.children("article-id") {
			ids[orDefault(aid.attr("pub-id-type"), "id")] = strings.TrimSpace(aid.text)
		}
		if j := front.find("journal-title"); j != nil {
			meta.Journal = jclean(inline(j))
		}
		if lic := am.find("license"); lic != nil {
			meta.License = lic.xlinkHref()
			if meta.License == "" {
				meta.License = runeCut(jclean(inline(lic)), 300)
			}
		}
		for _, k := range am.findAll("kwd") {
			meta.Keywords = append(meta.Keywords, jclean(inline(k)))
		}
	}
	meta.DOI = ids["doi"]
	lines = append(lines, strings.TrimRight("# "+meta.Title, " \t\n"), "")
	if len(meta.Authors) > 0 {
		lines = append(lines, strings.Join(meta.Authors, ", "), "")
	}
	bits := []string{meta.Journal}
	if meta.DOI != "" {
		bits = append(bits, "doi:"+meta.DOI)
	}
	if ids["pmcid"] != "" || ids["pmc"] != "" {
		if ids["pmcid"] != "" {
			bits = append(bits, ids["pmcid"])
		} else {
			bits = append(bits, "PMC"+strings.TrimLeft(ids["pmc"], "PMC"))
		}
	}
	if meta.License != "" {
		bits = append(bits, "license: "+meta.License)
	}
	var keep []string
	for _, b := range bits {
		if b != "" {
			keep = append(keep, b)
		}
	}
	lines = append(lines, strings.Join(keep, " | "), "")
	if am != nil {
		for _, ab := range am.children("abstract") {
			kind := ab.attr("abstract-type")
			if kind == "toc" || kind == "teaser" {
				continue
			}
			head := "Abstract"
			if kind != "" {
				head = "Abstract (" + kind + ")"
			}
			lines = append(lines, "## "+head, "")
			for _, ch := range ab.kids {
				jblock(ch, 2, &lines)
			}
		}
		if len(meta.Keywords) > 0 {
			lines = append(lines, "**Keywords:** "+strings.Join(meta.Keywords, "; "), "")
		}
	}
	if body := art.child("body"); body != nil {
		for _, ch := range body.kids {
			jblock(ch, 1, &lines)
		}
	}
	if back := art.child("back"); back != nil {
		for _, ch := range back.kids {
			if ch.name != "ref-list" {
				jblock(ch, 1, &lines)
			}
		}
		if refs := jrefs(back); len(refs) > 0 {
			lines = append(lines, "", "## References", "")
			lines = append(lines, refs...)
		}
	}
	md := pyStrip(nl3.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")) + "\n"
	return md, meta
}

func re2Replace(re *regexp2.Regexp, s, rep string) string {
	out, err := re.Replace(s, rep, -1, -1)
	if err != nil {
		return s
	}
	return out
}

func mdToText(md string) string {
	t := re2Replace(mdHead, md, "")
	t = mdLink.ReplaceAllString(t, "$1 ($2)")
	t = re2Replace(mdMarks, t, "")
	t = re2Replace(mdTblSep, t, "")
	t = strings.ReplaceAll(t, " | ", "\t")
	t = strings.ReplaceAll(t, "| ", "")
	t = strings.ReplaceAll(t, " |", "")
	return pyStrip(nl3.ReplaceAllString(t, "\n\n")) + "\n"
}
