# todo

Living list for sourcelens. Done items stay listed with their date, so the
history of features is kept.

sourcelens is a general tool: the topic comes from the command line or the
configuration. The aging-clock catalogue is the default topic. There are two
implementations of the same command, Python (`src/sourcelens/`, published on
PyPI) and Go (`cmd/sourcelens/`). Every feature goes into both, with the same
flags and output, and `parity/check.sh` must pass.

Legend: `[x]` done, `[ ]` open

## Done

### Pipeline, 2026-09-29 to 2026-10-04
- [x] Seeds from local reference folders; PubMed, Europe PMC and arXiv searches; seed resolution (PubMed, Europe PMC, Crossref, DataCite)
- [x] `progress.csv`: chronological and append-only. `added_on`, `notes` and `user_tags` are never overwritten. Changelog per day; rule-based classification.
- [x] OpenAlex citations and dates; bioRxiv preprint to journal links
- [x] Full texts from PMC S3, bioRxiv, Europe PMC, arXiv and Unpaywall: md, txt, pdf, xml and metadata.json. Downloads resume from disk and are deferred on HTTP 429.
- [x] Repositories (GitHub search, links in papers, CRAN, Bioconductor, PyPI, Zenodo); websites (curated and cited, with archive dates)
- [x] Findings summary, query tool, append-only test

### CLI, 2026-10-04
- [x] `update` in parallel stages; `--sources`, `--types`, `--paper-types`, `--range` (1d 7d 2w 1m 6m 1y 10y), `--from` / `--to`, `--dry-run` with a full-text estimate
- [x] `retry`, `status`, `sources`, `test`, `query`, `export` (APA AMA MLA CHI HAR VAN IEE NAT BIB RIS ENW CSL)
- [x] Python and Go implementations with the same commands, flags and output; parity check
- [x] Run lock shared by both implementations

### 1.0.0, 2026-10-04
- [x] Renamed to sourcelens (PyPI name, command, Go module github.com/bhagesh-h/sourcelens); publish workflow and publish.md
- [x] `sourcelens "TOPIC"`: a catalogue for any topic from one line; commas, semicolons and OR separate alternatives, words are required, quotes keep phrases
- [x] One catalogue per topic (`--topic`, `--dir`, `sourcelens list`); the default topic in the output folder itself
- [x] OpenAlex search (all fields of research), filtered locally to whole-word matches; OpenAlex open-access PDF links as a full-text source
- [x] A new topic starts 12 months back; an earlier `--from` / `--range` extends the window
- [x] Settings per machine (`sourcelens config`): output folder, contact email, API keys; environment overrides; `gh auth token` fallback
- [x] Topic-specific code moved into the configuration: reference folders, seed readers, origin and core categories, summary data layers, website relevance; `clocks` column renamed `entities`
- [x] Native installs: `pip install sourcelens` (PyMuPDF optional, poppler fallback) and `go install`; Docker optional
- [x] Unit tests (Go and Python), CI and release workflows (Go binaries, PyPI trusted publishing), Makefile, Dockerfile
- [x] README rewritten; `docs/configuration.md`; CHANGELOG
- [x] Output folder for this machine kept at the Zotero folder (settings); the existing catalogue migrated to the 1.0 format

## Open

- [ ] Register the pending trusted publishers on PyPI and TestPyPI (publish.md), then tag v1.0.0
- [ ] Go: PDF to Markdown closer to pymupdf4llm (headings, tables)
- [ ] Ordered JSON for records in Go, so `records.jsonl.gz` is byte-identical between implementations (content already is)
- [ ] Parity cases that run `update` and `retry` against network sources on a sandbox catalogue
- [ ] Windows support (file locks use flock)

## Ideas

- [ ] Shell completion (bash, zsh)
- [ ] Proximity search for multi-word topics where a source supports it (PubMed `[tiab:~N]`)
- [ ] Websites: archive-date lookups in parallel
