// parse.go — поиск термина на Рувики (ru.ruwiki.ru) через MediaWiki API.
//
// Сайт закрыт проверкой Qrator: главная отвечает 401 и подгружает /__qrator/qauth.js,
// скрипт делает POST /__qrator/validate, после чего приходит кука qrator_jsid2 (живёт ~17 мин).
// Прямой запрос к api.php без этих кук получает 403 БЕЗ какой-либо проверки, поэтому
// сначала нужно зайти на главную, и только потом ходить в API.
//
// Проверку проходит только настоящий браузер, поэтому запросы идут не из Go напрямую,
// а через вкладки ОБЫЧНОГО Chrome, запущенного отдельно с портом удалённой отладки:
//
//	chrome --remote-debugging-port=9222 --user-data-dir=<отдельная папка> --no-proxy-server
//
// (на сервере без графики — под Xvfb). Алгоритм браузерного запроса:
//  1. открыть api.php; если пришёл JSON — готово (сессия в профиле браузера ещё жива);
//  2. иначе открыть главную и дождаться, пока браузер пройдёт проверку;
//  3. снова открыть api.php.
//
// Зависимость:  go get github.com/chromedp/chromedp
//
// Проверка:     go run parse.go капитуляция
//
// Адрес отладочного порта можно поменять переменной окружения CHROME_DEBUG_URL.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

const (
	ruwikiHome = "https://ru.ruwiki.ru/"
	ruwikiAPI  = "https://ru.ruwiki.ru/w/api.php"

	maxTabs    = 2                // не больше стольких вкладок одновременно
	navTimeout = 90 * time.Second // общий лимит на один запрос (вместе с заходом на главную)
	apiPoll    = 15 * time.Second // сколько ждать ответ api.php
	homePoll   = 30 * time.Second // сколько ждать прохождения проверки Qrator на главной
	cacheTTL   = 24 * time.Hour

	// условия для chromedp.Poll (JS-выражения)
	jsonReady  = `document.body && document.body.innerText.trim().startsWith('{')`
	failedPage = `/^HTTP [45][0-9][0-9]/.test(document.title)`
	homeReady  = `/Рувики/i.test(document.title)`
)

var (
	chromeURL = envOr("CHROME_DEBUG_URL", "http://127.0.0.1:9222")
	tabSem    = make(chan struct{}, maxTabs)

	cacheMu sync.Mutex
	cache   = map[string]cachedTerm{}
)

type cachedTerm struct {
	title   string
	extract string
	at      time.Time
}

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

var blankLines = regexp.MustCompile(`(\n[ \t]*){3,}`)

// tidyExtract схлопывает длинные серии пустых строк, которые оставляет MediaWiki.
func tidyExtract(s string) string {
	return strings.TrimSpace(blankLines.ReplaceAllString(s, "\n\n"))
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// waitFor ждёт, пока JS-выражение expr станет истинным.
// Ошибки вроде «Inspected target navigated or closed» (страница перезагрузилась
// посреди проверки Qrator) считаются временными: просто пробуем ещё раз.
func waitFor(ctx context.Context, expr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		var ok bool
		if err := chromedp.Run(ctx, chromedp.Evaluate("!!("+expr+")", &ok)); err == nil && ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("не дождались условия %q за %s", expr, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// loadJSON открывает rawURL и ждёт либо JSON, либо страницу с ошибкой (HTTP 4xx/5xx).
// ok=true, если в ответе JSON.
func loadJSON(ctx context.Context, rawURL string) (string, bool, error) {
	if err := chromedp.Run(ctx, chromedp.Navigate(rawURL)); err != nil {
		return "", false, err
	}
	if err := waitFor(ctx, "("+jsonReady+") || ("+failedPage+")", apiPoll); err != nil {
		return "", false, err
	}
	var text string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.body.innerText`, &text)); err != nil {
		return "", false, err
	}
	return text, strings.HasPrefix(strings.TrimSpace(text), "{"), nil
}

// browserGet получает JSON по rawURL через вкладку запущенного Chrome.
func browserGet(rawURL string) ([]byte, error) {
	tabSem <- struct{}{}
	defer func() { <-tabSem }()

	allocCtx, cancelAlloc := chromedp.NewRemoteAllocator(context.Background(), chromeURL)
	defer cancelAlloc()

	// новая вкладка в браузере; закроется при cancelTab
	tabCtx, cancelTab := chromedp.NewContext(allocCtx)
	defer cancelTab()

	runCtx, cancelRun := context.WithTimeout(tabCtx, navTimeout)
	defer cancelRun()

	// fail добавляет к ошибке заголовок страницы (например, «HTTP 403») для диагностики
	fail := func(step string, err error) ([]byte, error) {
		var pageTitle string
		dctx, dcancel := context.WithTimeout(tabCtx, 5*time.Second)
		defer dcancel()
		_ = chromedp.Run(dctx, chromedp.Title(&pageTitle))
		return nil, fmt.Errorf("browserGet (%s): %w (заголовок страницы: %q)", step, err, pageTitle)
	}

	// 1. сессия Qrator уже есть в профиле браузера — API ответит сразу
	text, ok, err := loadJSON(runCtx, rawURL)
	if err != nil {
		return fail("запрос api", err)
	}
	if ok {
		return []byte(text), nil
	}

	// 2. сессии нет или она протухла: заходим на главную, браузер проходит проверку Qrator
	if err = chromedp.Run(runCtx, chromedp.Navigate(ruwikiHome)); err != nil {
		return fail("открытие главной", err)
	}
	if err = waitFor(runCtx, homeReady, homePoll); err != nil {
		return fail("проход проверки на главной", err)
	}

	// 3. повторяем запрос к API
	text, ok, err = loadJSON(runCtx, rawURL)
	if err != nil {
		return fail("повторный запрос api", err)
	}
	if !ok {
		return fail("повторный запрос api", fmt.Errorf("ответ не JSON"))
	}
	return []byte(text), nil
}

// searchTerm ищет термин одним запросом (generator=search + extracts) и возвращает
// заголовок найденной статьи и вводный абзац без вики-разметки.
func searchTerm(term string) (title, extract string, err error) {
	term = strings.TrimSpace(term)
	if term == "" {
		return "", "", fmt.Errorf("пустой запрос")
	}
	key := strings.ToLower(term)

	cacheMu.Lock()
	if c, ok := cache[key]; ok && time.Since(c.at) < cacheTTL {
		cacheMu.Unlock()
		return c.title, c.extract, nil
	}
	cacheMu.Unlock()

	q := url.Values{}
	q.Set("action", "query")
	q.Set("generator", "search")
	q.Set("gsrsearch", term)
	q.Set("gsrlimit", "1")
	q.Set("prop", "extracts")
	q.Set("exintro", "1")
	q.Set("explaintext", "1")
	q.Set("format", "json")

	body, err := browserGet(ruwikiAPI + "?" + q.Encode())
	if err != nil {
		return "", "", err
	}

	var result ruwikiResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", "", fmt.Errorf("разбор ответа рувики: %w", err)
	}
	for _, p := range result.Query.Pages {
		e := tidyExtract(p.Extract)
		if e == "" {
			continue
		}
		cacheMu.Lock()
		cache[key] = cachedTerm{title: p.Title, extract: e, at: time.Now()}
		cacheMu.Unlock()
		return p.Title, e, nil
	}
	return "", "", fmt.Errorf("ничего не найдено по запросу %q", term)
}

func main() {
	term := "капитуляция"
	if len(os.Args) > 1 {
		term = strings.Join(os.Args[1:], " ")
	}
	title, extract, err := searchTerm(term)
	if err != nil {
		fmt.Println("ОШИБКА:", err)
		os.Exit(1)
	}
	fmt.Println(title)
	fmt.Println()
	fmt.Println(extract)
}
