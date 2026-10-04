# litSearch <img src="logo/logo.png" alt="litSearch logo" width="180" align="right"/>

litSearch builds a chronological catalogue of everything published on a
research topic, and keeps it up to date. The catalogue covers papers,
preprints, code repositories, software packages, websites and databases.
For each paper it also stores the open-access full text (PDF, Markdown, plain
text, source XML) and a metadata file.

A YAML configuration defines the topic: the search terms, the classification
rules, and the repository and website settings. The tool itself has no topic
of its own.

- **Append-only.** A run adds what has appeared since the last run. It never
  removes rows or overwrites what you have edited.
- **Dockerised.** Everything runs in Docker.
- **Two implementations.** Python and Go provide the same commands, flags and
  outputs, each with its own Dockerfile and no shared scripts.

This workspace's configuration covers one use case: epigenetic clocks,
biological-age clocks and aging scores. See
[Use case: aging clocks](#use-case-aging-clocks) for its search terms,
status and findings.


## Quick start

```bash
# 1. local settings: paths on this machine and credentials (git-ignored)
cp config/local.template.yaml config/local.yaml
$EDITOR config/local.yaml                 # research_dir, reference_repos, contact_email, keys

# 2. run; the first call builds the Docker image and copies
#    config/litsearch.template.yaml to <research_dir>/config/litsearch.yaml
./litSearch update --dry-run              # show the plan and a full-text estimate
./litSearch update                        # everything, configured start date -> today
./litSearch update --range 1y             # only work published in the last year
./litSearch update --types papers --range 6m          # metadata only
./litSearch update --types pdf,md --sources pmc,unpaywall --range 2y
./litSearch update --types repo,website               # repositories, packages, websites
./litSearch retry                         # re-try deferred / partial / failed full texts
./litSearch status
./litSearch query --title "epigenetic clock" --range 6m
./litSearch export --from 2024 --tier landmark --format APA,BIB,RIS --out landmark

# a full update takes 30-90 min; run it detached so a closed terminal does not stop it
nohup setsid ./litSearch update > /dev/null 2>&1 &
```

`./litSearch` points to the Go implementation (`go/litSearch`). The Python
implementation is `python/litSearch`. Both take the same commands and flags,
read the same configuration and write the same output folder.


## Configuration

| file | in git | holds |
|---|---|---|
| `config/local.yaml` | **no** (git-ignored) | paths on this machine (`research_dir`, `reference_repos`) and credentials (`contact_email`, `github_token`, `openalex_api_key`, `ncbi_api_key`) |
| `config/local.template.yaml` | yes | placeholder copy of the above |
| `<research_dir>/config/litsearch.yaml` | output folder | **the live configuration**, kept and synced with the catalogue it produces |
| `config/litsearch.template.yaml` | yes | default configuration; copied to the output folder when that has none |
| `<research_dir>/logs/cli_<stamp>/litsearch.yaml` | output folder | snapshot of the configuration each `update` / `retry` run used |

Only the launchers (`python/run.sh`, `go/run.sh`, `python/pipeline/update_all.sh`)
read `local.yaml`. They do three things with it:

- mount the output folder at `/research`, the workspace at `/work`, and each
  reference project read-only at `/refs/<name>`;
- pass the credentials into the container as environment variables, by name
  only, so they appear neither in files nor on the `docker run` command line;
- fall back to `gh auth token` when `github_token` is empty.

Environment variables of the same names override `local.yaml`.
`LITSEARCH_RESEARCH` overrides `research_dir`.

`litsearch.yaml` has four sections:

| section | what it sets |
|---|---|
| `search` | date window (`start_date`, `end_date: today`), Europe PMC scope, and search groups. Each group has `terms`, an optional `require_any` gate, and a scope: `focused` groups enter `progress.csv`; `broad` groups are metadata only, in `corpus/broad_hits.csv`. |
| `classify` | regular-expression rules that fill the `modality`, `species`, `category`, `clocks` and `tier` columns: `core_title_terms`, `landmark_roles`, a dictionary of named measures |
| `repos` | GitHub search queries, the relevance pattern for repositories, package search terms, known packages per index |
| `websites` | curated websites, databases and calculators (`name`, `urls`, `type`, `note`, `related_doi`) |

Edit the live copy in the output folder. Classification columns are
recomputed from the rules on every run; `notes` and `user_tags` are never
touched.

Without a `contact_email`, requests to Crossref, OpenAlex and NCBI carry no
polite-pool address, and Unpaywall is skipped.


## Commands

| command | does |
|---|---|
| `update` | search, download and rebuild, in parallel stages |
| `retry` | download again the full texts whose last attempt was deferred (rate limit), partial or failed, then rebuild |
| `query` | filter the catalogue; print rows or write a CSV |
| `export` | write the selected entries as references in 12 formats |
| `status` | catalogue size, full-text coverage, last builds, last run and its failures, running update |
| `sources` | list the values that `--sources`, `--types`, `--paper-types` and `--format` accept |
| `test` | check the append-only contract on a throwaway catalogue |
| `version` | print the version and the implementation |

`litSearch <command> --help` lists a command's options.

### update

| option | values | default |
|---|---|---|
| `--sources` | `pubmed europepmc arxiv local openalex pmc biorxiv unpaywall github cran bioconductor pypi zenodo websites`, or groups `literature`, `fulltext`, `repos`, `packages` | all |
| `--types` | `papers` (metadata), `pdf`, `md`, `txt`, `xml`, `repo`, `website`, or groups `fulltext`, `all` | all |
| `--paper-types` | `article review preprint report thesis "conference paper" "book chapter"` (full texts only) | all |
| `--range` | span back from `--to`: `1d 7d 2w 1m 6m 1y 2y 10y` … | configured window |
| `--from`, `--to` | `2024`, `2024-03`, `2024-03-15`, `today`, or a span (`--from 2y`) | configured `start_date` … today |
| `--scope` | `focused`, `broad`, `all` (search groups) | all |
| `--tiers` | full-text tiers `landmark,core,related` | all three |
| `--workers` / `--parallel` | full-text download threads / steps run at once | 12 / 5 |
| `--min-stars` | GitHub search hits need this many stars | 3 |
| `--heartbeat` | seconds between progress lines | 120 |
| `--stop-on-error`, `--dry-run` | stop after a failed stage / print plan and estimate only | |

A **range** limits three things: the literature searches (by publication
date), the full-text downloads (by publication date) and the GitHub search (by
creation date). It never removes anything from `progress.csv`.

**Types** decide which files are kept. A PDF fetched only to derive Markdown or
text is deleted afterwards. Files already on disk are never removed.

**Sources** switch providers on or off. Repository sources left out keep their
rows from the last build.

Independent steps run at the same time:

| stage | steps |
|---|---|
| 1 | local seeds · PubMed · Europe PMC · arXiv · GitHub search |
| 2–3 | seed resolution, then the catalogue build |
| 4 | OpenAlex citations · preprint → journal links · full texts (12 download workers) |
| 5–6 | catalogue refresh, then mining papers for links |
| 7 | repositories and packages · websites |
| 8–9 | final catalogue build, findings summary |

Each run writes `logs/cli_<stamp>/` in the output folder:

- `plan.txt`
- `NN_<step>.log`, one per step
- `summary.txt`, with the return code and duration of each step
- `litsearch.yaml`, the configuration the run used

A run lock (`.pipeline.lock`) keeps two updates from running at once. Both
implementations and `update_all.sh` use the same lock.

### query and export

Filters combine with AND; a comma list within one filter combines with OR.

| filter | matches |
|---|---|
| `--text RE` / `--title RE` | regex (case-insensitive) over title, clocks, venue, matched groups, details / the title |
| `--doi LIST`, `--uid LIST` | comma list or `@file` (one per line); doi.org prefixes ignored |
| `--from`, `--to`, `--range` | publication date, as for `update` |
| `--tier`, `--type`, `--category` | exact values |
| `--modality`, `--clock`, `--species` | substrings |
| `--added-since DATE`, `--has-fulltext`, `--fulltext RE` | rows added since a date; rows with Markdown on disk; regex inside the downloaded full text (prints the passage) |
| `--sort` | `date`, `cited_by` (descending), `title`, `author` |

`query` prints the first 50 rows (`--limit N`). `--out file.csv` writes every
matching row.

`export --format` takes three-letter codes in a comma list:

| code | format | code | format |
|---|---|---|---|
| `APA` | APA 7th | `IEE` | IEEE |
| `AMA` | AMA 11th | `NAT` | Nature |
| `MLA` | MLA 9th | `BIB` | BibTeX |
| `CHI` | Chicago author-date | `RIS` | RIS (Zotero, Mendeley, EndNote) |
| `HAR` | Harvard (Cite Them Right) | `ENW` | EndNote tagged |
| `VAN` | Vancouver / ICMJE | `CSL` | CSL-JSON (Zotero, pandoc) |

Aliases such as `ieee`, `bibtex`, `endnote` and `csl-json` also work.
`--out name.ext` writes a single format to that file. `--out prefix` writes one
file per format (`prefix.apa.txt`, `prefix.bib`, …). Relative paths go to
`<output folder>/exports/`. References use the full author lists, volume,
issue and pages from `corpus/references.csv`.

```bash
./litSearch query --fulltext "DunedinPACE" --type article --limit 10
./litSearch query --category "clock development" --sort cited_by --out top.csv
./litSearch export --doi 10.18632/aging.101414,10.7554/elife.73420 --format APA,BIB
./litSearch export --doi @dois.txt --format CSL --out refs.json
```


## Two implementations

| | Python (`python/`) | Go (`go/`) |
|---|---|---|
| launcher | `python/litSearch` → `python/run.sh` | `go/litSearch` → `go/run.sh` (also `./litSearch`) |
| image | `litsearch-python:1.0` (`python/Dockerfile`, ~545 MB) | `litsearch-go:1.0` (`go/Dockerfile`, multi-stage, ~60 MB) |
| steps | one script per step, in folders by use case | one static binary; each step runs as a child process with its own log |
| PDF → Markdown / text | PyMuPDF, pymupdf4llm | poppler (`pdftohtml`, `pdftotext`) |

Commands, flags, help texts, plans, CSV files and the rows each step writes
are the same in both. Python's `csv` quoting, dict ordering, regular-expression
semantics and Unicode whitespace rules are reproduced in Go. The Go image
rebuilds itself whenever a Go source file changes.

`parity/check.sh` runs the commands in `parity/cases.txt` through both
implementations and compares stdout, stderr, the exit code and every file
written. It belongs to neither implementation. Step-by-step comparisons on
copies of the real output folder (2026-10-04) gave identical output from both
for:

- local seeds, the catalogue build, link mining, the findings summary, the
  repositories and the websites;
- PubMed, Europe PMC and arXiv over a one-week window, including records that
  had to be fetched again.

The Go catalogue build takes 21 s; the Python one takes 5.4 min.

Known differences:

- **PDF conversion.** Markdown and text derived from a PDF differ in detail,
  because the two conversion engines differ. XML-derived Markdown is identical.
- **Publisher bot checks.** Some publisher CDNs (e.g. nature.com) answer Go's
  TLS client with a JavaScript challenge instead of the PDF. litSearch does not
  try to get past such checks. Those PDFs come through the Python
  implementation, or later through PMC.
- **JSON key order.** The order of keys inside `records.jsonl.gz` and nested
  `metadata.json` objects can differ (Go sorts them). The content is the same,
  and either implementation reads the other's files.


## Pipeline steps

| step | what it does | sources |
|---|---|---|
| `seeds` | references and links from the local reference projects | files under `/refs/*` |
| `pubmed`, `europepmc`, `arxiv` | search every group of the `search` section | NCBI E-utilities (`[tiab]`); Europe PMC REST (non-MEDLINE by default); arXiv API (batched, incremental) |
| `resolve-seeds` | metadata for seed DOIs | PubMed → Europe PMC → Crossref → DataCite |
| `catalogue` | build `progress.csv`, append-only, classified by the `classify` rules | record store, search hits, seeds, full-text index, repositories, websites |
| `openalex`, `preprint-links` | citation counts and exact dates; preprint ↔ journal links | OpenAlex; bioRxiv / medRxiv API |
| `fulltext` | open-access full texts with `metadata.json` | PMC Open Access on AWS → bioRxiv / medRxiv → Europe PMC XML → arXiv → Unpaywall |
| `links` | code, data and website links in abstracts and full texts | catalogue + full texts |
| `github-search`, `repositories` | repositories and packages | GitHub API; CRAN (crandb), Bioconductor, PyPI, Zenodo |
| `websites` | curated sites and domains cited by several papers, dated by the Internet Archive | site home pages; Wayback CDX |
| `summary` | `summary/findings.md` | `progress.csv` |

Only open-access copies are downloaded. JATS XML is converted to Markdown:

- sections, lists, figure and table captions;
- tables as pipe tables;
- references with DOIs.

`metadata.json` records the source URL, SHA-256 and licence of every file. A
bioRxiv/medRxiv rate limit marks the record `deferred` instead of waiting for
it. `partial`, `deferred` and `error` records are retried on every run;
records with no open copy are retried after 30 days.


## Output folder

```
<research_dir>/
  progress.csv          THE CATALOGUE, oldest first (columns below)
  config/litsearch.yaml the live configuration
  changelog/            added_<date>.csv per run, runs.csv (one line per build)
  corpus/               records.jsonl.gz (every harvested record, with abstracts),
                        references.csv (full bibliographic data for export),
                        search_hits.csv, citations.csv, preprint_links.csv,
                        broad_hits.csv, pre2011_background.csv, offtopic_seeds.csv
  fulltext/<year>/<id>/ metadata.json, paper.pdf, paper.md, paper.txt, paper.jats.xml
  fulltext/fulltext_index.csv
  seeds/  repos/  websites/   intermediate tables of those steps
  summary/              findings.md, counts by year × type / modality, stats.json
  raw/                  per-run hit counts and exact queries; arXiv state
  exports/              query / export output with relative --out paths
  logs/cli_<stamp>/     plan.txt, NN_<step>.log, summary.txt, litsearch.yaml
```

Paths inside `progress.csv` are relative to the output folder.

### progress.csv

| column | meaning |
|---|---|
| `added_on` | date the row first entered the catalogue (never changes) |
| `date`, `year` | publication date; for repositories the creation date; for packages the first release; for websites the earliest Internet Archive snapshot |
| `resource_type` | article, review, preprint, report, thesis, repository, software package, dataset, archive, database, web calculator, website |
| `tier` | `landmark` (`landmark_roles` from the local projects), `core` (a `core_title_terms` match in the title, or a development, benchmark, review or software paper), `related` (abstract match only) |
| `category`, `modality`, `clocks`, `species` | from the `classify` rules |
| `title`, `authors`, `venue`, `doi`, `pmid`, `pmcid`, `url` | bibliographic data (first three authors; full lists in `corpus/references.csv`) |
| `code_links`, `related` | code / data links in the paper; preprint ↔ journal version, or papers linking to a repository / website |
| `open_access`, `license`, `fulltext_*`, `metadata_file` | OA status, licence and paths of downloaded files |
| `cited_by` | highest citation count from OpenAlex, Europe PMC or Crossref |
| `details`, `matched_groups`, `found_by`, `local_refs` | stars / language / archive dates; matching search groups; how it was found; which local project cites it |
| `status` | blank, `undated`, or `no longer matched by pipeline (kept)` |
| `uid` | stable key: `doi:…`, `pmid:…`, `pmcid:…`, `epmc:…` or `url:…` |
| `notes`, `user_tags` | **yours**: never touched by the tool |

### Append-only contract

- A resource whose `uid` is already catalogued keeps its `added_on`, `notes`
  and `user_tags`. Derived columns are refreshed.
- A row the pipeline no longer produces is kept and flagged in `status`.
- Every build writes `changelog/added_<date>.csv` (merged per day) and adds a
  line to `changelog/runs.csv`.
- `litSearch test` checks this contract on a throwaway catalogue. It must print
  `PASS`.
- Steps can run side by side:
  - the record store is saved under a file lock and folds in records that
    other writers added meanwhile;
  - the search-hit log is merged under a lock.


## Workspace

```
config/
  litsearch.template.yaml   default configuration (seeds <research_dir>/config/litsearch.yaml)
  local.template.yaml       placeholder for config/local.yaml
  local.yaml                git-ignored: paths on this machine, credentials
python/                     Python implementation
  Dockerfile  run.sh  litSearch
  cli/          litsearch.py: commands, plan, parallel stages, status
  common/       agelit.py (paths, config, HTTP, identifiers, dates, CSV), store.py, hits.py,
                flags.py, pubmed.py, crossref.py
  localseeds/   extract_seeds.py
  pullliturature/  search_pubmed.py, search_europepmc.py, search_arxiv.py, resolve_seeds.py,
                enrich_openalex.py, enrich_preprints.py, fetch_fulltext.py, jats.py
  pullrepos/    search_github.py, mine_links.py, build_repos.py
  websites/     build_websites.py
  buildcatalog/ build_progress.py, summarise.py, test_incremental.py
  query/        catalog.py (filters), search_catalog.py, export_refs.py, refs.py (12 formats)
  pipeline/     update_all.sh: the original sequential pipeline
go/                         Go implementation (package main, one binary)
  Dockerfile  run.sh  litSearch  go.mod  go.sum
  main.go flags.go          commands, plan, parallel stages, status
  util.go http.go store.go config.go classify.go    shared helpers
  seeds.go pubmed.go europepmc.go arxiv.go resolve.go catalogue.go enrich.go
  fulltext.go jats.go links.go github.go repos.go websites.go summary.go   steps
  query.go refs.go test.go  query, export, append-only test
parity/                     cases.txt + check.sh: run both implementations and compare
litSearch -> go/litSearch
todo.md                     feature list and open work (both implementations)
```


## Known limitations

- **Coverage.** PubMed, Europe PMC, arXiv, Crossref / DataCite (seeds) and
  OpenAlex (citations) are used. Google Scholar, Scopus and Web of Science
  have no open API and are not.
- **Classification** is rule-based: regular expressions over title, abstract,
  keywords and MeSH. Treat the derived counts as trends.
- **Full texts** are open-access copies only. bioRxiv / medRxiv rate-limit
  hard (records are deferred). Some publishers block automated downloads of
  their own OA PDFs.
- **Website dates** are the earliest Internet Archive snapshot of the address,
  which can predate the site's current purpose.
- **Use-case code still in the tool.** A few parts are written for the aging
  clock use case and are not driven by the configuration yet (listed in
  `todo.md`):
  - the seed adapters for the FALCONAge clock registry and the 300BCG
    literature manifests (the generic DOI/URL scan of any reference project is
    general);
  - the `clock development` category given to registry origin papers;
  - the summary's data-layer list and its "clock" headings.


## Use case: aging clocks

This workspace's configuration (`config/litsearch.template.yaml`, live copy in
the output folder) catalogues research on **epigenetic clocks, biological-age
clocks, aging scores, frailty indices and methylation risk scores** from
2011-01-01 to the run date.

### Search groups

Focused groups (results go into `progress.csv`):

- **epigenetic_clocks**: epigenetic clock / age / aging, DNA methylation age or clock, DNAm age, epigenetic pacemaker, drift
- **named_clocks**: Horvath, Hannum, PhenoAge, GrimAge(2), DunedinPACE / PoAm, skin & blood, PC clocks, AltumAge, CausAge, SystemsAge, OMICmAge, IntrinClock, PedBE, epiTOC, DNAmTL, …
- **omics_clocks**: proteomic, transcriptomic, metabolomic, lipidomic, glycomic, immune, microbiome, brain / retinal / ECG / facial imaging, organ, single-cell, chromatin and mitotic clocks
- **biological_age**, **aging_scores**, **biological_age_gated**: biological / phenotypic age, age acceleration, Klemera-Doubal, homeostatic dysregulation, pace of aging. Ambiguous terms (rejuvenation, age gap, allostatic load, intrinsic capacity) count only next to an aging-measurement term.
- **frailty_index**, **frailty_focused**: frailty index, deficit accumulation, FI-lab; frailty together with omics / biomarker terms
- **methylation_risk_scores**: methylation risk / profile scores, EpiScores, DNAm surrogates
- **biomarkers_of_aging**, **age_estimation_forensic**

Broad groups (metadata only, `corpus/broad_hits.csv`) cover bare aging with
biomarker / predictor / machine-learning terms, and bare frailty. Each of these
alone returns tens of thousands of clinical papers.

The reference projects (paths are in `config/local.yaml`) are:

- **FALCONAge**: clock registry with origin papers, bibliography, evidence table
- **300BCG**: literature manifests
- **300OB**: prior-art notes

### Status (2026-10-04)

| | count |
|---|---|
| rows in `progress.csv` | 27,926, oldest first |
| papers | 19,901 articles · 3,724 reviews · 3,415 preprints · 89 reports and datasets |
| tiers | 157 landmark · 13,716 core · 14,053 related |
| repositories and packages | 654 repositories · 27 packages · 46 Zenodo archives · 43 datasets |
| websites, databases, calculators | 69 |
| full texts | 15,999 complete (md + txt), 377 partial, 481 deferred, 10,228 without an open copy |
| broad corpus | 57,155 records (metadata only) |
| record store | 84,431 records with abstracts |

### Findings

The exact, regenerated numbers are in `summary/findings.md`. Counts come from
a rule-based classifier, so treat them as trends.

1. **The field grew about 18-fold.** It went from 255 catalogued papers in
   2011 to 4,495 in 2025, and 2026 had 4,444 by 3 October. Preprints have
   made up 13–17% of each year since 2020.
2. **Epigenetic clocks came in waves.** These dates are from the origin
   papers of the 178 clocks in the FALCONAge registry:
   - 2011–2014: chronological-age predictors (Bocklandt, Garagnani, Hannum,
     Horvath, Weidner);
   - 2016–2019: specialised clocks (gestational, placental, mitotic, mouse,
     skin & blood, paediatric);
   - 2017–2022: phenotype- and mortality-trained clocks (PhenoAge, GrimAge,
     DNAmTL, DunedinPoAm → DunedinPACE, GrimAge2);
   - 2018–2021: methylation surrogates;
   - 2022–2024: robustness and new model families (PC clocks, AltumAge,
     pan-mammalian clocks, CausAge, CpGPT);
   - 2025–2026: system-level clocks (SystemsAge, OMICmAge, EnsembleAge).
3. **Layers beyond DNA methylation grew fastest after 2023.**
   - Clinical-biomarker clocks: about 300 in 2025.
   - Proteomic clocks: 114 by October 2026.
   - Multi-omic clocks: 200 in 2026 so far.
   - Brain-age imaging is a large parallel line.
4. **Frailty indices are the largest single block**, mostly as clinical outcome
   measures. The methylation-based frailty score arrived in 2022.
5. **Tools.** 2,551 papers link to code or data. The most-starred tools
   specific to aging include BioAge, pyaging, brainageR, biolearn, CpGPT,
   dnaMethyAge, PC-Clocks, DunedinPACE and methylclock.
6. **Web resources.** 69 sites:
   - 25 curated;
   - the rest are mostly aging cohorts cited by three or more papers;
   - clock-specific sites range from the 2013 DNAm age calculator to
     ClockBase, Biolearn and TranslAGE (2023–2024).
7. **Openness.** 59% of catalogued papers have an open full text on disk
   (36 GB). 1,745 carry a preprint ↔ journal link.

### Log

- **2026-09-29**: First harvest.
  - Created the workspace, the Docker image and the pipeline.
  - Extracted seeds: 338 DOIs, 192 code/data URLs and 178 registry clocks.
  - Added aging, frailty and methylation-risk-score search groups and the
    append-only behaviour.
  - Europe PMC outages led to adaptive page sizes, back-off and skipping a
    group instead of failing.
  - bioRxiv / medRxiv rate limits led to deferral.
- **2026-10-04**: Re-run end to end; it appended the work published since
  29 September.
  - Tightened repository inclusion: a paper's own code needs study-specific
    wording, a landmark/core paper and fewer than 500 stars.
  - Batched the arXiv search and made it incremental.
  - Added the CLI, later renamed `litSearch`.
  - Moved outputs to the Zotero-synced output folder.
  - Added query and export (12 reference formats).
  - Ported every step to Go, with separate Dockerfiles and parity checks
    against Python.
  - Moved local paths and credentials into the git-ignored `config/local.yaml`.
  - Merged the configuration into one file, kept in the output folder.
