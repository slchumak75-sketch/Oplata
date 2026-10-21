# Модель данных, расчёты и HTTP API

## Файл данных (JSON, `pkg/model`, `pkg/store`)

Файл по умолчанию: `%AppData%\Oplata\data.json` (переопределение: флаг `-data`, переменная
`OPLATA_DATA`, флаг `-portable` → `oplata-data.json` рядом с exe). Сохранение атомарное
(tmp + rename); перед каждой перезаписью предыдущая версия копируется в `backups/`
рядом с файлом (хранятся последние 20). Файл можно смотреть и править вручную (JSON с отступами),
но структура обязана пройти `model.DB.Validate()` при загрузке.

Инварианты (нарушать нельзя — на них завязаны тесты и GUI):

- **Деньги — только целые копейки `int64`** (`*_cents`) везде: файл, API, расчёты. Никаких float.
- **Даты — строки `гггг-мм-дд`** (`model.Date`). Сравнение строк = сравнение дат.
- `Obligation.amount_cents > 0`. `Payment.amount_cents != 0`; отрицательная выплата = сторно.

```json
{
  "version": 2,
  "settings": {"company_name": "Строймаркет", "currency_label": "руб.", "warn_days": 3, "seeded_from": "…"},
  "suppliers":   [{"id": "sup-01…", "name": "АВИДО", "unp": "123456789", "deferral_days": 14, "note": "", "archived": false}],
  "obligations": [{"id": "obl-…", "supplier_id": "sup-01…", "due_date": "2025-01-15",
                   "amount_cents": 101349, "delivery_date": "2025-01-01", "doc_number": "ТН-12", "comment": ""}],
  "payments":    [{"id": "pay-…", "supplier_id": "sup-01…", "date": "2025-01-15",
                   "amount_cents": 101349, "doc_number": "ПП-7", "comment": ""}],
  "buyers":      [{"id": "buy-…", "name": "СтройДом ООО", "unp": "987654321", "note": ""}],
  "warnings": ["… предупреждения последнего импорта …"]
}
```

Поля:

- `Supplier.unp` — УНП поставщика (схема v2, необязательно; в Excel отсутствует).
  Нормализация при сохранении через API: без пробелов, верхний регистр (`model.NormalizeUNP`);
  проверка: 9–15 букв/цифр, без дублей среди поставщиков (`model.ValidateUNP`).
- `Supplier.deferral_days` — отсрочка по умолчанию (дней); 0 = не задана. Используется GUI и API
  для предложения `due_date = delivery_date + отсрочка`; хранится всегда явный `due_date`.
- `Buyer` — покупатель из справочника «Карточка покупателя»: `name`, `unp` (правила как выше,
  без дублей среди покупателей), `note`. С расчётами не связан, в Excel не выгружается,
  хранится только в JSON (и попадает в резервные копии). Импорт Excel покупателей не трогает;
  режим `replace` дополнительно переносит УНП поставщиков по наименованию.
- `Obligation.due_date` — **срок оплаты с учётом отсрочки** (дата колонки Excel). `delivery_date` необязательна.
- `Obligation` = колонка «Собств», `Payment` = колонка «Выплаты» (в Excel выплаты с минусом — инверсия только в `excelio`).
- `settings.warn_days` — порог статуса «скоро срок». `seeded_from` — пометка стартовых данных.
- `warnings` — показываются баннером в GUI, пока пользователь не скроет (`PUT /api/settings {clear_warnings:true}`).
- `version` (= `model.SchemaVersion`, сейчас **2**). v1→v2: добавлены `Supplier.unp` и
  `buyers`; миграция — в `store.Decode` (старый JSON просто получает пустые значения).
  Менять формат → увеличить версию, добавить миграцию в `store.Decode`/рядом, запись в CHANGELOG и здесь.

IDs: `sup-/obl-/pay-/buy-` + 12 hex-символов (`crypto/rand`); в seed — детерминированные `sup-001…`,
`obl-000001…` (порядок Excel). Порядок срезов в файле = порядок отображения списков.

**Seed:** `pkg/store/seed.json` встроен в exe (`//go:embed`) и используется только при первом
запуске (нет файла данных) и без `-no-seed`. Генерируется из Excel: `tools/excel2seed.py`
(см. BUILD.md). Сверка итогов seed — тест `store.TestSeedReconciliation`.

## Расчёты (`pkg/calc`, чистые функции)

### FIFO-разнесение выплат — `calc.Allocate(obligations, payments, asOf)`

1. Начисления сортируются по `(due_date, id)`.
2. Сумма всех выплат поставщика («пул») последовательно гасит начисления от самого раннего срока.
   Дата выплаты очередность **не** меняет: предоплата зачтётся в ближайшее по сроку начисление.
3. Остаток пула после всех начислений — **переплата**. Отрицательные выплаты уменьшают пул
   (пул ниже нуля не гасит ничего).
4. Статусы начисления на дату `asOf`:
   - `paid` — открытый остаток 0;
   - `overdue` — остаток > 0 и `due_date < asOf` (+`days_overdue`); в сам день срока — ещё не просрочено;
   - `partial` — остаток > 0, что-то оплачено, срок не наступил;
   - `pending` — остаток = сумме, срок не наступил;
   - `due_soon` — поверх `partial/pending` ставится `MarkDueSoon`, если `due_date ∈ [asOf, asOf+warn_days]`.

### Производные показатели

- **Задолженность** поставщика = `Σ обязательств − Σ выплат` за всё время (столбец B в Excel).
  Может быть отрицательной (переплата).
- **Просрочено** на дату = Σ открытых остатков FIFO со статусом `overdue`.
- **Сводка** (`BuildDashboard`): сумма положительных балансов = «Всего задолженность»;
  «срок сегодня» — открытые с `due_date = asOf`; «7 дней» — `(asOf, asOf+7]`;
  обороты месяца считаются по месяцу `asOf` (начисления по `due_date`, выплаты по `date`);
  списки просроченных (по убыванию дней просрочки) и ближайших сроков (14 дней).
- **Матрица** (`BuildMatrixRange(from, to)`; `BuildMatrix(year, month)` — совместимая
  обёртка): суммы по (поставщик, дата) за диапазон; каждый день несёт календарь РБ
  (`weekday` 1=пн…7=вс, `is_day_off` с учётом переносов, `holiday_name` — из
  `pkg/calc/holidays.go`); `range_balance_cents` — сумма колонки B за диапазон;
  `balance_cents` — задолженность за всё время (подсказка в GUI).
- **Карточка** (`BuildSupplierCard`): FIFO-состояния, выплаты по дате, нарастающий баланс
  по хронологии событий (начисление датой `due_date`, выплата датой `date`).

## HTTP API (`pkg/server`)

База: `http://127.0.0.1:<порт>`. Ответы — JSON
(`{"error": "…"}` при ошибке, тексты на русском — они показываются пользователю).
Суммы — `*_cents`, даты — `гггг-мм-дд`.

**Защита локального API:** проверяются `Host` (localhost/127.0.0.1/[::1]/wails.localhost, если не
LAN-режим), `Origin == Host` и — для всех не-GET `/api/*` — заголовок **`X-Oplata-Token`**.
Токен случайный на каждый запуск; фронтенд получает его из `GET /api/bootstrap`
(чужие сайты прочитать не могут из-за Same-Origin). Авторизации пользователей нет и не планируется для локального режима.

| Метод и путь | Описание |
|---|---|
| `GET /api/health` | `{status, version}` |
| `GET /api/bootstrap` | `{version, today, token, settings, warnings, data_file, counts}` — старт GUI |
| `PUT /api/settings` | Частично: `{company_name?, currency_label?, warn_days?, clear_warnings?}` → bootstrap |
| `GET /api/suppliers?date=&include_archived=1` | Поставщики со сводками (`calc.SupplierSummary[]`) на дату (по умолчанию сегодня) |
| `POST /api/suppliers` · `PUT/DELETE /api/suppliers/{id}` | CRUD. Удаление при наличии операций → 400 (предлагается архив) |
| `GET /api/suppliers/{id}/card?date=` | Карточка: FIFO-обязательства, выплаты, график баланса |
| `GET /api/buyers?q=` · `POST /api/buyers` · `PUT/DELETE /api/buyers/{id}` | Справочник покупателей (`name`, `unp`, `note`). Удаление без ограничений (операций нет) |
| `GET /api/operations?supplier_id=&kind=all\|obligation\|payment&from=&to=&q=&limit=500` | Единый журнал, новые сверху, `{total, operations[]}` |
| `POST /api/obligations` · `PUT/DELETE /api/obligations/{id}` | Начисление. Если `due_date` пуст, но есть `delivery_date` — срок = поставка + `deferral_days` (или отсрочка поставщика) |
| `POST /api/payments` · `PUT/DELETE /api/payments/{id}` | Выплата |
| `GET /api/dashboard?date=` | Сводка (`calc.Dashboard`) |
| `GET /api/matrix?from=&to=` | «Таблица» за диапазон дат (`calc.Matrix`): дни с календарём РБ (`is_day_off`, `holiday_name`…), максимум 371 день. Старый режим `?year=&month=` тоже работает (диапазон = месяц) |
| `POST /api/import?mode=replace\|merge&year=` (multipart `file`) | Импорт XLSX → `excelio.Report`. `replace` сохраняет текущие настройки; `merge` сопоставляет поставщиков по нормализованному имени |
| `GET /api/export.xlsx?year=` | Скачать книгу формата оригинала (+ лист «Операции») |
| `GET /api/export.csv` | Операции CSV (`;`, BOM, суммы с запятой) для Excel RU |
| `GET /api/backup.json` | Скачать текущую базу JSON |
| `POST /api/restore` (multipart `file`) | Восстановить базу из JSON (полная замена) |
| `POST /api/reset` `{confirm:"RESET"}` | Пустая база (настройки сохраняются) |
| `GET /` и статика | Встроенный GUI (`pkg/webui/assets`); неизвестный путь → `index.html` |

Фронтенд (`app.js`) — единственная страница, hash-роутинг:
`#/dashboard`, `#/matrix`, `#/suppliers`, `#/supplier/<id>`, `#/buyers`, `#/operations`, `#/data`.
В синей шапке — кнопки «Карточка поставщика» и «Карточка покупателя» (модальные карточки
справочников: наименование, УНП, у поставщика — отсрочка; добавить/удалить/сохранить).
Никаких внешних запросов (полностью офлайн): шрифты системные, библиотек нет.
