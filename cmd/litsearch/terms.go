package main

// Search terms. A term is a phrase ("base editing"), or several phrases joined
// with " & " that must all appear in the title or abstract
// ("CRISPR & base editing"). A search group matches a record when any of its
// terms matches.

import (
	"regexp"
	"strings"
	"unicode"
)

// termParts splits "CRISPR & base editing" into ["CRISPR", "base editing"].
func termParts(t string) []string {
	var out []string
	for _, p := range strings.Split(t, "&") {
		if p = strings.Join(strings.Fields(p), " "); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseTopic turns what a user types into search terms.
//
//	CRISPR base editing            -> ["CRISPR & base & editing"]  (all words required)
//	"base editing", prime editing  -> ["base editing", "prime & editing"]
//	graph neural networks OR GNN   -> ["graph & neural & networks", "GNN"]
//
// Commas, semicolons and the word OR separate alternatives; within an
// alternative, double quotes keep a phrase together.
func parseTopic(s string) []string {
	var alts []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if a := strings.TrimSpace(cur.String()); a != "" {
			alts = append(alts, a)
		}
		cur.Reset()
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case !inQuote && (r == ',' || r == ';'):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	var expanded []string
	for _, a := range alts {
		expanded = append(expanded, splitOutsideQuotes(a, " OR ")...)
	}
	var terms []string
	for _, a := range expanded {
		if t := topicTerm(a); t != "" && !contains(terms, t) {
			terms = append(terms, t)
		}
	}
	return terms
}

func splitOutsideQuotes(s, sep string) []string {
	var out []string
	inQuote, last := false, 0
	for i := 0; i < len(s); i++ {
		if s[i] == '"' {
			inQuote = !inQuote
		}
		if !inQuote && strings.HasPrefix(s[i:], sep) {
			out = append(out, s[last:i])
			last = i + len(sep)
			i += len(sep) - 1
		}
	}
	return append(out, s[last:])
}

// topicTerm: quoted parts are phrases, other words are required one by one.
func topicTerm(a string) string {
	var parts []string
	inQuote := false
	var cur strings.Builder
	push := func() {
		if p := strings.Join(strings.Fields(cur.String()), " "); p != "" {
			parts = append(parts, p)
		}
		cur.Reset()
	}
	for _, r := range a {
		switch {
		case r == '"':
			push()
			inQuote = !inQuote
		case !inQuote && unicode.IsSpace(r):
			push()
		case r == '&' && !inQuote:
			push()
		default:
			cur.WriteRune(r)
		}
	}
	push()
	return strings.Join(parts, " & ")
}

// --- source-specific queries -------------------------------------------------

func joinTermParts(parts []string, sep string, f func(string) string) string {
	q := make([]string, len(parts))
	for i, p := range parts {
		q[i] = f(p)
	}
	if len(q) == 1 {
		return q[0]
	}
	return "(" + strings.Join(q, sep) + ")"
}

func quoteTerm(p string) string { return `"` + strings.ReplaceAll(p, `"`, "") + `"` }

func pubmedTerm(t string) string {
	return joinTermParts(termParts(t), " AND ", func(p string) string { return quoteTerm(p) + "[tiab]" })
}

func epmcTerm(t string) string {
	return joinTermParts(termParts(t), " AND ", func(p string) string { return "TITLE_ABS:" + quoteTerm(p) })
}

func arxivTerm(t string) string {
	parts := termParts(t)
	if len(parts) == 1 {
		return "ti:" + quoteTerm(parts[0]) + " OR abs:" + quoteTerm(parts[0])
	}
	return joinTermParts(parts, " AND ", func(p string) string {
		return "(ti:" + quoteTerm(p) + " OR abs:" + quoteTerm(p) + ")"
	})
}

// openalexTerm: OpenAlex search syntax; phrases are quoted, single words are
// not (so OpenAlex can stem them); commas and | end a filter value there.
func openalexTerm(t string) string {
	return joinTermParts(termParts(t), " AND ", func(p string) string {
		p = strings.Join(strings.Fields(strings.NewReplacer(",", " ", "|", " ").Replace(p)), " ")
		if !strings.Contains(p, " ") {
			return strings.ReplaceAll(p, `"`, "")
		}
		return quoteTerm(p)
	})
}

// githubQuery: GitHub requires every quoted part.
func githubQuery(t string) string {
	parts := termParts(t)
	q := make([]string, len(parts))
	for i, p := range parts {
		q[i] = quoteTerm(p)
	}
	return strings.Join(q, " ")
}

// --- local matching ------------------------------------------------------------

// termMatcher reports whether text matches any term, every part of a term
// standing on its own (not inside a longer word or hyphenated compound).
func termMatcher(terms []string) func(string) bool {
	var res [][]*regexp.Regexp
	for _, t := range terms {
		var all []*regexp.Regexp
		for _, p := range termParts(t) {
			all = append(all, regexp.MustCompile(`(?i)(?:^|[^\w-])`+regexp.QuoteMeta(p)+`(?:$|[^\w-])`))
		}
		res = append(res, all)
	}
	return func(text string) bool {
		for _, all := range res {
			ok := true
			for _, re := range all {
				if !re.MatchString(text) {
					ok = false
					break
				}
			}
			if ok {
				return true
			}
		}
		return false
	}
}

// wordMatcher is looser than termMatcher: words stand on their own, but a
// hyphen counts as a separator ("CRISPR-Cas9" contains CRISPR), the words of a
// phrase may be joined by spaces or hyphens, and a plural "s" is allowed.
func wordMatcher(terms []string) func(string) bool {
	var res [][]*regexp.Regexp
	for _, t := range terms {
		var all []*regexp.Regexp
		for _, p := range termParts(t) {
			words := strings.FieldsFunc(p, func(r rune) bool { return unicode.IsSpace(r) || r == '-' })
			for i, w := range words {
				words[i] = regexp.QuoteMeta(w)
			}
			all = append(all, regexp.MustCompile(`(?i)(?:^|[^\pL\pN_])`+strings.Join(words, `[\s-]+`)+`(?:e?s)?(?:$|[^\pL\pN_])`))
		}
		res = append(res, all)
	}
	return func(text string) bool {
		for _, all := range res {
			ok := len(all) > 0
			for _, re := range all {
				if !re.MatchString(text) {
					ok = false
					break
				}
			}
			if ok {
				return true
			}
		}
		return false
	}
}

// termRegex builds a case-insensitive pattern (regexp2 syntax) that matches a
// text containing any term: words at word boundaries, a plural "s" allowed,
// spaces and hyphens interchangeable.
func termRegex(terms []string) string {
	word := func(p string) string {
		q := regexp.QuoteMeta(p)
		q = strings.ReplaceAll(q, `\ `, " ")
		q = regexp.MustCompile(`[ -]+`).ReplaceAllString(q, `[\s-]+`)
		rs := []rune(p)
		pre, post := "", ""
		if isWordRune(rs[0]) {
			pre = `\b`
		}
		if last := rs[len(rs)-1]; isWordRune(last) {
			post = `\b`
			if unicode.IsLetter(last) {
				// "networks" also matches "network": drop a plural s, then allow one
				if len(rs) > 3 && (last == 's' || last == 'S') && rs[len(rs)-2] != 's' && rs[len(rs)-2] != 'S' {
					q = q[:len(q)-1]
				}
				post = `(?:e?s)?\b`
			}
		}
		return pre + q + post
	}
	var alts []string
	for _, t := range terms {
		parts := termParts(t)
		if len(parts) == 0 {
			continue
		}
		if len(parts) == 1 {
			alts = append(alts, word(parts[0]))
			continue
		}
		var la strings.Builder
		la.WriteString("^")
		for _, p := range parts {
			la.WriteString(`(?=[\s\S]*` + word(p) + ")")
		}
		alts = append(alts, la.String())
	}
	if len(alts) == 0 {
		return `(?!)`
	}
	return "(?i)" + strings.Join(alts, "|")
}

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }
