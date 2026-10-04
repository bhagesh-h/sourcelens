package main

// Local seeds ("seeds" step): DOIs and links found in the reference folders
// listed under `references` in the catalogue's configuration. Every text file
// is scanned; a folder with a clock registry (registry/data/clocks.yaml) or
// literature manifests (literature/manifest*.tsv) is also read in detail.

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
	skipName  = regexp.MustCompile(`(?i)^fulltext|^full_text|\.fulltext\.`)
	seedURLRe = regexp.MustCompile("(?i)https?://[^\\s\"'<>)\\]}|,`]+")
	// code and data hosts; seeds.keep_url_domains in the configuration adds more
	codeDataHosts = `github\.com|gitlab\.|bitbucket\.org|zenodo\.org|figshare\.com|osf\.io|` +
		`bioconductor\.org|cran\.r-project\.org|pypi\.org|huggingface\.co|ncbi\.nlm\.nih\.gov/geo`
	seedCols     = []string{"doi", "role", "source_repo", "source_file", "registry_id", "registry_year", "data_type", "species", "context"}
	urlCols      = []string{"url", "kind", "source_repo", "source_file", "context"}
	registryCols = []string{"clock_id", "name", "year", "species", "data_type", "generation", "tissue", "platform",
		"predicts", "unit", "scale_type", "model_type", "population", "n_features", "availability", "doi",
		"citation", "notes", "coefficient_url"}
)

const maxSeedBytes = 5_000_000

// seedConfig: the `references` list and the `seeds` section of the configuration.
type seedConfig struct {
	refs          []string
	methodsTopics *regexp.Regexp
	skipPaths     []string
	urlKeep       *regexp.Regexp
}

func loadSeedConfig() seedConfig {
	var c struct {
		References []string `yaml:"references"`
		Seeds      struct {
			MethodsTopics  string   `yaml:"methods_topics"`
			SkipPaths      []string `yaml:"skip_paths"`
			KeepURLDomains string   `yaml:"keep_url_domains"`
		} `yaml:"seeds"`
	}
	if cfgRoot := configRoot(); cfgRoot != nil {
		_ = cfgRoot.Decode(&c)
	}
	sc := seedConfig{skipPaths: append([]string{"/papers/"}, c.Seeds.SkipPaths...)}
	for _, r := range c.References {
		if r = strings.TrimSpace(expandHome(r)); r != "" {
			sc.refs = append(sc.refs, filepath.Clean(r))
		}
	}
	if c.Seeds.MethodsTopics != "" {
		sc.methodsTopics = regexp.MustCompile(c.Seeds.MethodsTopics)
	}
	keep := codeDataHosts
	if c.Seeds.KeepURLDomains != "" {
		keep += "|" + c.Seeds.KeepURLDomains
	}
	sc.urlKeep = regexp.MustCompile(`(?i)` + keep)
	return sc
}

// findFile: the first path under base ending in suffix (shallow folders first).
func findFile(base, suffix string) string {
	found := ""
	_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return filepath.SkipDir
		}
		if d.IsDir() && skipDirs[d.Name()] {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(filepath.ToSlash(p), suffix) {
			found = p
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

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

// seedsRegistry reads a clock registry: registry/data/clocks.yaml (one entry
// per clock with its origin DOI and coefficient source), the evidence.yaml
// beside it, and docs/references.yml at the folder root.
func seedsRegistry(base string, seeds, urls *[]Row, log *Logger) []Row {
	repo := filepath.Base(base)
	parent := filepath.Dir(base)
	rel := func(p string) string { r, _ := filepath.Rel(parent, p); return filepath.ToSlash(r) }
	var entries []Row
	if reg := findFile(base, "registry/data/clocks.yaml"); reg != "" {
		regRel := rel(reg)
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
				entries = append(entries, row)
				if row["doi"] != "" {
					*seeds = append(*seeds, Row{"doi": row["doi"], "role": "registry_origin", "source_repo": repo,
						"source_file": regRel, "registry_id": kv.Key, "registry_year": row["year"],
						"data_type": row["data_type"], "species": row["species"], "context": row["citation"]})
				}
				if row["coefficient_url"] != "" {
					*urls = append(*urls, Row{"url": row["coefficient_url"], "kind": urlKind(row["coefficient_url"]),
						"source_repo": repo, "source_file": regRel, "context": "coefficient source of " + kv.Key})
				}
			}
			log.Printf("%s registry: %d entries", repo, len(entries))
		}
		ev := filepath.Join(filepath.Dir(reg), "evidence.yaml")
		if root := loadYAMLNode(ev); root != nil {
			for _, kv := range orderedMapping(nodeChild(root, "sources")) {
				sm := ymap(nodeValue(kv.Value))
				if d := normDOI(pyStr(sm["doi"])); d != "" {
					*seeds = append(*seeds, Row{"doi": d, "role": "benchmark", "source_repo": repo,
						"source_file": rel(ev), "context": strings.Join(pyFields(pyStr(sm["citation"])), " ")})
				}
			}
		}
	}
	refsFile := filepath.Join(base, "docs", "references.yml")
	if root := loadYAMLNode(refsFile); root != nil {
		n := 0
		for _, g := range jlist(nodeValue(nodeChild(root, "groups"))) {
			gm := ymap(g)
			for _, e := range jlist(gm["entries"]) {
				em := ymap(e)
				d := normDOI(orDefault(pyStr(em["doi"]), pyStr(em["text"])))
				if d != "" {
					*seeds = append(*seeds, Row{"doi": d, "role": "registry_reference", "source_repo": repo,
						"source_file": rel(refsFile), "context": fmt.Sprintf("[%s] %s", pyStr(gm["title"]), pyStr(em["text"]))})
					n++
				}
			}
		}
		log.Printf("%s docs/references.yml: %d DOIs", repo, n)
	}
	return entries
}

// seedsManifests reads literature manifests (literature/manifest*.tsv with
// doi, topic, title and question columns).
func seedsManifests(base string, sc seedConfig, seeds *[]Row, log *Logger) {
	repo := filepath.Base(base)
	for _, nr := range [][2]string{{"manifest_methods.tsv", "methods_reference"},
		{"manifest_local.tsv", "methods_reference"}, {"manifest.tsv", "cohort_paper"}} {
		path := filepath.Join(base, "literature", nr[0])
		rel := repo + "/literature/" + nr[0]
		f, err := os.Open(path)
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
			if role == "methods_reference" && sc.methodsTopics != nil && !sc.methodsTopics.MatchString(topic) {
				role = "methods_other"
			}
			ctx := fmt.Sprintf("[%s] %s", topic, m["title"])
			if m["question"] != "" {
				ctx += " | " + m["question"]
			}
			*seeds = append(*seeds, Row{"doi": d, "role": role, "source_repo": repo, "source_file": rel, "context": ctx})
		}
		f.Close()
		log.Printf("%s %s: %d rows", repo, nr[0], n)
	}
}

func pyStripRight(s, chars string) string { return strings.TrimRight(s, chars) }

// seedsScan: every DOI and every code, data or kept website link in the text
// files of a reference folder.
func seedsScan(base string, sc seedConfig, seeds, urls *[]Row, log *Logger) {
	repo := filepath.Base(base)
	parent := filepath.Dir(base)
	nDOI, nURL := 0, 0
	_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !textSuffixes[strings.ToLower(filepath.Ext(p))] {
			return nil
		}
		rel, _ := filepath.Rel(parent, p)
		rel = filepath.ToSlash(rel)
		for _, sp := range sc.skipPaths {
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
				if sc.urlKeep.MatchString(u) && !strings.Contains(u, "doi.org") {
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
	var seeds, urls, registry []Row
	sc := loadSeedConfig()
	if len(sc.refs) == 0 {
		log.Printf("no reference folders in the configuration (references); nothing to read")
	}
	for _, base := range sc.refs {
		if !fileExists(base) {
			log.Printf("missing %s; skipped", base)
			continue
		}
		registry = append(registry, seedsRegistry(base, &seeds, &urls, log)...)
		seedsManifests(base, sc, &seeds, log)
		seedsScan(base, sc, &seeds, &urls, log)
	}
	seen := map[string]bool{}
	var uniq []Row
	for _, s := range seeds {
		k := s["doi"] + "\x00" + s["role"] + "\x00" + s["source_file"] + "\x00" + s["registry_id"]
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
	if err := writeCSV(rpath("seeds", "registry_entries.csv"), registry, registryCols); err != nil {
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
