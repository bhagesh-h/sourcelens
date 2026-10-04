package main

// Repositories, packages and archives ("repositories" step). Python twin: src/sourcelens/pullrepos/build_repos.py.

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dlclark/regexp2"
)

var (
	repoSources = []string{"github", "cran", "bioconductor", "pypi", "zenodo"}
	venueKind   = map[string]string{"GitHub": "github", "CRAN": "cran", "Bioconductor": "bioconductor", "PyPI": "pypi", "Zenodo": "zenodo"}
	availRe     = regexp2.MustCompile(`(?i)\b(code|scripts?|software|analys[ie]s|pipeline|implementation|data)\b[^.]{0,60}`+
		`\b(for|of|from|used in|underlying|supporting|to reproduce|to replicate|accompanying)\s+`+
		`(this|the present|the current|our)\s+(study|work|paper|article|manuscript|analys[ie]s|results|findings)`+
		`|\b(code|data and code|software) availability\b`+
		`|\bour (code|scripts|software|package|pipeline|implementation|repository|model|tool)\b`+
		`|\b(we|authors) (have )?(made|make|provide|release|deposit|share)[^.]{0,40}\b(code|scripts|software|package|implementation)`, regexp2.None)
	repoKeyRe   = regexp.MustCompile(`(?i)^https?://(www\.)?github\.com/([\p{L}\p{N}_.-]+)/([\p{L}\p{N}_.-]+)`)
	repoAllCols = []string{"uid", "kind", "name", "url", "description", "created", "updated", "stars",
		"language", "license", "topics", "archived", "linked_from_papers", "linked_from_local", "search_queries",
		"included", "reason", "doi", "checked_on"}
	cranStrip = regexp.MustCompile(`.*/package=|.*/packages/`)
	pypiStrip = regexp.MustCompile(`.*pypi\.org/project/`)
	zenodoRec = regexp.MustCompile(`zenodo\.org/(?:records?|deposit)/(\d+)`)
	zenodoDOI = regexp.MustCompile(`zenodo\.(\d+)`)
	biocVer   = regexp.MustCompile(`release_version:\s*"([\d.]+)"`)
)

const ownCodeMaxStars = 500

type repoItem struct {
	kind, name, url, description, created, updated, language, license, topics string
	archived, fork, searchQueries, doi, resource, checkedOn                   string
	stars                                                                     int
	starsSet, relevant, relevantSet, ownCode, registry, missing               bool
	papers, local                                                             map[string]bool
}

func repoKey(u string) string {
	m := repoKeyRe.FindStringSubmatch(u)
	if m == nil {
		return ""
	}
	// python: re.sub(r'.git$', '', ...): any one character followed by "git"
	name := []rune(m[3])
	if len(name) >= 4 && string(name[len(name)-3:]) == "git" {
		name = name[:len(name)-4]
	}
	return strings.ToLower(m[2] + "/" + string(name))
}

func ghRepo(c *Client, full string) map[string]string {
	r := c.Get("https://api.github.com/repos/"+full, reqOpts{})
	if r == nil || r.Status != 200 {
		return nil
	}
	j := decodeJSON(r.Body)
	var topics []string
	for _, t := range jlist(j["topics"]) {
		if s, ok := t.(string); ok {
			topics = append(topics, s)
		}
	}
	stars, _ := toInt(j["stargazers_count"])
	arch, _ := j["archived"].(bool)
	fork, _ := j["fork"].(bool)
	return map[string]string{"full_name": jstr(j, "full_name"), "url": jstr(j, "html_url"),
		"description": jstr(j, "description"), "created": runeCut(jstr(j, "created_at"), 10),
		"updated": runeCut(jstr(j, "pushed_at"), 10), "stars": strconv.Itoa(stars), "language": jstr(j, "language"),
		"license": jstr(jmap(j, "license"), "spdx_id"), "topics": strings.Join(topics, "; "),
		"archived": pyBool(arch), "fork": pyBool(fork)}
}

func biocFirstMonth(c *Client, pkg string) string {
	r := c.Get(fmt.Sprintf("https://bioconductor.org/packages/stats/bioc/%s/%s_stats.tab", pkg, pkg), reqOpts{})
	if r == nil || r.Status != 200 {
		return ""
	}
	months := map[string]int{"Jan": 1, "Feb": 2, "Mar": 3, "Apr": 4, "May": 5, "Jun": 6, "Jul": 7, "Aug": 8,
		"Sep": 9, "Oct": 10, "Nov": 11, "Dec": 12}
	first := ""
	lines := splitLinesPy(r.Text())
	for _, line := range lines[min(1, len(lines)):] {
		p := strings.Split(line, "\t")
		if len(p) >= 4 {
			if mo, ok := months[p[1]]; ok && isDigits(strings.TrimSpace(p[3])) && atoiSafe(p[3]) > 0 {
				d := fmt.Sprintf("%s-%02d-01", p[0], mo)
				if first == "" || d < first {
					first = d
				}
			}
		}
	}
	return first
}

// jsonObjectInOrder decodes a top-level JSON object keeping key order.
func jsonObjectInOrder(b []byte) ([]string, map[string]map[string]any) {
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.UseNumber()
	vals := map[string]map[string]any{}
	var keys []string
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return nil, vals
	}
	for d.More() {
		t, err := d.Token()
		if err != nil {
			break
		}
		k, _ := t.(string)
		var v map[string]any
		if d.Decode(&v) != nil {
			break
		}
		keys = append(keys, k)
		vals[k] = v
	}
	return keys, vals
}

func strAny(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		parts := []string{}
		for _, e := range x {
			parts = append(parts, strAny(e))
		}
		return strings.Join(parts, ", ")
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

func stepRepositories(args []string, log *Logger) error {
	o := parseStepArgs(args)
	minStars := atoiSafe(orDefault(o["min-stars"], "3"))
	srcs := map[string]bool{}
	for _, s := range splitList(orDefault(o["sources"], strings.Join(repoSources, ","))) {
		s = strings.ToLower(s)
		if !contains(repoSources, s) {
			return fmt.Errorf("sources must be within %v", repoSources)
		}
		srcs[s] = true
	}
	cfg := loadRepoConfig()
	pkgRx := pyRe(cfg.PackageTerms)
	rel := pyRe(cfg.Relevance)
	clf := LoadClassifier()
	progRows, _ := readCSV(rpath("progress.csv"))
	progress := map[string]Row{}
	for _, r := range progRows {
		progress[r["uid"]] = r
	}
	items := map[string]*repoItem{}
	var order []string
	// assign mirrors python's items[k] = ...: replaces the value, keeps the first position
	assign := func(k string, it *repoItem) {
		if _, ok := items[k]; !ok {
			order = append(order, k)
		}
		items[k] = it
	}
	put := func(k string, it *repoItem) *repoItem {
		if old, ok := items[k]; ok {
			return old
		}
		items[k] = it
		order = append(order, k)
		return it
	}
	relText := func(name, desc, topics string) bool {
		parts := strings.Split(name, "/")
		return reSearch(rel, strings.Join([]string{strings.ReplaceAll(parts[len(parts)-1], "-", " "), desc,
			strings.ReplaceAll(topics, "-", " ")}, " "))
	}
	if srcs["github"] {
		gs, _ := readCSV(rpath("repos", "github_search.csv"))
		for _, r := range gs {
			k := "gh:" + strings.ToLower(r["full_name"])
			it := &repoItem{kind: "github", name: r["full_name"], url: r["url"], description: r["description"],
				created: runeCut(r["created_at"], 10), updated: runeCut(r["pushed_at"], 10), stars: atoiSafe(r["stars"]),
				starsSet: true, language: r["language"], license: r["license"], topics: r["topics"],
				archived: r["archived"], fork: r["fork"], searchQueries: r["queries"],
				relevant: relText(r["full_name"], r["description"], r["topics"]), relevantSet: true,
				papers: map[string]bool{}, local: map[string]bool{}}
			assign(k, it)
		}
	}
	var otherLinks []Row
	pl, _ := readCSV(rpath("repos", "paper_links.csv"))
	for _, r := range pl {
		if _, ok := progress[r["uid"]]; !ok {
			continue
		}
		k := ""
		if srcs["github"] {
			k = repoKey(r["url"])
		}
		if k != "" {
			it := put("gh:"+k, &repoItem{kind: "github", name: k, url: r["url"], papers: map[string]bool{}, local: map[string]bool{}})
			it.papers[r["uid"]] = true
			if r["where"] == "abstract" || reSearch(availRe, r["context"]) {
				it.ownCode = true
			}
		} else if contains([]string{"zenodo", "cran", "bioconductor", "pypi", "gitlab", "bitbucket", "huggingface",
			"figshare", "osf", "codeocean", "shiny"}, r["kind"]) {
			otherLinks = append(otherLinks, r)
		}
	}
	lu, _ := readCSV(rpath("seeds", "local_urls.csv"))
	for _, r := range lu {
		k := ""
		if srcs["github"] {
			k = repoKey(r["url"])
		}
		if k != "" {
			it := put("gh:"+k, &repoItem{kind: "github", name: k, url: r["url"], papers: map[string]bool{}, local: map[string]bool{}})
			it.local[r["source_repo"]] = true
			if strings.HasSuffix(r["source_file"], "registry/data/clocks.yaml") {
				it.registry = true
			}
		}
	}
	gh := ghClient(60*time.Second, log)
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		gh = ghClient(750*time.Millisecond, log)
	}
	cutoff := time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	cachedRows, _ := readCSV(rpath("repos", "repositories.csv"))
	cached := map[string]Row{}
	for _, r := range cachedRows {
		if r["kind"] == "github" && orDefault(r["checked_on"], today()) >= cutoff {
			cached[strings.ToLower(r["name"])] = r
		}
	}
	for _, k := range order {
		v := items[k]
		c, ok := cached[strings.ToLower(v.name)]
		if !ok {
			c, ok = cached[k[3:]]
		}
		if v.kind == "github" && v.created == "" && ok {
			if c["created"] != "" {
				v.url, v.description, v.created, v.updated = c["url"], c["description"], c["created"], c["updated"]
				v.language, v.license, v.topics, v.archived = c["language"], c["license"], c["topics"], c["archived"]
				v.stars, v.starsSet, v.name = atoiSafe(c["stars"]), true, c["name"]
				v.checkedOn = orDefault(c["checked_on"], today())
			} else if c["reason"] == "repository gone" {
				v.missing = true
				v.checkedOn = orDefault(c["checked_on"], today())
			}
		}
	}
	var need []string
	for _, k := range order {
		v := items[k]
		if v.kind == "github" && v.created == "" && !v.missing {
			need = append(need, k)
		}
	}
	log.Printf("GitHub: %d repositories; fetching metadata for %d", len(items), len(need))
	for i, k := range need {
		if m := ghRepo(gh, items[k].name); m != nil {
			v := items[k]
			v.url, v.description, v.created, v.updated = m["url"], m["description"], m["created"], m["updated"]
			v.stars, v.starsSet, v.language, v.license = atoiSafe(m["stars"]), true, m["language"], m["license"]
			v.topics, v.archived, v.fork, v.name = m["topics"], m["archived"], m["fork"], m["full_name"]
		} else {
			items[k].missing = true
		}
		if (i+1)%200 == 0 {
			log.Printf("  %d/%d", i+1, len(need))
		}
	}
	for _, k := range order {
		v := items[k]
		if v.kind == "github" && !v.relevantSet {
			v.relevant, v.relevantSet = relText(v.name, v.description, v.topics), true
		}
	}
	// ---- packages ----
	ph := NewClient(200*time.Millisecond, nil, log)
	newPkg := func(kind, name, url, desc, created, updated, lic, q string) *repoItem {
		return &repoItem{kind: kind, name: name, url: url, description: desc, created: created, updated: updated,
			license: lic, relevant: true, relevantSet: true, searchQueries: q, papers: map[string]bool{}, local: map[string]bool{}}
	}
	if srcs["cran"] {
		if r := ph.Get("https://crandb.r-pkg.org/-/desc", reqOpts{}); r != nil && r.Status == 200 {
			cran := decodeJSON(r.Body)
			hits := map[string]bool{}
			for p, d := range cran {
				if reSearch(pkgRx, jstr(ymap(d), "title")) {
					hits[p] = true
				}
			}
			for _, p := range cfg.Known["cran"] {
				if _, ok := cran[p]; ok {
					hits[p] = true
				}
			}
			for _, x := range otherLinks {
				if x["kind"] == "cran" {
					p := strings.SplitN(strings.SplitN(cranStrip.ReplaceAllString(x["url"], ""), "/", 2)[0], "&", 2)[0]
					if _, ok := cran[p]; ok {
						hits[p] = true
					}
				}
			}
			for _, p := range sortedKeys(hits) {
				rr := ph.Get("https://crandb.r-pkg.org/"+p+"/all", reqOpts{})
				if rr == nil || rr.Status != 200 {
					continue
				}
				j := decodeJSON(rr.Body)
				var times []string
				for _, t := range jmap(j, "timeline") {
					if s, ok := t.(string); ok {
						times = append(times, s)
					}
				}
				sort.Strings(times)
				latest := jmap(jmap(j, "versions"), jstr(j, "latest"))
				c, u := "", ""
				if len(times) > 0 {
					c, u = runeCut(times[0], 10), runeCut(times[len(times)-1], 10)
				}
				assign("cran:"+p, newPkg("cran", p, "https://cran.r-project.org/package="+p,
					strAny(latest["Title"])+". "+strAny(latest["Description"]), c, u, strAny(latest["License"]), "CRAN title match"))
			}
		}
	}
	if srcs["bioconductor"] {
		ver := "3.22"
		if r := ph.Get("https://bioconductor.org/config.yaml", reqOpts{}); r != nil {
			if m := biocVer.FindStringSubmatch(r.Text()); m != nil {
				ver = m[1]
			}
		}
		if r := ph.Get("https://bioconductor.org/packages/json/"+ver+"/bioc/packages.json", reqOpts{}); r != nil && r.Status == 200 {
			keys, vals := jsonObjectInOrder(r.Body)
			for _, p := range keys {
				d := vals[p]
				text := strAny(d["Title"]) + " " + strAny(d["Description"])
				if reSearch(pkgRx, text) || contains(cfg.Known["bioconductor"], p) {
					assign("bioc:"+p, newPkg("bioconductor", p, "https://bioconductor.org/packages/"+p,
						strings.Join(pyFields(text), " "), biocFirstMonth(ph, p),
						runeCut(strAny(d["git_last_commit_date"]), 10), strAny(d["License"]), "Bioconductor "+ver))
				}
			}
		}
	}
	if srcs["pypi"] {
		names := map[string]bool{}
		for _, p := range cfg.Known["pypi"] {
			names[p] = true
		}
		for _, x := range otherLinks {
			if x["kind"] == "pypi" {
				names[strings.SplitN(strings.Trim(pypiStrip.ReplaceAllString(x["url"], ""), "/"), "/", 2)[0]] = true
			}
		}
		for _, p := range sortedKeys(names) {
			rr := ph.Get("https://pypi.org/pypi/"+p+"/json", reqOpts{})
			if rr == nil || rr.Status != 200 {
				continue
			}
			j := decodeJSON(rr.Body)
			var ups []string
			for _, files := range jmap(j, "releases") {
				for _, f := range jlist(files) {
					ups = append(ups, jstr(ymap(f), "upload_time"))
				}
			}
			sort.Strings(ups)
			info := jmap(j, "info")
			name := orDefault(jstr(info, "name"), p)
			c, u := "", ""
			if len(ups) > 0 {
				c, u = runeCut(ups[0], 10), runeCut(ups[len(ups)-1], 10)
			}
			assign("pypi:"+strings.ToLower(p), newPkg("pypi", name, "https://pypi.org/project/"+name+"/",
				jstr(info, "summary"), c, u, jstr(info, "license"), "known / linked"))
		}
	}
	if srcs["zenodo"] {
		for _, x := range otherLinks {
			if x["kind"] != "zenodo" {
				continue
			}
			m := zenodoRec.FindStringSubmatch(x["url"])
			if m == nil {
				m = zenodoDOI.FindStringSubmatch(x["url"])
			}
			if m == nil {
				continue
			}
			key := "zenodo:" + m[1]
			if _, ok := items[key]; !ok {
				rr := ph.Get("https://zenodo.org/api/records/"+m[1], reqOpts{})
				if rr == nil || rr.Status != 200 {
					continue
				}
				j := decodeJSON(rr.Body)
				md := jmap(j, "metadata")
				it := newPkg("zenodo", orDefault(jstr(md, "title"), key), orDefault(jstr(jmap(j, "links"), "html"), x["url"]),
					runeCut(tagRe.ReplaceAllString(jstr(md, "description"), " "), 600),
					runeCut(orDefault(jstr(j, "created"), jstr(md, "publication_date")), 10), runeCut(jstr(j, "updated"), 10),
					jstr(jmap(md, "license"), "id"), "")
				it.doi, it.resource = jstr(j, "doi"), jstr(jmap(md, "resource_type"), "type")
				put(key, it)
			}
			items[key].papers[x["uid"]] = true
		}
	}
	// ---- inclusion ----
	var allRows, catRows []Row
	for _, k := range order {
		it := items[k]
		var reason string
		inc := false
		anyCore := false
		for u := range it.papers {
			if t := progress[u]["tier"]; t == "landmark" || t == "core" {
				anyCore = true
			}
		}
		switch {
		case it.missing || it.fork == "True":
			reason = "fork"
			if it.missing {
				reason = "repository gone"
			}
		case len(it.papers) > 0 && (it.relevant || (it.ownCode && it.stars < ownCodeMaxStars && anyCore)):
			reason, inc = "linked from catalogued paper", true
		case len(it.local) > 0 && (it.relevant || it.registry):
			reason, inc = "linked from local project", true
		case contains([]string{"cran", "bioconductor", "pypi", "zenodo"}, it.kind):
			reason, inc = "package index match", true
		case it.relevant && it.stars >= minStars:
			reason, inc = fmt.Sprintf("relevant search hit, >= %d stars", minStars), true
		default:
			reason = "search hit below inclusion bar"
		}
		uid := makeUID("", "", "", "", it.url)
		papers := sortedKeys(it.papers)
		var dois []string
		for _, u := range papers {
			if d := progress[u]["doi"]; d != "" {
				dois = append(dois, d)
			}
		}
		starsS := ""
		if it.starsSet {
			starsS = strconv.Itoa(it.stars)
		}
		allRows = append(allRows, Row{"uid": uid, "kind": it.kind, "name": it.name, "url": it.url,
			"description": it.description, "created": it.created, "updated": it.updated, "stars": starsS,
			"language": it.language, "license": it.license, "topics": it.topics, "archived": it.archived,
			"linked_from_papers": strings.Join(papers, "; "), "linked_from_local": strings.Join(sortedKeys(it.local), "; "),
			"search_queries": it.searchQueries, "included": pyBool(inc), "reason": reason, "doi": it.doi,
			"checked_on": orDefault(it.checkedOn, today())})
		if !inc {
			continue
		}
		ann := clf.Annotate(Rec{"title": it.name, "abstract": it.description + " " + it.topics})
		tier := "related"
		if anyCore || len(it.local) > 0 || it.stars >= 20 || contains([]string{"cran", "bioconductor", "pypi"}, it.kind) {
			tier = "core"
		}
		rtype := map[string]string{"github": "repository", "gitlab": "repository", "cran": "software package",
			"bioconductor": "software package", "pypi": "software package"}[it.kind]
		if it.kind == "zenodo" {
			rtype = "archive"
			if it.resource == "dataset" {
				rtype = "dataset"
			}
		}
		if rtype == "" {
			rtype = "repository"
		}
		venue := map[string]string{"github": "GitHub", "cran": "CRAN", "bioconductor": "Bioconductor", "pypi": "PyPI",
			"zenodo": "Zenodo"}[it.kind]
		if venue == "" {
			venue = it.kind
		}
		var details []string
		if it.starsSet {
			details = append(details, "stars="+strconv.Itoa(it.stars))
		}
		if it.language != "" {
			details = append(details, "language="+it.language)
		}
		if it.updated != "" {
			details = append(details, "last_update="+it.updated)
		}
		if it.archived == "True" {
			details = append(details, "archived")
		}
		foundBy := map[string]string{"linked from catalogued paper": "paper link", "linked from local project": "local seed"}[reason]
		gs := ""
		if it.searchQueries != "" && it.kind == "github" {
			gs = "github search"
		}
		pi := ""
		if contains([]string{"cran", "bioconductor", "pypi"}, it.kind) {
			pi = "package index"
		}
		title := strings.TrimRight(pyStrip(it.name+": "+it.description), ":")
		author := ""
		if strings.Contains(it.name, "/") {
			author = strings.SplitN(it.name, "/", 2)[0]
		}
		catRows = append(catRows, Row{"date": it.created, "year": runeCut(it.created, 4), "resource_type": rtype,
			"tier": tier, "category": "software/resource", "modality": ann.Modality, "entities": ann.Entities,
			"species": ann.Species, "title": runeCut(title, 500), "authors": author, "venue": venue, "doi": it.doi,
			"url": it.url, "related": strings.Join(dois[:min(20, len(dois))], "; "), "license": it.license,
			"open_access": "yes", "matched_groups": it.searchQueries, "found_by": joinNonEmpty("; ", foundBy, gs, pi),
			"local_refs": strings.Join(sortedKeys(it.local), "; "), "details": strings.Join(details, "; "), "uid": uid})
	}
	dedup := func(rows []Row) []Row {
		seen := map[string]bool{}
		var out []Row
		for _, r := range rows {
			if !seen[r["uid"]] {
				seen[r["uid"]] = true
				out = append(out, r)
			}
		}
		return out
	}
	allRows, catRows = dedup(allRows), dedup(catRows)
	skipped := map[string]bool{}
	for _, s := range repoSources {
		if !srcs[s] {
			skipped[s] = true
		}
	}
	if len(skipped) > 0 {
		done := map[string]bool{}
		for _, r := range allRows {
			done[r["uid"]] = true
		}
		for _, r := range cachedRows {
			if skipped[r["kind"]] && !done[r["uid"]] {
				allRows = append(allRows, r)
			}
		}
		done = map[string]bool{}
		for _, r := range catRows {
			done[r["uid"]] = true
		}
		prevCat, _ := readCSV(rpath("repos", "repositories_catalogue.csv"))
		for _, r := range prevCat {
			if skipped[venueKind[r["venue"]]] && !done[r["uid"]] {
				catRows = append(catRows, r)
			}
		}
	}
	rt := newCounter() // python counts the rows before sorting them
	for _, r := range catRows {
		rt.add(r["resource_type"], 1)
	}
	sort.SliceStable(allRows, func(i, j int) bool {
		if allRows[i]["kind"] != allRows[j]["kind"] {
			return allRows[i]["kind"] < allRows[j]["kind"]
		}
		return strings.ToLower(allRows[i]["name"]) < strings.ToLower(allRows[j]["name"])
	})
	sort.SliceStable(catRows, func(i, j int) bool {
		return orDefault(catRows[i]["date"], "9999") < orDefault(catRows[j]["date"], "9999")
	})
	if err := writeCSV(rpath("repos", "repositories.csv"), allRows, repoAllCols); err != nil {
		return err
	}
	if err := writeCSV(rpath("repos", "repositories_catalogue.csv"), catRows, progressCols); err != nil {
		return err
	}
	log.Printf("%d repositories/packages seen; %d catalogued %s", len(allRows), len(catRows), pyCounterRepr(rt))
	return nil
}
