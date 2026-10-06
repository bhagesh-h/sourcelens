package main

// Overview rows: the catalogue with what each row is about and what is
// pending, for the dry run and the run report. Python twin:
// src/sourcelens/buildcatalog/overview.py.

import (
	"io"
	"os"
	"strings"
	"time"
)

var overviewExtra = []string{"summary", "summary_from", "keywords", "fulltext_sources", "pending"}

const summaryMax = 2000

func collapseText(s string) string { return strings.Join(strings.FieldsFunc(s, pyIsSpace), " ") }

func recItems(v any) []string {
	var out []string
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			if s := pyStrip(anyString(x)); s != "" {
				out = append(out, s)
			}
		}
	case string:
		for _, x := range strings.Split(t, ";") {
			if s := pyStrip(x); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func anyString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		b, _ := jsonMarshal(t)
		return strings.Trim(string(b), `"`)
	}
}

func fulltextExcerpt(r Row) string {
	for _, col := range []string{"fulltext_txt", "fulltext_md"} {
		rel := r[col]
		if rel == "" {
			continue
		}
		f, err := os.Open(researchFile(rel))
		if err != nil {
			continue
		}
		buf := make([]byte, 20000)
		n, _ := io.ReadFull(f, buf)
		f.Close()
		if text := collapseText(strings.ToValidUTF8(string(buf[:n]), "")); text != "" {
			return runeCut(text, summaryMax)
		}
	}
	return ""
}

// overviewDescriptions: uid -> description of repositories, packages and websites.
func overviewDescriptions() map[string]string {
	out := map[string]string{}
	repos, _ := readCSV(rpath("repos", "repositories.csv"))
	for _, r := range repos {
		if r["uid"] != "" && r["description"] != "" {
			out[r["uid"]] = collapseText(r["description"])
		}
	}
	sites, _ := readCSV(rpath("websites", "websites.csv"))
	for _, r := range sites {
		if r["url"] == "" {
			continue
		}
		var parts []string
		for _, k := range []string{"page_title", "page_description", "note"} {
			if r[k] != "" {
				parts = append(parts, r[k])
			}
		}
		text := collapseText(strings.Join(parts, " "))
		if text == "" {
			continue
		}
		if uid := makeUID("", "", "", "", r["url"]); uid != "" {
			if _, ok := out[uid]; !ok {
				out[uid] = text
			}
		}
	}
	return out
}

func fulltextSources(rec Rec) string {
	doi := strings.ToLower(str(rec, "doi"))
	var out []string
	if str(rec, "pmcid") != "" {
		out = append(out, "pmc")
	}
	if strings.HasPrefix(doi, "10.1101/") {
		out = append(out, "biorxiv")
	}
	for _, e := range strList(rec, "epmc_ids") {
		if strings.HasPrefix(e, "PPR/") {
			out = append(out, "europepmc")
			break
		}
	}
	if strings.HasPrefix(doi, "10.48550/arxiv.") {
		out = append(out, "arxiv")
	}
	if doi != "" {
		out = append(out, "unpaywall")
	}
	if str(rec, "oa_pdf_url") != "" {
		out = append(out, "openalex")
	}
	return strings.Join(out, ",")
}

func pendingOf(r Row, idx Row, atts []Row, cutoff string) string {
	if !paperTypeSet[r["resource_type"]] {
		return ""
	}
	var out []string
	status := idx["status"]
	switch {
	case idx == nil:
		out = append(out, "full text")
	case status == "partial" || status == "deferred" || status == "error" || (status == "none" && idx["checked_on"] < cutoff):
		out = append(out, "full text (retry)")
	}
	if r["pmcid"] != "" && status != "none" && status != "removed" {
		failed, listed := false, false
		for _, a := range atts {
			if a["status"] == "failed" {
				failed = true
			}
			if a["status"] == "listed" && a["reason"] != "extension not selected" {
				listed = true
			}
		}
		switch {
		case failed:
			out = append(out, "attachments (retry)")
		case idx["attachments"] == "" || listed:
			out = append(out, "attachments")
		}
	}
	return strings.Join(out, "; ")
}

// overviewRows: the overview rows of the current catalogue (progress.csv columns
// + overviewExtra).
func overviewRows(progress []Row) []Row {
	if progress == nil {
		progress, _ = readCSV(rpath("progress.csv"))
	}
	store := LoadStore()
	idxRows, _ := readCSV(rpath("fulltext", "fulltext_index.csv"))
	index := map[string]Row{}
	for _, r := range idxRows {
		index[r["uid"]] = r
	}
	atts := readAttIndex()
	desc := overviewDescriptions()
	cutoff := time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	out := make([]Row, 0, len(progress))
	for _, r := range progress {
		uid := r["uid"]
		rec := store.Get(uid)
		if rec == nil {
			rec = Rec{}
		}
		summary, from := runeCut(collapseText(str(rec, "abstract")), summaryMax), "abstract"
		if summary == "" {
			summary, from = fulltextExcerpt(r), "fulltext"
		}
		if summary == "" {
			summary, from = runeCut(desc[uid], summaryMax), "description"
		}
		if summary == "" {
			from = ""
		}
		var kw []string
		seen := map[string]bool{}
		for _, k := range append(recItems(rec["keywords"]), recItems(rec["mesh"])...) {
			if !seen[k] {
				seen[k] = true
				kw = append(kw, k)
			}
		}
		o := Row{}
		for k, v := range r {
			o[k] = v
		}
		o["summary"], o["summary_from"], o["keywords"] = summary, from, strings.Join(kw, "; ")
		if paperTypeSet[r["resource_type"]] {
			o["fulltext_sources"] = fulltextSources(rec)
		} else {
			o["fulltext_sources"] = ""
		}
		o["pending"] = pendingOf(r, index[uid], atts[uid], cutoff)
		out = append(out, o)
	}
	return out
}
