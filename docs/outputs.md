# Outputs

## The catalogue folder

Every catalogue is a folder:

```
<catalogue>/
  progress.csv               the catalogue, oldest first
  config/sourcelens.yaml     its configuration (edit freely)
  fulltext/<year>/<id>/      paper.pdf, paper.md, paper.txt, paper.jats.xml, metadata.json,
                             attachments/ (figures, tables, supplementary files)
  fulltext/fulltext_index.csv       every paper tried: status, reason, files, attachments
  fulltext/attachments_index.csv    every attachment known: type, label, caption, size, status
  corpus/                    every harvested record with abstract (records.jsonl.gz),
                             references.csv (full author lists, for export), search hits,
                             citation counts, preprint links, broad-search hits
  repos/  websites/  seeds/  intermediate tables of those steps
  changelog/                 added_<date>.csv for every day with new rows, runs.csv
  summary/findings.md        growth per year, most-cited work, tools, websites, coverage
  exports/                   files written by query and export
  reports/                   runreport_<stamp>.html after every run, dryrun_<stamp>.csv after a dry run
  logs/cli_<stamp>/          plan, one log per step, summary, the configuration used
```

The output folder (see [Configuration](configuration.md)) holds the default
topic's catalogue directly, and one subfolder per other topic.

A paper gets a folder under `fulltext/` only when at least one of its files
was downloaded. A paper without an open copy has no folder: its row in
`fulltext/fulltext_index.csv` and in `progress.csv` says why, for example
`no PMC copy; Unpaywall: not open access` or `bioRxiv / medRxiv rate limit;
tried again on the next run`.

## Attachments

Open-access articles in PMC come with their figures and supplementary files
(spreadsheets, slides, documents, archives, data). They are saved in the
paper's `attachments/` folder and listed in `fulltext/attachments_index.csv`,
one row per file:

| column | meaning |
|---|---|
| `uid`, `file`, `ext` | the paper, the file name, its extension |
| `kind` | `figure`, `table` or `supplementary` |
| `label`, `caption` | from the article, e.g. "Figure 2" and its caption, "eTable 1" |
| `bytes` | size |
| `status` | `listed` (known, not downloaded), `ok`, `skipped` (larger than `--max-attachment-mb`), `failed`, `moved`, `deleted`; `none` for a paper without attachments |
| `path`, `url` | where it is on disk (or where it was moved to), where it came from |
| `checked_on`, `reason` | when, and why it was not downloaded |

`--ext` limits the extensions downloaded, `--max-attachment-mb` the size;
files left out stay `listed` and can be fetched later with `download`.

## progress.csv

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
| `fulltext_reason` | why there is no full text (or not every format) |
| `attachments` | attachments downloaded/known and their types, e.g. `3/5: jpg 3, pdf 1, xlsx 1` |
| `cited_by` | highest citation count from OpenAlex, Europe PMC or Crossref |
| `details`, `matched_groups`, `found_by`, `local_refs` | repository stars and languages; which searches found the row; which reference folder mentions it |
| `status` | empty, `undated`, or `no longer matched by pipeline (kept)` |
| `uid` | stable key: `doi:...`, `pmid:...`, `pmcid:...`, `epmc:...` or `url:...` |
| `notes`, `user_tags` | yours; sourcelens never changes them |

## Updates only add

- A row whose `uid` is already in the catalogue keeps its `added_on`, `notes`
  and `user_tags`. Derived columns are refreshed.
- A row the searches no longer find is kept and marked in `status`.
- Every update writes the new rows of the day to `changelog/added_<date>.csv`.
- Full texts already on disk are skipped. Rate-limited, incomplete and failed
  downloads are retried on the next run; papers without an open copy are
  retried after 30 days. Failed attachments are retried too.
- Files moved or deleted with `sourcelens files` are not downloaded again.
- Only one update runs on a catalogue at a time (`.pipeline.lock`).
- `sourcelens test` checks these rules on a throwaway catalogue.
