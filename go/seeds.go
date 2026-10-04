package main

// Local seeds ("seeds" step). Mirrors python/localseeds/extract_seeds.py.

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	textSuffixes = map[string]bool{".md": true, ".qmd": true, ".rmd": true, ".yml": true, ".yaml": true, ".r": true,
		".py": true, ".tsv": true, ".csv": true, ".cff": true, ".bib": true, ".txt": true, ".json": true,
		".html": true, ".rd": true}
	skipDirs = map[string]bool{".git": true, "__pycache__": true, ".ruff_cache": true, "node_modules": true,
		"_site": true, "output": true, "renv": true, ".venv": true, "site-packages": true, "logs": true}
	skipPathParts = []string{"300BCG/literature/0", "300BCG/literature/1", "/papers/"}
	skipName      = regexp.MustCompile(`(?i)^fulltext|^full_text|\.fulltext\.`)
	seedURLRe     = regexp.MustCompile("(?i)https?://[^\\s\"'<>)\\]}|,`]+")
	urlKeep       = regexp.MustCompile(`(?i)github\.com|gitlab\.|bitbucket\.org|zenodo\.org|figshare\.com|osf\.io|` +
		`bioconductor\.org|cran\.r-project\.org|pypi\.org|huggingface\.co|` +
		`clockfoundation|shinyapps\.io|dnamage|clockbase|biolearn|` +
		`computage|agingbiomarkers|biomarkersofaging|genomics\.senescence|` +
		`ngdc\.cncb\.ac\.cn|ncbi\.nlm\.nih\.gov/geo|synapse\.org|ukbiobank`)
	seedCols  = []string{"doi", "role", "source_repo", "source_file", "clock_name", "clock_year", "data_type", "species", "context"}
	urlCols   = []string{"url", "kind", "source_repo", "source_file", "context"}
	clockCols = []string{"clock_id", "name", "year", "species", "data_type", "generation", "tissue", "platform",
		"predicts", "unit", "scale_type", "model_type", "population", "n_features", "availability", "doi",
		"citation", "notes", "coefficient_url"}
)

const maxSeedBytes = 5_000_000

func urlKind(u string) string {
	ul := strings.ToLower(u)
	for _, kv := range [][2]string{{"github.com", "github"}, {"gitlab.", "gitlab"}, {"bitbucket.org", "bitbucket"},
		{"zenodo.org", "zenodo"}, {"figshare.com", "figshare"}, {"osf.io", "osf"},
		{"bioconductor.org", "bioconductor"}, {"cran.r-project.org", "cran"}, {"pypi.org", "pypi"},
		{"huggingface.co", "huggingface"}, {"ncbi.nlm.nih.gov/geo", "geo"}} {
		if strings.Contains(ul, kv[0]) {
			return kv[1]
		}
	}
	return "website"
}

// pyStr renders a YAML scalar the way Python's str() / csv would.
func pyStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = pyStr(e)
		}
		return strings.Join(parts, "; ")
	case bool:
		if x {
			return "True"
		}
		return "False"
	default:
		return fmt.Sprint(x)
	}
}

func nodeValue(n *yaml.Node) any {
	if n == nil {
		return nil
	}
	var v any
	_ = n.Decode(&v)
	return v
}

func ymap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func seedsFalconage(seeds, urls *[]Row, log *Logger) []Row {
	base := filepath.Join(Refs, "FALCONAge")
	regRel := "FALCONAge/python/src/falconage/registry/data/clocks.yaml"
	reg := filepath.Join(Refs, regRel)
	var clocks []Row
	if root := loadYAMLNode(reg); root != nil {
		for _, kv := range orderedMapping(nodeChild(root, "clocks")) {
			c := ymap(nodeValue(kv.Value))
			src := ymap(c["coefficient_source"])
			name := pyStr(c["name"])
			if _, ok := c["name"]; !ok {
				name = kv.Key
			}
			row := Row{"clock_id": kv.Key, "name": name, "year": pyStr(c["year"]), "species": pyStr(c["species"]),
				"data_type": pyStr(c["data_type"]), "generation": pyStr(c["generation"]), "tissue": pyStr(c["tissue"]),
				"platform": pyStr(c["platform"]), "predicts": pyStr(c["predicts"]), "unit": pyStr(c["unit"]),
				"scale_type": pyStr(c["scale_type"]), "model_type": pyStr(c["model_type"]),
				"population": pyStr(c["population"]), "n_features": pyStr(c["n_features"]),
				"availability": pyStr(c["availability"]), "doi": normDOI(pyStr(c["doi"])),
				"citation": pyStr(c["citation"]), "notes": pyStr(c["notes"]), "coefficient_url": pyStr(src["url"])}
			clocks = append(clocks, row)
			if row["doi"] != "" {
				*seeds = append(*seeds, Row{"doi": row["doi"], "role": "clock_origin", "source_repo": "FALCONAge",
					"source_file": regRel, "clock_name": kv.Key, "clock_year": row["year"],
					"data_type": row["data_type"], "species": row["species"], "context": row["citation"]})
			}
			if row["coefficient_url"] != "" {
				*urls = append(*urls, Row{"url": row["coefficient_url"], "kind": urlKind(row["coefficient_url"]),
					"source_repo": "FALCONAge", "source_file": regRel, "context": "coefficient source of clock " + kv.Key})
			}
		}
		log.Printf("FALCONAge registry: %d clocks", len(clocks))
	}
	refsRel := "FALCONAge/docs/references.yml"
	if root := loadYAMLNode(filepath.Join(Refs, refsRel)); root != nil {
		n := 0
		for _, g := range jlist(nodeValue(nodeChild(root, "groups"))) {
			gm := ymap(g)
			for _, e := range jlist(gm["entries"]) {
				em := ymap(e)
				d := normDOI(orDefault(pyStr(em["doi"]), pyStr(em["text"])))
				if d != "" {
					*seeds = append(*seeds, Row{"doi": d, "role": "clock_reference", "source_repo": "FALCONAge",
						"source_file": refsRel, "context": fmt.Sprintf("[%s] %s", pyStr(gm["title"]), pyStr(em["text"]))})
					n++
				}
			}
		}
		log.Printf("FALCONAge references.yml: %d DOIs", n)
	}
	evRel := "FALCONAge/python/src/falconage/registry/data/evidence.yaml"
	if root := loadYAMLNode(filepath.Join(Refs, evRel)); root != nil {
		for _, kv := range orderedMapping(nodeChild(root, "sources")) {
			sm := ymap(nodeValue(kv.Value))
			if d := normDOI(pyStr(sm["doi"])); d != "" {
				*seeds = append(*seeds, Row{"doi": d, "role": "benchmark", "source_repo": "FALCONAge",
					"source_file": evRel, "context": strings.Join(pyFields(pyStr(sm["citation"])), " ")})
			}
		}
	}
	_ = base
	return clocks
}

func seedsBCG(seeds *[]Row, log *Logger) {
	for _, nr := range [][2]string{{"manifest_methods.tsv", "methods_reference"},
		{"manifest_local.tsv", "methods_reference"}, {"manifest.tsv", "cohort_paper"}} {
		rel := "300BCG/literature/" + nr[0]
		f, err := os.Open(filepath.Join(Refs, rel))
		if err != nil {
			continue
		}
		r := csv.NewReader(bufio.NewReader(f))
		r.Comma = '\t'
		r.FieldsPerRecord = -1
		r.LazyQuotes = true
		head, _ := r.Read()
		n := 0
		for {
			rec, err := r.Read()
			if err != nil {
				break
			}
			n++
			m := Row{}
			for i, h := range head {
				if i < len(rec) {
					m[h] = rec[i]
				}
			}
			d := normDOI(m["doi"])
			if d == "" {
				continue
			}
			topic := m["topic"]
			role := nr[1]
			if role == "methods_reference" && !(strings.HasPrefix(topic, "clocks") || strings.Contains(topic, "aging-clocks")) {
				role = "methods_other"
			}
			ctx := fmt.Sprintf("[%s] %s", topic, m["title"])
			if m["question"] != "" {
				ctx += " | " + m["question"]
			}
			*seeds = append(*seeds, Row{"doi": d, "role": role, "source_repo": "300BCG", "source_file": rel, "context": ctx})
		}
		f.Close()
		log.Printf("300BCG %s: %d rows", nr[0], n)
	}
}

func pyStripRight(s, chars string) string { return strings.TrimRight(s, chars) }

func seedsScan(seeds, urls *[]Row, log *Logger) {
	for _, repo := range []string{"FALCONAge", "300BCG", "300OB"} {
		base := filepath.Join(Refs, repo)
		if !fileExists(base) {
			log.Printf("missing %s; skipped", base)
			continue
		}
		nDOI, nURL := 0, 0
		_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !textSuffixes[strings.ToLower(filepath.Ext(p))] {
				return nil
			}
			rel, _ := filepath.Rel(Refs, p)
			for _, part := range strings.Split(rel, string(filepath.Separator)) {
				if skipDirs[part] {
					return nil
				}
			}
			for _, sp := range skipPathParts {
				if strings.Contains(rel, sp) {
					return nil
				}
			}
			if skipName.MatchString(d.Name()) {
				return nil
			}
			if info, err := d.Info(); err != nil || info.Size() > maxSeedBytes {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			text := strings.ToValidUTF8(string(b), "")
			for _, line := range splitLinesPy(text) {
				for _, m := range doiRe.FindAllString(line, -1) {
					if dd := normDOI(m); dd != "" {
						*seeds = append(*seeds, Row{"doi": dd, "role": "mentioned", "source_repo": repo,
							"source_file": rel, "context": runeCut(strings.TrimSpace(line), 400)})
						nDOI++
					}
				}
				for _, m := range seedURLRe.FindAllString(line, -1) {
					u := pyStripRight(m, ".;:*_")
					if urlKeep.MatchString(u) && !strings.Contains(u, "doi.org") {
						*urls = append(*urls, Row{"url": u, "kind": urlKind(u), "source_repo": repo,
							"source_file": rel, "context": runeCut(strings.TrimSpace(line), 300)})
						nURL++
					}
				}
			}
			return nil
		})
		log.Printf("%s: %d DOI mentions, %d code/website URL mentions", repo, nDOI, nURL)
	}
}

// splitLinesPy splits like Python's str.splitlines() for \n, \r\n and \r.
func splitLinesPy(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func stepSeeds(args []string, log *Logger) error {
	var seeds, urls []Row
	clocks := seedsFalconage(&seeds, &urls, log)
	seedsBCG(&seeds, log)
	seedsScan(&seeds, &urls, log)
	seen := map[string]bool{}
	var uniq []Row
	for _, s := range seeds {
		k := s["doi"] + "\x00" + s["role"] + "\x00" + s["source_file"] + "\x00" + s["clock_name"]
		if !seen[k] {
			seen[k] = true
			uniq = append(uniq, s)
		}
	}
	sort.SliceStable(uniq, func(i, j int) bool {
		a, b := uniq[i], uniq[j]
		if a["doi"] != b["doi"] {
			return a["doi"] < b["doi"]
		}
		if a["role"] != b["role"] {
			return a["role"] < b["role"]
		}
		return a["source_file"] < b["source_file"]
	})
	seenU := map[string]bool{}
	var uu []Row
	for _, u := range urls {
		k := u["url"] + "\x00" + u["source_file"]
		if !seenU[k] {
			seenU[k] = true
			uu = append(uu, u)
		}
	}
	sort.SliceStable(uu, func(i, j int) bool {
		if uu[i]["kind"] != uu[j]["kind"] {
			return uu[i]["kind"] < uu[j]["kind"]
		}
		return uu[i]["url"] < uu[j]["url"]
	})
	if err := writeCSV(rpath("seeds", "falconage_clocks.csv"), clocks, clockCols); err != nil {
		return err
	}
	if err := writeCSV(rpath("seeds", "local_seeds.csv"), uniq, seedCols); err != nil {
		return err
	}
	if err := writeCSV(rpath("seeds", "local_urls.csv"), uu, urlCols); err != nil {
		return err
	}
	dois, us := map[string]bool{}, map[string]bool{}
	for _, s := range uniq {
		dois[s["doi"]] = true
	}
	for _, u := range uu {
		us[u["url"]] = true
	}
	log.Printf("%d unique DOIs in %d seed rows; %d unique URLs", len(dois), len(uniq), len(us))
	return nil
}
