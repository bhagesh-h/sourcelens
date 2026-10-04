package main

// `litSearch test`: the append-only contract of the catalogue step on a
// throwaway catalogue. Mirrors python/buildcatalog/test_incremental.py.

import (
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func testRec(doi, date, title string) Rec {
	return Rec{"uid": "doi:" + doi, "doi": doi, "pmid": "", "pmcid": "", "title": title,
		"authors": "Doe J, Roe R", "journal": "Test J", "pub_date": date, "pub_year": date[:4],
		"pub_types": "Journal Article", "abstract": "We developed an epigenetic clock of biological age.",
		"sources": []any{"test"}}
}

func testWriteStore(recs []Rec) error {
	p := rpath("corpus", "records.jsonl.gz")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(f)
	for _, r := range recs {
		b, _ := jsonMarshal(r)
		zw.Write(append(b, '\n'))
	}
	zw.Close()
	return f.Close()
}

func testWriteHits(uids []string) error {
	var rows []Row
	for _, u := range uids {
		rows = append(rows, Row{"uid": u, "group": "epigenetic_clocks", "scope": "focused", "source": "test",
			"first_seen": "2026-01-01", "last_seen": "2026-01-01"})
	}
	return writeCSV(rpath("corpus", "search_hits.csv"), rows, hitCols)
}

func cmdTest() int {
	tmp, err := os.MkdirTemp("", "aging_test_")
	if err != nil {
		return fail(err.Error())
	}
	defer os.RemoveAll(tmp)
	// the configuration in use, else the template, copied into the throwaway output folder
	src := filepath.Join(Root, "config", "litsearch.template.yaml")
	if fileExists(configFile()) {
		src = configFile()
	}
	cfgCopy := filepath.Join(tmp, "research", "config", "litsearch.yaml")
	cb, err := os.ReadFile(src)
	if err == nil {
		_ = os.MkdirAll(filepath.Dir(cfgCopy), 0o755)
		err = os.WriteFile(cfgCopy, cb, 0o644)
	}
	if err != nil {
		return fail(err.Error())
	}
	// never the real output folder
	Root, Research = tmp, filepath.Join(tmp, "research")
	os.Setenv("LITSEARCH_CONFIG", cfgCopy)
	log := &Logger{w: os.Stderr}
	check := func(ok bool, msg string) {
		if !ok {
			fmt.Fprintf(os.Stderr, "AssertionError: %s\n", msg)
			os.RemoveAll(tmp)
			os.Exit(1)
		}
	}
	build := func() {
		if err := stepCatalogue(nil, log); err != nil {
			check(false, err.Error())
		}
	}
	read := func() []Row { rows, _ := readCSV(rpath("progress.csv")); return rows }

	a := testRec("10.1/a", "2015-03-01", "An epigenetic clock A")
	b := testRec("10.1/b", "2019-06-01", "An epigenetic clock B")
	c := testRec("10.1/c", "2021-09-01", "An epigenetic clock C")
	uid := func(r Rec) string { return r["uid"].(string) }
	check(testWriteStore([]Rec{a, b, c}) == nil, "cannot write the store")
	check(testWriteHits([]string{uid(a), uid(b), uid(c)}) == nil, "cannot write the hits")
	build()
	rows := read()
	var got []string
	for _, r := range rows {
		got = append(got, r["uid"])
	}
	check(strings.Join(got, " ") == strings.Join([]string{uid(a), uid(b), uid(c)}, " "), fmt.Sprint(rows))

	// the user edits the catalogue
	_, cols := readCSV(rpath("progress.csv"))
	for _, r := range rows {
		if r["uid"] == uid(a) {
			r["notes"], r["user_tags"], r["added_on"] = "read this", "must-read", "2026-01-15"
		}
	}
	check(writeCSV(rpath("progress.csv"), rows, cols) == nil, "cannot write progress.csv")

	// new research appears; C drops out of the searches
	d := testRec("10.1/d", "2017-01-20", "An epigenetic clock D")
	check(testWriteStore([]Rec{a, b, c, d}) == nil, "cannot write the store")
	check(testWriteHits([]string{uid(a), uid(b), uid(d)}) == nil, "cannot write the hits")
	build()
	rows = read()
	by := map[string]Row{}
	var dates []string
	for _, r := range rows {
		by[r["uid"]] = r
		dates = append(dates, r["date"])
	}
	td := today()
	check(len(rows) == 4, fmt.Sprintf("expected 4 rows, got %d", len(rows)))
	check(by[uid(d)]["added_on"] == td, "new record must be appended with today's date")
	check(by[uid(a)]["notes"] == "read this" && by[uid(a)]["user_tags"] == "must-read", "user columns lost")
	check(by[uid(a)]["added_on"] == "2026-01-15", "added_on of an existing row changed")
	check(strings.HasPrefix(by[uid(c)]["status"], "no longer matched"), "unmatched row must be kept and flagged")
	check(sort.StringsAreSorted(dates), "not chronological")
	added, _ := readCSV(rpath("changelog", "added_"+td+".csv"))
	inLog := false
	for _, r := range added {
		inLog = inLog || r["uid"] == uid(d)
	}
	check(inLog, "changelog misses the new record")
	fmt.Println("PASS: append-only contract holds " +
		"(new row appended, user edits and added_on kept, unmatched row kept, chronological, changelog)")
	return 0
}
