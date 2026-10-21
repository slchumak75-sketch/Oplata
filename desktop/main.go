// Команда oplata-desktop — десктопный вариант приложения «Оплаты»:
// настоящее окно Windows на Wails v2 (WebView2) с тем же HTML GUI и тем же API,
// что и браузерный вариант (cmd/oplata). Сеть не используется вовсе:
// запросы GUI к /api/* обслуживает встроенный обработчик pkg/server.
//
// Отдельный Go-модуль (desktop/go.mod), чтобы зависимости Wails не попадали
// в корневой модуль: ядро остаётся на чистой стандартной библиотеке и
// тестируется на Linux. Сборка — только на Windows (GitHub Actions windows-latest),
// см. docs/BUILD.md. Требует WebView2 Runtime (в Windows 11 предустановлен).
//
// Файл данных тот же, что у браузерного варианта:
//   - если рядом с exe лежит oplata-data.json — используется он (портативный режим);
//   - иначе %AppData%\Oplata\data.json (переопределяется переменной OPLATA_DATA).
package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"github.com/slchumak75-sketch/oplata/pkg/server"
	"github.com/slchumak75-sketch/oplata/pkg/store"
	"github.com/slchumak75-sketch/oplata/pkg/webui"
)

// version вшивается при сборке: -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	dataPath, err := resolveDataPath()
	if err != nil {
		fatalLog(err)
		return
	}
	logger, closeLog := makeLogger(dataPath)
	defer closeLog()
	logger.Printf("oplata-desktop %s: запуск, файл данных %s", version, dataPath)

	st, err := store.Open(dataPath, true)
	if err != nil {
		logger.Printf("ОШИБКА: %v", err)
		fatalLog(fmt.Errorf("не удалось открыть файл данных %s: %w (подробности — в oplata.log рядом с данными)", dataPath, err))
		return
	}
	srv := server.New(st, version, logger)

	err = wails.Run(&options.App{
		Title:     "Оплаты — Строймаркет",
		Width:     1280,
		Height:    820,
		MinWidth:  900,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets:  webui.FS(),
			Handler: srv.Handler(),
		},
		BackgroundColour: &options.RGBA{R: 243, G: 245, B: 249, A: 1},
		Windows:          &windows.Options{},
	})
	if err != nil {
		logger.Printf("ОШИБКА Wails: %v", err)
		fatalLog(err)
	}
}

func resolveDataPath() (string, error) {
	if p := os.Getenv("OPLATA_DATA"); p != "" {
		return p, nil
	}
	if portable, err := store.DefaultDataPath(true); err == nil {
		if _, err := os.Stat(portable); err == nil {
			return portable, nil
		}
	}
	return store.DefaultDataPath(false)
}

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

func fatalLog(err error) {
	fmt.Fprintf(os.Stderr, "ОШИБКА: %v\n", err)
	os.Exit(1)
}
