package main

// Enrichment steps: "openalex" (citations, exact dates) and "preprint-links"
// (bioRxiv / medRxiv -> journal DOI). Mirrors enrich_openalex.py and
// enrich_preprints.py.

import (
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

var citationCols = []string{"uid", "doi", "cited_by", "publication_date", "oa_status", "oa_url", "openalex_id", "checked_on"}

func stepOpenAlex(args []string, log *Logger) error {
	rows, _ := readCSV(rpath("progress.csv"))
	byDOI := map[string]string{}
	for _, r := range rows {
		if r["doi"] != "" {
			byDOI[strings.ToLower(r["doi"])] = r["uid"]
		}
	}
	oldRows, _ := readCSV(rpath("corpus", "citations.csv"))
	out := map[string]Row{}
	for _, r := range oldRows {
		out[r["uid"]] = r
	}
	c := NewClient(150*time.Millisecond, nil, log)
	base := url.Values{"select": {"doi,cited_by_count,publication_date,open_access,id"}, "per_page": {"50"}}
	if contact() != "" {
		base.Set("mailto", contact())
	}
	if k := os.Getenv("OPENALEX_API_KEY"); k != "" {
		base.Set("api_key", k)
	}
	dois := sortedKeys(byDOI)
	for i := 0; i < len(dois); i += 50 {
		var chunk []string
		for _, d := range dois[i:min(i+50, len(dois))] {
			if !strings.ContainsAny(d, "|,") {
				chunk = append(chunk, d)
			}
		}
		p := url.Values{}
		for k, v := range base {
			p[k] = v
		}
		p.Set("filter", "doi:"+strings.Join(chunk, "|"))
		r := c.Get("https://api.openalex.org/works", reqOpts{params: p})
		if r == nil || r.Status != 200 {
			log.Printf("OpenAlex failed at %d", i)
			continue
		}
		for _, w := range jlist(decodeJSON(r.Body)["results"]) {
			wm := ymap(w)
			d := normDOI(jstr(wm, "doi"))
			uid := byDOI[d]
			if uid == "" {
				continue
			}
			oa := jmap(wm, "open_access")
			cited, _ := toInt(wm["cited_by_count"])
			out[uid] = Row{"uid": uid, "doi": d, "cited_by": itoa(cited), "publication_date": jstr(wm, "publication_date"),
				"oa_status": jstr(oa, "oa_status"), "oa_url": jstr(oa, "oa_url"), "openalex_id": jstr(wm, "id"),
				"checked_on": today()}
		}
		if (i/50)%50 == 49 {
			log.Printf("  %d/%d", i+50, len(dois))
		}
	}
	keys := sortedKeys(out)
	res := make([]Row, len(keys))
	for i, k := range keys {
		res[i] = out[k]
	}
	if err := writeCSV(rpath("corpus", "citations.csv"), res, citationCols); err != nil {
		return err
	}
	log.Printf("%d records with OpenAlex data (%d DOIs looked up)", len(out), len(dois))
	return nil
}

var preprintCols = []string{"preprint_doi", "server", "published_doi", "published_date", "checked_on"}

func stepPreprintLinks(args []string, log *Logger) error {
	o := parseStepArgs(args)
	recheck := 30
	if v := o["recheck-days"]; v != "" {
		recheck = atoiSafe(v)
	}
	rows, _ := readCSV(rpath("progress.csv"))
	set := map[string]bool{}
	for _, r := range rows {
		if strings.HasPrefix(r["doi"], "10.1101/") {
			set[r["doi"]] = true
		}
	}
	dois := sortedKeys(set)
	oldRows, _ := readCSV(rpath("corpus", "preprint_links.csv"))
	old := map[string]Row{}
	for _, r := range oldRows {
		old[r["preprint_doi"]] = r
	}
	cutoff := time.Now().AddDate(0, 0, -recheck).Format("2006-01-02")
	var todo []string
	for _, d := range dois {
		r, ok := old[d]
		if !ok || (r["published_doi"] == "" && r["checked_on"] < cutoff) {
			todo = append(todo, d)
		}
	}
	log.Printf("%d bioRxiv/medRxiv DOIs; %d to check", len(dois), len(todo))
	c := NewClient(400*time.Millisecond, nil, log)
	write := func() error {
		keys := sortedKeys(old)
		res := make([]Row, len(keys))
		for i, k := range keys {
			res[i] = old[k]
		}
		return writeCSV(rpath("corpus", "preprint_links.csv"), res, preprintCols)
	}
	for i, d := range todo {
		row := Row{"preprint_doi": d, "server": "", "published_doi": "", "published_date": "", "checked_on": today()}
		for _, server := range []string{"biorxiv", "medrxiv"} {
			r := c.Get("https://api.biorxiv.org/details/"+server+"/"+d, reqOpts{tries: 3})
			if r == nil || r.Status != 200 {
				continue
			}
			coll := jlist(decodeJSON(r.Body)["collection"])
			if len(coll) == 0 {
				continue
			}
			row["server"] = server
			for j := len(coll) - 1; j >= 0; j-- {
				p := jstr(ymap(coll[j]), "published")
				if p != "" && p != "NA" {
					row["published_doi"] = normDOI(p)
					break
				}
			}
			break
		}
		if row["published_doi"] != "" {
			r := c.Get("https://api.biorxiv.org/pubs/"+row["server"]+"/"+d, reqOpts{tries: 2})
			if r != nil && r.Status == 200 {
				if coll := jlist(decodeJSON(r.Body)["collection"]); len(coll) > 0 {
					row["published_date"] = jstr(ymap(coll[0]), "published_date")
				}
			}
		}
		old[d] = row
		if (i+1)%200 == 0 {
			_ = write()
			log.Printf("  %d/%d", i+1, len(todo))
		}
	}
	if err := write(); err != nil {
		return err
	}
	n := 0
	for _, r := range old {
		if r["published_doi"] != "" {
			n++
		}
	}
	log.Printf("%d preprints checked; %d have a published version", len(old), n)
	return nil
}

func itoa(i int) string { return strconv.Itoa(i) }

var _ = sort.Strings
