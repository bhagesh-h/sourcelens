package main

// The dry run's table ("dryrun" step): lists the attachments of open-access PMC
// articles (nothing is downloaded) and writes reports/dryrun_<stamp>.csv, the
// progress.csv columns plus the overview columns. Python twin:
// src/sourcelens/buildcatalog/dryrun.py.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// listOne: the attachment rows of one article from the bucket listing (nil:
// the bucket could not be read).
func (x *ftCtx) listOne(uid, pmcid, folder string) []Row {
	l, failed := x.pmcListing(pmcid)
	if failed {
		return nil
	}
	if l == nil {
		return []Row{noneRow(uid, "not in the PMC open-access subset")}
	}
	xmlBytes, _ := os.ReadFile(filepath.Join(folder, "paper.jats.xml"))
	caps := attCaptions(xmlBytes)
	var rows []Row
	for _, mf := range pmcMedia(pmcid, l) {
		fname := safeName(mf.name)
		d := describe(mf.name, caps)
		target := filepath.Join(folder, "attachments", fname)
		status, path := "listed", ""
		if fileExists(target) {
			status, path = "ok", researchRel(target)
		}
		size := ""
		if mf.size >= 0 {
			size = strconv.Itoa(mf.size)
		}
		rows = append(rows, Row{"uid": uid, "file": fname, "ext": fileExt(fname), "kind": d.kind, "label": d.label,
			"caption": d.caption, "bytes": size, "status": status, "path": path,
			"url": fmt.Sprintf("%s/%s.%d/%s", s3Base, pmcid, l.version, urlQuote(mf.name)), "checked_on": today(), "reason": ""})
	}
	if len(rows) == 0 {
		rows = []Row{noneRow(uid, "no attachments")}
	}
	return rows
}

func stepDryrun(args []string, log *Logger) error {
	o := parseStepArgs(args)
	if o["stamp"] == "" {
		return fmt.Errorf("--stamp is required")
	}
	workers := atoiSafe(orDefault(o["workers"], "8"))
	progress, _ := readCSV(rpath("progress.csv"))
	byUID := map[string]Row{}
	for _, r := range progress {
		byUID[r["uid"]] = r
	}
	atts := readAttIndex()
	var todo []Row
	for _, r := range progress {
		if _, listed := atts[r["uid"]]; r["pmcid"] != "" && !listed && paperTypeSet[r["resource_type"]] {
			todo = append(todo, r)
		}
	}
	log.Printf("%d catalogue rows; listing the attachments of %d PMC articles", len(progress), len(todo))
	if len(todo) > 0 {
		store := LoadStore()
		f := &fetcher{lim: NewLimiter(time.Second, hostInterval), blocked: map[string]time.Time{}, log: log}
		var mu sync.Mutex
		var wg sync.WaitGroup
		ch := make(chan Row)
		done := 0
		for w := 0; w < max(1, workers); w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				x := &ftCtx{f: f, c: f.client()}
				for r := range ch {
					rec := store.Get(r["uid"])
					if rec == nil {
						rec = Rec{"uid": r["uid"], "pmcid": r["pmcid"]}
					}
					rows := x.listOne(r["uid"], r["pmcid"], folderFor(rec, byUID[r["uid"]]))
					mu.Lock()
					if rows != nil {
						atts[r["uid"]] = rows
					}
					done++
					if done%200 == 0 {
						_ = writeAttIndex(atts)
						log.Printf("  %d/%d listed", done, len(todo))
					}
					mu.Unlock()
				}
			}()
		}
		for _, r := range todo {
			ch <- r
		}
		close(ch)
		wg.Wait()
		_ = writeAttIndex(atts)
	}
	out := overviewRows(progress)
	path := rpath("reports", "dryrun_"+o["stamp"]+".csv")
	if err := writeCSV(path, out, append(append([]string(nil), progressCols...), overviewExtra...)); err != nil {
		return err
	}
	exts := newCounter()
	nfiles, npapers := 0, 0
	for _, uid := range sortedKeys(atts) {
		has := false
		for _, a := range atts[uid] {
			if a["file"] != "" {
				nfiles++
				exts.add(orDefault(a["ext"], "?"), 1)
				has = true
			}
		}
		if has {
			npapers++
		}
	}
	ks := append([]string(nil), exts.keys...)
	sort.SliceStable(ks, func(i, j int) bool { return exts.n[ks[i]] > exts.n[ks[j]] })
	if len(ks) > 8 {
		ks = ks[:8]
	}
	parts := make([]string, len(ks))
	for i, k := range ks {
		parts[i] = fmt.Sprintf("%s %d", k, exts.n[k])
	}
	log.Printf("attachments known: %d files in %d papers; %s", nfiles, npapers, strings.Join(parts, ", "))
	log.Printf("wrote %s: %d rows", researchRel(path), len(out))
	return nil
}
