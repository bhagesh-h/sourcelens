# Changelog

All notable changes to sourcelens. Versions follow semantic versioning.

## Unreleased

### Added

- `--dry-run` fetches metadata only and writes `reports/dryrun_<stamp>.csv`:
  every row with its summary (abstract, start of the full text, or a site's
  description), keywords, likely full-text sources, attachments and what an
  update would download. The old plan-only output is now `--plan`.
- `query --in FILE` filters a dry-run table or any query output; new filters
  `--summary`, `--fulltext-status`, `--ext`, `--has-attachments`.
- `sourcelens download FILE`: downloads the full texts and attachments of the
  rows in a CSV, such as a filtered dry run.
- Attachments: the figures, tables and supplementary files (spreadsheets,
  slides, documents, archives) of open-access PMC articles, with their labels
  and captions, in `<paper>/attachments/` and
  `fulltext/attachments_index.csv`; type `attachments`, options `--ext` and
  `--max-attachment-mb`.
- `sourcelens files`: lists, copies, moves or deletes full texts and
  attachments by extension, name, caption, kind, status or paper; moved and
  deleted files are not downloaded again.
- A run report after every update, dry run, download and retry:
  `reports/runreport_<stamp>.html`, one self-contained page with the logo,
  date, version, the run's steps, the catalogue's numbers and charts, and a
  searchable, filterable table of every row. `sourcelens report` writes one
  at any time.
- Windows: both implementations run on Windows; releases have
  `sourcelens_windows_amd64.exe` and `sourcelens_windows_arm64.exe`.
- `install.sh` (Linux, macOS) and `install.ps1` (Windows) install the right
  binary; releases have `SHA256SUMS`.
- CI on Linux, macOS (Apple silicon) and Windows; every release binary runs
  on its own system before the release is published.

### Changed

- A paper without any downloadable file gets no folder, only its index row,
  with the reason (`fulltext_reason` in `progress.csv`). Folders that earlier
  versions left with only `metadata.json` are removed on the next update.
- macOS: one universal binary, `sourcelens_macos`, runs on Apple silicon and
  Intel; `sourcelens_darwin_arm64` and `sourcelens_darwin_amd64` are copies of
  it. (The Intel-only file stopped with "bad CPU type in executable" on Apple
  silicon without Rosetta.)
- `progress.csv` has two new columns, `fulltext_reason` and `attachments`.
- Text files are written as UTF-8 with `\n` line ends on every platform.

## 1.0.0 (2026-10-04)

### Added

- `publish/publish.sh`: the local release commands (build and check, install
  tests from TestPyPI and PyPI, manual upload) run in a Docker container, so
  nothing is installed on the machine that releases.
- Project website (`site/index.html`), published to GitHub Pages by
  `.github/workflows/pages.yml`: install buttons for PyPI and the Go
  binaries, the basic commands, links to the documentation.
- A progress bar in the terminal: one line, redrawn in place, with the
  finished steps, the time so far and each running step's count or time.
  Output to a file keeps the `--heartbeat` lines.

### Changed

- The README covers only installing and basic use. Everything else moved to
  `docs/`: install options, usage, outputs, configuration, sources and
  limitations, development, and the default topic.
- Release binaries are named `sourcelens_<os>_<arch>`, without the version,
  so links to `releases/latest/download/` stay valid across releases.
- Logo: the black rim around the hexagon removed and the image cropped to it.
- PyPI project links point to the website, the source and `docs/`.

### Fixed

- A server asking for a very long wait (OpenAlex sends 7 to 8 hours once its
  daily limit is used up) no longer stalls the update: waits longer than 10
  minutes are not kept, the step fails with the time to try again, and the
  other steps go on.
- Ctrl+C prints "interrupted" instead of a Python traceback and exits
  with code 130, in both implementations.

## 0.0.1 (2026-10-04)

First public release, under the name sourcelens (developed as litsearch and
litSearch before). sourcelens is a general tool: the research topic comes from
the command line or from a configuration file, and the aging-clock catalogue
it was built for is the default topic.

### Added

- `sourcelens "TOPIC"`: start or update a catalogue for any topic from one
  line. Commas, semicolons and `OR` separate alternatives; the words of an
  alternative are all required; double quotes keep a phrase together.
- One catalogue per topic: the default topic in the output folder, every other
  topic in a subfolder named after it. `--topic` and `--dir` select a catalogue
  in every command; `sourcelens list` shows them.
- OpenAlex search, covering every field of research, and OpenAlex
  open-access PDF links as a full-text source.
- OpenAlex results are kept only when the topic words appear as whole words in
  the title or abstract.
- Further versions of a stored work (same title, venue and year, another DOI)
  are skipped.
- A new topic starts 12 months back. Asking later for older work with
  `--from` or `--range` extends the window.
- `sourcelens config`: per-machine settings (output folder, contact email, API
  keys) in `~/.config/sourcelens/settings.yaml`, with environment overrides.
- The newest additions are printed after every update.
- `pip install sourcelens` (Python 3.10+) and `go install` (one static binary)
  install the same command. PyMuPDF is an optional extra (`sourcelens[pdf]`);
  without it poppler-utils converts PDFs.
- Unit tests for both implementations, a parity check between them, CI and
  release workflows, a Makefile and a Dockerfile.
- `publish.yml` and `publish.md`: PyPI and TestPyPI publishing with trusted
  publishing, with version, README and wheel checks before every upload.
- `docs/configuration.md`: every configuration key.

### Changed

- Topic-specific parts moved from the code into the configuration:
  - reference folders (`references`, `seeds`);
  - the category of registry origin papers (`origin_category`);
  - the core categories and the default category;
  - the summary's data layers;
  - website relevance.
- Column `clocks` is now `entities`; configuration key `clock_names` is now
  `entities`; query option `--clock` is now `--entity`. Older catalogues and
  configurations are still read.
- Seed roles `clock_origin` and `clock_reference` are now `registry_origin`
  and `registry_reference`; `seeds/falconage_clocks.csv` is now
  `seeds/registry_entries.csv`; `corpus/pre2011_background.csv` is now
  `corpus/before_window.csv`.
- Docker is optional: both implementations run natively.

### Removed

- The Docker launchers (`run.sh`), the sequential `update_all.sh` and the
  per-workspace `config/local.yaml`; settings replace them.

## litSearch 2.0 (2026-10-04, unreleased)

- Python and Go implementations with the same commands, flags and outputs,
  each with its own Docker image.
- `query` and `export` (APA, AMA, MLA, Chicago, Harvard, Vancouver, IEEE,
  Nature, BibTeX, RIS, EndNote, CSL-JSON).
- One configuration file kept in the output folder.

## agingcat 1.x (2026-09-29 to 2026-10-04, unreleased)

- The aging-clock literature pipeline: PubMed, Europe PMC and arXiv searches,
  seeds from local projects, an append-only chronological `progress.csv`,
  open-access full texts, repositories, packages and websites, and the
  `agingcat` CLI with parallel stages, source, type and date-range selection.
