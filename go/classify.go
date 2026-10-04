package main

// Rule-based annotation from the classify section of the configuration. The patterns are Python
// regular expressions (look-arounds, inline flags), so they are compiled with
// regexp2, which follows the same syntax.

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
	modality, species, clocks []namedRe
	category                  []catRule
	coreTitle                 *regexp2.Regexp
	landmarkRoles             map[string]bool
	human                     *regexp2.Regexp
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
	for _, rn := range nodeChild(root, "category").Content {
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
	c.coreTitle = mustRe(nodeChild(root, "core_title_terms").Value, true)
	for _, x := range nodeChild(root, "landmark_roles").Content {
		c.landmarkRoles[x.Value] = true
	}
	for _, kv := range orderedMapping(nodeChild(root, "clock_names")) {
		v := kv.Value.Value
		var p string
		if strings.HasPrefix(v, "(?i)") {
			p = `(?i)(?<![\w-])(?:` + v[4:] + `)(?![\w-])`
		} else {
			p = `(?<![\w-])(?:` + v + `)(?![\w-])`
		}
		c.clocks = append(c.clocks, namedRe{kv.Key, mustRe(p, false)})
	}
	return c
}

type Annotation struct {
	Modality, Species, Category, Clocks string
	CoreTitle                           bool
}

func (c *Classifier) Annotate(rec Rec) Annotation {
	title := str(rec, "title")
	parts := []string{}
	for _, k := range []string{"title", "abstract", "keywords", "mesh"} {
		parts = append(parts, str(rec, k))
	}
	text := strings.Join(parts, " ")
	pubtypes := str(rec, "pub_types")
	var mods, sp, clocks []string
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
	cat := "application/association"
	for _, r := range c.category {
		if (r.pubTypes != nil && reSearch(r.pubTypes, pubtypes)) || (r.title != nil && reSearch(r.title, title)) ||
			(r.text != nil && reSearch(r.text, text)) {
			cat = r.name
			break
		}
	}
	for _, k := range c.clocks {
		if reSearch(k.re, text) {
			clocks = append(clocks, k.name)
		}
	}
	return Annotation{strings.Join(mods, "; "), strings.Join(sp, "; "), cat, strings.Join(clocks, "; "),
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
