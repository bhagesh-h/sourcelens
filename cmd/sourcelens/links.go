package main

// Code / data / website links in papers ("links" step). Python twin: src/sourcelens/pullrepos/mine_links.py.

import (
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	linkURLRe = regexp.MustCompile("(?i)(?:https?://|www\\.)[^\\s\\p{Z}\\x85<>\"'`\\]\\[{}|\\\\^]+")
	// python: (?<![\w/.])((?:github\.com|gitlab\.com|bitbucket\.org)/[\w.-]+/[\w.-]+) with unicode \w;
	// the look-behind is checked in extractLinks
	linkBareRe = regexp.MustCompile(`(?i)(?:github\.com|gitlab\.com|bitbucket\.org)/[\p{L}\p{N}_.-]+/[\p{L}\p{N}_.-]+`)
	codeHosts  = [][2]string{{"github.com", "github"}, {"gitlab.com", "gitlab"}, {"bitbucket.org", "bitbucket"},
		{"sourceforge.net", "sourceforge"}, {"zenodo.org", "zenodo"}, {"figshare.com", "figshare"},
		{"osf.io", "osf"}, {"cran.r-project.org", "cran"}, {"bioconductor.org", "bioconductor"},
		{"pypi.org", "pypi"}, {"huggingface.co", "huggingface"}, {"codeocean.com", "codeocean"},
		{"shinyapps.io", "shiny"}, {"datadryad.org", "dryad"}, {"synapse.org", "synapse"},
		{"github.io", "github-pages"}, {"r-universe.dev", "r-universe"}, {"readthedocs.io", "docs"},
		{"kaggle.com", "kaggle"}, {"mendeley.com/datasets", "mendeley-data"}}
	codeHostKind = map[string]string{"github.com": "github", "gitlab.com": "gitlab", "bitbucket.org": "bitbucket"}
	skipDomains  = regexp.MustCompile(`(?i)(^|\.)(doi\.org|dx\.doi\.org|creativecommons\.org|orcid\.org|w3\.org|crossmark|` +
		`ncbi\.nlm\.nih\.gov|nih\.gov|europepmc\.org|ebi\.ac\.uk|pubmed|scholar\.google|` +
		`elsevier\.com|sciencedirect\.com|springer\.com|springernature|nature\.com|wiley\.com|` +
		`tandfonline|oup\.com|academic\.oup|biomedcentral|frontiersin|mdpi\.com|plos\.org|` +
		`cell\.com|sagepub|karger|bmj\.com|jamanetwork|thelancet|nejm|aging-us\.com|` +
		`biorxiv\.org|medrxiv\.org|arxiv\.org|researchsquare|ssrn\.com|elifesciences|pnas\.org|` +
		`science\.org|sciencemag|acs\.org|rsc\.org|ieee\.org|acm\.org|jstor|clinicaltrials\.gov|` +
		`who\.int|twitter\.com|x\.com|facebook\.com|linkedin\.com|youtube\.com|example\.com|` +
		`mailto|localhost|apa\.org|annualreviews|cambridge\.org|lww\.com|jci\.org|embopress|` +
		`physiology\.org|ahajournals|diabetesjournals|genome\.cshlp|cshlp|epigeneticsandchromatin|` +
		`doi:|hdl\.handle\.net|identifiers\.org|isrctn|chictr|anzctr|clinicaltrialsregister|` +
		`biorender|bit\.ly|tinyurl|goo\.gl|genome\.ucsc|illumina\.com|babraham|crd\.york|prospero|` +
		`umin\.ac\.jp|python\.org|r-project\.org$|rstudio|posit\.co|anaconda|qualtrics|redcap|` +
		`surveymonkey|graphpad|mathworks|microsoft|google\.com/(forms|maps)|apple\.com|ensembl\.org|` +
		`geneontology|kegg\.jp|string-db|uniprot|reactome|gtexportal|gwas\.mrcieu|opengwas|` +
		`covidence|amegroups|data\.bris\.ac\.uk|agrf\.org\.au|disgenet|proteinatlas)`)
	repoPathRe = regexp.MustCompile(`(?i)^(github\.com|gitlab\.com|bitbucket\.org)/([\p{L}\p{N}_.-]+)/([\p{L}\p{N}_.-]+)`)
	refsCut    = regexp.MustCompile(`(?m)^#+\s*References\b|^References\s*$|^REFERENCES\s*$`)
	schemeRe   = regexp.MustCompile(`(?i)^https?://(www\.)?`)
	wwwRe      = regexp.MustCompile(`(?i)^www\.`)
	httpRe     = regexp.MustCompile(`(?i)^http://`)
	gitSuffix  = regexp.MustCompile(`\.git$`)
)

func cleanURL(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), ".,;:)]}>'\"*_")
	u = wwwRe.ReplaceAllString(u, "https://www.")
	u = httpRe.ReplaceAllString(u, "https://")
	// "https://github.com/a/bhttps://..." glued by PDF extraction
	for i := 1; i < len(u); i++ {
		if strings.HasPrefix(u[i:], "https://") || strings.HasPrefix(u[i:], "http://") {
			return u[:i]
		}
	}
	return u
}

func normaliseLink(u string) (string, string, string) {
	bare := schemeRe.ReplaceAllString(u, "")
	domain := strings.ToLower(strings.SplitN(bare, "/", 2)[0])
	if m := repoPathRe.FindStringSubmatch(bare); m != nil {
		host, owner, repo := strings.ToLower(m[1]), m[2], gitSuffix.ReplaceAllString(m[3], "")
		switch strings.ToLower(repo) {
		case "issues", "pulls", "releases", "blob", "tree", "wiki", "archive", "raw":
			return "https://" + host + "/" + owner, codeHostKind[host] + "-user", domain
		}
		return "https://" + host + "/" + owner + "/" + repo, codeHostKind[host], domain
	}
	low := strings.ToLower(bare)
	for _, kv := range codeHosts {
		if strings.Contains(low, kv[0]) {
			return "https://" + bare, kv[1], domain
		}
	}
	return "https://" + bare, "website", domain
}

// runeWindow: text[s-120 : e+80] in characters, whitespace-normalised.
func runeWindow(text string, s, e, before, after int) string {
	i := s
	for n := 0; n < before && i > 0; n++ {
		_, w := utf8.DecodeLastRuneInString(text[:i])
		i -= w
	}
	j := e
	for n := 0; n < after && j < len(text); n++ {
		_, w := utf8.DecodeRuneInString(text[j:])
		j += w
	}
	return strings.Join(pyFields(text[i:j]), " ")
}

type found struct{ raw, ctx string }

func extractLinks(text string) []found {
	var out []found
	for _, m := range linkURLRe.FindAllStringIndex(text, -1) {
		raw := cleanURL(text[m[0]:m[1]])
		if utf8.RuneCountInString(raw) < 12 {
			continue
		}
		out = append(out, found{raw, runeWindow(text, m[0], m[1], 120, 80)})
	}
	for pos := 0; pos < len(text); {
		loc := linkBareRe.FindStringIndex(text[pos:])
		if loc == nil {
			break
		}
		st, en := pos+loc[0], pos+loc[1]
		if st > 0 {
			if r, _ := utf8.DecodeLastRuneInString(text[:st]); r == '/' || r == '.' || r == '_' ||
				unicode.IsLetter(r) || unicode.IsNumber(r) {
				pos = st + 1 // look-behind failed: python tries the next position
				continue
			}
		}
		if raw := cleanURL(text[st:en]); utf8.RuneCountInString(raw) >= 12 {
			out = append(out, found{raw, runeWindow(text, st, en, 120, 80)})
		}
		pos = en
	}
	return out
}

var linkCols = []string{"uid", "url", "kind", "where", "context"}

func stepLinks(args []string, log *Logger) error {
	all, _ := readCSV(rpath("progress.csv"))
	var rows []Row
	for _, r := range all {
		if contains(articleTypes, r["resource_type"]) {
			rows = append(rows, r)
		}
	}
	store := LoadStore()
	type key struct{ uid, url string }
	links := map[key]Row{}
	var linkOrder []key
	type webInfo struct {
		papers  map[string]bool
		urls    *counter
		context string
	}
	web := map[string]*webInfo{}
	var webOrder []string
	nFT := 0
	for _, r := range rows {
		uid := r["uid"]
		rec := store.Get(uid)
		type src struct{ where, text string }
		sources := []src{{"abstract", str(rec, "abstract")}}
		p := r["fulltext_md"]
		if p == "" {
			p = r["fulltext_txt"]
		}
		if p != "" {
			if b, err := os.ReadFile(researchFile(p)); err == nil {
				txt := strings.ToValidUTF8(string(b), "")
				if loc := refsCut.FindStringIndex(txt); loc != nil {
					txt = txt[:loc[0]]
				}
				sources = append(sources, src{"fulltext", txt})
				nFT++
			}
		}
		for _, s := range sources {
			for _, fnd := range extractLinks(s.text) {
				u, kind, domain := normaliseLink(fnd.raw)
				host := u
				if strings.Count(u, "/") > 2 {
					host = strings.SplitN(u, "/", 4)[2]
				}
				if skipDomains.MatchString(domain) || skipDomains.MatchString(host) {
					continue
				}
				if kind == "website" {
					w := web[domain]
					if w == nil {
						w = &webInfo{papers: map[string]bool{}, urls: newCounter()}
						web[domain] = w
						webOrder = append(webOrder, domain)
					}
					w.papers[uid] = true
					w.urls.add(u, 1)
					if w.context == "" {
						w.context = runeCut(fnd.ctx, 300)
					}
					continue
				}
				k := key{uid, u}
				if _, ok := links[k]; !ok {
					linkOrder = append(linkOrder, k)
					links[k] = Row{"uid": uid, "url": u, "kind": kind, "where": s.where, "context": runeCut(fnd.ctx, 400)}
				}
			}
		}
	}
	lr := make([]Row, 0, len(links))
	for _, r := range links {
		lr = append(lr, r)
	}
	sort.Slice(lr, func(i, j int) bool {
		if lr[i]["uid"] != lr[j]["uid"] {
			return lr[i]["uid"] < lr[j]["uid"]
		}
		return lr[i]["url"] < lr[j]["url"]
	})
	if err := writeCSV(rpath("repos", "paper_links.csv"), lr, linkCols); err != nil {
		return err
	}
	var cand []Row
	for _, d := range webOrder {
		w := web[d]
		papers := sortedKeys(w.papers)
		cand = append(cand, Row{"domain": d, "n_papers": strconv.Itoa(len(w.papers)), "top_url": w.urls.mostCommon(1)[0],
			"n_urls": strconv.Itoa(len(w.urls.keys)), "example_context": w.context,
			"example_papers": strings.Join(papers[:min(5, len(papers))], "; ")})
	}
	sort.SliceStable(cand, func(i, j int) bool { return atoiSafe(cand[i]["n_papers"]) > atoiSafe(cand[j]["n_papers"]) })
	if err := writeCSV(rpath("websites", "candidate_websites.csv"), cand,
		[]string{"domain", "n_papers", "top_url", "n_urls", "example_context", "example_papers"}); err != nil {
		return err
	}
	kinds := newCounter()
	for _, k := range linkOrder {
		kinds.add(links[k]["kind"], 1)
	}
	var kp []string
	for _, k := range kinds.keys {
		kp = append(kp, "'"+k+"': "+strconv.Itoa(kinds.n[k]))
	}
	log.Printf("%d articles (%d with full text): %d code/data links {%s}; %d candidate website domains",
		len(rows), nFT, len(lr), strings.Join(kp, ", "), len(cand))
	return nil
}
