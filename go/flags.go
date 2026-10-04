package main

// Command-line flags, as python/common/flags.py: --name value, --name=value,
// -name, bool flags (--flag, --flag=false); -h / --help / -help print the help.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type flagKind int

const (
	kStr flagKind = iota
	kInt
	kBool
)

type flagSpec struct {
	name string
	def  string
	kind flagKind
}

type flagSet map[string]string

func (f flagSet) str(k string) string { return f[k] }
func (f flagSet) int(k string) int    { n, _ := strconv.Atoi(strings.TrimPrefix(f[k], "+")); return n }
func (f flagSet) bool(k string) bool  { return f[k] == "true" }

var intRe = regexp.MustCompile(`^[+-]?\d+$`)

// errHelp: the help text was printed; exit 0.
type errHelp struct{}

func (errHelp) Error() string { return "help" }

func parseFlags(argv []string, spec []flagSpec, help string) (flagSet, error) {
	o := flagSet{}
	kinds := map[string]flagKind{}
	for _, s := range spec {
		o[s.name] = s.def
		kinds[s.name] = s.kind
	}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "-h" || a == "--help" || a == "-help" {
			fmt.Print(help)
			return nil, errHelp{}
		}
		if !strings.HasPrefix(a, "-") {
			return nil, fmt.Errorf(`unexpected argument "%s"`, a)
		}
		name, val, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		kind, ok := kinds[name]
		if !ok {
			return nil, fmt.Errorf("flag provided but not defined: -%s", name)
		}
		if kind == kBool {
			v := strings.ToLower(val)
			o[name] = pyBoolFlag(val == "" || (v != "false" && v != "0"))
			continue
		}
		if val == "" {
			i++
			if i >= len(argv) {
				return nil, fmt.Errorf("flag needs an argument: -%s", name)
			}
			val = argv[i]
		}
		if kind == kInt && !intRe.MatchString(val) {
			return nil, fmt.Errorf(`invalid value "%s" for flag -%s: not an integer`, val, name)
		}
		o[name] = val
	}
	return o, nil
}

func pyBoolFlag(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// parseStepArgs reads the options the CLI passes to a step (--name value, --name=value, bare --flag).
func parseStepArgs(args []string) map[string]string {
	o := map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			continue
		}
		name, val, hasEq := strings.Cut(a[2:], "=")
		if !hasEq {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				val = args[i+1]
				i++
			} else {
				val = "true"
			}
		}
		o[name] = val
	}
	return o
}
