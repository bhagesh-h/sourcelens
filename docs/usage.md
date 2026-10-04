# Usage

## Quick start

1. Tell sourcelens your email address. Crossref, OpenAlex and NCBI ask for one
   from automated clients, and Unpaywall needs it to find open-access PDFs.

   ```bash
   sourcelens config set contact_email you@example.org
   ```

2. Choose where catalogues are stored. The default is `~/sourcelens`.

   ```bash
   sourcelens config set output ~/Documents/research
   ```

3. Start a catalogue:

   ```bash
   sourcelens "CRISPR base editing"
   ```

   This searches all sources for work published in the last 12 months. It
   then downloads the open-access full texts, finds linked code and websites,
   and writes the catalogue to `~/Documents/research/crispr-base-editing/`. At
   the end it prints the newest additions and the path of `progress.csv`.

   A first run takes a few minutes for a narrow topic. Large topics, or runs
   that download thousands of PDFs, take longer.

4. Later, fetch what has been published since:

   ```bash
   sourcelens "CRISPR base editing"
   ```

5. Look at the results, or export them:

   ```bash
   sourcelens query --topic "CRISPR base editing" --range 1m
   sourcelens export --topic "CRISPR base editing" --format BIB --out crispr.bib
   ```

Faster, smaller first runs:

```bash
sourcelens "CRISPR base editing" --types papers           # metadata only, no downloads
sourcelens "CRISPR base editing" --range 1m               # only the last month
sourcelens "CRISPR base editing" --dry-run                # show what would run
```

## How a topic becomes a search

| you type | sourcelens searches for |
|---|---|
| `sourcelens "CRISPR base editing"` | titles or abstracts containing CRISPR, base and editing |
| `sourcelens '"base editing"'` | the exact phrase "base editing" |
| `sourcelens '"base editing", "prime editing"'` | either phrase |
| `sourcelens "graph neural networks OR GNN"` | all three words, or GNN |

Rules:

- Commas, semicolons and `OR` separate alternatives.
- Within an alternative, every word is required.
- Double quotes keep a phrase together.

The terms are written to the catalogue's configuration file. Edit that file
to refine the search, for example to add synonyms or a second required term
(see [Configuration](configuration.md)).

The time window:

- A new topic starts 12 months back. `--range 5y` or `--from 2015` start it
  earlier.
- Asking later for older work extends the window.
- Each later update searches the whole window up to today, and adds only what
  the catalogue does not already contain.

## Commands

| command | what it does |
|---|---|
| `sourcelens "TOPIC"` | same as `sourcelens update --topic "TOPIC"` |
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

`sourcelens help COMMAND` or `sourcelens COMMAND --help` lists every option of a
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

Options that take several values accept a comma list, for example
`--sources pubmed,arxiv`.

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
sourcelens query --range 1m                                     # published in the last month
sourcelens query --type review --sort cited_by --limit 20       # most-cited reviews
sourcelens query --title "base editor" --from 2024
sourcelens query --fulltext "off-target" --type article         # search inside the downloaded full texts
sourcelens query --added-since 2026-10-01 --out new.csv         # what the last runs added
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
sourcelens export --topic "CRISPR base editing" --type review --format APA
sourcelens export --doi 10.1038/s41586-019-1711-4 --format BIB,RIS --out anzalone
sourcelens export --from 2025 --tier core --format CSL --out core2025.json
```

`--out name.ext` writes one format to that file. `--out name` writes one file
per format (`name.apa.txt`, `name.bib`, ...). Relative paths go to the
catalogue's `exports/` folder. Without `--out`, the references are printed.
