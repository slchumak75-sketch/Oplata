# Сборка, CI и релизы

## Состав и варианты

```
go.mod / pkg/... / cmd/oplata      Корневой (и единственный) модуль: ТОЛЬКО стандартная библиотека Go (go.sum не нужен!)
.github/workflows/ci.yml           Один быстрый Linux-job: релевантные проверки + кросс-сборка на PR/main
.github/workflows/release.yml      Один Linux-job при пуше тега v*: кросс-сборка + zip + SHA256SUMS.txt
tools/exportdemo + verify_export.py CI-проверка прямого импорта исходного Excel и последующего экспорта
```

> С 2026-10-21 по решению владельца собирается только `oplata.exe`: варианты
> `oplata-console.exe` и `oplata-desktop.exe` (Wails, модуль `desktop/`) удалены из
> репозитория (история — в git и в релизах v0.1.0–v0.2.0 на GitHub).

Версия зафиксирована: **Go 1.25** (`env.GO_VERSION` в workflow; корневой `go.mod` заявляет `go 1.23` —
минимум из-за паттернов `http.ServeMux` и пакета `slices`).
Экшены: `actions/checkout@v7`, `actions/setup-go@v7`, `upload-artifact@v7`,
`softprops/action-gh-release@v3` (обновлять осознанно, проверяя changelog экшенов).

Ядро на чистом stdlib тестируется на Linux за секунды и кросс-компилируется в Windows exe отовсюду.
Общие пакеты лежат в `pkg/` (не `internal/`!) — исторически ради удалённого desktop-модуля; не переносить.

## Автоматическая сборка на GitHub Actions

- **CI** (автоматически: `pull_request` и `push` только в `main`; для рабочей ветки без PR —
  ручной `workflow_dispatch`): чистые Markdown-изменения workflow не запускают. В остальных
  случаях работает один job на ubuntu: по списку изменённых файлов он запускает только
  релевантное — `gofmt` и `go test ./...` для Go/go.mod/workflow, `node --check` для
  `app.js`, openpyxl-сверку для Excel/модели/инструментов и кросс-компиляцию
  `oplata.exe` для Windows при изменениях кода или встроенных GUI-ассетов. Отдельный
  `go vet` не выполняется (`go test` включает встроенный поднабор vet). Устаревшие запуски
  того же PR/ref отменяются (`concurrency.cancel-in-progress`), теги CI не обслуживает.
- **Release** (`push` тега `v*`): один job на ubuntu кросс-компилирует `oplata.exe`,
  упаковывает ZIP/SHA256 и публикует GitHub Release. Тесты и нативная Windows-сборка
  не повторяются — тег разрешается ставить только на коммит `main` с уже зелёным CI.
  По постоянному указанию владельца публикация следующего релиза — обязательный
  финальный шаг каждой завершённой задачи, без отдельной просьбы.
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
go test ./...                          # все тесты
go run ./cmd/oplata -no-open           # сервер на http://127.0.0.1:8765
go run ./cmd/oplata -data ./dev.json   # отдельный файл данных для экспериментов

# Сборка Windows exe кросс-компиляцией с любой ОС
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H windowsgui -X main.version=dev" -o oplata.exe ./cmd/oplata
# Тот же бинарь без -H windowsgui — с видимой консолью (удобно для диагностики):
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=dev" -o oplata-debug.exe ./cmd/oplata
```

Фронтенд не требует сборки/Node: правки в `pkg/webui/assets/*` подхватываются `//go:embed`
при пересборке Go (`go run` — сразу). Синтаксис JS удобно проверять `node --check app.js`.


## Стартовые данные и проверка Excel без seed

Встроенных стартовых данных в `oplata.exe` **нет**: при первом запуске `store.Open`
создаёт пустую базу `model.NewEmptyDB()`. Исходный `Оплаты Строймаркет.xlsx` остаётся
в репозитории как эталон формата и при необходимости импортируется вручную через
вкладку «Данные». Не возвращать `pkg/store/seed.json`, `//go:embed` seed или генератор
seed без прямого решения владельца.

Ту же сверку импорта/экспорта, что CI выполняет при релевантных изменениях, можно запустить локально:

```sh
go run ./tools/exportdemo /tmp/oplata-2025.xlsx "Оплаты Строймаркет.xlsx"
python3 tools/verify_export.py /tmp/oplata-2025.xlsx
```

`exportdemo` напрямую разбирает оригинал Go-импортёром (`excelio.ParseXLSX`) и пишет
`ExportXLSX`; `verify_export.py` открывает результат через openpyxl и сверяет итоги с
`docs/EXCEL_FORMAT.md`.

## Диагностика проблем

| Симптом | Что делать |
|---|---|
| oplata.exe «ничего не делает» | Сборка windowsgui без консоли — смотреть `%AppData%\Oplata\oplata.log`; для живого вывода пересобрать без `-H windowsgui` (`oplata-debug.exe`, см. выше). |
| Открылся не тот/старый интерфейс | Порт 8765 занят предыдущей копией: программа уйдёт на 8766+ (URL — в логе). Закройте старые копии. |
| «Файл данных повреждён» | Восстановите `backups/data-*.json` (переименовать в `data.json` или через «Данные → Восстановить», если программа с пустой базой поднялась с `-data` на временный файл). |
| В десктопном варианте не скачивается экспорт | WebView2 должен показать загрузку; если нет — используйте браузерный вариант для экспорта (известное ограничение оболочки, данные общие). |
| SmartScreen ругается на exe | Бинарники без цифровой подписи: «Подробнее → Выполнить в любом случае». Подпись — в TODO. |
