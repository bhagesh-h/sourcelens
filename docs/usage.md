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
sourcelens "CRISPR base editing" --plan                   # show what would run
```

## Dry run: look first, then download what you pick

A dry run searches and fetches metadata, but downloads no full texts and no
attachments. It writes a table of everything it found, which you filter, and
then you download only the rows you kept:

```bash
sourcelens "CRISPR base editing" --dry-run
sourcelens query --topic "CRISPR base editing" --in reports/dryrun_<stamp>.csv \
    --summary "prime editing|off-target" --type article,review --out picked.csv
sourcelens download --topic "CRISPR base editing" exports/picked.csv
```

The dry run prints its numbers and the exact path of its table,
`reports/dryrun_YYYY_MM_DD_HH_MM_SS.csv`. The table has every column of
`progress.csv` plus:

| column | meaning |
|---|---|
| `summary` | the abstract; else the start of the downloaded full text; else a repository's or website's description |
| `summary_from` | `abstract`, `fulltext` or `description` |
| `keywords` | author keywords and MeSH headings |
| `fulltext_sources` | where an open full text may come from: `pmc`, `biorxiv`, `europepmc`, `arxiv`, `unpaywall`, `openalex` |
| `pending` | what the next update would download: `full text`, `attachments`, with `(retry)` after a failed attempt |
| `attachments` | files known for the paper, e.g. `0/5: jpg 3, pdf 1, xlsx 1` (downloaded/known, then by type) |

Filter it with any `query` option (`--in` reads it instead of
`progress.csv`), with your own tools, or by deleting rows in a spreadsheet;
`download` only needs the `uid` (or `doi`) column of what is left.

The dry run lists the attachments of open-access PMC articles (one quick
request per article, cached in `fulltext/attachments_index.csv`), so you can
pick by file type before downloading anything.

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
| `download` | download the full texts and attachments of the rows in a CSV, such as a filtered dry run |
| `retry` | download again the full texts that were rate-limited, incomplete or failed |
| `query` | filter a catalogue or a dry-run table; print the rows or save them as CSV |
| `files` | list, copy, move or delete downloaded files by extension, name, kind or paper |
| `export` | write catalogue entries as references |
| `report` | write the run report of a catalogue now |
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
| `--types` | `papers` (metadata), `pdf`, `md`, `txt`, `xml`, `attachments`, `repo`, `website`, `all`; groups `fulltext` (pdf, md, txt, xml) and `files` (the same plus attachments) | all |
| `--sources` | `pubmed europepmc arxiv openalex local pmc biorxiv unpaywall github cran bioconductor pypi zenodo websites`, or groups `literature`, `fulltext`, `repos`, `packages` | all |
| `--paper-types` | `article review preprint report thesis "conference paper" "book chapter"` (full texts only) | all |
| `--tiers` | full texts for `landmark,core,related` rows | all three |
| `--scope` | search groups `focused`, `broad`, `all` | all |
| `--workers` | parallel full-text downloads | 12 |
| `--parallel` | steps run at the same time | 5 |
| `--min-stars` | GitHub search hits need this many stars | 3 |
| `--heartbeat` | seconds between progress lines when the output is not a terminal (0: no progress output) | 120 |
| `--stop-on-error` | stop after the first failed stage | |
| `--ext` | attachment file extensions to download, e.g. `xlsx,csv,pptx` | all |
| `--max-attachment-mb` | larger attachments are listed but not downloaded (0: no limit) | 100 |
| `--dry-run` | metadata only, no downloads; writes the dry-run table (see above) | |
| `--plan` | print the plan and a full-text estimate, then stop | |

Options that take several values accept a comma list, for example
`--sources pubmed,arxiv`.

In a terminal, progress is one line at the bottom, redrawn in place: a bar
over all steps, the time so far, and each running step with its own count
(such as `fulltext 340/1200`) or its running time. When the output goes to a
file, a line per running step is written every `--heartbeat` seconds
instead. Ctrl+C stops the running steps and the update; the next update
starts normally.

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

### download

`sourcelens download FILE` downloads the full texts and attachments of the
rows in FILE, a CSV with a `uid` or `doi` column, then rebuilds the catalogue.
Rows downloaded before are checked again; attachments not downloaded before
(another extension, a failed download) are fetched.

| option | values | default |
|---|---|---|
| `--types` | `pdf`, `md`, `txt`, `xml`, `attachments` | all |
| `--sources` | full-text sources, as for `update` | all |
| `--ext`, `--max-attachment-mb` | as for `update` | all, 100 |
| `--workers` | parallel downloads | 12 |
| `--plan` | count the rows and stop | |

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
| `--summary RE` | regular expression over the summary (abstract, start of the full text, or a site's description) and keywords |
| `--fulltext-status LIST` | `ok`, `partial`, `none`, `deferred`, `error`, `removed`; `-` for not tried |
| `--ext LIST` | rows with attachments of these file types |
| `--has-attachments` | rows with attachments |
| `--in FILE` | filter this CSV (a dry-run table or a query output) instead of `progress.csv` |
| `--sort` | `date`, `cited_by` (highest first), `title`, `author` |

`query` prints 50 rows (`--limit N` changes that). `--out FILE` saves every
matching row as CSV.

### files

Lists the files of the catalogue: full texts (`paper.pdf`, `paper.md`,
`paper.txt`, `paper.jats.xml`) and attachments (figures, tables,
supplementary files). Every `query` filter picks the papers; these pick the
files:

| option | meaning |
|---|---|
| `--ext LIST` | file extensions, e.g. `pdf`, `xlsx,csv`, `pptx,docx`, `jpg,png` |
| `--name RE` | regular expression over the file name, label and caption ("Figure 2", "eTable 1") |
| `--kind LIST` | `paper`, `figure`, `table`, `supplementary`; `attachment` for the last three |
| `--status LIST` | `ok` (on disk, the default), `listed` (known, not downloaded), `skipped`, `failed`, `moved`, `deleted`, or `all` |

Then one action:

| option | meaning |
|---|---|
| (none) | print the files; `--limit N` lines, `--out FILE` all of them as CSV |
| `--copy-to DIR` | copy to `DIR/<paper>/<file>` (attachments under `DIR/<paper>/attachments/`), with a list in `DIR/sourcelens_files.csv` |
| `--move-to DIR` | the same, then the files leave the catalogue |
| `--delete --yes` | delete the files |
| `--flat` | with `--copy-to` or `--move-to`: `DIR/<paper>__<file>`, no subfolders |

```bash
sourcelens files --ext xlsx,csv --copy-to ~/tables
sourcelens files --kind figure --title "epigenetic clock" --range 2y --copy-to ~/figures --flat
sourcelens files --in exports/picked.csv --kind paper --ext pdf --copy-to ~/to-read
sourcelens files --status listed --ext pptx           # slides known but not downloaded
sourcelens files --ext txt --delete --yes
```

Moved and deleted files are recorded in the indexes, the paper's
`metadata.json` and `progress.csv`, so later updates do not download them
again.

### report

Every `update`, dry run, `download` and `retry` writes
`reports/runreport_YYYY_MM_DD_HH_MM_SS.html`, and `sourcelens report` writes
one for the catalogue as it is. It is a single file that opens in any
browser, without a network connection:

- the logo, the date and time, the version and the command;
- the steps of the run, with their time and last message;
- the catalogue's numbers: rows, papers, full texts downloaded and missing
  with the reasons, pending downloads, attachments by file type, code and
  data, websites, rows per year, types, tiers, categories, venues;
- every row in a table you can search (title, authors, venue, DOI, summary,
  keywords, reason), filter (type, tier, full-text status, years, with
  attachments, added on the run's date) and sort; a click on a row shows its
  details, and the rows you see can be saved as CSV.

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
