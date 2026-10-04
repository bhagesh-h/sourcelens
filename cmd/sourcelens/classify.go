package main

// Rule-based annotation from the classify section of the configuration. The
// patterns use Perl/Python regular-expression syntax (look-arounds, inline
// flags), so they are compiled with regexp2.

import (
	"runtime"
	"strings"
	"sync"

	"github.com/dlclark/regexp2"
	"gopkg.in/yaml.v3"
)

type namedRe struct {
	name string
	re   *regexp2.Regexp
}

type catRule struct {
	name                  string
	pubTypes, title, text *regexp2.Regexp
}

type Classifier struct {
	modality, species, entities []namedRe
	category                    []catRule
	coreTitle                   *regexp2.Regexp
	landmarkRoles               map[string]bool
	human                       *regexp2.Regexp
	defaultCategory             string
	originCategory              string
	coreCategories              []string
}

func mustRe(p string, ci bool) *regexp2.Regexp {
	opt := regexp2.RegexOptions(0)
	if ci {
		opt = regexp2.IgnoreCase
	}
	return regexp2.MustCompile(p, opt)
}

func reSearch(re *regexp2.Regexp, s string) bool {
	if re == nil {
		return false
	}
	ok, err := re.MatchString(s)
	return err == nil && ok
}

func namedPatterns(n *yaml.Node, ci bool) []namedRe {
	var out []namedRe
	for _, kv := range orderedMapping(n) {
		out = append(out, namedRe{kv.Key, mustRe(kv.Value.Value, ci)})
	}
	return out
}

func LoadClassifier() *Classifier {
	root := configSection("classify")
	c := &Classifier{landmarkRoles: map[string]bool{}}
	c.modality = namedPatterns(nodeChild(root, "modality"), true)
	c.species = namedPatterns(nodeChild(root, "species"), true)
	for _, s := range c.species {
		if s.name == "human" {
			c.human = s.re
		}
	}
	var catNodes []*yaml.Node
	if n := nodeChild(root, "category"); n != nil {
		catNodes = n.Content
	}
	for _, rn := range catNodes {
		m := ymap(nodeValue(rn))
		r := catRule{name: pyStr(m["name"])}
		if v := pyStr(m["pub_types"]); v != "" {
			r.pubTypes = mustRe(v, true)
		}
		if v := pyStr(m["title"]); v != "" {
			r.title = mustRe(v, true)
		}
		if v := pyStr(m["text"]); v != "" {
			r.text = mustRe(v, true)
		}
		c.category = append(c.category, r)
	}
	if n := nodeChild(root, "core_title_terms"); n != nil && n.Value != "" {
		c.coreTitle = mustRe(n.Value, true)
	}
	if n := nodeChild(root, "landmark_roles"); n != nil {
		for _, x := range n.Content {
			c.landmarkRoles[x.Value] = true
			// catalogues configured before 0.0.1 name registry roles clock_*
			c.landmarkRoles[strings.Replace(x.Value, "clock_", "registry_", 1)] = true
		}
	}
	scalar := func(key, def string) string {
		if n := nodeChild(root, key); n != nil && n.Value != "" {
			return n.Value
		}
		return def
	}
	c.defaultCategory = scalar("default_category", "application/association")
	c.originCategory = scalar("origin_category", "method development")
	c.coreCategories = []string{c.originCategory, "benchmark/comparison", "review", "software/resource"}
	if n := nodeChild(root, "core_categories"); n != nil && len(n.Content) > 0 {
		c.coreCategories = nil
		for _, x := range n.Content {
			c.coreCategories = append(c.coreCategories, x.Value)
		}
	}
	ents := nodeChild(root, "entities")
	if ents == nil {
		ents = nodeChild(root, "clock_names") // name used before 0.0.1
	}
	for _, kv := range orderedMapping(ents) {
		v := kv.Value.Value
		var p string
		if strings.HasPrefix(v, "(?i)") {
			p = `(?i)(?<![\w-])(?:` + v[4:] + `)(?![\w-])`
		} else {
			p = `(?<![\w-])(?:` + v + `)(?![\w-])`
		}
		c.entities = append(c.entities, namedRe{kv.Key, mustRe(p, false)})
	}
	return c
}

type Annotation struct {
	Modality, Species, Category, Entities string
	CoreTitle                             bool
}

func (c *Classifier) Annotate(rec Rec) Annotation {
	title := str(rec, "title")
	parts := []string{}
	for _, k := range []string{"title", "abstract", "keywords", "mesh"} {
		parts = append(parts, str(rec, k))
	}
	text := strings.Join(parts, " ")
	pubtypes := str(rec, "pub_types")
	var mods, sp, ents []string
	for _, m := range c.modality {
		if reSearch(m.re, text) {
			mods = append(mods, m.name)
		}
	}
	for _, s := range c.species {
		if s.name != "human" && reSearch(s.re, text) {
			sp = append(sp, s.name)
		}
	}
	if reSearch(c.human, text) || strings.Contains(str(rec, "mesh"), "Humans") {
		sp = append([]string{"human"}, sp...)
	}
	cat := c.defaultCategory
	for _, r := range c.category {
		if (r.pubTypes != nil && reSearch(r.pubTypes, pubtypes)) || (r.title != nil && reSearch(r.title, title)) ||
			(r.text != nil && reSearch(r.text, text)) {
			cat = r.name
			break
		}
	}
	for _, k := range c.entities {
		if reSearch(k.re, text) {
			ents = append(ents, k.name)
		}
	}
	return Annotation{strings.Join(mods, "; "), strings.Join(sp, "; "), cat, strings.Join(ents, "; "),
		reSearch(c.coreTitle, title)}
}

// AnnotateAll annotates records in parallel; results keep the input order.
func (c *Classifier) AnnotateAll(recs []Rec) []Annotation {
	out := make([]Annotation, len(recs))
	var wg sync.WaitGroup
	ch := make(chan int, 256)
	for w := 0; w < runtime.NumCPU(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				out[i] = c.Annotate(recs[i])
			}
		}()
	}
	for i := range recs {
		ch <- i
	}
	close(ch)
	wg.Wait()
	return out
}
