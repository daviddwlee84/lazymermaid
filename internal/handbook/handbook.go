// Package handbook exposes a versioned, offline copy of upstream Mermaid docs.
package handbook

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

//go:embed docs/*.md metadata.json LICENSE.upstream
var snapshot embed.FS

type Page struct {
	Slug    string `json:"slug"`
	Title   string `json:"title"`
	Source  string `json:"source"`
	Version string `json:"version"`
}

type Hit struct {
	Page
	Excerpt string `json:"excerpt"`
	Score   int    `json:"score"`
}

var pages = loadPages()

func loadPages() []Page {
	data, err := snapshot.ReadFile("metadata.json")
	if err != nil {
		panic(err)
	}
	var metadata struct {
		Pages []Page `json:"pages"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		panic(err)
	}
	return metadata.Pages
}

func List() []Page { return append([]Page(nil), pages...) }

func Read(slug string) (Page, string, error) {
	for _, page := range pages {
		if page.Slug == slug {
			data, err := snapshot.ReadFile("docs/" + page.Slug + ".md")
			return page, string(data), err
		}
	}
	return Page{}, "", fmt.Errorf("unknown handbook page %q", slug)
}

func License() string {
	data, _ := snapshot.ReadFile("LICENSE.upstream")
	return string(data)
}

// Search matches all whitespace-separated terms, ignoring case. The displayed
// excerpt is copied from upstream Markdown, not generated syntax guidance.
func Search(query string, limit int) []Hit {
	if limit <= 0 {
		limit = 20
	}
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	var hits []Hit
	for _, page := range pages {
		_, body, _ := Read(page.Slug)
		lower := strings.ToLower(body)
		title := strings.ToLower(page.Title + " " + page.Slug)
		score, first, matches := 0, -1, true
		for _, term := range terms {
			index := strings.Index(lower, term)
			if index < 0 && !strings.Contains(title, term) {
				matches = false
				break
			}
			if index >= 0 && (first < 0 || index < first) {
				first = index
			}
			score += strings.Count(lower, term)
			if strings.Contains(title, term) {
				score += 100
			}
		}
		if !matches {
			continue
		}
		// Lowercasing can change UTF-8 byte length. Re-find a matching line in
		// the original text rather than slicing original bytes by lower's index.
		excerpt := page.Title
		if first >= 0 {
			for _, line := range strings.Split(body, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				found := false
				for _, term := range terms {
					if strings.Contains(strings.ToLower(line), term) {
						found = true
						break
					}
				}
				if found {
					excerpt = truncate(line, 200)
					break
				}
			}
		}
		hits = append(hits, Hit{Page: page, Excerpt: excerpt, Score: score})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			return hits[i].Title < hits[j].Title
		}
		return hits[i].Score > hits[j].Score
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

func truncate(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit]) + "…"
}
