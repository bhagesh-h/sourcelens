package main

// Websites, databases and calculators ("websites" step). Python twin: src/sourcelens/websites/build_websites.py.

import (
	"encoding/json"
	"fmt"
	"html"
	"mime"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

func siteUA() string { return "Mozilla/5.0 (X11; Linux x86_64) sourcelens/" + version }

var (
	siteCols = []string{"name", "type", "url", "status", "page_title", "page_description", "first_archived",
		"source", "n_papers", "included", "note", "related_doi", "checked_on"}
	siteTitle  = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	siteDesc   = regexp.MustCompile(`(?i)<meta[^>]+name=["']description["'][^>]+content=["']([^"']*)`)
	siteOGDesc = regexp.MustCompile(`(?i)<meta[^>]+property=["']og:description["'][^>]+content=["']([^"']*)`)
	siteScheme = regexp.MustCompile(`^https?://(www\.)?`)
)

type siteCfg struct {
	Name       string   `yaml:"name"`
	Type       string   `yaml:"type"`
	URLs       []string `yaml:"urls"`
	Note       string   `yaml:"note"`
	RelatedDOI string   `yaml:"related_doi"`
}

// decodePage follows requests: a declared charset is used, a missing or
// ISO-8859-1 one is replaced by detection (UTF-8 when the bytes are valid UTF-8).
func decodePage(r *Resp) string {
	cs := ""
	if _, p, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil {
		cs = strings.ToLower(p["charset"])
	}
	latin1 := func(b []byte) string {
		rs := make([]rune, len(b))
		for i, c := range b {
			rs[i] = rune(c)
		}
		return string(rs)
	}
	switch cs {
	case "", "iso-8859-1", "latin-1", "latin1":
		if utf8.Valid(r.Body) {
			return string(r.Body)
		}
		return latin1(r.Body)
	case "utf-8", "utf8":
		return strings.ToValidUTF8(string(r.Body), "�")
	case "windows-1252", "cp1252", "us-ascii", "ascii":
		return latin1(r.Body)
	}
	return strings.ToValidUTF8(string(r.Body), "�")
}

func siteClean(s string) string {
	if s == "" {
		return ""
	}
	return runeCut(strings.Join(strings.FieldsFunc(html.UnescapeString(s), pyIsSpace), " "), 300)
}

// siteFetch returns (status, final url, title, description); status 0 on network failure.
func siteFetch(c *Client, u string) (int, string, string, string) {
	r := c.Get(u, reqOpts{timeout: 40 * time.Second, tries: 2, no404: true})
	if r == nil {
		return 0, u, "", ""
	}
	if r.Status != 200 {
		return r.Status, u, "", ""
	}
	text := runeCut(decodePage(r), 400_000)
	t, d := "", ""
	if m := siteTitle.FindStringSubmatch(text); m != nil {
		t = m[1]
	}
	if m := siteDesc.FindStringSubmatch(text); m != nil {
		d = m[1]
	} else if m := siteOGDesc.FindStringSubmatch(text); m != nil {
		d = m[1]
	}
	return 200, r.URL, siteClean(t), siteClean(d)
}

func firstArchived(c *Client, u string) string {
	bare := strings.TrimRight(siteScheme.ReplaceAllString(u, ""), "/")
	r := c.Get("https://web.archive.org/cdx/search/cdx", reqOpts{params: url.Values{"url": {bare}, "output": {"json"},
		"limit": {"1"}, "fl": {"timestamp"}}, timeout: 60 * time.Second, tries: 3})
	var rows [][]string
	if r != nil && r.Status == 200 {
		_ = json.Unmarshal(r.Body, &rows)
	}
	if len(rows) > 1 && len(rows[1]) > 0 {
		ts := rows[1][0]
		if len(ts) >= 8 {
			return ts[:4] + "-" + ts[4:6] + "-" + ts[6:8]
		}
	}
	return ""
}

// pyListRepr formats a list of strings as python's repr(list) does.
func pyListRepr(xs []string) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		q := "'"
		if strings.Contains(x, "'") && !strings.Contains(x, `"`) {
			q = `"`
		}
		e := strings.ReplaceAll(x, `\`, `\\`)
		if q == "'" {
			e = strings.ReplaceAll(e, "'", `\'`)
		}
		parts[i] = q + e + q
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func stepWebsites(args []string, log *Logger) error {
	o := parseStepArgs(args)
	minPapers := atoiSafe(orDefault(o["min-papers"], "3"))
	workers := max(1, atoiSafe(orDefault(o["workers"], "8")))
	cutoff := time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	ua := map[string]string{"User-Agent": siteUA()}
	h := NewClient(time.Second, ua, log)
	clf := LoadClassifier()
	prevRows, _ := readCSV(rpath("websites", "websites.csv"))
	previous := map[string]Row{}
	for _, r := range prevRows {
		previous[r["url"]] = r
	}
	var cfg struct {
		Sites     []siteCfg `yaml:"sites"`
		Relevance string    `yaml:"relevance"`
	}
	if err := loadConfig("websites", &cfg); err != nil {
		return err
	}
	// a site cited by papers is kept when its title or description matches
	// websites.relevance, else repos.relevance
	relPat := cfg.Relevance
	if relPat == "" {
		relPat = loadRepoConfig().Relevance
	}
	siteRelevant := pyRe(orDefault(relPat, `(?!)`))
	var out []Row
	for _, site := range cfg.Sites {
		row := Row{"name": site.Name, "type": orDefault(site.Type, "website"), "source": "curated",
			"note": site.Note, "related_doi": site.RelatedDOI}
		var tried []Row
		for _, u := range site.URLs {
			st, _, title, desc := siteFetch(h, u)
			tried = append(tried, Row{"url": u, "status": strconv.Itoa(st), "page_title": title, "page_description": desc})
			if st == 200 {
				break
			}
		}
		pick := tried[0]
		for _, t := range tried {
			if t["status"] == "200" {
				pick = t
				break
			}
		}
		for k, v := range pick {
			row[k] = v
		}
		out = append(out, row)
	}
	for _, row := range out {
		row["checked_on"] = today()
	}
	seen := map[string]bool{}
	for _, r := range out {
		seen[strings.SplitN(siteScheme.ReplaceAllString(r["url"], ""), "/", 2)[0]] = true
	}
	type cand struct {
		c    Row
		home string
	}
	var todo []cand
	cands, _ := readCSV(rpath("websites", "candidate_websites.csv"))
	for _, c := range cands {
		if atoiSafe(c["n_papers"]) < minPapers || seen[strings.ReplaceAll(c["domain"], "www.", "")] {
			continue
		}
		home := "https://" + c["domain"] + "/"
		if old, ok := previous[home]; ok && old["checked_on"] >= cutoff {
			// checked recently: reuse instead of fetching every site on every run
			row := Row{}
			for k, v := range old {
				row[k] = v
			}
			row["n_papers"], row["status"] = c["n_papers"], strconv.Itoa(atoiSafe(old["status"]))
			out = append(out, row)
		} else {
			todo = append(todo, cand{c, home})
		}
	}
	log.Printf("%d candidate sites to check (%d curated or recently checked)", len(todo), len(out))
	checked := make([]Row, len(todo))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(workers, max(1, len(todo))); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := NewClient(time.Second, ua, log)
			for i := range jobs {
				x := todo[i]
				st, _, title, desc := siteFetch(c, x.home)
				checked[i] = Row{"name": orDefault(title, x.c["domain"]), "type": "website (linked from papers)",
					"url": x.home, "status": strconv.Itoa(st), "page_title": title, "page_description": desc,
					"source": "paper links", "n_papers": x.c["n_papers"], "note": runeCut(x.c["example_context"], 200),
					"checked_on": today()}
			}
		}()
	}
	for i := range todo {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	out = append(out, checked...)

	var cat []Row
	for _, row := range out {
		curated := row["source"] == "curated"
		relevant := curated || reSearch(siteRelevant, row["page_title"]+" "+row["page_description"])
		old := previous[row["url"]]
		link, availability := row["url"], ""
		status := atoiSafe(row["status"])
		var included bool
		switch {
		case status == 200:
			included = relevant
		case curated && (status == 401 || status == 403 || status == 429):
			included = true
			availability = fmt.Sprintf("site refuses automated requests (HTTP %d); open in a browser", status)
		case curated:
			// offline: keep it if the Internet Archive has a copy
			row["first_archived"] = old["first_archived"]
			if row["first_archived"] == "" {
				row["first_archived"] = firstArchived(h, row["url"])
			}
			included = row["first_archived"] != ""
			if included {
				link = "https://web.archive.org/web/" + strings.ReplaceAll(row["first_archived"], "-", "") + "/" + row["url"]
				availability = "offline; link points to the earliest Internet Archive copy"
			}
		}
		row["included"] = pyBool(included)
		if !included {
			continue
		}
		if row["first_archived"] == "" {
			row["first_archived"] = old["first_archived"]
		}
		if row["first_archived"] == "" {
			row["first_archived"] = firstArchived(h, row["url"])
		}
		date := row["first_archived"]
		ann := clf.Annotate(Rec{"title": row["name"], "abstract": row["page_title"] + " " + row["page_description"] + " " + row["note"]})
		rtype := "website"
		if strings.Contains(row["type"], "database") {
			rtype = "database"
		} else if strings.Contains(row["type"], "calculator") {
			rtype = "web calculator"
		}
		tier, foundBy := "related", "linked from papers"
		if curated {
			tier, foundBy = "core", "curated list (websites section of the configuration)"
		}
		title := row["name"]
		if row["page_title"] != "" && strings.ToLower(row["page_title"]) != strings.ToLower(row["name"]) {
			title = row["name"] + ": " + row["page_title"]
		}
		dated := "undated (no archive snapshot)"
		if date != "" {
			dated = "first archived " + date + " (domain age; may predate its use for this topic)"
		}
		cited := ""
		if row["n_papers"] != "" {
			cited = "cited by " + row["n_papers"] + " catalogued papers"
		}
		cat = append(cat, Row{"date": date, "year": runeCut(date, 4), "resource_type": rtype, "tier": tier,
			"category": "software/resource", "modality": ann.Modality, "entities": ann.Entities, "species": ann.Species,
			"title": title, "venue": row["type"], "url": link, "related": row["related_doi"], "open_access": "yes",
			"found_by": foundBy, "details": joinNonEmpty("; ", availability, dated, cited, runeCut(row["note"], 200)),
			"uid": makeUID("", "", "", "", row["url"])})
	}
	if err := writeCSV(rpath("websites", "websites.csv"), out, siteCols); err != nil {
		return err
	}
	sort.SliceStable(cat, func(i, j int) bool { return orDefault(cat[i]["date"], "9999") < orDefault(cat[j]["date"], "9999") })
	if err := writeCSV(rpath("websites", "websites_catalogue.csv"), cat, progressCols); err != nil {
		return err
	}
	var down []string
	for _, r := range out {
		if r["source"] == "curated" && r["status"] != "200" {
			down = append(down, r["name"])
		}
	}
	log.Printf("%d sites checked; %d catalogued; unreachable: %s", len(out), len(cat), pyListRepr(down))
	return nil
}
