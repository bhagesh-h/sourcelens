# Sources and limitations

## Sources

| source | used for |
|---|---|
| PubMed (NCBI E-utilities) | search, biomedical literature |
| Europe PMC | search (preprints and records outside MEDLINE), full-text XML |
| arXiv | search, PDFs |
| OpenAlex | search across all fields, citation counts, exact dates, open-access PDF links |
| Crossref, DataCite | metadata for DOIs found in reference folders |
| PMC open-access bucket (AWS) | full texts: JATS XML, text, PDF, licence; figures and supplementary files |
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

A server that asks for a wait longer than 10 minutes gets no more requests
in that run. OpenAlex does this when its daily limit is used up, until
midnight UTC. The step is marked as failed with the time to try again, the
rest of the update continues, and the next update searches again. An
OpenAlex API key (`sourcelens config set openalex_api_key KEY`) raises the
limit.

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
