package main

// GitHub repository search ("github-search" step). Mirrors python/pullrepos/search_github.py.

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dlclark/regexp2"
)

var ghSearchCols = []string{"full_name", "url", "description", "topics", "stars", "created_at", "pushed_at",
	"language", "license", "fork", "archived", "queries", "relevant", "first_seen", "last_seen"}

func ghClient(interval time.Duration, log *Logger) *Client {
	h := map[string]string{"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"}
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		h["Authorization"] = "Bearer " + t
	}
	return NewClient(interval, h, log)
}

type repoConfig struct {
	Queries      []string            `yaml:"github_queries"`
	Relevance    string              `yaml:"relevance"`
	PackageTerms string              `yaml:"package_terms"`
	Known        map[string][]string `yaml:"known_packages"`
}

func loadRepoConfig() repoConfig {
	var c repoConfig
	_ = loadConfig("repos", &c)
	return c
}

func pyRe(p string) *regexp2.Regexp { return regexp2.MustCompile(p, regexp2.None) }

func pyBool(b bool) string {
	if b {
		return "True"
	}
	return "False"
}

func ghSearch(c *Client, q, created string, log *Logger) []map[string]any {
	query := q
	if !strings.HasPrefix(q, "topic:") {
		query = fmt.Sprintf(`"%s" in:name,description,topics,readme`, q)
	}
	if created != "" {
		query += " created:" + created
	}
	var out []map[string]any
	for page := 1; page <= 10; {
		r := c.Get("https://api.github.com/search/repositories", reqOpts{params: url.Values{"q": {query},
			"per_page": {"100"}, "page": {strconv.Itoa(page)}, "sort": {"stars"}}})
		if r == nil {
			break
		}
		if r.Status == 403 {
			wait := atoiSafe(r.Header.Get("Retry-After"))
			if wait == 0 {
				wait = 60
			}
			log.Printf("rate limited; sleeping %ds", wait)
			time.Sleep(time.Duration(wait) * time.Second)
			continue
		}
		items := jlist(decodeJSON(r.Body)["items"])
		for _, it := range items {
			out = append(out, ymap(it))
		}
		if len(items) < 100 {
			break
		}
		page++
	}
	return out
}

func stepGithubSearch(args []string, log *Logger) error {
	cfg := loadRepoConfig()
	rel := pyRe(cfg.Relevance)
	iv := 6500 * time.Millisecond
	tok := "no (slow, 10 searches/min)"
	if os.Getenv("GITHUB_TOKEN") != "" {
		iv, tok = 2100*time.Millisecond, "yes"
	}
	c := ghClient(iv, log)
	log.Printf("GitHub token: %s", tok)
	out := rpath("repos", "github_search.csv")
	old, _ := readCSV(out)
	rows := map[string]Row{}
	for _, r := range old {
		rows[strings.ToLower(r["full_name"])] = r
	}
	run := today()
	created := ""
	if searchWindowOverridden() {
		s, e := searchWindow()
		created = s + ".." + e
		log.Printf("repositories created %s", created)
	}
	write := func() error {
		keys := make([]string, 0, len(rows))
		for k := range rows {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		res := make([]Row, len(keys))
		for i, k := range keys {
			res[i] = rows[k]
		}
		return writeCSV(out, res, ghSearchCols)
	}
	for _, q := range cfg.Queries {
		items := ghSearch(c, q, created, log)
		kept := 0
		for _, it := range items {
			key := strings.ToLower(jstr(it, "full_name"))
			var topics []string
			for _, t := range jlist(it["topics"]) {
				if s, ok := t.(string); ok {
					topics = append(topics, s)
				}
			}
			text := strings.Join([]string{jstr(it, "name"), jstr(it, "description"), strings.Join(topics, " ")}, " ")
			relevant := reSearch(rel, text)
			row := rows[key]
			if row == nil {
				row = Row{"full_name": jstr(it, "full_name"), "queries": "", "first_seen": run}
			}
			stars, _ := toInt(it["stargazers_count"])
			fork, _ := it["fork"].(bool)
			arch, _ := it["archived"].(bool)
			row["url"] = jstr(it, "html_url")
			row["description"] = strings.ReplaceAll(jstr(it, "description"), "\n", " ")
			row["topics"] = strings.Join(topics, "; ")
			row["stars"] = strconv.Itoa(stars)
			row["created_at"], row["pushed_at"] = jstr(it, "created_at"), jstr(it, "pushed_at")
			row["language"] = jstr(it, "language")
			row["license"] = jstr(jmap(it, "license"), "spdx_id")
			row["fork"], row["archived"], row["relevant"], row["last_seen"] = pyBool(fork), pyBool(arch), pyBool(relevant), run
			qs := map[string]bool{q: true}
			for _, x := range strings.Split(row["queries"], "; ") {
				if x != "" {
					qs[x] = true
				}
			}
			row["queries"] = strings.Join(sortedKeys(qs), "; ")
			rows[key] = row
			if relevant {
				kept++
			}
		}
		log.Printf("'%s': %d hits, %d relevant", q, len(items), kept)
		if err := write(); err != nil {
			return err
		}
	}
	n := 0
	for _, r := range rows {
		if r["relevant"] == "True" {
			n++
		}
	}
	log.Printf("done: %d repositories, %d relevant", len(rows), n)
	return nil
}
