package main

// Управление Chrome для поиска терминов на Рувики: процесс запускается и останавливается
// самой программой, отдельно руками его поднимать не нужно.
//
// Chrome запускается ОБЫЧНЫМ os/exec, а не через chromedp.NewExecAllocator — тот добавляет
// флаг --enable-automation (из-за него navigator.webdriver = true и защита сайта режет
// запрос). chromedp здесь только подключается снаружи к уже работающему браузеру
// (chromedp.NewRemoteAllocator, см. terms.go).
//
// Если процесс бота упадёт, будет убит (kill -9) или уйдёт в OOM, ядро Linux само пришлёт
// Chrome SIGKILL (см. setPdeathsig в chrome_linux.go) — зависших процессов не останется.
// При обычном завершении Chrome останавливает Close().

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// chromeCandidates — известные имена бинарников на движке Chromium (CDP, который нужен
// chromedp, поддерживают только они). Firefox сюда не входит принципиально: у него другой
// протокол удалённого управления, chromedp с ним не работает.
var chromeCandidates = []string{
	"google-chrome-stable", "google-chrome",
	"chromium", "chromium-browser",
	"microsoft-edge-stable", "microsoft-edge",
	"brave-browser",
}

// findChromeBinary возвращает путь к браузеру на Chromium: сначала смотрит переменную
// окружения CHROME_BIN (если задана руками), иначе перебирает chromeCandidates в PATH.
func findChromeBinary() (string, error) {
	if bin := os.Getenv("CHROME_BIN"); bin != "" {
		if path, err := exec.LookPath(bin); err == nil {
			return path, nil
		}
		return "", fmt.Errorf("CHROME_BIN=%q не найден в PATH", bin)
	}
	for _, name := range chromeCandidates {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New(
		"не нашёл браузер на движке Chromium (Chrome, Chromium, Edge, Brave — подходит любой). " +
			"Установи один из них или укажи путь переменной окружения CHROME_BIN")
}

const (
	chromePort = "9222"
	chromeUA   = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/154.0.0.0 Safari/537.36"

	chromeStartTimeout = 10 * time.Second
)

func chromeProfileDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "ruwiki-chrome")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("каталог профиля chrome (%s): %w", dir, err)
	}
	return dir, nil
}

type chromeProc struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func startChrome() (*chromeProc, error) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	ready := make(chan error, 1)

	go func() {
		defer close(done)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		bin, err := findChromeBinary()
		if err != nil {
			ready <- err
			return
		}
		profileDir, err := chromeProfileDir()
		if err != nil {
			ready <- err
			return
		}
		cmd := exec.CommandContext(ctx, bin,
			"--headless=new",
			"--remote-debugging-port="+chromePort,
			"--user-data-dir="+profileDir,
			"--user-agent="+chromeUA,
			"--no-first-run",
			"--no-default-browser-check",
			"about:blank",
		)
		setPdeathsig(cmd)

		if err := cmd.Start(); err != nil {
			ready <- fmt.Errorf("запуск chrome: %w", err)
			return
		}
		ready <- waitChromePort(chromeStartTimeout)
		cmd.Wait() // держим и горутину, и ОС-поток живыми, пока жив Chrome
	}()

	if err := <-ready; err != nil {
		cancel()
		<-done
		return nil, err
	}
	return &chromeProc{cancel: cancel, done: done}, nil
}

// Close останавливает Chrome и ждёт завершения процесса.
func (c *chromeProc) Close() {
	c.cancel()
	<-c.done
}

func waitChromePort(timeout time.Duration) error {
	url := "http://127.0.0.1:" + chromePort + "/json/version"
	deadline := time.Now().Add(timeout)
	for {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("chrome не поднял порт %s за %s: %w", chromePort, timeout, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
