# Default topic: aging clocks

sourcelens was built to follow research on measuring biological aging. That
catalogue is the default topic: `sourcelens update` without `--topic` builds
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

## State on 2026-10-04

| | count |
|---|---|
| rows in `progress.csv` | 27,926 |
| papers | 19,901 articles, 3,724 reviews, 3,415 preprints, 46 reports |
| tiers | 157 landmark, 13,716 core, 14,053 related |
| code and data | 654 repositories, 27 packages, 46 archives, 43 datasets |
| websites, databases, calculators | 69 |
| full texts | 15,999 complete, 377 partial, 481 deferred |

## Findings

From the catalogue's `summary/findings.md`:

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
