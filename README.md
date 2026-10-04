# litsearch <img src="logo/logo.png" alt="litsearch logo" width="160" align="right"/>

litsearch collects the latest research on a topic and keeps it in one
chronological catalogue. Give it a topic and it gathers five kinds of material:

- papers and preprints from PubMed, Europe PMC, arXiv and OpenAlex;
- the open-access full texts, as PDF, Markdown and plain text;
- the code repositories and software packages those papers link to;
- the websites and databases of the field;
- an exact reference for every paper, ready to export to Zotero or LaTeX.

Run it again later and it adds only what is new. Nothing you have edited in
the catalogue is ever overwritten.

```bash
pip install litsearch
litsearch "CRISPR base editing"
```

## Contents

- [Install](#install)
- [Quick start](#quick-start)
- [How a topic becomes a search](#how-a-topic-becomes-a-search)
- [Commands](#commands)
- [Where the results go](#where-the-results-go)
- [Settings](#settings)
- [Configuration of a catalogue](#configuration-of-a-catalogue)
- [Sources](#sources)
- [Python and Go](#python-and-go)
- [Development](#development)
- [Limitations](#limitations)
- [Default topic: aging clocks](#default-topic-aging-clocks)

## Install

litsearch runs on Linux and macOS. There are two implementations of the same
command. Install one of them.

**Python** (3.10 or newer):

```bash
pip install litsearch            # or: pipx install litsearch
pip install "litsearch[pdf]"     # adds PyMuPDF for better PDF to Markdown conversion
```

**Go** (a single static binary, about 8 MB):

```bash
go install github.com/bhagesh-h/litSearch/cmd/litsearch@latest
```

Prebuilt binaries for Linux and macOS are attached to each
[release](https://github.com/bhagesh-h/litSearch/releases).

**Docker:**

```bash
docker build -t litsearch https://github.com/bhagesh-h/litSearch.git
docker run --rm -v "$HOME/litsearch:/data" litsearch "CRISPR base editing"
```

Optional:

- `poppler-utils` (`apt install poppler-utils`, `brew install poppler`)
  converts PDFs to text and Markdown. The Go binary needs it for that; the
  Python package uses it when PyMuPDF is not installed.
- `gh`, the GitHub CLI: when it is logged in, litsearch uses its token for
  faster GitHub searches.

## Quick start

1. Tell litsearch your email address. Crossref, OpenAlex and NCBI ask for one
   from automated clients, and Unpaywall needs it to find open-access PDFs.

   ```bash
   litsearch config set contact_email you@example.org
   ```

2. Choose where catalogues are stored. The default is `~/litsearch`.

   ```bash
   litsearch config set output ~/Documents/research
   ```

3. Start a catalogue:

   ```bash
   litsearch "CRISPR base editing"
   ```

   This searches all sources for work published in the last 12 months. It
   then downloads the open-access full texts, finds linked code and websites,
   and writes the catalogue to `~/Documents/research/crispr-base-editing/`. At
   the end it prints the newest additions and the path of `progress.csv`.

   A first run takes a few minutes for a narrow topic. Large topics, or runs
   that download thousands of PDFs, take longer.

4. Later, fetch what has been published since:

   ```bash
   litsearch "CRISPR base editing"
   ```

5. Look at the results, or export them:

   ```bash
   litsearch query --topic "CRISPR base editing" --range 1m
   litsearch export --topic "CRISPR base editing" --format BIB --out crispr.bib
   ```

Faster, smaller first runs:

```bash
litsearch "CRISPR base editing" --types papers           # metadata only, no downloads
litsearch "CRISPR base editing" --range 1m               # only the last month
litsearch "CRISPR base editing" --dry-run                # show what would run
```

## How a topic becomes a search

| you type | litsearch searches for |
|---|---|
| `litsearch "CRISPR base editing"` | titles or abstracts containing CRISPR, base and editing |
| `litsearch '"base editing"'` | the exact phrase "base editing" |
| `litsearch '"base editing", "prime editing"'` | either phrase |
| `litsearch "graph neural networks OR GNN"` | all three words, or GNN |

Rules:

- Commas, semicolons and `OR` separate alternatives.
- Within an alternative, every word is required.
- Double quotes keep a phrase together.

The terms are written to the catalogue's configuration file. Edit that file
to refine the search, for example to add synonyms or a second required term
(see [Configuration](#configuration-of-a-catalogue)).

The time window:

- A new topic starts 12 months back. `--range 5y` or `--from 2015` start it
  earlier.
- Asking later for older work extends the window.
- Each later update searches the whole window up to today, and adds only what
  the catalogue does not already contain.

## Commands

| command | what it does |
|---|---|
| `litsearch "TOPIC"` | same as `litsearch update --topic "TOPIC"` |
| `update` | search, download and rebuild a catalogue (the default topic when no `--topic` is given) |
| `retry` | download again the full texts that were rate-limited, incomplete or failed |
| `query` | filter a catalogue; print the rows or save them as CSV |
| `export` | write catalogue entries as references |
| `status` | size, full-text coverage, recent runs and failures of a catalogue |
| `list` | the catalogues in the output folder |
| `config` | show or change the settings of this machine |
| `sources` | the values that `--sources`, `--types`, `--paper-types` and `--format` accept |
| `test` | check, on a throwaway catalogue, that updates only ever add |
| `version` | print the version |

`litsearch help COMMAND` or `litsearch COMMAND --help` lists every option of a
command.

### Choosing a catalogue

Every command that works on a catalogue takes:

| option | meaning |
|---|---|
| `--topic TEXT` | the catalogue of that topic (default: the default topic) |
| `--dir DIR` | the catalogue in DIR, anywhere on disk |

### update

| option | values | default |
|---|---|---|
| `--range` | span back from `--to`: `1d 7d 2w 1m 6m 1y 5y` | the catalogue's window |
| `--from`, `--to` | `2024`, `2024-03`, `2024-03-15`, `today`, or a span (`--from 2y`) | window start, today |
| `--types` | `papers` (metadata), `pdf`, `md`, `txt`, `xml`, `repo`, `website`, `all` | all |
| `--sources` | `pubmed europepmc arxiv openalex local pmc biorxiv unpaywall github cran bioconductor pypi zenodo websites`, or groups `literature`, `fulltext`, `repos`, `packages` | all |
| `--paper-types` | `article review preprint report thesis "conference paper" "book chapter"` (full texts only) | all |
| `--tiers` | full texts for `landmark,core,related` rows | all three |
| `--scope` | search groups `focused`, `broad`, `all` | all |
| `--workers` | parallel full-text downloads | 12 |
| `--parallel` | steps run at the same time | 5 |
| `--min-stars` | GitHub search hits need this many stars | 3 |
| `--heartbeat` | seconds between progress lines (0: none) | 120 |
| `--stop-on-error` | stop after the first failed stage | |
| `--dry-run` | print the plan and a full-text estimate | |

The steps of an update run in stages:

| stage | steps (run in parallel) |
|---|---|
| 1 | searches (PubMed, Europe PMC, arXiv, OpenAlex), local reference folders, GitHub search |
| 2 | metadata for DOIs found in reference folders |
| 3 | catalogue build |
| 4 | citation counts and exact dates, preprint to journal links, full texts |
| 5 | catalogue refresh |
| 6 | links to code, data and websites found in the papers |
| 7 | repositories and packages, websites |
| 8 | final catalogue build |
| 9 | summary of findings |

### query

Filters combine with AND. A comma list within one filter combines with OR.

```bash
litsearch query --range 1m                                     # published in the last month
litsearch query --type review --sort cited_by --limit 20       # most-cited reviews
litsearch query --title "base editor" --from 2024
litsearch query --fulltext "off-target" --type article         # search inside the downloaded full texts
litsearch query --added-since 2026-10-01 --out new.csv         # what the last runs added
```

| filter | matches |
|---|---|
| `--text RE`, `--title RE` | regular expression (any case) over title, entities, venue, matched groups and details, or the title only |
| `--doi LIST`, `--uid LIST` | a comma list, or `@file` with one per line |
| `--from`, `--to`, `--range` | publication date |
| `--tier`, `--type`, `--category` | exact values |
| `--modality`, `--entity`, `--species` | parts of the column value |
| `--added-since DATE` | rows that entered the catalogue on or after DATE |
| `--has-fulltext` | rows with a downloaded Markdown full text |
| `--fulltext RE` | regular expression searched in the downloaded full texts; prints the matching passage |
| `--sort` | `date`, `cited_by` (highest first), `title`, `author` |

`query` prints 50 rows (`--limit N` changes that). `--out FILE` saves every
matching row as CSV.

### export

Takes the same filters as `query`, plus `--format` with one or more
three-letter codes:

| code | format | code | format |
|---|---|---|---|
| `APA` | APA 7th | `IEE` | IEEE |
| `AMA` | AMA 11th | `NAT` | Nature |
| `MLA` | MLA 9th | `BIB` | BibTeX |
| `CHI` | Chicago author-date | `RIS` | RIS (Zotero, Mendeley, EndNote) |
| `HAR` | Harvard | `ENW` | EndNote tagged |
| `VAN` | Vancouver | `CSL` | CSL-JSON (Zotero, pandoc) |

```bash
litsearch export --topic "CRISPR base editing" --type review --format APA
litsearch export --doi 10.1038/s41586-019-1711-4 --format BIB,RIS --out anzalone
litsearch export --from 2025 --tier core --format CSL --out core2025.json
```

`--out name.ext` writes one format to that file. `--out name` writes one file
per format (`name.apa.txt`, `name.bib`, ...). Relative paths go to the
catalogue's `exports/` folder. Without `--out`, the references are printed.

## Where the results go

Every catalogue is a folder:

```
<catalogue>/
  progress.csv               the catalogue, oldest first
  config/litsearch.yaml      its configuration (edit freely)
  fulltext/<year>/<id>/      paper.pdf, paper.md, paper.txt, paper.jats.xml, metadata.json
  fulltext/fulltext_index.csv
  corpus/                    every harvested record with abstract (records.jsonl.gz),
                             references.csv (full author lists, for export), search hits,
                             citation counts, preprint links, broad-search hits
  repos/  websites/  seeds/  intermediate tables of those steps
  changelog/                 added_<date>.csv for every day with new rows, runs.csv
  summary/findings.md        growth per year, most-cited work, tools, websites, coverage
  exports/                   files written by query and export
  logs/cli_<stamp>/          plan, one log per step, summary, the configuration used
```

The output folder holds the default topic's catalogue directly, and one
subfolder per other topic.

### progress.csv

One row per resource, sorted by date.

| column | meaning |
|---|---|
| `added_on` | the day the row entered the catalogue; never changes |
| `date`, `year` | publication date; for repositories the creation date; for packages the first release; for websites the first Internet Archive snapshot |
| `resource_type` | article, review, preprint, report, thesis, conference paper, book chapter, dataset, repository, software package, archive, database, web calculator, website |
| `tier` | `landmark` (key papers named in your reference folders), `core` (topic in the title, or a method, benchmark, review or software paper), `related` (topic in the abstract only) |
| `category`, `modality`, `entities`, `species` | from the classification rules of the configuration |
| `title`, `authors`, `venue`, `doi`, `pmid`, `pmcid`, `url` | bibliographic data (first three authors; all of them in `corpus/references.csv`) |
| `code_links`, `related` | code and data links found in the paper; preprint and journal versions of the same work |
| `open_access`, `license`, `fulltext_status`, `fulltext_pdf`, `fulltext_md`, `fulltext_txt`, `metadata_file` | what was downloaded, and where (paths relative to the catalogue) |
| `cited_by` | highest citation count from OpenAlex, Europe PMC or Crossref |
| `details`, `matched_groups`, `found_by`, `local_refs` | repository stars and languages; which searches found the row; which reference folder mentions it |
| `status` | empty, `undated`, or `no longer matched by pipeline (kept)` |
| `uid` | stable key: `doi:...`, `pmid:...`, `pmcid:...`, `epmc:...` or `url:...` |
| `notes`, `user_tags` | yours; litsearch never changes them |

### Updates only add

- A row whose `uid` is already in the catalogue keeps its `added_on`, `notes`
  and `user_tags`. Derived columns are refreshed.
- A row the searches no longer find is kept and marked in `status`.
- Every update writes the new rows of the day to `changelog/added_<date>.csv`.
- Full texts already on disk are skipped. Rate-limited, incomplete and failed
  downloads are retried on the next run; papers without an open copy are
  retried after 30 days.
- Only one update runs on a catalogue at a time (`.pipeline.lock`).
- `litsearch test` checks these rules on a throwaway catalogue.

## Settings

Settings belong to the machine, not to a catalogue:

```bash
litsearch config                                   # show them (keys are masked)
litsearch config set output ~/research             # where catalogues are stored
litsearch config set contact_email you@example.org
litsearch config set ncbi_api_key KEY              # optional: faster PubMed
litsearch config set github_token TOKEN            # optional: or log in with the gh CLI
litsearch config unset github_token
```

| setting | environment variable | used for |
|---|---|---|
| `output` | `LITSEARCH_OUTPUT` | folder that holds the catalogues (default `~/litsearch`) |
| `contact_email` | `LITSEARCH_EMAIL` | polite access to Crossref, OpenAlex and NCBI; required for Unpaywall |
| `github_token` | `GITHUB_TOKEN` | GitHub search rate limit |
| `openalex_api_key` | `OPENALEX_API_KEY` | OpenAlex premium access |
| `ncbi_api_key` | `NCBI_API_KEY` | PubMed rate limit |

The settings file is `~/.config/litsearch/settings.yaml` on Linux and
`~/Library/Application Support/litsearch/settings.yaml` on macOS. It is
readable by you only. Environment variables override it.

## Configuration of a catalogue

Each catalogue has its own `config/litsearch.yaml`, created on its first
update. It has four sections:

| section | sets |
|---|---|
| `search` | time window, sources, search groups and their terms |
| `classify` | rules that fill the `category`, `modality`, `entities`, `species` and `tier` columns |
| `repos` | GitHub searches, a relevance pattern for repositories, package searches |
| `websites` | curated websites and a relevance pattern for sites cited by papers |

Top-level keys name the topic and list local reference folders. litsearch
mines those folders for DOIs and links; the papers they cite become seeds of
the catalogue.

A configuration created from a topic searches all four literature sources.
It has general classification rules: review, commentary, correction, method
development, benchmark, software, trial.

Add your own:

- `entities`: names of methods, models or measures to tag;
- `modality`: data types to track;
- `websites.sites`: websites to include.

Every key is described in [docs/configuration.md](docs/configuration.md).

## Sources

| source | used for |
|---|---|
| PubMed (NCBI E-utilities) | search, biomedical literature |
| Europe PMC | search (preprints and records outside MEDLINE), full-text XML |
| arXiv | search, PDFs |
| OpenAlex | search across all fields, citation counts, exact dates, open-access PDF links |
| Crossref, DataCite | metadata for DOIs found in reference folders |
| PMC open-access bucket (AWS) | full texts: JATS XML, text, PDF, licence |
| bioRxiv / medRxiv | full texts of preprints, links from preprints to journal versions |
| Unpaywall | open-access PDFs (needs a contact email) |
| GitHub, CRAN, Bioconductor, PyPI, Zenodo | repositories and packages |
| Internet Archive | first-seen dates of websites |

OpenAlex matches word stems, so litsearch keeps an OpenAlex result only when
the topic words appear as whole words in its title or abstract. It also skips
further versions of a work already in the catalogue: the same title, venue
and year under another DOI, as with Zenodo and figshare versions.

Only open-access copies are downloaded. Requests are spaced per host and
retried with back-off. bioRxiv and medRxiv rate limits mark downloads as
deferred instead of waiting; `litsearch retry` picks them up later.

## Python and Go

The two implementations take the same commands and options, write the same
files and print the same text. Use whichever is easier to install. Both
always run their own steps; they share no code.

| | Python | Go |
|---|---|---|
| install | `pip install litsearch` | `go install` or a release binary |
| source | `src/litsearch/` | `cmd/litsearch/` |
| PDF conversion | PyMuPDF if installed, else poppler | poppler |

Known differences:

- Markdown made from PDFs differs in detail between PyMuPDF and poppler.
- Some publisher sites (for example nature.com) answer Go's HTTP client with a
  bot check instead of the PDF. litsearch does not try to get past such
  checks.
- The keys inside `corpus/records.jsonl.gz` may be in a different order. Both
  read either file.

Both implementations can work on the same catalogue, one after the other.

## Development

```bash
git clone https://github.com/bhagesh-h/litSearch.git && cd litSearch
make python        # pip install -e ".[dev]"
make go            # bin/litsearch
make test          # go vet, go test, pytest, and litsearch test for both
make parity        # run parity/cases.txt through both and compare every output
```

`parity/check.sh` runs about 70 commands through both implementations:

- help texts, plans and errors;
- queries and exports of a real catalogue;
- a new topic created and built offline.

It then compares stdout, stderr, exit codes and every file written. A change
to one implementation is finished when the parity check passes.

Releasing: push a tag such as `v1.0.1` and the release workflow does two
things:

- it attaches Go binaries for Linux and macOS to a GitHub release;
- it publishes the Python package to PyPI.

PyPI publishing uses trusted publishing. Register the repository on pypi.org
once before the first release. The version lives in
`src/litsearch/__init__.py`; the Go build reads it from there.

## Limitations

- **Coverage.** PubMed, Europe PMC, arXiv and OpenAlex are searched. Google
  Scholar, Scopus and Web of Science have no open API and are not.
- **Matching.** Searches match title and abstract. A topic word that appears
  only in the full text is not found.
- **Classification.** Categories, tiers and tags come from regular
  expressions. They are useful for sorting and filtering, but counts derived
  from them are approximate.
- **Full texts.** Only open-access copies. Paywalled papers are listed
  without a full text.
- **Websites.** A website's date is its first Internet Archive snapshot,
  which can be older than its current purpose.
- **Platforms.** Linux and macOS (file locking uses `flock`).

## Default topic: aging clocks

litsearch was built to follow research on measuring biological aging. That
catalogue is the default topic: `litsearch update` without `--topic` builds
it.

The default topic covers:

- epigenetic and DNA methylation clocks, and named clocks such as Horvath,
  Hannum, PhenoAge, GrimAge and DunedinPACE;
- clocks built from omics, imaging and clinical data;
- biological age and aging scores, frailty indices and methylation risk
  scores;
- biomarkers of aging, and forensic age estimation.

It starts in 2011. Broad searches for aging and frailty are kept as metadata
only. The reference folders of the author's projects provide landmark
papers: a clock registry with the origin paper of each clock, and curated
reading lists.

State of that catalogue on 2026-10-04:

| | count |
|---|---|
| rows in `progress.csv` | 27,926 |
| papers | 19,901 articles, 3,724 reviews, 3,415 preprints, 46 reports |
| tiers | 157 landmark, 13,716 core, 14,053 related |
| code and data | 654 repositories, 27 packages, 46 archives, 43 datasets |
| websites, databases, calculators | 69 |
| full texts | 15,999 complete, 377 partial, 481 deferred |

Findings, from its `summary/findings.md`:

1. **Growth.** Catalogued papers rose from 255 in 2011 to 4,495 in 2025.
   Preprints have made up 13 to 17% of each year since 2020.
2. **Epigenetic clocks came in waves.** These dates are from the origin papers
   of the 178 clocks in the registry:
   - 2011 to 2014: chronological-age predictors (Hannum, Horvath);
   - 2016 to 2019: tissue- and species-specific clocks;
   - 2017 to 2022: clocks trained on mortality and morbidity (PhenoAge,
     GrimAge, DunedinPACE);
   - 2022 onward: reliability-focused and deep-learning clocks;
   - 2025 onward: system-level clocks.
3. **Other data layers.** Clocks built from proteomic, multi-omic and
   clinical-biomarker data grew fastest after 2023.
4. **Frailty indices** form the largest single block of the catalogue, mostly
   as clinical outcome measures.
5. **Openness.** 59% of catalogued papers have an open full text.

## License

GPL-3.0. See [LICENSE](LICENSE).
