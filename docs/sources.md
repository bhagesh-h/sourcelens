# Sources and limitations

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

OpenAlex matches word stems, so sourcelens keeps an OpenAlex result only when
the topic words appear as whole words in its title or abstract. It also skips
further versions of a work already in the catalogue: the same title, venue
and year under another DOI, as with Zenodo and figshare versions.

Only open-access copies are downloaded. Requests are spaced per host and
retried with back-off. bioRxiv and medRxiv rate limits mark downloads as
deferred instead of waiting; `sourcelens retry` picks them up later.

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
