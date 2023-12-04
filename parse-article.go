package main

import (
	"fmt"
	"net/http"
	nurl "net/url"
	"strings"
	"time"

	"github.com/go-shiori/dom"
	"github.com/go-shiori/go-readability"
)

func ParseArticle(pageURL string, timeout time.Duration) (readability.Article, error) {
	// Make sure URL is valid
	parsedURL, err := nurl.ParseRequestURI(pageURL)
	if err != nil {
		return readability.Article{}, fmt.Errorf("failed to parse URL: %v", err)
	}

	// Fetch page from URL
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(pageURL)
	if err != nil {
		return readability.Article{}, fmt.Errorf("failed to fetch the page: %v", err)
	}
	defer resp.Body.Close()

	// Make sure content type is HTML
	cp := resp.Header.Get("Content-Type")
	if !strings.Contains(cp, "text/html") {
		return readability.Article{}, fmt.Errorf("URL is not a HTML document")
	}

	// Parse content
	parser := readability.NewParser()

	// Parse input
	doc, err := dom.Parse(resp.Body)
	if err != nil {
		return readability.Article{}, fmt.Errorf("failed to parse input: %v", err)
	}

	for _, img := range dom.QuerySelectorAll(doc, "img") {
		for _, attr := range img.Attr {
			if attr.Key != "src" {
				continue
			}
			attrUrl, err := nurl.Parse(attr.Val)
			if err != nil {
				continue
			}
			attr.Val = parsedURL.ResolveReference(attrUrl).String()
		}
	}

	return parser.ParseDocument(doc, parsedURL)
}

