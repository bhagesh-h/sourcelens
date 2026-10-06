# sourcelens <img src="https://raw.githubusercontent.com/bhagesh-h/sourcelens/main/logo/logo.png" alt="sourcelens logo" width="140" align="right"/>

sourcelens collects the latest research on any topic into one chronological
catalogue. It searches PubMed, Europe PMC, arXiv and OpenAlex, downloads the
open-access full texts with their figures and supplementary files, finds the
code and packages the papers link to, and exports references for Zotero or
LaTeX. Run it again later and it adds only what is new; notes you write in
the catalogue are never overwritten.

Website: https://bhagesh-h.github.io/sourcelens/

## Install

**Python** (3.10 or newer, any system):

```bash
pip install sourcelens
```

**Go binary** (one file, no dependencies). Linux and macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/bhagesh-h/sourcelens/main/install.sh | sh
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/bhagesh-h/sourcelens/main/install.ps1 | iex
```

Or download `sourcelens_linux_amd64`, `sourcelens_linux_arm64`,
`sourcelens_macos` (Apple silicon and Intel) or
`sourcelens_windows_amd64.exe` from the
[latest release](https://github.com/bhagesh-h/sourcelens/releases/latest).
Both implementations have the same commands. To convert PDFs to text, the Go
binary needs poppler (`apt install poppler-utils`, `brew install poppler`).

## Use

```bash
sourcelens config set contact_email you@example.org    # once; Crossref, OpenAlex, NCBI and Unpaywall ask for it
sourcelens "CRISPR base editing"                        # start a catalogue; run again later to add what is new
sourcelens query --topic "CRISPR base editing" --range 1m
sourcelens export --topic "CRISPR base editing" --format BIB --out crispr.bib
```

Look first, then download only what you pick:

```bash
sourcelens "CRISPR base editing" --dry-run              # metadata and a table, no downloads
sourcelens query --topic "CRISPR base editing" --in reports/dryrun_<stamp>.csv --summary "off-target" --out picked.csv
sourcelens download --topic "CRISPR base editing" exports/picked.csv
sourcelens files --topic "CRISPR base editing" --ext xlsx,csv --copy-to ~/tables
```

Commas, semicolons and `OR` separate alternatives, every word of an
alternative is required, and double quotes keep a phrase together:
`sourcelens '"base editing", "prime editing"'`.

| option | meaning |
|---|---|
| `--range 1m`, `--from 2024` | how far back to search (a new topic starts 12 months back) |
| `--types papers` | metadata only, no downloads |
| `--sources pubmed,arxiv` | only these sources |
| `--ext xlsx,pptx` | attachment file types to download |
| `--dry-run` | metadata and a table of what could be downloaded, no downloads |
| `--dir DIR` | keep the catalogue in DIR |

Each topic gets a folder such as `~/sourcelens/crispr-base-editing/`, with
`progress.csv` as the main table, and every run writes
`reports/runreport_<stamp>.html`, a searchable page of the whole catalogue.
`sourcelens config set output DIR` changes the folder, and `sourcelens help`
lists every command.

## Documentation

- [Install options](https://github.com/bhagesh-h/sourcelens/blob/main/docs/install.md): every system, PDF extras, Docker, Python or Go
- [Usage](https://github.com/bhagesh-h/sourcelens/blob/main/docs/usage.md): dry run and download, files, reports, every command and option
- [Outputs](https://github.com/bhagesh-h/sourcelens/blob/main/docs/outputs.md): the catalogue folder, attachments, the columns of `progress.csv`
- [Configuration](https://github.com/bhagesh-h/sourcelens/blob/main/docs/configuration.md): settings and catalogue configuration
- [Sources and limitations](https://github.com/bhagesh-h/sourcelens/blob/main/docs/sources.md)
- [Development](https://github.com/bhagesh-h/sourcelens/blob/main/docs/development.md) and [publishing](https://github.com/bhagesh-h/sourcelens/blob/main/publish.md)
- [Default topic: aging clocks](https://github.com/bhagesh-h/sourcelens/blob/main/docs/aging-clocks.md)

## License

GPL-3.0. See [LICENSE](https://github.com/bhagesh-h/sourcelens/blob/main/LICENSE).
