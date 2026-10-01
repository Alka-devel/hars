package ruwiki

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

var chromeCandidates = []string{
	"google-chrome-stable", "google-chrome",
	"chromium", "chromium-browser",
	"microsoft-edge-stable", "microsoft-edge",
	"brave-browser",
}

func chromeWellKnownPaths() []string {
	pf := os.Getenv("ProgramFiles")
	pf86 := os.Getenv("ProgramFiles(x86)")
	lad := os.Getenv("LocalAppData")
	return []string{
		filepath.Join(pf, "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(pf86, "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(lad, "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(pf86, "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(pf, "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(lad, "BraveSoftware", "Brave-Browser", "Application", "brave.exe"),
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
	}
}

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
	for _, path := range chromeWellKnownPaths() {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
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

type Browser struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func StartChrome() (*Browser, error) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	ready := make(chan error, 1)

	go func() {
		defer close(done)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		bin, err := findChromeBinary()
		if err != nil {
			fmt.Println("u kow")
			ready <- err
			return
		}
		profileDir, err := chromeProfileDir()
		if err != nil {
			ready <- err
			return
		}
		args := []string{
			"--remote-debugging-port=" + chromePort,
			"--user-data-dir=" + profileDir,
			"--user-agent=" + chromeUA,
			"--no-first-run",
			"--no-default-browser-check",
			"about:blank",
		}
		// CHROME_HEADLESS=0 — запустить с обычным окном, для разработки на машине с
		// графикой это проще и надёжнее (headless даёт о себе знать не только через UA).
		// На Linux-сервере без графики headless остаётся по умолчанию.
		if os.Getenv("CHROME_HEADLESS") != "0" {
			args = append([]string{"--headless=new"}, args...)
		}
		cmd := exec.CommandContext(ctx, bin, args...)
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
	return &Browser{cancel: cancel, done: done}, nil
}

// Close останавливает Chrome и ждёт завершения процесса.
func (b *Browser) Close() {
	b.cancel()
	<-b.done
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
