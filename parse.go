package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

func diagnose() {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", false), // видимое окно, UA не подменяем
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	ctx, cancelT := context.WithTimeout(ctx, 60*time.Second)
	defer cancelT()

	var title, href, ua, html string
	var cookies []*network.Cookie
	err := chromedp.Run(ctx,
		chromedp.Navigate(ruwikiHome),
		chromedp.Sleep(15*time.Second),
		chromedp.Title(&title),
		chromedp.Location(&href),
		chromedp.Evaluate(`navigator.userAgent`, &ua),
		chromedp.OuterHTML("html", &html, chromedp.ByQuery),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			cookies, err = network.GetCookies().WithURLs([]string{ruwikiHome}).Do(ctx)
			return err
		}),
	)
	fmt.Println("err:", err)
	fmt.Println("title:", title)
	fmt.Println("url:", href)
	fmt.Println("ua:", ua)
	for _, c := range cookies {
		fmt.Println("cookie:", c.Name)
	}
	if len(html) > 600 {
		html = html[:600]
	}
	fmt.Println("html:", html)
}

func main() {
	diagnose()
}

const ruwikiAPI = "https://ru.ruwiki.ru/w/api.php"

type ruwikiPage struct {
	PageID  int    `json:"pageid"`
	Title   string `json:"title"`
	Extract string `json:"extract"`
}

type ruwikiResponse struct {
	Query struct {
		Pages map[string]ruwikiPage `json:"pages"`
	} `json:"query"`
}

const (
	ruwikiHome = "https://ru.ruwiki.ru/"
	browserUA  = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
)

var (
	wikiJar, _ = cookiejar.New(nil)
	wikiClient = &http.Client{Jar: wikiJar, Timeout: 15 * time.Second} // редиректы Go следует сам
	warmMu     sync.Mutex
	warmed     bool
)

func setHeaders(req *http.Request, accept string) {
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")
}

// warmUp заходит на главную (с редиректами), чтобы jar получил куки
// refreshCookies открывает главную в headless Chrome, ждёт прохождения проверки Qrator
// и переносит все куки в wikiJar.
func refreshCookies() error {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		// тот же UA, что у Go-клиента: Qrator может привязывать куки к User-Agent
		chromedp.UserAgent(browserUA),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelAlloc()
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	defer cancelCtx()
	ctx, cancelT := context.WithTimeout(ctx, 45*time.Second)
	defer cancelT()

	var cookies []*network.Cookie
	err := chromedp.Run(ctx,
		chromedp.Navigate(ruwikiHome),
		// пока висит страница-испытание, в title нет «Рувики»; после перезагрузки появляется
		chromedp.Poll(`/Рувики/i.test(document.title)`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			cookies, err = network.GetCookies().WithURLs([]string{ruwikiHome}).Do(ctx)
			return err
		}),
	)
	if err != nil {
		return fmt.Errorf("chromedp: %w", err)
	}

	u, _ := url.Parse(ruwikiHome)
	hc := make([]*http.Cookie, 0, len(cookies))
	for _, c := range cookies {
		hc = append(hc, &http.Cookie{Name: c.Name, Value: c.Value, Path: c.Path, Domain: c.Domain, Secure: c.Secure})
		fmt.Println("cookie:", c.Name) // для отладки, потом убери
	}
	wikiJar.SetCookies(u, hc)
	return nil
}

func ensureWarm(force bool) error {
	warmMu.Lock()
	defer warmMu.Unlock()
	if warmed && !force {
		return nil
	}
	if err := refreshCookies(); err != nil {
		return err
	}
	warmed = true
	return nil
}

// apiGet: GET к api.php; при 403 один раз перепрогревает куки и повторяет
func apiGet(rawURL string) ([]byte, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if err := ensureWarm(attempt > 0); err != nil {
			return nil, err
		}
		req, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		setHeaders(req, "application/json")
		resp, err := wikiClient.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized) && attempt == 0 {
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("ruwiki: HTTP %s", resp.Status)
		}
		return body, nil
	}
	return nil, fmt.Errorf("ruwiki: 403 даже после прогрева")
}

func searchTerm(term string) (title, extract string, err error) {
	q := url.Values{}
	q.Set("action", "query")
	q.Set("generator", "search")
	q.Set("gsrsearch", term)
	q.Set("gsrlimit", "1")
	q.Set("prop", "extracts")
	q.Set("exintro", "1")
	q.Set("explaintext", "1")
	q.Set("format", "json")

	body, err := apiGet(ruwikiAPI + "?" + q.Encode())
	if err != nil {
		return "", "", err
	}

	var result ruwikiResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", "", fmt.Errorf("разбор ответа рувики: %w", err)
	}
	for _, page := range result.Query.Pages {
		if e := strings.TrimSpace(page.Extract); e != "" {
			return page.Title, e, nil
		}
	}
	return "", "", fmt.Errorf("ничего не найдено по запросу %q", term)
}
