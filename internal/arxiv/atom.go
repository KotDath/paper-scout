package arxiv

import (
	"bytes"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

type Paper struct {
	ID          string    `json:"id"`
	ArxivID     string    `json:"arxiv_id"`
	Version     int       `json:"version"`
	Title       string    `json:"title"`
	Authors     []string  `json:"authors"`
	Abstract    string    `json:"abstract"`
	AbstractURL string    `json:"abstract_url"`
	HTMLURL     string    `json:"html_url"`
	PDFURL      string    `json:"pdf_url"`
	PublishedAt time.Time `json:"published_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type atomEntry struct {
	ID        string `xml:"http://www.w3.org/2005/Atom id"`
	Title     string `xml:"http://www.w3.org/2005/Atom title"`
	Summary   string `xml:"http://www.w3.org/2005/Atom summary"`
	Published string `xml:"http://www.w3.org/2005/Atom published"`
	Updated   string `xml:"http://www.w3.org/2005/Atom updated"`
	Authors   []struct {
		Name string `xml:"http://www.w3.org/2005/Atom name"`
	} `xml:"http://www.w3.org/2005/Atom author"`
	Links []struct {
		Href string `xml:"href,attr"`
	} `xml:"http://www.w3.org/2005/Atom link"`
}

func parseAtom(body []byte, limit int) ([]Paper, error) {
	var feed struct {
		XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
		Entries []atomEntry `xml:"http://www.w3.org/2005/Atom entry"`
	}
	decoder := xml.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&feed); err != nil {
		return nil, errors.New("arXiv returned invalid Atom")
	}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("arXiv returned invalid Atom")
		}
		if data, ok := token.(xml.CharData); !ok || strings.TrimSpace(string(data)) != "" {
			return nil, errors.New("arXiv returned trailing XML content")
		}
	}
	if len(feed.Entries) > limit {
		return nil, errors.New("arXiv exceeded result limit")
	}
	papers := make([]Paper, 0, len(feed.Entries))
	seen := map[string]bool{}
	for _, entry := range feed.Entries {
		if strings.Contains(entry.ID, "/api/errors") {
			return nil, &Error{Code: "invalid_query"}
		}
		id, err := ParseID(entry.ID)
		if err != nil {
			return nil, errors.New("arXiv returned invalid article ID")
		}
		// Older Atom feeds sometimes omit vN in <id> but include it in links.
		for _, link := range entry.Links {
			linked, err := ParseID(link.Href)
			if err != nil || linked.ArxivID != id.ArxivID {
				continue
			}
			if linked.Version != 0 {
				if id.Version != 0 && linked.Version != id.Version {
					return nil, errors.New("arXiv returned conflicting versions")
				}
				id.Version = linked.Version
			}
		}
		if id.Version == 0 {
			return nil, errors.New("arXiv metadata has no explicit version")
		}
		published, e1 := time.Parse(time.RFC3339, entry.Published)
		updated, e2 := time.Parse(time.RFC3339, entry.Updated)
		title, abstract := strings.Join(strings.Fields(entry.Title), " "), strings.TrimSpace(entry.Summary)
		authors := make([]string, 0, len(entry.Authors))
		for _, author := range entry.Authors {
			if name := strings.Join(strings.Fields(author.Name), " "); name != "" {
				authors = append(authors, name)
			}
		}
		if e1 != nil || e2 != nil || updated.Before(published) || title == "" || abstract == "" || len(authors) == 0 {
			return nil, errors.New("arXiv returned incomplete article metadata")
		}
		if seen[id.String()] {
			continue
		}
		seen[id.String()] = true
		papers = append(papers, Paper{
			ID: fmt.Sprintf("pv_%x", sha256.Sum256([]byte(id.String()))), ArxivID: id.ArxivID, Version: id.Version,
			Title: title, Abstract: abstract, Authors: authors,
			AbstractURL: canonicalURL("abs", id), HTMLURL: canonicalURL("html", id), PDFURL: canonicalURL("pdf", id),
			PublishedAt: published.UTC(), UpdatedAt: updated.UTC(),
		})
	}
	return papers, nil
}
