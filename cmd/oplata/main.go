// Команда oplata — браузерный вариант приложения «Оплаты»:
// локальный HTTP-сервер (127.0.0.1) + HTML GUI в окне Edge (--app) или браузера по умолчанию.
//
// Сборка для Windows (см. docs/BUILD.md):
//
//	GOOS=windows GOARCH=amd64 go build -ldflags "-H windowsgui -X main.version=v1.0.0" -o oplata.exe ./cmd/oplata
//
// Флаги:
//
//	-addr      адрес сервера (по умолчанию 127.0.0.1:8765; занятый порт -> пробуются следующие)
//	-data      путь к файлу данных JSON (по умолчанию %AppData%\Oplata\data.json или OPLATA_DATA)
//	-portable  данные рядом с exe (oplata-data.json)
//	-no-open   не открывать браузер автоматически
//	-no-seed   при первом запуске не загружать стартовые данные из Excel 2025
//	-version   напечатать версию и выйти
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/slchumak75-sketch/oplata/pkg/server"
	"github.com/slchumak75-sketch/oplata/pkg/store"
)

// version вшивается при сборке: -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	addrFlag := flag.String("addr", "127.0.0.1:8765", "адрес HTTP-сервера")
	dataFlag := flag.String("data", "", "путь к файлу данных JSON")
	portable := flag.Bool("portable", false, "хранить данные рядом с исполняемым файлом")
	noOpen := flag.Bool("no-open", false, "не открывать браузер")
	noSeed := flag.Bool("no-seed", false, "не загружать стартовые данные при первом запуске")
	showVersion := flag.Bool("version", false, "показать версию и выйти")
	flag.Parse()

	if *showVersion {
		fmt.Println("oplata", version)
		return
	}

	dataPath := *dataFlag
	if dataPath == "" {
		dataPath = os.Getenv("OPLATA_DATA")
	}
	if dataPath == "" {
		var err error
		dataPath, err = store.DefaultDataPath(*portable)
		if err != nil {
			fatal("не удалось определить папку данных", err)
		}
	}

	logger, closeLog := makeLogger(dataPath)
	defer closeLog()
	logger.Printf("oplata %s: запуск, файл данных %s", version, dataPath)

	st, err := store.Open(dataPath, !*noSeed)
	if err != nil {
		logger.Printf("ОШИБКА: %v", err)
		fatal("не удалось открыть файл данных", err)
	}

	listener, url, err := listenWithFallback(*addrFlag)
	if err != nil {
		logger.Printf("ОШИБКА: %v", err)
		fatal("не удалось запустить сервер", err)
	}
	srv := server.New(st, version, logger)
	if host, _, herr := net.SplitHostPort(*addrFlag); herr == nil && host != "" && host != "127.0.0.1" && host != "localhost" && host != "::1" {
		srv.AllowAnyHost = true
		logger.Printf("ВНИМАНИЕ: LAN-режим (%s) — приложение без пароля доступно по сети!", *addrFlag)
	}
	httpServer := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	logger.Printf("интерфейс: %s (Ctrl+C для остановки)", url)
	fmt.Printf("Оплаты %s\nИнтерфейс: %s\nФайл данных: %s\n", version, url, dataPath)
	if !*noOpen {
		go func() {
			time.Sleep(300 * time.Millisecond)
			if err := openBrowser(url); err != nil {
				logger.Printf("не удалось открыть браузер: %v (откройте %s вручную)", err, url)
			}
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		logger.Print("остановка…")
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutCtx)
	}()

	if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Printf("ОШИБКА сервера: %v", err)
		fatal("ошибка HTTP-сервера", err)
	}
	logger.Print("остановлено")
}

// listenWithFallback слушает addr; если порт занят — следующие 20 портов, затем случайный.
func listenWithFallback(addr string) (net.Listener, string, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, "", err
	}
	basePort, _ := strconv.Atoi(portStr)
	for delta := 0; delta <= 20; delta++ {
		port := basePort + delta
		if delta == 20 {
			port = 0 // случайный свободный
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)))
		if err == nil {
			actual := ln.Addr().(*net.TCPAddr)
			displayHost := host
			if displayHost == "" || displayHost == "0.0.0.0" || displayHost == "::" {
				displayHost = "127.0.0.1"
			}
			return ln, fmt.Sprintf("http://%s:%d/", displayHost, actual.Port), nil
		}
	}
	return nil, "", fmt.Errorf("не удалось занять порт %s и соседние", addr)
}

// makeLogger пишет одновременно в stderr и в файл oplata.log рядом с данными.
// Это важно для windowsgui-сборки, где консоли нет и ошибки иначе не увидеть.
func makeLogger(dataPath string) (*log.Logger, func()) {
	dir := filepath.Dir(dataPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return log.New(os.Stderr, "", log.LstdFlags), func() {}
	}
	f, err := os.OpenFile(filepath.Join(dir, "oplata.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return log.New(os.Stderr, "", log.LstdFlags), func() {}
	}
	return log.New(io.MultiWriter(os.Stderr, f), "", log.LstdFlags), func() { _ = f.Close() }
}

// fatal печатает ошибку и завершает процесс. В windowsgui-сборке stderr не виден —
// та же ошибка уже записана в oplata.log логгером вызывающей стороны.
func fatal(msg string, err error) {
	fmt.Fprintf(os.Stderr, "ОШИБКА: %s: %v\n", msg, err)
	os.Exit(1)
}

// openBrowser открывает URL: Windows — Edge в режиме приложения (отдельное окно без вкладок),
// при неудаче — браузер по умолчанию; macOS — open; Linux — xdg-open.
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		if edge := findEdge(); edge != "" {
			if err := exec.Command(edge, "--app="+url).Start(); err == nil {
				return nil
			}
		}
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

func findEdge() string {
	candidates := []string{}
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
		if dir := os.Getenv(env); dir != "" {
			candidates = append(candidates, filepath.Join(dir, "Microsoft", "Edge", "Application", "msedge.exe"))
		}
	}
	for _, c := range candidates {
		if strings.TrimSpace(c) != "" {
			if _, err := os.Stat(c); err == nil {
				return c
			}
		}
	}
	if path, err := exec.LookPath("msedge.exe"); err == nil {
		return path
	}
	return ""
}
