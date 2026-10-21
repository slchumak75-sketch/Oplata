# Сборка, CI и релизы

## Состав и варианты

```
go.mod / pkg/... / cmd/oplata      Корневой модуль: ТОЛЬКО стандартная библиотека Go (go.sum не нужен!)
desktop/ (свой go.mod)             Wails-вариант; ядро подключает через replace => ../
.github/workflows/ci.yml           Тесты + сборка обоих вариантов при каждом push/PR (артефакты)
.github/workflows/release.yml      Релиз при пуше тега v*: все exe + zip + SHA256SUMS.txt
tools/excel2seed.py                Генератор стартовых данных pkg/store/seed.json из Excel
```

Версии зафиксированы: **Go 1.25** (`env.GO_VERSION` в workflow; корневой `go.mod` заявляет `go 1.23` —
минимум из-за паттернов `http.ServeMux` и пакета `slices`), **Wails v2.17.0** (`desktop/go.mod`).
Экшены: `actions/checkout@v7`, `actions/setup-go@v7`, `upload-artifact@v7`, `download-artifact@v8`,
`softprops/action-gh-release@v3` (обновлять осознанно, проверяя changelog экшенов).

Почему desktop — отдельный модуль: Wails тянет десятки зависимостей и собирается только под
GUI-платформы. Ядро на чистом stdlib тестируется на Linux за секунды и кросс-компилируется в
Windows exe отовсюду. Общие пакеты лежат в `pkg/` (не `internal/`!) именно чтобы desktop-модуль
мог их импортировать.

## Автоматическая сборка на GitHub Actions

- **CI** (`push`, `pull_request`, ручной запуск): 
  `test` (ubuntu: gofmt-проверка `cmd`+`pkg`, `go vet`, `go test`, пробная кросс-компиляция) →
  `build-windows` (windows-latest: `oplata.exe` с `-H windowsgui` и `oplata-console.exe`,
  дымовой тест `-version`, артефакт **oplata-windows-amd64**) и
  `build-desktop` (windows-latest: `go mod tidy` в `desktop/`, gofmt/vet, сборка,
  артефакт **oplata-desktop-amd64**). Джобы независимы: падение Wails не мешает браузерному варианту.
- **Release** (`push` тега `v*`):
  ```sh
  git tag v0.1.0 && git push origin v0.1.0
  ```
  Собирает всё с версией = имя тега (вшивается `-ldflags -X main.version=…`), создаёт
  GitHub Release: три exe + `oplata-<тег>-windows-amd64.zip` + `SHA256SUMS.txt`.
  Тег с дефисом (`v0.1.0-beta.1`) → pre-release. Если desktop-джоба упала, релиз выйдет
  без `oplata-desktop.exe` (это отражено в шаблоне описания).
- Версия в CI-артефактах — `git describe --tags --always` (нужен `fetch-depth: 0` у checkout).

Скачивание: Actions → запуск → блок **Artifacts** внизу страницы (хранятся 90 дней),
либо Releases.

## Локальная разработка (нужен Go 1.23+, для desktop — 1.25)

```sh
# Ядро + браузерный вариант (Linux/macOS/Windows)
go test ./...                          # все тесты (включая сверку seed с Excel!)
go run ./cmd/oplata -no-open           # сервер на http://127.0.0.1:8765
go run ./cmd/oplata -data ./dev.json   # отдельный файл данных для экспериментов

# Сборка Windows exe кросс-компиляцией с любой ОС
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H windowsgui -X main.version=dev" -o oplata.exe ./cmd/oplata
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=dev" -o oplata-console.exe ./cmd/oplata

# Desktop (Wails) — ТОЛЬКО на Windows с WebView2 SDK в рантайме
cd desktop && go mod tidy
go build -trimpath -ldflags "-s -w -H windowsgui -X main.version=dev" -o oplata-desktop.exe .
# Альтернатива через Wails CLI (даёт иконку/манифест): wails build  (конфиг desktop/wails.json)
```

Фронтенд не требует сборки/Node: правки в `pkg/webui/assets/*` подхватываются `//go:embed`
при пересборке Go (`go run` — сразу). Синтаксис JS удобно проверять `node --check app.js`.

`desktop/go.sum` в репозитории может отсутствовать (проект начинался без локального Go —
см. AGENTS.md): CI делает `go mod tidy` перед сборкой. Если у вас есть Go — выполните
`cd desktop && go mod tidy` и закоммитьте `go.mod`/`go.sum`.

## Перегенерация стартовых данных (seed)

```sh
pip install openpyxl
python3 tools/excel2seed.py "Оплаты Строймаркет.xlsx" pkg/store/seed.json
go test ./pkg/store/    # сверит итоги; при другом файле обновить цифры в тесте и docs/EXCEL_FORMAT.md
```

Сборка **без** встроенных реальных данных (например, для публичного распространения):
заменить `pkg/store/seed.json` на пустую базу
`{"version":2,"settings":{"company_name":"","currency_label":"руб.","warn_days":3},"suppliers":[],"obligations":[],"payments":[],"buyers":[],"warnings":[]}`
и обновить/отключить `TestSeedReconciliation`.

## Диагностика проблем

| Симптом | Что делать |
|---|---|
| oplata.exe «ничего не делает» | Сборка windowsgui без консоли — смотреть `%AppData%\Oplata\oplata.log`; запустить `oplata-console.exe` для живого вывода. |
| Открылся не тот/старый интерфейс | Порт 8765 занят предыдущей копией: программа уйдёт на 8766+ (URL — в логе). Закройте старые копии. |
| «Файл данных повреждён» | Восстановите `backups/data-*.json` (переименовать в `data.json` или через «Данные → Восстановить», если программа с пустой базой поднялась с `-data` на временный файл). |
| oplata-desktop.exe не стартует | Нужен WebView2 Runtime: в Windows 11 встроен; в Windows 10 поставить «Microsoft Edge WebView2 Runtime» с сайта Microsoft. Детали — в том же `oplata.log`. |
| Сборка desktop в CI: нет gcc/cgo | На windows-latest MinGW предустановлен и Wails собирается нативно. Если образ изменится — добавить `msys2/setup-msys2` или собирать `wails build` через официальный экшен Wails. |
| В десктопном варианте не скачивается экспорт | WebView2 должен показать загрузку; если нет — используйте браузерный вариант для экспорта (известное ограничение оболочки, данные общие). |
| SmartScreen ругается на exe | Бинарники без цифровой подписи: «Подробнее → Выполнить в любом случае». Подпись — в TODO. |
