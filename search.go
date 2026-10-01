package ruwiki

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	cu "github.com/Davincible/chromedp-undetected"
	"github.com/chromedp/chromedp"
)

const (
	wikiHome = "https://ru.ruwiki.ru/"
	wikiAPI  = wikiHome + "w/api.php"
)

// chromeDebugURL указывает на уже запущенный Chrome (StartChrome, chrome.go) — порт
// общий для обоих файлов, задан константой chromePort, чтобы не разъезжался.
var chromeDebugURL = "http://127.0.0.1:" + chromePort

var blanks = regexp.MustCompile(`(\n[ \t]*){3,}`)

// waitFor ждёт истинности JS-выражения. Ошибки вроде «target navigated» (страница
// перезагрузилась посреди проверки Qrator) считаются временными — просто пробуем ещё раз.
func waitFor(ctx context.Context, expr string, d time.Duration) bool {
	for end := time.Now().Add(d); time.Now().Before(end) && ctx.Err() == nil; time.Sleep(300 * time.Millisecond) {
		var ok bool
		if chromedp.Run(ctx, chromedp.Evaluate("!!("+expr+")", &ok)) == nil && ok {
			return true
		}
	}
	return false
}

// getJSON открывает u НОВОЙ ВКЛАДКОЙ в уже работающем Chrome (NewRemoteAllocator,
// а не NewExecAllocator — второй запускал бы свой браузер на каждый вызов). Если Qrator
// не пускает сразу, заходит на главную, чтобы пройти проверку, и повторяет запрос.
func getJSON(u string) (string, error) {
	cfg := cu.NewConfig(cu.WithTimeout(90 * time.Second))
	// cfg.ChromeFlags = append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))
	ctx, cancel, err := cu.New(cfg)
	if err != nil {
		return "", fmt.Errorf("не удалось запустить undetected-браузер: %w", err)
	}
	defer func() {
		cancel()
	}()
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
	tctx, c2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer c2()
	chromedp.Run(tctx, chromedp.Title(&title))
	return "", fmt.Errorf("рувики недоступна (заголовок страницы: %q)", title)
}

type ruwikiResp struct {
	Query struct {
		Pages map[string]struct{ Title, Extract string } `json:"pages"`
	} `json:"query"`
}

// SearchTerm ищет термин на Рувики и возвращает заголовок найденной статьи и вводный
// абзац без разметки. Chrome должен быть уже запущен через StartChrome — каждый вызов
// лишь открывает новую вкладку в нём, а не новый браузер.
func SearchTerm(term string) (title, text string, err error) {
	term = strings.TrimSpace(term)
	if term == "" {
		return "", "", fmt.Errorf("пустой запрос")
	}
	q := url.Values{
		"action": {"query"}, "generator": {"search"},
		"gsrsearch": {term}, "gsrlimit": {"1"},
		"gsrnamespace": {"0"}, "redirects": {"1"},
		"prop": {"extracts"}, "exintro": {"1"}, "exsentences": {"3"}, "explaintext": {"1"}, "format": {"json"},
	}
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
