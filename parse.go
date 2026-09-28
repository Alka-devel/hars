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
	"cmp"
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
	wikiHome = "https://ru.ruwiki.ru/"
	wikiAPI  = wikiHome + "w/api.php"
)

var (
	chromeURL = cmp.Or(os.Getenv("CHROME_DEBUG_URL"), "http://127.0.0.1:9222")
	lookupMu  sync.Mutex 
	termCache sync.Map   
	blanks    = regexp.MustCompile(`(\n[ \t]*){3,}`)
)

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
	lookupMu.Lock()
	defer lookupMu.Unlock()
	alloc, stop := chromedp.NewRemoteAllocator(context.Background(), chromeURL)
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
	key := strings.ToLower(strings.TrimSpace(term))
	if key == "" {
		return "", "", fmt.Errorf("пустой запрос")
	}
	if v, ok := termCache.Load(key); ok {
		r := v.([2]string)
		return r[0], r[1], nil
	}
	q := url.Values{"action": {"query"}, "generator": {"search"}, "gsrsearch": {key}, "gsrlimit": {"1"},
		"prop": {"extracts"}, "exintro": {"1"}, "explaintext": {"1"}, "format": {"json"}}
	body, err := getJSON(wikiAPI + "?" + q.Encode())
	if err != nil {
		return
	}
	var r ruwikiResp
	if err = json.Unmarshal([]byte(body), &r); err != nil {
		return
	}
	for _, p := range r.Query.Pages {
		if t := strings.TrimSpace(blanks.ReplaceAllString(p.Extract, "\n\n")); t != "" {
			termCache.Store(key, [2]string{p.Title, t})
			return p.Title, t, nil
		}
	}
	return "", "", fmt.Errorf("ничего не найдено по запросу %q", term)
}
