# todo — litSearch (Python and Go implementations)

This is the living feature list for `litSearch`. Done items stay listed with
their date, so the feature history is kept. Nothing marked done may be removed.

litSearch is a general tool. The research topic comes from the configuration.
The aging-clock catalogue is this workspace's use case, not a feature of the
tool.

There are two independent implementations with **the same commands, flags
("tags") and outputs**. Each has its own Dockerfile, and they share no
scripts:

- `python/`: Python implementation (`python/Dockerfile`, launcher `python/litSearch`)
- `go/`: Go implementation (`go/Dockerfile`, launcher `go/litSearch`; `./litSearch` links here)
- shared data only:
  - `config/litsearch.template.yaml`
  - `config/local.yaml` (git-ignored)
  - the output folder named in `local.yaml`, holding the live
    `config/litsearch.yaml`
- `parity/`: runs both implementations and compares them; belongs to neither

Legend: `[x]` done · `[~]` in progress · `[ ]` open

## Done

### Pipeline (Python), 2026-09-29 … 2026-10-04
- [x] Seeds from local reference projects; PubMed, Europe PMC, arXiv searches; seed resolution (PubMed → Europe PMC → Crossref → DataCite)
- [x] Catalogue `progress.csv`: chronological and append-only. `added_on`, `notes` and `user_tags` are never overwritten. Changelog per run; rule-based classification.
- [x] OpenAlex citations / dates; bioRxiv preprint → journal links
- [x] Full texts: PMC S3 → bioRxiv → Europe PMC → arXiv → Unpaywall; md / txt / pdf / xml + metadata.json; resume from disk; deferral on 429
- [x] Repositories (GitHub search + links in papers + CRAN / Bioconductor / PyPI / Zenodo), websites (curated + cited, archive dates)
- [x] Findings summary, query tool, append-only contract test
- [x] Outputs moved to the Zotero-synced output folder (logs in `logs/`) — 2026-10-04

### CLI v1 (Python, `agingcat`, 2026-10-04), now `litSearch`
- [x] `update` in parallel stages; `--sources`, `--types`, `--range` (1d 7d 2w 1m 6m 1y 10y), `--from` / `--to`
- [x] full-text `--formats` / `--sources`; repository `--sources` with carry-over
- [x] `--dry-run`, `--parallel`, `--workers`, `--scope`, `--tiers`, `--min-stars`, `--stop-on-error`, `--heartbeat`
- [x] per-step logs + plan.txt; `query`, `status`, `sources`, `test`
- [x] locks on record store and search-hit log; arXiv batched + incremental; website / GitHub caches
- [x] Renamed to `litSearch` (2026-10-04)

### litSearch 2.0: both implementations, 2026-10-04
- [x] Python code in `python/`, with its own Dockerfile and launcher; output folder mounted at `/research`; catalogue paths relative to it
- [x] run lock `.pipeline.lock` in the output folder, shared by both CLIs and `update_all.sh`
- [x] `update --paper-types article,review,preprint,…` (full-text selection)
- [x] `update --dry-run`: plan + full-text estimate
- [x] `retry [--status] [--types] [--sources] [--workers] [--dry-run]`
- [x] `status`: rows, types, tiers, full-text index, papers not yet tried, last builds, last CLI run with failures, running lock
- [x] `query`: `--text --title --doi --uid --from --to --range --tier --type --category --modality --clock --species --added-since --has-fulltext --fulltext --sort --limit --out`
- [x] `export`: same filters + `--format APA,AMA,MLA,CHI,HAR,VAN,IEE,NAT,BIB,RIS,ENW,CSL` (aliases), `--out` file or prefix
- [x] `corpus/references.csv` (full authors, volume, issue, pages) written by the catalogue build
- [x] one flag parser with the same rules and error messages in both (`python/common/flags.py`, `go/flags.go`); one date parser with the same errors
- [x] Go port of every step, no Python involved: seeds, PubMed, Europe PMC, arXiv, seed resolution, catalogue + classification, OpenAlex, preprint links, full texts (JATS → md; PDF → txt / md via poppler), links, GitHub search, repositories / packages, websites, summary
- [x] `go/Dockerfile` (multi-stage, static binary + poppler, ~60 MB); `go/run.sh` rebuilds the image when Go sources change; `./litSearch` → Go
- [x] Go writes CSV byte-for-byte like Python's `csv` module, keeps Python's dict / JSON key order where files are compared, follows Python's Unicode `\s` and `.split()`, and decodes XML character references
- [x] Python follows redirects with non-UTF-8 `Location` headers (as Go and browsers do); Go sends `Accept: */*` (as `requests`)
- [x] Parity: `parity/check.sh` (56 CLI cases identical); step-by-step comparison on copies of the output folder gives identical outputs for seeds, catalogue, links, summary, repositories, websites, and PubMed / Europe PMC / arXiv (one-week window, incl. re-fetched records)

### Configuration and local settings, 2026-10-04
- [x] `config/local.yaml` (git-ignored) for paths and credentials; `config/local.template.yaml` placeholder; `.gitignore`
- [x] launchers read `local.yaml`; credentials passed to containers by name only (not on the command line); `gh auth token` fallback
- [x] contact email no longer hard-coded: optional; without it, no polite-pool address and Unpaywall skipped (logged)
- [x] one configuration file with sections `search`, `classify`, `repos`, `websites` (was four files)
- [x] live configuration kept in the output folder (`<research_dir>/config/litsearch.yaml`), seeded from `config/litsearch.template.yaml`; snapshot per run in `logs/cli_<stamp>/litsearch.yaml`
- [x] topic-neutral tool wording (help, User-Agent `litSearch/2.0`); default start date from the configuration instead of a fixed 2011
- [x] README rewritten: general tool first, aging clocks as the documented use case

## Open

- [ ] Use-case code into the configuration:
  - seed adapters for the FALCONAge registry / 300BCG manifests as configurable adapters;
  - `clock development` for registry origin papers;
  - the summary's data-layer list and "clock" headings
- [ ] Go: PDF → Markdown quality closer to pymupdf4llm (headings, tables)
- [ ] Parity cases for `retry` and `update` runs on a sandbox output folder (not just `--dry-run`)
- [ ] Ordered JSON for records in Go, so `records.jsonl.gz` is byte-identical too (content already equal)

## Backlog / ideas

- [ ] Shell completion (bash/zsh)
- [ ] Websites: archive-date lookups in parallel
- [ ] Go: faster link mining (currently ~1.5× Python on 16k full texts)
