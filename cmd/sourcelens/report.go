package main

// The run report: reports/runreport_<stamp>.html, one self-contained page with
// the logo, date, time and version, the run's steps, the catalogue's numbers
// and a searchable, filterable table of every row. The template
// assets/report.html is shared with the Python twin
// src/sourcelens/buildcatalog/report.py; the data is embedded as
// gzip-compressed, base64-encoded JSON.

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed assets/report.html
var reportTemplate string

//go:embed assets/logo.png
var reportLogo []byte

var (
	reportCols = []string{"date", "added_on", "year", "resource_type", "tier", "category", "title", "authors", "venue", "doi",
		"url", "cited_by", "open_access", "license", "fulltext_status", "fulltext_reason", "fulltext_pdf",
		"fulltext_md", "attachments", "code_links", "modality", "entities", "species", "summary", "summary_from",
		"keywords", "fulltext_sources", "pending", "found_by", "related", "details", "uid", "notes", "user_tags"}
	reportCut = map[string]int{"summary": 500, "keywords": 300, "code_links": 300, "details": 200, "entities": 300}
)

func (m omap) get(k string) (any, bool) {
	for _, kv := range m {
		if kv.k == k {
			return kv.v, true
		}
	}
	return nil, false
}

// htmlEscape is python's html.escape (quotes included).
var htmlEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#x27;").Replace

// reportSteps: the steps of a run from its logs/cli_<stamp>/summary.txt.
func reportSteps(logdir string) []any {
	out := []any{}
	if logdir == "" {
		return out
	}
	b, err := os.ReadFile(filepath.Join(logdir, "summary.txt"))
	if err != nil {
		return out
	}
	for _, line := range splitLinesPy(string(b)) {
		parts := strings.Split(line, "\t")
		if len(parts) < 4 {
			continue
		}
		out = append(out, omap{{"name", parts[0]}, {"ok", parts[1] == "rc=0"},
			{"minutes", strings.ReplaceAll(parts[2], " min", "")}, {"last", lastLine(filepath.Join(logdir, parts[3]))}})
	}
	return out
}

// attachmentStats: per file extension, files known and files downloaded.
func attachmentStats() []any {
	total, ok := map[string]int{}, map[string]int{}
	for _, rows := range readAttIndex() {
		for _, a := range rows {
			if a["file"] == "" {
				continue
			}
			e := orDefault(a["ext"], "?")
			total[e]++
			if a["status"] == "ok" {
				ok[e]++
			}
		}
	}
	ks := sortedKeys(total)
	sort.SliceStable(ks, func(i, j int) bool { return total[ks[i]] > total[ks[j]] })
	out := []any{}
	for _, e := range ks {
		out = append(out, omap{{"ext", e}, {"total", total[e]}, {"ok", ok[e]}})
	}
	return out
}

func reportData(label string, started time.Time, logdir, command, topic string, rc int, finished time.Time) omap {
	rows := overviewRows(nil)
	steps := reportSteps(logdir)
	failed := 0
	for _, s := range steps {
		if v, _ := s.(omap).get("ok"); v == false {
			failed++
		}
	}
	outcome := "completed"
	switch {
	case failed == 1:
		outcome = "1 step failed"
	case failed > 1:
		outcome = fmt.Sprintf("%d steps failed", failed)
	case rc != 0:
		outcome = "failed"
	}
	logs := ""
	if logdir != "" {
		if st, err := os.Stat(logdir); err == nil && st.IsDir() {
			logs = researchRel(logdir)
		}
	}
	meta := omap{{"title", "sourcelens run report: " + topic}, {"topic", topic}, {"label", label}, {"command", command},
		{"folder", Research}, {"version", version}, {"impl", "go"},
		{"started", started.Format("2006-01-02 15:04:05")}, {"finished", finished.Format("2006-01-02 15:04:05")},
		{"minutes", fmt.Sprintf("%.1f", finished.Sub(started).Minutes())}, {"outcome", outcome},
		{"run_date", started.Format("2006-01-02")}, {"logs", logs}, {"summary_max", reportCut["summary"]}}
	table := make([]any, 0, len(rows))
	for _, r := range rows {
		cells := make([]any, len(reportCols))
		for i, c := range reportCols {
			v := r[c]
			if n, ok := reportCut[c]; ok {
				v = runeCut(v, n)
			}
			cells[i] = v
		}
		table = append(table, cells)
	}
	cols := make([]any, len(reportCols))
	for i, c := range reportCols {
		cols[i] = c
	}
	return omap{{"meta", meta}, {"steps", steps}, {"columns", cols}, {"rows", table}, {"attachments", attachmentStats()}}
}

func reportPage(d omap) (string, error) {
	raw, err := jsonMarshal(d)
	if err != nil {
		return "", err
	}
	var gz bytes.Buffer
	w, _ := gzip.NewWriterLevel(&gz, gzip.BestCompression)
	if _, err := w.Write(raw); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	meta, _ := d.get("meta")
	title, _ := meta.(omap).get("title")
	page := strings.Replace(reportTemplate, "__SL_TITLE__", htmlEscape(title.(string)), 1)
	page = strings.Replace(page, "__SL_LOGO__", "data:image/png;base64,"+base64.StdEncoding.EncodeToString(reportLogo), 1)
	page = strings.Replace(page, "__SL_DATA__", base64.StdEncoding.EncodeToString(gz.Bytes()), 1)
	return page, nil
}

// writeRunReport writes reports/runreport_<stamp>.html and returns its path.
func writeRunReport(label, stamp string, started time.Time, logdir, command, topic string, rc int) (string, error) {
	page, err := reportPage(reportData(label, started, logdir, command, topic, rc, time.Now()))
	if err != nil {
		return "", err
	}
	path := rpath("reports", "runreport_"+stamp+".html")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(page), 0o644); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}
