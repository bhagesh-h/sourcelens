package main

// Attachments of a paper: figures, tables and supplementary files of articles
// in the PMC open-access bucket. Python twin:
// src/sourcelens/pullliturature/attachments.py.
//
// Downloaded files go to <paper folder>/attachments/<file>; every file offered,
// downloaded or not, is one row of <catalogue>/fulltext/attachments_index.csv
// (status listed, ok, skipped, failed, moved, deleted, or none for a paper
// without attachments).

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	attColumns = []string{"uid", "file", "ext", "kind", "label", "caption", "bytes", "status", "path", "url",
		"checked_on", "reason"}
	imageExts  = map[string]bool{"bmp": true, "eps": true, "gif": true, "jpeg": true, "jpg": true, "png": true, "svg": true, "tif": true, "tiff": true, "webp": true}
	attTargets = map[string]string{"fig": "figure", "table-wrap": "table", "supplementary-material": "supplementary"}
	hrefTags   = map[string]bool{"graphic": true, "media": true, "inline-graphic": true, "inline-supplementary-material": true, "supplementary-material": true}
	unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

const captionMax = 300

func attIndexPath() string { return rpath("fulltext", "attachments_index.csv") }

// readAttIndex: uid -> its attachment rows, in file order.
func readAttIndex() map[string][]Row {
	out := map[string][]Row{}
	rows, _ := readCSV(attIndexPath())
	for _, r := range rows {
		out[r["uid"]] = append(out[r["uid"]], r)
	}
	return out
}

func writeAttIndex(m map[string][]Row) error {
	var rows []Row
	for _, uid := range sortedKeys(m) {
		rows = append(rows, m[uid]...)
	}
	return writeCSV(attIndexPath(), rows, attColumns)
}

func baseName(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

func fileExt(name string) string {
	b := baseName(name)
	if i := strings.LastIndex(b, "."); i >= 0 {
		return strings.ToLower(b[i+1:])
	}
	return ""
}

func fileStem(name string) string {
	b := baseName(name)
	if i := strings.LastIndex(b, "."); i >= 0 {
		b = b[:i]
	}
	return strings.ToLower(b)
}

// itertext: the text of a node and its descendants (lxml el.itertext()).
func (n *xnode) itertext() []string {
	out := []string{n.text}
	for _, k := range n.kids {
		out = append(out, k.itertext()...)
		out = append(out, k.tail)
	}
	return out
}

func collapse(parts []string) string {
	return strings.Join(strings.FieldsFunc(strings.Join(parts, " "), pyIsSpace), " ")
}

type attDesc struct{ kind, label, caption string }

// attCaptions: file reference (lower case) -> kind, label, caption from the
// article XML; each reference is keyed as written and without its last
// extension, and the first element (document order) naming a key wins.
func attCaptions(xmlBytes []byte) map[string]attDesc {
	out := map[string]attDesc{}
	if len(xmlBytes) == 0 {
		return out
	}
	root := parseXML(xmlBytes)
	root.iter(func(el *xnode) {
		kind, ok := attTargets[el.name]
		if !ok {
			return
		}
		label, caption := "", ""
		if l := el.child("label"); l != nil {
			label = collapse(l.itertext())
		}
		if c := el.child("caption"); c != nil {
			caption = runeCut(collapse(c.itertext()), captionMax)
		}
		el.iter(func(d *xnode) {
			if !hrefTags[d.name] {
				return
			}
			href := d.xlinkHref()
			if href == "" {
				return
			}
			for _, key := range []string{strings.ToLower(baseName(href)), fileStem(href)} {
				if _, seen := out[key]; !seen {
					out[key] = attDesc{kind, label, caption}
				}
			}
		})
	})
	return out
}

// describe: kind, label, caption of a file, from the XML or else by extension.
func describe(name string, caps map[string]attDesc) attDesc {
	for _, key := range []string{strings.ToLower(baseName(name)), fileStem(name)} {
		if d, ok := caps[key]; ok {
			return d
		}
	}
	if imageExts[fileExt(name)] {
		return attDesc{kind: "figure"}
	}
	return attDesc{kind: "supplementary"}
}

// attSummary: the attachments column, "downloaded/available: ext count, ..."
// ("" when unknown).
func attSummary(rows []Row) string {
	if len(rows) == 0 {
		return ""
	}
	ok, n := 0, 0
	exts := map[string]int{}
	for _, r := range rows {
		if r["file"] == "" {
			continue
		}
		n++
		if r["status"] == "ok" {
			ok++
		}
		exts[orDefault(r["ext"], "?")]++
	}
	head := fmt.Sprintf("%d/%d", ok, n)
	if len(exts) == 0 {
		return head
	}
	var parts []string
	for _, e := range sortedKeys(exts) {
		parts = append(parts, e+" "+strconv.Itoa(exts[e]))
	}
	return head + ": " + strings.Join(parts, ", ")
}

// summaryExts: extensions named in an attachments column value.
func summaryExts(v string) map[string]bool {
	out := map[string]bool{}
	_, rest, ok := strings.Cut(v, ":")
	if !ok {
		return out
	}
	for _, p := range strings.Split(rest, ",") {
		if f := strings.Fields(p); len(f) > 0 {
			out[strings.ToLower(f[0])] = true
		}
	}
	return out
}

func humanBytes(s string) string {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return ""
	}
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	f := float64(n)
	for _, unit := range []string{"KB", "MB", "GB"} {
		f /= 1024
		if f < 1024 || unit == "GB" {
			return fmt.Sprintf("%.1f %s", f, unit)
		}
	}
	return ""
}

// safeName: a bucket file name that is safe to write (no folders, no odd characters).
func safeName(name string) string {
	s := strings.Trim(unsafeName.ReplaceAllString(baseName(name), "_"), "._")
	if s == "" {
		return "file"
	}
	return s
}

// urlQuote: python's urllib.parse.quote(s) (safe "/").
func urlQuote(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '.', c == '-', c == '~', c == '/':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func noneRow(uid, reason string) Row {
	return Row{"uid": uid, "file": "", "ext": "", "kind": "", "label": "", "caption": "", "bytes": "",
		"status": "none", "path": "", "url": "", "checked_on": today(), "reason": reason}
}

// attColumn: the fulltext_index.csv attachments column, downloaded/offered.
func attColumn(rows []Row) string {
	ok, n := 0, 0
	for _, r := range rows {
		if r["file"] == "" {
			continue
		}
		n++
		if r["status"] == "ok" {
			ok++
		}
	}
	return fmt.Sprintf("%d/%d", ok, n)
}

// attMeta: the attachments list of metadata.json.
func attMeta(rows []Row) []any {
	out := []any{}
	for _, r := range rows {
		if r["file"] == "" {
			continue
		}
		o := omap{}
		for _, k := range []string{"file", "kind", "label", "caption", "bytes", "status", "url"} {
			var v any = r[k]
			if k == "bytes" {
				if n, err := strconv.Atoi(r[k]); err == nil {
					v = n
				}
			}
			o = append(o, okv{k, v})
		}
		out = append(out, o)
	}
	return out
}
