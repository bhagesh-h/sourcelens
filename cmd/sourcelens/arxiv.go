package main

// arXiv search ("arxiv" step). Python twin: src/sourcelens/pullliturature/search_arxiv.py.

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const arxivAPI = "https://export.arxiv.org/api/query"

type atomFeed struct {
	Entries []struct {
		ID        string `xml:"id"`
		Title     string `xml:"title"`
		Summary   string `xml:"summary"`
		Published string `xml:"published"`
		DOI       string `xml:"http://arxiv.org/schemas/atom doi"`
		Authors   []struct {
			Name string `xml:"name"`
		} `xml:"author"`
		Categories []struct {
			Term string `xml:"term,attr"`
		} `xml:"category"`
	} `xml:"entry"`
}

var arxivVer = regexp.MustCompile(`v\d+$`)

func arxivSearch(c *Client, terms []string, start, end string) []Rec {
	parts := make([]string, len(terms))
	for i, t := range terms {
		parts[i] = arxivTerm(t)
	}
	q := strings.Join(parts, " OR ")
	var out []Rec
	offset := 0
	for {
		r := c.Get(arxivAPI, reqOpts{params: url.Values{"search_query": {q}, "start": {fmt.Sprint(offset)},
			"max_results": {"200"}, "sortBy": {"submittedDate"}, "sortOrder": {"descending"}},
			timeout: 120 * time.Second})
		if r == nil || r.Status != 200 {
			break
		}
		var feed atomFeed
		if xml.Unmarshal(r.Body, &feed) != nil {
			break
		}
		stop := false
		for _, e := range feed.Entries {
			pub := e.Published
			if len(pub) > 10 {
				pub = pub[:10]
			}
			if pub < start {
				stop = true
				continue
			}
			if pub > end {
				continue
			}
			id := e.ID
			if i := strings.LastIndex(id, "/abs/"); i >= 0 {
				id = id[i+5:]
			}
			aid := arxivVer.ReplaceAllString(id, "")
			var cats, names []string
			for _, ct := range e.Categories {
				cats = append(cats, ct.Term)
			}
			for _, a := range e.Authors {
				names = append(names, cleanText(a.Name))
			}
			top := cats
			if len(top) > 3 {
				top = top[:3]
			}
			rec := Rec{
				"epmc_id": aid, "source_db": "ARXIV", "doi": strings.ToLower("10.48550/arxiv." + aid),
				"pmid": "", "pmcid": "", "title": cleanText(e.Title), "authors": strings.Join(names, ", "),
				"journal": "arXiv (" + strings.Join(top, ", ") + ")", "pub_date": pub, "pub_year": pub[:4],
				"pub_types": "preprint", "is_preprint": true, "is_open_access": true,
				"abstract": cleanText(e.Summary), "keywords": strings.Join(cats, "; "),
				"fulltext_urls": []any{"https://arxiv.org/pdf/" + aid}, "related_doi": strings.ToLower(e.DOI),
			}
			rec["uid"] = makeUID(str(rec, "doi"), "", "", "", "")
			out = append(out, rec)
		}
		if stop || len(feed.Entries) < 200 {
			break
		}
		offset += 200
	}
	return out
}

func stepArxiv(args []string, log *Logger) error {
	o := parseStepArgs(args)
	all, _ := loadGroups()
	var groups []Group
	for _, g := range filterGroups(all, o["groups"], "focused") {
		groups = append(groups, g)
	}
	start, end := searchWindow()
	explicit := searchWindowOverridden()
	stateFile := rpath("raw", "arxiv", "state.json")
	var state map[string]any
	if b, err := os.ReadFile(stateFile); err == nil {
		_ = json.Unmarshal(b, &state)
	}
	if last, _ := state["last_complete_run"].(string); last != "" && o["full"] == "" && o["groups"] == "" && !explicit {
		if t, err := time.Parse("2006-01-02", last); err == nil {
			since := t.AddDate(0, 0, -14).Format("2006-01-02")
			if since > start {
				start = since
			}
		}
	}
	log.Printf("arXiv window %s .. %s", start, end)
	c := NewClient(3100*time.Millisecond, nil, log)
	store := LoadStore()
	hits := loadHits()
	run := today()
	for _, g := range groups {
		var req func(string) bool
		if len(g.RequireAny) > 0 {
			req = termMatcher(g.RequireAny)
		}
		kept := 0
		for i := 0; i < len(g.Terms); i += 8 {
			chunk := g.Terms[i:min(i+8, len(g.Terms))]
			phrase := termMatcher(chunk)
			for _, rec := range arxivSearch(c, chunk, start, end) {
				text := str(rec, "title") + " " + str(rec, "abstract")
				if !phrase(text) || (req != nil && !req(text)) {
					continue
				}
				uid := store.Merge(rec, "arxiv")
				if recordHit(hits, uid, g.Name, "focused", "arxiv", run) {
					kept++
				}
			}
		}
		log.Printf("%s: %d new arXiv hits", g.Name, kept)
		if err := store.Save(); err != nil {
			return err
		}
		if err := saveHits(hits); err != nil {
			return err
		}
	}
	if o["groups"] == "" && !explicit {
		_ = os.MkdirAll(filepath.Dir(stateFile), 0o755)
		_ = os.WriteFile(stateFile, []byte(fmt.Sprintf(`{"last_complete_run": "%s", "window": ["%s", "%s"]}`, run, start, end)), 0o644)
	}
	log.Printf("done: store %d records", store.Len())
	return nil
}
