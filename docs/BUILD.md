# Сборка, CI и релизы

## Состав и варианты

```
go.mod / pkg/... / cmd/oplata      Корневой (и единственный) модуль: ТОЛЬКО стандартная библиотека Go (go.sum не нужен!)
.github/workflows/ci.yml           Тесты + сборка oplata.exe при каждом push/PR (артефакты)
.github/workflows/release.yml      Релиз при пуше тега v*: oplata.exe + zip + SHA256SUMS.txt
tools/excel2seed.py                Генератор стартовых данных pkg/store/seed.json из Excel
```

> С 2026-10-21 по решению владельца собирается только `oplata.exe`: варианты
> `oplata-console.exe` и `oplata-desktop.exe` (Wails, модуль `desktop/`) удалены из
> репозитория (история — в git и в релизах v0.1.0–v0.2.0 на GitHub).

Версия зафиксирована: **Go 1.25** (`env.GO_VERSION` в workflow; корневой `go.mod` заявляет `go 1.23` —
минимум из-за паттернов `http.ServeMux` и пакета `slices`).
Экшены: `actions/checkout@v7`, `actions/setup-go@v7`, `upload-artifact@v7`, `download-artifact@v8`,
`softprops/action-gh-release@v3` (обновлять осознанно, проверяя changelog экшенов).

Ядро на чистом stdlib тестируется на Linux за секунды и кросс-компилируется в Windows exe отовсюду.
Общие пакеты лежат в `pkg/` (не `internal/`!) — исторически ради удалённого desktop-модуля; не переносить.

## Автоматическая сборка на GitHub Actions

- **CI** (`push`, `pull_request`, ручной запуск):
  `test` (ubuntu: gofmt-проверка `cmd`+`pkg`, `go vet`, `go test`, пробная кросс-компиляция,
  проверка экспорта Excel через openpyxl) → `build-windows` (windows-latest: `oplata.exe`
  с `-H windowsgui`, проверка PE-заголовка, артефакт **oplata-windows-amd64**).
- **Release** (`push` тега `v*`):
  ```sh
  git tag v0.1.0 && git push origin v0.1.0
  ```
  Собирает `oplata.exe` с версией = имя тега (вшивается `-ldflags -X main.version=…`),
  создаёт GitHub Release: `oplata.exe` + `oplata-<тег>-windows-amd64.zip` + `SHA256SUMS.txt`.
  Тег с дефисом (`v0.1.0-beta.1`) → pre-release.
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
# Тот же бинарь без -H windowsgui — с видимой консолью (удобно для диагностики):
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=dev" -o oplata-debug.exe ./cmd/oplata
```

Фронтенд не требует сборки/Node: правки в `pkg/webui/assets/*` подхватываются `//go:embed`
при пересборке Go (`go run` — сразу). Синтаксис JS удобно проверять `node --check app.js`.


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
| oplata.exe «ничего не делает» | Сборка windowsgui без консоли — смотреть `%AppData%\Oplata\oplata.log`; для живого вывода пересобрать без `-H windowsgui` (`oplata-debug.exe`, см. выше). |
| Открылся не тот/старый интерфейс | Порт 8765 занят предыдущей копией: программа уйдёт на 8766+ (URL — в логе). Закройте старые копии. |
| «Файл данных повреждён» | Восстановите `backups/data-*.json` (переименовать в `data.json` или через «Данные → Восстановить», если программа с пустой базой поднялась с `-data` на временный файл). |
| В десктопном варианте не скачивается экспорт | WebView2 должен показать загрузку; если нет — используйте браузерный вариант для экспорта (известное ограничение оболочки, данные общие). |
| SmartScreen ругается на exe | Бинарники без цифровой подписи: «Подробнее → Выполнить в любом случае». Подпись — в TODO. |
