package main

// PubMed: esearch / efetch / parse, and the "pubmed" step.
// Python twin: src/sourcelens/common/pubmed.py and src/sourcelens/pullliturature/search_pubmed.py.

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const eutils = "https://eutils.ncbi.nlm.nih.gov/entrez/eutils"
const esearchCap = 9000

var monthNum = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7,
	"aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}

func pubmedClient(log *Logger) *Client {
	iv := 360 * time.Millisecond
	if os.Getenv("NCBI_API_KEY") != "" {
		iv = 120 * time.Millisecond
	}
	return NewClient(iv, nil, log)
}

func withKey(v url.Values) url.Values {
	if k := os.Getenv("NCBI_API_KEY"); k != "" {
		v.Set("api_key", k)
	}
	return v
}

func esearch(c *Client, term string, retmax int) (int, []string, error) {
	r := c.Post(eutils+"/esearch.fcgi", withKey(url.Values{"db": {"pubmed"}, "term": {term},
		"retmode": {"json"}, "retmax": {strconv.Itoa(retmax)}}), reqOpts{})
	if r == nil || r.Status != 200 {
		return 0, nil, fmt.Errorf("esearch failed")
	}
	var js struct {
		R struct {
			Count  string   `json:"count"`
			IDList []string `json:"idlist"`
		} `json:"esearchresult"`
	}
	if err := json.Unmarshal(r.Body, &js); err != nil {
		return 0, nil, err
	}
	n, _ := strconv.Atoi(js.R.Count)
	return n, js.R.IDList, nil
}

// --- efetch XML ---

type xText struct {
	Inner string `xml:",innerxml"`
}

func (x xText) Text() string { return cleanText(xmlUnescape(x.Inner)) }

func xmlUnescape(s string) string {
	// inner XML still has entities and tags: strip the tags, then decode the
	// entities, numeric references (&#x2009;) included, as lxml's itertext does
	return html.UnescapeString(tagRe.ReplaceAllString(s, ""))
}

type pmDate struct {
	Year, Month, Day, MedlineDate string
}

type pmArticle struct {
	PMID    string `xml:"MedlineCitation>PMID"`
	Article struct {
		Title    xText `xml:"ArticleTitle"`
		Abstract struct {
			Texts []struct {
				Label string `xml:"Label,attr"`
				xText
			} `xml:"AbstractText"`
		} `xml:"Abstract"`
		Authors []struct {
			Last, Initials, CollectiveName string `xml:"-"`
			LastName                       string `xml:"LastName"`
			Init                           string `xml:"Initials"`
			Collective                     xText  `xml:"CollectiveName"`
		} `xml:"AuthorList>Author"`
		Journal struct {
			Title string `xml:"Title"`
			Issue struct {
				Volume  string `xml:"Volume"`
				Issue   string `xml:"Issue"`
				PubDate pmDate `xml:"PubDate"`
			} `xml:"JournalIssue"`
		} `xml:"Journal"`
		Pages     string   `xml:"Pagination>MedlinePgn"`
		ELoc      []eloc   `xml:"ELocationID"`
		Language  string   `xml:"Language"`
		ArtDates  []pmDate `xml:"ArticleDate"`
		PubTypes  []xText  `xml:"PublicationTypeList>PublicationType"`
		GrantList []struct {
			Agency string `xml:"Agency"`
		} `xml:"GrantList>Grant"`
	} `xml:"MedlineCitation>Article"`
	Keywords []xText `xml:"MedlineCitation>KeywordList>Keyword"`
	Mesh     []struct {
		Descriptor xText `xml:"DescriptorName"`
	} `xml:"MedlineCitation>MeshHeadingList>MeshHeading"`
	IDs []struct {
		Type string `xml:"IdType,attr"`
		Val  string `xml:",chardata"`
	} `xml:"PubmedData>ArticleIdList>ArticleId"`
}

type eloc struct {
	Type string `xml:"EIdType,attr"`
	Val  string `xml:",chardata"`
}

func fmtDate(y, m, d string) string {
	mi := 1
	if m != "" {
		if v, ok := monthNum[strings.ToLower(m[:min(3, len(m))])]; ok {
			mi = v
		} else if v, err := strconv.Atoi(m); err == nil {
			mi = v
		}
	}
	di := 1
	if v, err := strconv.Atoi(d); err == nil {
		di = v
	}
	return fmt.Sprintf("%s-%02d-%02d", y, mi, di)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func pmPubDate(a *pmArticle) string {
	var cands []string
	for _, ad := range a.Article.ArtDates {
		if ad.Year != "" {
			cands = append(cands, fmtDate(ad.Year, ad.Month, ad.Day))
		}
	}
	pd := a.Article.Journal.Issue.PubDate
	y, m := pd.Year, pd.Month
	if y == "" && pd.MedlineDate != "" {
		y = pd.MedlineDate[:min(4, len(pd.MedlineDate))]
		if len(pd.MedlineDate) > 7 {
			m = pd.MedlineDate[5:8]
		} else {
			m = ""
		}
	}
	if isDigits(y) {
		cands = append(cands, fmtDate(y, m, pd.Day))
	}
	best := ""
	for _, c := range cands {
		if best == "" || c < best {
			best = c
		}
	}
	return best
}

func parsePubmed(body []byte) []Rec {
	var set struct {
		Articles []pmArticle `xml:"PubmedArticle"`
	}
	if xml.Unmarshal(body, &set) != nil {
		return nil
	}
	var out []Rec
	for i := range set.Articles {
		a := &set.Articles[i]
		doi, pmcid := "", ""
		for _, id := range a.IDs {
			if id.Type == "doi" && doi == "" {
				doi = strings.TrimSpace(id.Val)
			} else if id.Type == "pmc" {
				pmcid = strings.TrimSpace(id.Val)
			}
		}
		if doi == "" {
			for _, e := range a.Article.ELoc {
				if e.Type == "doi" {
					doi = strings.TrimSpace(e.Val)
				}
			}
		}
		var authors []string
		for _, au := range a.Article.Authors {
			if c := au.Collective.Text(); c != "" {
				authors = append(authors, c)
			} else {
				authors = append(authors, strings.TrimSpace(au.LastName+" "+au.Init))
			}
		}
		var abs []string
		for _, t := range a.Article.Abstract.Texts {
			p := t.Text()
			if t.Label != "" {
				p = t.Label + ": " + p
			}
			abs = append(abs, p)
		}
		var pts, kws, mesh []string
		retracted := false
		isPre := false
		for _, p := range a.Article.PubTypes {
			s := p.Text()
			pts = append(pts, s)
			if s == "Retracted Publication" || s == "Retraction of Publication" {
				retracted = true
			}
			if s == "Preprint" {
				isPre = true
			}
		}
		for _, k := range a.Keywords {
			kws = append(kws, k.Text())
		}
		for _, m := range a.Mesh {
			mesh = append(mesh, m.Descriptor.Text())
		}
		fund := map[string]bool{}
		for _, g := range a.Article.GrantList {
			if g.Agency != "" {
				fund[g.Agency] = true
			}
		}
		date := pmPubDate(a)
		year := ""
		if len(date) >= 4 {
			year = date[:4]
		}
		r := Rec{
			"epmc_id": a.PMID, "source_db": "MED", "doi": normDOI(doi), "pmid": normPMID(a.PMID),
			"pmcid": normPMCID(pmcid), "title": a.Article.Title.Text(), "authors": strings.Join(authors, ", "),
			"journal": a.Article.Journal.Title, "volume": a.Article.Journal.Issue.Volume,
			"issue": a.Article.Journal.Issue.Issue, "pages": a.Article.Pages, "pub_date": date, "pub_year": year,
			"pub_types": strings.Join(pts, "; "), "is_preprint": isPre, "in_pmc": pmcid != "",
			"language": a.Article.Language, "abstract": strings.Join(abs, " "),
			"keywords": strings.Join(kws, "; "), "mesh": strings.Join(mesh, "; "),
			"funders": strings.Join(sortedKeys(fund), "; "), "retracted": retracted,
		}
		r["uid"] = makeUID(str(r, "doi"), str(r, "pmid"), str(r, "pmcid"), "", "")
		out = append(out, r)
	}
	return out
}

func efetch(c *Client, pmids []string, log *Logger) []Rec {
	var out []Rec
	for i := 0; i < len(pmids); i += 200 {
		chunk := pmids[i:min(i+200, len(pmids))]
		r := c.Post(eutils+"/efetch.fcgi", withKey(url.Values{"db": {"pubmed"}, "id": {strings.Join(chunk, ",")},
			"retmode": {"xml"}}), reqOpts{timeout: 180 * time.Second})
		if r == nil || r.Status != 200 {
			log.Printf("efetch failed for %d PMIDs starting %s", len(chunk), chunk[0])
			continue
		}
		out = append(out, parsePubmed(r.Body)...)
		if (i/200)%10 == 9 || i+200 >= len(pmids) {
			log.Printf("  efetch %d/%d", min(i+200, len(pmids)), len(pmids))
		}
	}
	return out
}

// --- step ---

func pmBlock(terms []string) string {
	q := make([]string, len(terms))
	for i, t := range terms {
		q[i] = pubmedTerm(t)
	}
	return "(" + strings.Join(q, " OR ") + ")"
}

func pmDated(q, a, b string) string {
	return fmt.Sprintf(`%s AND ("%s"[dp] : "%s"[dp])`, q, strings.ReplaceAll(a, "-", "/"), strings.ReplaceAll(b, "-", "/"))
}

func pmCollect(c *Client, q, a, b string) (map[string]bool, error) {
	n, _, err := esearch(c, pmDated(q, a, b), 0)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	if n == 0 {
		return out, nil
	}
	if n <= esearchCap || a == b {
		_, ids, err := esearch(c, pmDated(q, a, b), esearchCap+999)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			out[id] = true
		}
		return out, nil
	}
	d0, _ := time.Parse("2006-01-02", a)
	d1, _ := time.Parse("2006-01-02", b)
	mid := d0.Add(d1.Sub(d0) / 2)
	for _, w := range [][2]string{{a, mid.Format("2006-01-02")}, {mid.AddDate(0, 0, 1).Format("2006-01-02"), b}} {
		part, err := pmCollect(c, q, w[0], w[1])
		if err != nil {
			return nil, err
		}
		for k := range part {
			out[k] = true
		}
	}
	return out, nil
}

func stepPubmed(args []string, log *Logger) error {
	o := parseStepArgs(args)
	all, _ := loadGroups()
	groups := filterGroups(all, o["groups"], orDefault(o["scope"], "all"))
	start, end := searchWindow()
	c := pubmedClient(log)
	store := LoadStore()
	hits := loadHits()
	run := today()
	var counts omap // python dicts keep insertion order
	for _, g := range groups {
		q := pmBlock(g.Terms)
		if len(g.RequireAny) > 0 {
			q += " AND " + pmBlock(g.RequireAny)
		}
		total, _, err := esearch(c, pmDated(q, start, end), 0)
		if err != nil {
			return err
		}
		pmids, err := pmCollect(c, q, start, end)
		if err != nil {
			return err
		}
		var missing []string
		for p := range pmids {
			if store.FindID("", p, "") == "" {
				missing = append(missing, p)
			}
		}
		sort.Strings(missing)
		log.Printf("%s: PubMed count %d, collected %d, %d not in store", g.Name, total, len(pmids), len(missing))
		for _, r := range efetch(c, missing, log) {
			store.Merge(r, "pubmed")
		}
		nNew := 0
		for p := range pmids {
			uid := store.FindID("", p, "")
			if uid == "" {
				uid = "pmid:" + p
			}
			if recordHit(hits, uid, g.Name, g.Scope, "pubmed", run) {
				nNew++
			}
		}
		counts = append(counts, okv{g.Name, omap{{"count", total}, {"collected", len(pmids)}, {"fetched", len(missing)},
			{"new_hits", nNew}, {"query", pmDated(q, start, end)}}})
		if err := store.Save(); err != nil {
			return err
		}
		if err := saveHits(hits); err != nil {
			return err
		}
	}
	_ = writeJSON(rpath("raw", "pubmed", run, "counts.json"), omap{{"window", []string{start, end}}, {"groups", counts}})
	log.Printf("done: store %d records", store.Len())
	return nil
}
