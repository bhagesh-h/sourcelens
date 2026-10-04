# Configuration reference

sourcelens reads two kinds of files.

| file | scope | holds |
|---|---|---|
| `settings.yaml` | this machine | default output folder, contact email, API keys |
| `<catalogue>/config/sourcelens.yaml` | one catalogue | topic, search terms, sources, classification rules, repository and website settings |

## Settings (per machine)

Location: `~/.config/sourcelens/settings.yaml` on Linux,
`~/Library/Application Support/sourcelens/settings.yaml` on macOS. Run
`sourcelens config path` to print it; set `SOURCELENS_SETTINGS` to use another file.

Change values with `sourcelens config set KEY VALUE` and remove them with
`sourcelens config unset KEY`. `sourcelens config` shows the current values,
with keys masked.

| key | meaning | environment override |
|---|---|---|
| `output` | folder that holds the catalogues (default `~/sourcelens`) | `SOURCELENS_OUTPUT` |
| `contact_email` | sent to Crossref, OpenAlex and NCBI (polite pools); required by Unpaywall | `SOURCELENS_EMAIL` |
| `github_token` | raises the GitHub search rate limit; when empty, `gh auth token` is used if the GitHub CLI is logged in | `GITHUB_TOKEN` |
| `openalex_api_key` | OpenAlex premium key | `OPENALEX_API_KEY` |
| `ncbi_api_key` | faster PubMed requests | `NCBI_API_KEY` |

The file is written with permissions 600. Credentials are passed to the
pipeline steps as environment variables and are never written anywhere else.

## Catalogue folders

The default topic's catalogue is the output folder itself. Every other topic
gets a subfolder named after it:

```
<output>/                         default topic
<output>/crispr-base-editing/     sourcelens "CRISPR base editing"
<output>/graph-neural-networks/   sourcelens "graph neural networks"
```

`--dir DIR` uses any other folder. `sourcelens list` shows the catalogues in
the output folder.

A catalogue's configuration is created on its first update:

- from the topic you typed;
- or, for the default topic, from the configuration built into sourcelens.

Edit `<catalogue>/config/sourcelens.yaml` freely. The next update reads it
again, and every run saves the copy it used in `logs/cli_<stamp>/`.

## The catalogue configuration

### Top level

| key | meaning |
|---|---|
| `topic` | name of the catalogue; `sourcelens --topic` and `sourcelens list` use it |
| `references` | local folders (absolute paths) mined for DOIs and links, for example your notes, a project, or a reading list |
| `seeds` | how reference folders are read (optional, see below) |

### `seeds` (optional)

Every text file in a reference folder is scanned for DOIs and for links to
code and data hosts (GitHub, GitLab, Zenodo, CRAN, Bioconductor, PyPI, ...).
Two folder layouts are read in more detail:

- **Clock registry.** A file ending in `registry/data/clocks.yaml`, with
  `evidence.yaml` beside it and `docs/references.yml` at the folder root.
- **Literature manifests.** `literature/manifest_methods.tsv`,
  `manifest_local.tsv` and `manifest.tsv`, with `doi`, `topic`, `title` and
  `question` columns.

| key | meaning |
|---|---|
| `methods_topics` | regular expression; manifest rows whose topic matches it are `methods_reference` seeds, the others `methods_other` |
| `skip_paths` | path fragments to skip, relative to the parent of a reference folder (`/papers/` is always skipped) |
| `keep_url_domains` | regular expression of further website domains to keep besides code and data hosts |

Seed roles are, strongest first:

1. `registry_origin`
2. `registry_reference`
3. `benchmark`
4. `methods_reference`
5. `cohort_paper`
6. `mentioned`

### `search`

| key | meaning |
|---|---|
| `start_date` | first publication date of the catalogue window (`YYYY-MM-DD`). An update with an earlier `--from` or `--range` moves it back. |
| `end_date` | last date; `today` means the date of each run |
| `sources` | literature sources searched for this topic: any of `pubmed`, `europepmc`, `arxiv`, `openalex`. The `--sources` option of a run can only narrow this list. Without the key: `pubmed`, `europepmc`, `arxiv`. |
| `europepmc_sources` | `nonmed` (Europe PMC records outside MEDLINE, because PubMed already returns MEDLINE) or `all` |
| `max_results` | at most this many works per group from OpenAlex, newest first (default 5000) |
| `groups` | named search groups (below) |

Each group:

| key | meaning |
|---|---|
| `scope` | `focused`: matches enter `progress.csv`. `broad`: metadata only, in `corpus/broad_hits.csv`. |
| `label` | a readable name |
| `terms` | list of terms, matched in title or abstract; any term matches |
| `require_any` | optional second list; a record must also match one of these |

A term is a phrase (`"base editing"`). Parts joined with `&` must all appear:
`CRISPR & base editing` needs both "CRISPR" and "base editing".

What you type after `sourcelens` becomes terms as follows:

| input | terms |
|---|---|
| `CRISPR base editing` | `CRISPR & base & editing` (every word required) |
| `"base editing", prime editing` | `base editing`, `prime & editing` |
| `graph neural networks OR GNN` | `graph & neural & networks`, `GNN` |

Commas, semicolons and `OR` separate alternatives. Double quotes keep a phrase
together.

### `classify`

Every catalogue row is annotated by these rules on every run. Patterns are
regular expressions in Perl/Python syntax (look-arounds allowed), matched
without regard to case.

| key | meaning |
|---|---|
| `category` | ordered list of rules; the first match wins. A rule has `name` and any of `pub_types`, `title`, `text` (title, abstract, keywords and MeSH). |
| `default_category` | category when no rule matches |
| `origin_category` | category of papers that introduced a registry entry |
| `core_categories` | categories that make a focused row `core` even without a title match |
| `core_title_terms` | a focused row whose title matches is `core`; other focused rows are `related` |
| `landmark_roles` | seed roles that make a row `landmark` |
| `modality` | name: pattern; the data layers a work uses (`modality` column) |
| `summary_modalities` | the layers shown in the summary tables (default: every `modality` rule) |
| `species` | name: pattern (`species` column); `human` also matches the MeSH heading Humans |
| `entities` | name: pattern; named methods, models, measures or tools (`entities` column), matched as whole words, case-sensitive unless the pattern starts with `(?i)` |

### `repos`

| key | meaning |
|---|---|
| `github_queries` | GitHub repository searches (terms as above, or `topic:NAME`) |
| `relevance` | a search hit is kept only when its name, description or topics match |
| `package_terms` | matched against CRAN and Bioconductor package titles and descriptions |
| `known_packages` | `cran`, `bioconductor`, `pypi`: package names always included |

### `websites`

| key | meaning |
|---|---|
| `sites` | curated sites: `name`, `urls` (alternatives tried in order), `type`, `note`, `related_doi` |
| `relevance` | a site cited by several papers is kept when its title or description matches (default: `repos.relevance`) |
