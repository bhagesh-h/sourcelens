package main

// User settings (per machine) and catalogue folders (per topic).
//
// Settings live in <user config dir>/sourcelens/settings.yaml (Linux:
// ~/.config/sourcelens/settings.yaml) and hold the default output folder and
// the optional credentials. Each catalogue is a folder with its own
// configuration in config/sourcelens.yaml. The default topic's catalogue is
// the output folder itself; any other topic gets a subfolder named after it.

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed defaults/default.yaml
var defaultConfig []byte

// Settings: values for this machine; environment variables take precedence.
type Settings struct {
	Output         string `yaml:"output"`
	ContactEmail   string `yaml:"contact_email"`
	GithubToken    string `yaml:"github_token"`
	OpenAlexAPIKey string `yaml:"openalex_api_key"`
	NCBIAPIKey     string `yaml:"ncbi_api_key"`
}

var settingKeys = []string{"output", "contact_email", "github_token", "openalex_api_key", "ncbi_api_key"}

func settingsPath() string {
	if p := os.Getenv("SOURCELENS_SETTINGS"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(homeDir(), ".config")
	}
	return filepath.Join(dir, "sourcelens", "settings.yaml")
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

func loadSettings() Settings {
	var s Settings
	if b, err := os.ReadFile(settingsPath()); err == nil {
		_ = yaml.Unmarshal(b, &s)
	}
	return s
}

// expandHome turns "~/x" into an absolute path.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir(), p[1:])
	}
	return p
}

// outputBase: --output wins, then SOURCELENS_OUTPUT, then settings, then ~/sourcelens.
func outputBase(s Settings) string {
	p := orDefault(os.Getenv("SOURCELENS_OUTPUT"), s.Output)
	if p == "" {
		p = filepath.Join(homeDir(), "sourcelens")
	}
	if abs, err := filepath.Abs(expandHome(p)); err == nil {
		return abs
	}
	return p
}

// applySettings exports credentials for the step processes.
func applySettings(s Settings) {
	set := func(env, v string) {
		if os.Getenv(env) == "" && v != "" {
			os.Setenv(env, v)
		}
	}
	set("CONTACT_EMAIL", orDefault(os.Getenv("SOURCELENS_EMAIL"), s.ContactEmail))
	set("GITHUB_TOKEN", s.GithubToken)
	set("OPENALEX_API_KEY", s.OpenAlexAPIKey)
	set("NCBI_API_KEY", s.NCBIAPIKey)
}

// githubTokenFromCLI: an empty GITHUB_TOKEN is taken from `gh auth token` when the
// GitHub CLI is installed and logged in (it raises the search rate limit).
func githubTokenFromCLI() {
	if os.Getenv("GITHUB_TOKEN") != "" {
		return
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return
	}
	if out, err := exec.Command("gh", "auth", "token").Output(); err == nil {
		if t := strings.TrimSpace(string(out)); t != "" {
			os.Setenv("GITHUB_TOKEN", t)
		}
	}
}

// --- topics and catalogue folders ------------------------------------------------

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func topicSlug(t string) string {
	s := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(t), "-"), "-")
	if len(s) > 60 {
		s = strings.TrimRight(s[:60], "-")
	}
	return s
}

// configTopic reads the top-level `topic` of a configuration file.
func configTopic(b []byte) string {
	var c struct {
		Topic string `yaml:"topic"`
	}
	_ = yaml.Unmarshal(b, &c)
	return c.Topic
}

func defaultTopic() string { return configTopic(defaultConfig) }

// project: one catalogue folder and its configuration file.
type project struct {
	dir, config, topic string
	created            bool
}

// resolveProject finds (and with create, makes) the catalogue folder for a topic.
//
//	--output DIR            DIR
//	--topic T (default)     the output folder
//	--topic T (other)       <output folder>/<slug of T>
//	neither                 the output folder
func resolveProject(topic, output string, create bool, start string) (project, error) {
	s := loadSettings()
	applySettings(s)
	base := outputBase(s)
	var p project
	switch {
	case output != "":
		abs, err := filepath.Abs(expandHome(output))
		if err != nil {
			return p, err
		}
		p.dir = abs
	case topic != "" && topicSlug(topic) != topicSlug(defaultTopic()):
		p.dir = filepath.Join(base, topicSlug(topic))
	default:
		p.dir = base
	}
	p.config = filepath.Join(p.dir, "config", "sourcelens.yaml")
	if b, err := os.ReadFile(p.config); err == nil {
		p.topic = orDefault(configTopic(b), defaultTopic())
		if topic != "" && topicSlug(topic) != topicSlug(p.topic) {
			return p, fmt.Errorf("%s holds the catalogue for %q; use --output to choose another folder for %q",
				p.dir, p.topic, topic)
		}
	} else {
		if !create {
			what := "the default topic"
			if topic != "" {
				what = fmt.Sprintf("%q", topic)
			}
			return p, fmt.Errorf("no catalogue for %s in %s yet; run: sourcelens update%s", what, p.dir, topicFlag(topic))
		}
		var cfg []byte
		if topic == "" || topicSlug(topic) == topicSlug(defaultTopic()) {
			cfg, p.topic = defaultConfig, defaultTopic()
		} else {
			cfg, p.topic = topicConfig(topic, start), topic
		}
		if err := os.MkdirAll(filepath.Dir(p.config), 0o755); err != nil {
			return p, err
		}
		if err := os.WriteFile(p.config, cfg, 0o644); err != nil {
			return p, err
		}
		p.created = true
	}
	Research = p.dir
	os.Setenv("SOURCELENS_PROJECT", p.dir)
	os.Setenv("SOURCELENS_CONFIG", p.config)
	return p, nil
}

func topicFlag(t string) string {
	if t == "" {
		return ""
	}
	return fmt.Sprintf(" --topic %q", t)
}

// extendStart moves the catalogue's start_date back when a run asks for older work.
func extendStart(p project, start string) (bool, error) {
	cur, _ := queryWindow()
	if start == "" || start >= cur {
		return false, nil
	}
	b, err := os.ReadFile(p.config)
	if err != nil {
		return false, err
	}
	re := regexp.MustCompile(`(?m)^(\s*start_date:\s*)["']?\d{4}-\d{2}-\d{2}["']?`)
	if !re.Match(b) {
		return false, nil
	}
	b = re.ReplaceAll(b, []byte(`${1}"`+start+`"`))
	return true, os.WriteFile(p.config, b, 0o644)
}

// --- configuration for a new topic -------------------------------------------------

// yq: a double-quoted YAML string (JSON syntax, no HTML escaping)
func yq(s string) string  { b, _ := jsonMarshal(s); return string(b) }
func ysq(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// genericCategories: what a work contributes, for any topic; first match wins.
const genericCategories = `  category:
    - name: correction/retraction
      pub_types: 'Erratum|Retraction|Correction|Expression of Concern'
      title: '^(correction|erratum|retraction|retracted)\b|^(author )?correction to'
    - name: commentary
      pub_types: '(^|; )(Comment|Editorial|Letter|News)(;|$)'
      title: '^(comment|editorial|reply|response to|letter)\b'
    - name: review
      pub_types: 'Review|Meta-Analysis|review-article'
      title: '\breview\b|systematic review|meta-analys|\boverview\b|\bsurvey\b|state of the art|\bprimer\b|perspective|scoping|\bconsensus\b|\broadmap\b'
    - name: software/resource
      title: '\b(R|Python) package|\bpackage\b|\bsoftware\b|\btool(kit|box)?s?\b|web ?server|\bdatabase\b|\batlas\b|\bpipeline\b|\bdataset\b|\bbenchmark(ing)? (dataset|platform|suite)|\bplatform\b|\blibrary\b'
    - name: benchmark/comparison
      text: '\bbenchmark|head-to-head|systematic(ally)? (evaluat|compar)|\bcomparison of\b|\bcomparative (study|evaluation|analysis)\b'
    - name: method development
      text: '\b(we|here,? we|this (study|work|paper))\b[^.]{0,100}\b(develop|propose|present|introduce|design|construct|build|built|train)\w*\b[^.]{0,120}\b(method|model|algorithm|framework|approach|tool|technique|system|architecture|pipeline|predictor|classifier|score|index)s?\b'
    - name: intervention/trial
      text: '\brandomi[sz]ed\b|\bclinical trial\b|\bplacebo\b'
`

// topicConfig writes the configuration of a new catalogue from what the user typed.
func topicConfig(topic, start string) []byte {
	terms := parseTopic(topic)
	if start == "" {
		start = time.Now().AddDate(-1, 0, 0).Format("2006-01-02")
	}
	re := termRegex(terms)
	var b strings.Builder
	fmt.Fprintf(&b, "# sourcelens configuration for the topic %s,\n", yq(topic))
	fmt.Fprintf(&b, "# created %s from: sourcelens %s\n", today(), yq(topic))
	b.WriteString("#\n# Edit freely: the next update reads this file again. Every key is described in\n")
	b.WriteString("# docs/configuration.md of the sourcelens repository.\n\n")
	fmt.Fprintf(&b, "topic: %s\n\n", yq(topic))
	b.WriteString("# Local folders mined for DOIs and links (absolute paths), e.g. your notes or a project.\n")
	b.WriteString("references: []\n\n")
	b.WriteString("search:\n")
	fmt.Fprintf(&b, "  start_date: %s      # moved back automatically when a run asks for older work\n", yq(start))
	b.WriteString("  end_date: \"today\"\n")
	b.WriteString("  sources: [pubmed, europepmc, arxiv, openalex]\n")
	b.WriteString("  europepmc_sources: nonmed    # PubMed already returns MEDLINE\n")
	b.WriteString("  max_results: 5000            # per group from OpenAlex, newest first\n")
	b.WriteString("  groups:\n    topic:\n      scope: focused\n")
	fmt.Fprintf(&b, "      label: %s\n      terms:\n", yq(topic))
	for _, t := range terms {
		fmt.Fprintf(&b, "        - %s\n", yq(t))
	}
	b.WriteString("\nclassify:\n")
	b.WriteString("  default_category: study\n")
	b.WriteString("  origin_category: method development\n")
	b.WriteString("  core_categories: [method development, benchmark/comparison, review, software/resource]\n")
	b.WriteString("  # a row is \"core\" when its title matches this pattern (or its category is a core category)\n")
	fmt.Fprintf(&b, "  core_title_terms: %s\n", ysq(re))
	b.WriteString("  landmark_roles: []\n")
	b.WriteString(genericCategories)
	b.WriteString("  modality: {}\n")
	b.WriteString("  species: {}\n")
	b.WriteString("  # named methods, models or measures to tag in the entities column; name: pattern\n")
	b.WriteString("  entities: {}\n\n")
	b.WriteString("repos:\n  github_queries:\n")
	for _, t := range terms {
		fmt.Fprintf(&b, "    - %s\n", yq(t))
	}
	fmt.Fprintf(&b, "  relevance: %s\n", ysq(re))
	fmt.Fprintf(&b, "  package_terms: %s\n", ysq(re))
	b.WriteString("  known_packages: {}\n\n")
	b.WriteString("websites:\n  # name, urls (alternatives tried in order), type, note, related_doi\n  sites: []\n")
	return []byte(b.String())
}

// --- `sourcelens config` ------------------------------------------------------------

const configHelp = `sourcelens config: show or change the settings of this machine

  sourcelens config                         settings file, output folder, credentials (masked)
  sourcelens config set KEY VALUE           change a setting
  sourcelens config unset KEY               remove a setting
  sourcelens config path                    print the settings file path

keys: output, contact_email, github_token, openalex_api_key, ncbi_api_key

Environment variables override the file: SOURCELENS_OUTPUT, SOURCELENS_EMAIL,
GITHUB_TOKEN, OPENALEX_API_KEY, NCBI_API_KEY. SOURCELENS_SETTINGS points to
another settings file.
`

func saveSettings(m map[string]string) error {
	path := settingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# sourcelens settings for this machine (see: sourcelens config --help)\n")
	for _, k := range settingKeys {
		if v, ok := m[k]; ok && v != "" {
			fmt.Fprintf(&b, "%s: %s\n", k, yq(v))
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func settingsMap(s Settings) map[string]string {
	return map[string]string{"output": s.Output, "contact_email": s.ContactEmail, "github_token": s.GithubToken,
		"openalex_api_key": s.OpenAlexAPIKey, "ncbi_api_key": s.NCBIAPIKey}
}

func mask(v string) string {
	switch {
	case v == "":
		return "(not set)"
	case len(v) <= 6:
		return "set"
	}
	return v[:4] + strings.Repeat("*", 8)
}

func cmdConfig(argv []string) int {
	if hasHelp(argv) {
		fmt.Print(configHelp)
		return 0
	}
	s := loadSettings()
	m := settingsMap(s)
	switch {
	case len(argv) == 0:
		fmt.Printf("settings file   %s\n", settingsPath())
		fmt.Printf("output folder   %s\n", outputBase(s))
		fmt.Printf("default topic   %s\n", defaultTopic())
		fmt.Printf("contact_email   %s\n", orDefault(orDefault(os.Getenv("SOURCELENS_EMAIL"), s.ContactEmail), "(not set: Unpaywall is skipped)"))
		for _, k := range []string{"github_token", "openalex_api_key", "ncbi_api_key"} {
			fmt.Printf("%-15s %s\n", k, mask(m[k]))
		}
		fmt.Printf("pdf conversion  %s\n", pdfToolsStatus())
		return 0
	case argv[0] == "path" && len(argv) == 1:
		fmt.Println(settingsPath())
		return 0
	case argv[0] == "set" && len(argv) == 3, argv[0] == "unset" && len(argv) == 2:
		k := argv[1]
		if !contains(settingKeys, k) {
			return fail(fmt.Sprintf("unknown setting %q (one of: %s)", k, strings.Join(settingKeys, ", ")))
		}
		if argv[0] == "set" {
			v := argv[2]
			if k == "output" {
				abs, err := filepath.Abs(expandHome(v))
				if err != nil {
					return fail(err.Error())
				}
				v = abs
			}
			m[k] = v
		} else {
			delete(m, k)
		}
		if err := saveSettings(m); err != nil {
			return fail(err.Error())
		}
		fmt.Printf("saved %s\n", settingsPath())
		return 0
	}
	return fail("usage: sourcelens config [set KEY VALUE | unset KEY | path]")
}

// --- `sourcelens list` ----------------------------------------------------------------

func cmdList(argv []string) int {
	if hasHelp(argv) {
		fmt.Print("sourcelens list: the catalogues in the output folder (the default topic and one subfolder per topic)\n")
		return 0
	}
	base := outputBase(loadSettings())
	type entry struct{ topic, dir string }
	var found, subsFound []entry
	if b, err := os.ReadFile(filepath.Join(base, "config", "sourcelens.yaml")); err == nil {
		found = append(found, entry{orDefault(configTopic(b), defaultTopic()), base})
	}
	subs, _ := os.ReadDir(base)
	for _, d := range subs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(base, d.Name())
		if b, err := os.ReadFile(filepath.Join(dir, "config", "sourcelens.yaml")); err == nil {
			subsFound = append(subsFound, entry{orDefault(configTopic(b), d.Name()), dir})
		}
	}
	sort.SliceStable(subsFound, func(i, j int) bool { return subsFound[i].topic < subsFound[j].topic })
	found = append(found, subsFound...)
	if len(found) == 0 {
		fmt.Printf("no catalogues in %s yet; start one with: sourcelens \"your topic\"\n", base)
		return 0
	}
	for _, e := range found {
		rows, _ := readCSV(filepath.Join(e.dir, "progress.csv"))
		fmt.Printf("%-40s %6d rows  %s\n", runeCut(e.topic, 40), len(rows), e.dir)
	}
	return 0
}
