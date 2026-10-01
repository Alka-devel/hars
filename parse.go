package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

const (
	wikiHome = "https://ru.ruwiki.ru/"
	wikiAPI  = wikiHome + "w/api.php"
)

var chromeDebugURL = "http://127.0.0.1:" + chromePort

var blanks = regexp.MustCompile(`(\n[ \t]*){3,}`)

func waitFor(ctx context.Context, expr string, d time.Duration) bool {
	for end := time.Now().Add(d); time.Now().Before(end) && ctx.Err() == nil; time.Sleep(300 * time.Millisecond) {
		var ok bool
		if chromedp.Run(ctx, chromedp.Evaluate("!!("+expr+")", &ok)) == nil && ok {
			return true
		}
	}
	return false
}

func getJSON(u string) (string, error) {
	alloc, stop := chromedp.NewRemoteAllocator(context.Background(), chromeDebugURL)
	defer stop()
	tab, closeTab := chromedp.NewContext(alloc)
	defer closeTab()
	ctx, cancel := context.WithTimeout(tab, 90*time.Second)
	defer cancel()

	var text string
	load := func() bool {
		return chromedp.Run(ctx, chromedp.Navigate(u)) == nil &&
			waitFor(ctx, `document.body.innerText.trim()[0]=='{' || /^HTTP [45]/.test(document.title)`, 15*time.Second) &&
			chromedp.Run(ctx, chromedp.Evaluate(`document.body.innerText`, &text)) == nil &&
			strings.HasPrefix(strings.TrimSpace(text), "{")
	}
	if load() {
		return text, nil
	}
	if chromedp.Run(ctx, chromedp.Navigate(wikiHome)) == nil &&
		waitFor(ctx, `/Рувики/i.test(document.title)`, 30*time.Second) && load() {
		return text, nil
	}
	var title string
	tctx, c2 := context.WithTimeout(tab, 5*time.Second)
	defer c2()
	chromedp.Run(tctx, chromedp.Title(&title))
	return "", fmt.Errorf("рувики недоступна (заголовок страницы: %q)", title)
}

type ruwikiResp struct {
	Query struct {
		Pages map[string]struct{ Title, Extract string } `json:"pages"`
	} `json:"query"`
}

func searchTerm(term string) (title, text string, err error) {
	term = strings.TrimSpace(term)
	if term == "" {
		return "", "", fmt.Errorf("пустой запрос")
	}
	q := url.Values{"action": {"query"}, "generator": {"search"}, "gsrsearch": {term}, "gsrlimit": {"1"},
		"prop": {"extracts"}, "exintro": {"1"}, "explaintext": {"1"}, "format": {"json"}}
	body, err := getJSON(wikiAPI + "?" + q.Encode())
	if err != nil {
		return "", "", err
	}
	var r ruwikiResp
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		return "", "", fmt.Errorf("разбор ответа рувики: %w", err)
	}
	for _, p := range r.Query.Pages {
		if t := strings.TrimSpace(blanks.ReplaceAllString(p.Extract, "\n\n")); t != "" {
			return p.Title, t, nil
		}
	}
	return "", "", fmt.Errorf("ничего не найдено по запросу %q", term)
}
