package main

// The configuration file (sections search, classify, repos, websites), read in
// file order where order matters. Mirrors config() in src/sourcelens/common/agelit.py.

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

// configFile: the one configuration file; its live copy is kept in the output folder.
func configFile() string {
	if p := os.Getenv("SOURCELENS_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(Research, "config", "sourcelens.yaml")
}

var (
	cfgOnce sync.Once
	cfgRoot *yaml.Node
)

// configSection: one section of the configuration (search, classify, repos, websites).
func configSection(name string) *yaml.Node {
	cfgOnce.Do(func() { cfgRoot = loadYAMLNode(configFile()) })
	if cfgRoot == nil {
		fmt.Fprintf(os.Stderr, "sourcelens: no configuration at %s (run sourcelens update to create the catalogue)\n", configFile())
		os.Exit(1)
	}
	n := nodeChild(cfgRoot, name)
	if n == nil {
		fmt.Fprintf(os.Stderr, "sourcelens: section '%s' missing from %s\n", name, configFile())
		os.Exit(1)
	}
	return n
}

func loadConfig(section string, out any) error { return configSection(section).Decode(out) }

// configRoot: the whole configuration document (nil when there is none).
func configRoot() *yaml.Node {
	cfgOnce.Do(func() { cfgRoot = loadYAMLNode(configFile()) })
	return cfgRoot
}

type Group struct {
	Name       string
	Scope      string   `yaml:"scope"`
	Label      string   `yaml:"label"`
	Terms      []string `yaml:"terms"`
	RequireAny []string `yaml:"require_any"`
}

// loadGroups: search groups of the search section in file order.
func loadGroups() ([]Group, string) {
	top := configSection("search")
	var groups []Group
	sources := "nonmed"
	for i := 0; i+1 < len(top.Content); i += 2 {
		key, val := top.Content[i].Value, top.Content[i+1]
		switch key {
		case "europepmc_sources":
			sources = val.Value
		case "groups":
			for j := 0; j+1 < len(val.Content); j += 2 {
				var g Group
				_ = val.Content[j+1].Decode(&g)
				g.Name = val.Content[j].Value
				groups = append(groups, g)
			}
		}
	}
	return groups, sources
}

func filterGroups(all []Group, names, scope string) []Group {
	var out []Group
	want := splitList(names)
	for _, g := range all {
		if len(want) > 0 && !contains(want, g.Name) {
			continue
		}
		if scope != "" && scope != "all" && g.Scope != scope {
			continue
		}
		out = append(out, g)
	}
	return out
}

// orderedMap decodes a YAML mapping keeping key order.
type kvPair struct {
	Key   string
	Value *yaml.Node
}

func orderedMapping(n *yaml.Node) []kvPair {
	var out []kvPair
	if n == nil || n.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, kvPair{n.Content[i].Value, n.Content[i+1]})
	}
	return out
}

func loadYAMLNode(path string) *yaml.Node {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc yaml.Node
	if yaml.Unmarshal(b, &doc) != nil || len(doc.Content) == 0 {
		return nil
	}
	return doc.Content[0]
}

func nodeChild(n *yaml.Node, key string) *yaml.Node {
	for _, kv := range orderedMapping(n) {
		if kv.Key == key {
			return kv.Value
		}
	}
	return nil
}
