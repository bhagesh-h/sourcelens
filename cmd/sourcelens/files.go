package main

// `sourcelens files`: list, copy, move or delete the full texts and attachments
// of a catalogue. Moved and deleted files are recorded in the indexes, the
// paper's metadata.json and progress.csv, so updates do not fetch them again.
// Python twin: src/sourcelens/query/files.py.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dlclark/regexp2"
)

var (
	fileColumns = []string{"uid", "title", "kind", "file", "ext", "bytes", "status", "path", "label", "caption", "url"}
	paperFiles  = [][2]string{{"pdf", "paper.pdf"}, {"md", "paper.md"}, {"txt", "paper.txt"}, {"xml", "paper.jats.xml"}}
	fileKinds   = map[string]bool{"paper": true, "figure": true, "table": true, "supplementary": true}
)

const filesHelp = `sourcelens files [filters] [file options] [action]: list, copy, move or delete
the full texts and attachments of a catalogue

  sourcelens files --ext xlsx,csv                             every downloaded spreadsheet
  sourcelens files --kind figure --title "epigenetic clock"   figures of matching papers
  sourcelens files --ext pptx,docx --copy-to ~/slides
  sourcelens files --in exports/picked.csv --kind paper --ext pdf --copy-to ~/to-read --flat
  sourcelens files --status listed --ext xlsx                 spreadsheets offered but not downloaded
  sourcelens files --kind figure --range 5y --move-to /data/figures
  sourcelens files --ext txt --delete --yes

` + projectHelp + "\n" + filterHelp + `
files
  --ext LIST          file extensions: pdf,md,txt,xml,jpg,png,xlsx,csv,docx,pptx,zip,...
  --name RE           regex over the file name, label and caption
  --kind LIST         paper (full texts), figure, table, supplementary; attachment = the last three
  --status LIST       ok (on disk, default), listed (offered, not downloaded), skipped,
                      failed, moved, deleted; all for every file

action (one; default: list the files)
  --limit N           files printed (default 50; --out gets all)
  --out FILE          CSV of all matching files; relative paths go to <catalogue>/exports/
  --copy-to DIR       copy the files to DIR/<paper>/<file> (attachments in DIR/<paper>/attachments/)
  --move-to DIR       the same, then remove them from the catalogue; updates will not fetch them again
  --delete            delete the files from the catalogue (needs --yes); updates will not fetch them again
  --yes               confirm --delete
  --flat              copy or move to DIR/<paper>__<file>, without subfolders
`

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// collectFiles: every file (full text or attachment) of the given catalogue rows.
func collectFiles(rows []Row) []Row {
	idxRows, _ := readCSV(rpath("fulltext", "fulltext_index.csv"))
	index := map[string]Row{}
	for _, r := range idxRows {
		index[r["uid"]] = r
	}
	atts := readAttIndex()
	var out []Row
	for _, r := range rows {
		uid, title := r["uid"], r["title"]
		if folder := index[uid]["folder"]; folder != "" {
			for _, pf := range paperFiles {
				rel := folder + "/" + pf[1]
				if st, err := os.Stat(researchFile(rel)); err == nil && !st.IsDir() {
					out = append(out, Row{"uid": uid, "title": title, "kind": "paper", "file": pf[1], "ext": pf[0],
						"bytes": strconv.FormatInt(st.Size(), 10), "status": "ok", "path": rel, "label": "", "caption": "", "url": ""})
				}
			}
		}
		for _, a := range atts[uid] {
			if a["file"] != "" {
				out = append(out, Row{"uid": uid, "title": title, "kind": a["kind"], "file": a["file"], "ext": a["ext"],
					"bytes": a["bytes"], "status": a["status"], "path": a["path"], "label": a["label"],
					"caption": a["caption"], "url": a["url"]})
			}
		}
	}
	return out
}

func pickFiles(files []Row, o flagSet) ([]Row, error) {
	exts, kinds, statuses := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, e := range strings.Split(o["ext"], ",") {
		if e = pyStrip(e); e != "" {
			exts[strings.TrimLeft(strings.ToLower(e), ".")] = true
		}
	}
	for _, k := range strings.Split(o["kind"], ",") {
		if k = strings.ToLower(pyStrip(k)); k != "" {
			kinds[k] = true
		}
	}
	if kinds["attachment"] || kinds["attachments"] {
		kinds["figure"], kinds["table"], kinds["supplementary"] = true, true, true
	}
	var unknown []string
	for k := range kinds {
		if !fileKinds[k] && k != "attachment" && k != "attachments" {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("--kind must be paper, figure, table, supplementary or attachment, not %s", sortedCopy(unknown)[0])
	}
	for _, x := range strings.Split(o["status"], ",") {
		if x = strings.ToLower(pyStrip(x)); x != "" {
			statuses[x] = true
		}
	}
	var nrx *regexp2.Regexp
	if o["name"] != "" {
		var err error
		if nrx, err = regexp2.Compile(o["name"], regexp2.IgnoreCase); err != nil {
			return nil, fmt.Errorf("invalid regular expression for --name")
		}
	}
	var out []Row
	for _, f := range files {
		if len(exts) > 0 && !exts[strings.ToLower(f["ext"])] {
			continue
		}
		if len(kinds) > 0 && !kinds[f["kind"]] {
			continue
		}
		if !statuses["all"] && !statuses[f["status"]] {
			continue
		}
		if nrx != nil && !reSearch(nrx, f["file"]+" "+f["label"]+" "+f["caption"]) {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

func fileDestination(base string, f Row, flat bool) string {
	paper := slug(f["uid"], 120)
	switch {
	case flat:
		return filepath.Join(base, paper+"__"+f["file"])
	case f["kind"] == "paper":
		return filepath.Join(base, paper, f["file"])
	}
	return filepath.Join(base, paper, "attachments", f["file"])
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if st, err := os.Stat(src); err == nil {
		_ = os.Chtimes(dst, st.ModTime(), st.ModTime())
	}
	return nil
}

func moveFile(src, dst string) error {
	if os.Rename(src, dst) == nil {
		return nil
	}
	// another file system: copy, then remove
	if err := copyFile(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

// recordRemoved: moved or deleted files are recorded in the indexes, metadata.json and progress.csv.
func recordRemoved(done []Row, action, where string) {
	status, reason := "deleted", "deleted by sourcelens files"
	if action == "move" {
		status, reason = "moved", "moved to "+where+" by sourcelens files"
	}
	byUID := map[string][]Row{}
	var order []string
	for _, f := range done {
		if _, ok := byUID[f["uid"]]; !ok {
			order = append(order, f["uid"])
		}
		byUID[f["uid"]] = append(byUID[f["uid"]], f)
	}
	atts := readAttIndex()
	idxRows, _ := readCSV(rpath("fulltext", "fulltext_index.csv"))
	index := map[string]Row{}
	for _, r := range idxRows {
		index[r["uid"]] = r
	}
	for _, uid := range order {
		fs := byUID[uid]
		names := map[string]Row{}
		paperGone := map[string]bool{}
		hasPaper := false
		for _, f := range fs {
			if f["kind"] != "paper" {
				names[f["file"]] = f
			} else {
				paperGone[f["file"]] = true
				hasPaper = true
			}
		}
		for _, a := range atts[uid] {
			if f, ok := names[a["file"]]; ok && a["file"] != "" {
				a["status"], a["path"], a["reason"], a["checked_on"] = status, f["dest"], reason, today()
			}
		}
		idx := index[uid]
		if idx == nil || idx["folder"] == "" {
			continue
		}
		folder := researchFile(idx["folder"])
		anyLeft := false
		for _, pf := range paperFiles {
			on := fileExists(filepath.Join(folder, pf[1]))
			idx["has_"+pf[0]] = pyBool(on)
			anyLeft = anyLeft || on
		}
		if !anyLeft && hasPaper {
			idx["status"], idx["reason"] = "removed", reason
		}
		if idx["attachments"] != "" && atts[uid] != nil {
			idx["attachments"] = attColumn(atts[uid])
		}
		metaPath := filepath.Join(folder, "metadata.json")
		if b, err := os.ReadFile(metaPath); err == nil {
			if meta := decodeJSON(b); meta != nil {
				ft := jmap(meta, "fulltext")
				if ft == nil {
					ft = map[string]any{}
				}
				kept := map[string]any{}
				for k, v := range ymap(ft["files"]) {
					if !paperGone[k] {
						kept[k] = v
					}
				}
				ft["files"] = kept
				for _, a := range jlist(ft["attachments"]) {
					if am := ymap(a); am != nil {
						if _, ok := names[jstr(am, "file")]; ok {
							am["status"] = status
						}
					}
				}
				if idx["status"] == "removed" {
					ft["status"], ft["reason"] = "removed", reason
				}
				meta["fulltext"] = ft
				_ = writeJSON(metaPath, meta)
			}
		}
	}
	_ = writeAttIndex(atts)
	keys := sortedKeys(index)
	out := make([]Row, len(keys))
	for i, k := range keys {
		out[i] = index[k]
	}
	_ = writeCSV(rpath("fulltext", "fulltext_index.csv"), out, indexCols)

	progress, _ := readCSV(rpath("progress.csv"))
	cols := map[string]string{"pdf": "fulltext_pdf", "md": "fulltext_md", "txt": "fulltext_txt"}
	for _, r := range progress {
		fs := byUID[r["uid"]]
		if len(fs) == 0 {
			continue
		}
		for _, f := range fs {
			if c, ok := cols[f["ext"]]; ok && f["kind"] == "paper" {
				r[c] = ""
			}
		}
		if index[r["uid"]]["status"] == "removed" {
			r["fulltext_status"], r["fulltext_reason"] = "removed", reason
		}
		r["attachments"] = attSummary(atts[r["uid"]])
	}
	_ = writeCSV(rpath("progress.csv"), progress, progressCols)
}

func cmdFiles(argv []string) int {
	o, err := parseFlags(argv, withSpec(flagSpec{"name", "", kStr}, flagSpec{"kind", "", kStr}, flagSpec{"status", "ok", kStr},
		flagSpec{"copy-to", "", kStr}, flagSpec{"move-to", "", kStr}, flagSpec{"delete", "false", kBool},
		flagSpec{"yes", "false", kBool}, flagSpec{"flat", "false", kBool}, flagSpec{"limit", "50", kInt},
		flagSpec{"out", "", kStr}), filesHelp)
	if _, ok := err.(errHelp); ok {
		return 0
	}
	if err != nil {
		return fail(err.Error())
	}
	if _, err := resolveProject(o["topic"], o["dir"], false, ""); err != nil {
		return fail(err.Error())
	}
	var actions []string
	if o["copy-to"] != "" {
		actions = append(actions, "copy")
	}
	if o["move-to"] != "" {
		actions = append(actions, "move")
	}
	if o.bool("delete") {
		actions = append(actions, "delete")
	}
	if len(actions) > 1 {
		return fail("choose one of --copy-to, --move-to and --delete")
	}
	// --ext picks files here, not papers with attachments of that extension
	rowFilter := flagSet{}
	for k, v := range o {
		rowFilter[k] = v
	}
	rowFilter["ext"] = ""
	rows, _, err := inputRows(rowFilter)
	if err != nil {
		return fail(err.Error())
	}
	sel, _, err := selectRows(rowFilter, rows)
	if err != nil {
		return fail(err.Error())
	}
	files, err := pickFiles(collectFiles(sel), o)
	if err != nil {
		return fail(err.Error())
	}
	total := 0
	for _, f := range files {
		total += atoiSafe(f["bytes"])
	}
	fmt.Printf("%s (%s) of %d matching papers\n", plural(len(files), "file"), orDefault(humanBytes(strconv.Itoa(total)), "0 B"), len(sel))
	limit := o.int("limit")
	for i, f := range files {
		if i >= limit {
			break
		}
		fmt.Println(strings.Join([]string{runeCut(f["kind"], 13), runeCut(f["ext"], 5), orDefault(humanBytes(f["bytes"]), "-"),
			f["status"], runeCut(f["file"], 60), runeCut(f["title"], 70)}, " | "))
	}
	if o["out"] != "" {
		path := outPath(o["out"])
		if err := writeCSV(path, files, fileColumns); err != nil {
			return fail(err.Error())
		}
		fmt.Printf("wrote %s\n", path)
	}
	if len(actions) == 0 {
		return 0
	}
	action := actions[0]
	var todo []Row
	for _, f := range files {
		if f["status"] == "ok" && f["path"] != "" && fileExists(researchFile(f["path"])) {
			todo = append(todo, f)
		}
	}
	if action == "delete" && !o.bool("yes") {
		fmt.Printf("%s would be deleted from the catalogue; add --yes to delete them\n", plural(len(todo), "file"))
		return 1
	}
	if action != "copy" {
		lock, err := acquirePipelineLock("sourcelens files")
		if err != nil {
			fmt.Fprintln(os.Stderr, "sourcelens: an update is running; try again later")
			return 2
		}
		defer lock.Close()
	}
	base := ""
	if action != "delete" {
		base = expandHome(orDefault(o["copy-to"], o["move-to"]))
		if abs, err := filepath.Abs(base); err == nil {
			base = abs
		}
	}
	var done []Row
	for _, f := range todo {
		src := researchFile(f["path"])
		if action == "delete" {
			if err := os.Remove(src); err != nil {
				return fail(err.Error())
			}
		} else {
			dest := fileDestination(base, f, o.bool("flat"))
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return fail(err.Error())
			}
			var err error
			if action == "copy" {
				err = copyFile(src, dest)
			} else {
				err = moveFile(src, dest)
			}
			if err != nil {
				return fail(err.Error())
			}
			f["dest"] = filepath.ToSlash(dest)
		}
		done = append(done, f)
	}
	if base != "" && len(done) > 0 {
		manifest := make([]Row, len(done))
		for i, f := range done {
			m := Row{}
			for k, v := range f {
				m[k] = v
			}
			m["path"] = f["dest"]
			manifest[i] = m
		}
		_ = writeCSV(filepath.Join(base, "sourcelens_files.csv"), manifest, fileColumns)
	}
	if (action == "move" || action == "delete") && len(done) > 0 {
		recordRemoved(done, action, filepath.ToSlash(base))
	}
	verb := map[string]string{"copy": "copied to", "move": "moved to", "delete": "deleted"}[action]
	if base != "" {
		fmt.Printf("%s %s %s\n", plural(len(done), "file"), verb, base)
	} else {
		fmt.Printf("%s %s\n", plural(len(done), "file"), verb)
	}
	return 0
}
