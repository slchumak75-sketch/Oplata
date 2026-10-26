package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slchumak75-sketch/oplata/pkg/excelio"
	"github.com/slchumak75-sketch/oplata/pkg/model"
	"github.com/slchumak75-sketch/oplata/pkg/store"
)

type testEnv struct {
	handler http.Handler
	token   string
}

func newTestEnv(t *testing.T) testEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = st.Update(func(db *model.DB) error {
		db.Suppliers = append(db.Suppliers, model.Supplier{ID: "s1", Name: "АВИДО", DeferralDays: 14})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(st, "test", log.New(io.Discard, "", 0))
	srv.AllowAnyHost = true // httptest использует Host=example.com
	return testEnv{handler: srv.Handler(), token: srv.Token()}
}

func (e testEnv) req(t *testing.T, method, path, body string, withToken bool) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if withToken {
		req.Header.Set("X-Oplata-Token", e.token)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 && rec.Code != http.StatusNoContent {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out
}

func TestTokenGuardAndCRUD(t *testing.T) {
	env := newTestEnv(t)
	// Без токена изменяющий запрос запрещён.
	if code, _ := env.req(t, "POST", "/api/obligations", `{"supplier_id":"s1","due_date":"2025-01-10","amount_cents":10000}`, false); code != http.StatusForbidden {
		t.Fatalf("без токена: код %d", code)
	}
	// Создание начисления: срок считается из поставки + отсрочка поставщика (14 дней).
	code, out := env.req(t, "POST", "/api/obligations", `{"supplier_id":"s1","delivery_date":"2025-01-01","amount_cents":10000}`, true)
	if code != http.StatusCreated || out["due_date"] != "2025-01-15" {
		t.Fatalf("создание начисления: код=%d ответ=%v", code, out)
	}
	oblID, _ := out["id"].(string)
	// Выплата частично.
	if code, _ := env.req(t, "POST", "/api/payments", `{"supplier_id":"s1","date":"2025-01-20","amount_cents":4000}`, true); code != http.StatusCreated {
		t.Fatalf("выплата: код %d", code)
	}
	// Сводка на 01.02.2025: долг 60,00, просрочено 60,00.
	code, dash := env.req(t, "GET", "/api/dashboard?date=2025-02-01", "", false)
	if code != 200 || dash["total_debt_cents"].(float64) != 6000 || dash["overdue_cents"].(float64) != 6000 {
		t.Fatalf("dashboard: код=%d ответ=%v", code, dash)
	}
	// Карточка поставщика: статус просрочено.
	code, card := env.req(t, "GET", "/api/suppliers/s1/card?date=2025-02-01", "", false)
	if code != 200 {
		t.Fatalf("card: код %d", code)
	}
	obs, _ := card["obligations"].([]any)
	if len(obs) != 1 || obs[0].(map[string]any)["status"] != "overdue" {
		t.Fatalf("card obligations: %v", card["obligations"])
	}
	// Матрица за январь: день 15 — Собств 100,00.
	code, matrix := env.req(t, "GET", "/api/matrix?year=2025&month=1", "", false)
	if code != 200 {
		t.Fatalf("matrix: код %d", code)
	}
	totals, _ := matrix["day_totals"].([]any)
	if totals[14].(map[string]any)["sobstv_cents"].(float64) != 10000 {
		t.Fatalf("matrix totals[14]: %v", totals[14])
	}
	// Праздники больше не берутся из законодательства: 06–07.01 вводим вручную
	// через тот же API, который использует вкладка «Данные».
	if code, _ := env.req(t, "PUT", "/api/settings", `{"holidays":[{"date":"2025-01-07","name":"Ручной праздник"},{"date":"2025-01-06","name":"Ручной нерабочий день"}]}`, true); code != 200 {
		t.Fatalf("сохранение ручных праздников: код %d", code)
	}
	// Матрица по диапазону (неделя пн–вс) с ручным календарём: 06.01.2025 — понедельник.
	code, week := env.req(t, "GET", "/api/matrix?from=2025-01-06&to=2025-01-12&date=2025-02-01", "", false)
	if code != 200 {
		t.Fatalf("matrix range: код %d", code)
	}
	days, _ := week["days"].([]any)
	if len(days) != 7 {
		t.Fatalf("matrix range: дней %d", len(days))
	}
	d0 := days[0].(map[string]any)
	if d0["weekday"].(float64) != 1 || d0["is_day_off"] != true {
		t.Fatalf("matrix range day0: %v", d0)
	}
	d1 := days[1].(map[string]any)
	if d1["is_holiday"] != true || d1["holiday_name"] != "Ручной праздник" {
		t.Fatalf("matrix range ручной праздник: %v", d1)
	}
	if week["grand_overdue_cents"].(float64) != 6000 {
		t.Fatalf("matrix overdue на 01.02.2025: %v", week)
	}
	if code, _ := env.req(t, "GET", "/api/matrix?from=2025-01-12&to=2025-01-06", "", false); code != 400 {
		t.Fatalf("matrix reversed range: код %d", code)
	}
	// Удаление начисления.
	if code, _ := env.req(t, "DELETE", "/api/obligations/"+oblID, "", true); code != http.StatusNoContent {
		t.Fatalf("delete: код %d", code)
	}
}

func TestDeliveryVATAndWorkingDueDate(t *testing.T) {
	env := newTestEnv(t) // у s1 отсрочка 14 дней
	if code, _ := env.req(t, "PUT", "/api/settings", `{"holidays":[{"date":"2025-01-06","name":"Ручной нерабочий день"},{"date":"2025-01-07","name":"Ручной праздник"}]}`, true); code != 200 {
		t.Fatalf("сохранение ручных праздников: код %d", code)
	}
	// Панель поставки: 100,00 без НДС + 20,00 НДС. Поставка 21.12.2024 + 14 дней
	// = суббота 04.01.2025, поэтому срок сдвигается на среду 08.01.2025
	// (05.01 вс, 06–07.01 вручную введённые нерабочие дни).
	code, out := env.req(t, "POST", "/api/obligations",
		`{"supplier_id":"s1","delivery_date":"2024-12-21","amount_excl_vat_cents":10000,"vat_cents":2000}`, true)
	if code != http.StatusCreated || out["due_date"] != "2025-01-08" || out["amount_cents"].(float64) != 12000 {
		t.Fatalf("поставка с НДС: код=%d ответ=%v", code, out)
	}
	if out["amount_excl_vat_cents"].(float64) != 10000 || out["vat_cents"].(float64) != 2000 {
		t.Fatalf("разбивка НДС не сохранилась: %v", out)
	}
	// Сумма с НДС обязана равняться сумме без НДС + НДС.
	if code, _ := env.req(t, "POST", "/api/obligations",
		`{"supplier_id":"s1","delivery_date":"2025-01-02","amount_cents":11900,"amount_excl_vat_cents":10000,"vat_cents":2000}`, true); code != http.StatusBadRequest {
		t.Fatalf("несходящаяся разбивка НДС: код %d", code)
	}
	// Одна сумма разбивки без второй не принимается.
	if code, _ := env.req(t, "POST", "/api/obligations",
		`{"supplier_id":"s1","delivery_date":"2025-01-02","amount_cents":10000,"amount_excl_vat_cents":10000}`, true); code != http.StatusBadRequest {
		t.Fatalf("неполная разбивка НДС: код %d", code)
	}
	// Журнал операций отдаёт разбивку для отображения и редактирования в GUI.
	code, opsResp := env.req(t, "GET", "/api/operations?supplier_id=s1&kind=obligation", "", false)
	ops, _ := opsResp["operations"].([]any)
	if code != 200 || len(ops) != 1 || ops[0].(map[string]any)["vat_cents"].(float64) != 2000 {
		t.Fatalf("операции с НДС: код=%d ответ=%v", code, opsResp)
	}
	// Ручной ввод в ячейку «Таблицы» задаёт срок оплаты; сервер синхронизирует
	// дату поставки по отсрочке поставщика с учётом переноса нерабочих дней.
	code, manual := env.req(t, "POST", "/api/obligations",
		`{"supplier_id":"s1","due_date":"2025-01-08","amount_cents":5000,"infer_delivery_date":true}`, true)
	if code != http.StatusCreated || manual["delivery_date"] != "2024-12-21" {
		t.Fatalf("ручной ввод с восстановлением поставки: код=%d ответ=%v", code, manual)
	}
	manualID, _ := manual["id"].(string)
	// Та же запись видна в карточке поставщика с обеими датами.
	code, card := env.req(t, "GET", "/api/suppliers/s1/card?date=2025-01-31", "", false)
	cardObs, _ := card["obligations"].([]any)
	foundManual := false
	for _, raw := range cardObs {
		state := raw.(map[string]any)
		obligation := state["obligation"].(map[string]any)
		if obligation["id"] == manualID {
			foundManual = obligation["delivery_date"] == "2024-12-21" && obligation["due_date"] == "2025-01-08"
		}
	}
	if code != 200 || !foundManual {
		t.Fatalf("карточка после ручного ввода: код=%d ответ=%v", code, card)
	}
	// Без infer_delivery_date старое поведение сохраняется: дата поставки не выдумывается.
	code, plain := env.req(t, "POST", "/api/obligations",
		`{"supplier_id":"s1","due_date":"2025-01-15","amount_cents":6000}`, true)
	if _, hasDelivery := plain["delivery_date"]; code != http.StatusCreated || hasDelivery {
		t.Fatalf("обычное создание без infer: код=%d ответ=%v", code, plain)
	}
	// Прямое редактирование такой записи с infer заполняет недостающую дату поставки.
	plainID, _ := plain["id"].(string)
	code, updated := env.req(t, "PUT", "/api/obligations/"+plainID,
		`{"supplier_id":"s1","due_date":"2025-01-15","amount_cents":6500,"infer_delivery_date":true}`, true)
	if code != 200 || updated["delivery_date"] != "2025-01-01" {
		t.Fatalf("обновление с infer: код=%d ответ=%v", code, updated)
	}
	// Суббота, вручную отмеченная рабочей в календаре «Данные», не переносит срок.
	if code, _ := env.req(t, "PUT", "/api/settings", `{"working_days":["2025-01-04"]}`, true); code != 200 {
		t.Fatalf("сохранение рабочего дня: код %d", code)
	}
	code, workingSaturday := env.req(t, "POST", "/api/obligations",
		`{"supplier_id":"s1","delivery_date":"2024-12-21","amount_cents":1000}`, true)
	if code != http.StatusCreated || workingSaturday["due_date"] != "2025-01-04" {
		t.Fatalf("срок на рабочей субботе: код=%d ответ=%v", code, workingSaturday)
	}
}

func TestImportExportRoundTripViaAPI(t *testing.T) {
	env := newTestEnv(t)
	// Собираем книгу напрямую через excelio из тестовой базы и импортируем через API.
	db := model.NewEmptyDB()
	db.Suppliers = []model.Supplier{{ID: "x1", Name: "МАВ"}}
	db.Obligations = []model.Obligation{{ID: "o1", SupplierID: "x1", DueDate: "2025-03-05", AmountCents: 259776}}
	db.Payments = []model.Payment{{ID: "p1", SupplierID: "x1", Date: "2025-03-06", AmountCents: 259776}}
	data, err := excelio.ExportXLSX(db, 2025)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "oplata-2025.xlsx")
	_, _ = fw.Write(data)
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/api/import?mode=replace", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Oplata-Token", env.token)
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("import: код=%d тело=%s", rec.Code, rec.Body.String())
	}
	code, dash := env.req(t, "GET", "/api/dashboard?date=2025-12-31", "", false)
	if code != 200 || dash["total_debt_cents"].(float64) != 0 {
		t.Fatalf("после импорта долг должен быть 0: %v", dash)
	}
	// Экспорт XLSX отдаётся файлом.
	req = httptest.NewRequest("GET", "/api/export.xlsx?year=2025", nil)
	rec = httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	if rec.Code != 200 || !bytes.HasPrefix(rec.Body.Bytes(), []byte("PK")) {
		t.Fatalf("export.xlsx: код=%d", rec.Code)
	}
	// Статика GUI.
	req = httptest.NewRequest("GET", "/", nil)
	rec = httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("GUI: код=%d", rec.Code)
	}
}

func TestSupplierPaymentModesDisableDeferral(t *testing.T) {
	env := newTestEnv(t)
	code, created := env.req(t, "POST", "/api/suppliers", `{"name":"Предоплата ООО","deferral_days":30,"prepayment":true}`, true)
	if code != http.StatusCreated || created["prepayment"] != true {
		t.Fatalf("создание поставщика с предоплатой: код=%d ответ=%v", code, created)
	}
	supID, _ := created["id"].(string)
	// 02.01.2025 — четверг. Несмотря на сохранённую и явно переданную отсрочку 30 дней,
	// при предоплате режим отсрочки неактивен: срок равен дате поставки.
	code, obl := env.req(t, "POST", "/api/obligations", `{"supplier_id":"`+supID+`","delivery_date":"2025-01-02","deferral_days":30,"amount_cents":10000}`, true)
	if code != http.StatusCreated || obl["due_date"] != "2025-01-02" {
		t.Fatalf("срок при предоплате: код=%d ответ=%v", code, obl)
	}
	// 10.01.2025 — пятница. При ручном вводе из ячейки отсрочка также равна нулю.
	code, manual := env.req(t, "POST", "/api/obligations", `{"supplier_id":"`+supID+`","due_date":"2025-01-10","amount_cents":5000,"infer_delivery_date":true}`, true)
	if code != http.StatusCreated || manual["delivery_date"] != "2025-01-10" {
		t.Fatalf("поставка при предоплате: код=%d ответ=%v", code, manual)
	}
	if code, _ := env.req(t, "POST", "/api/suppliers", `{"name":"Сразу оба режима","prepayment":true,"on_realization":true}`, true); code != http.StatusBadRequest {
		t.Fatalf("одновременная предоплата+реализация: код %d", code)
	}
}

func TestBuyersAndSupplierWithoutUNP(t *testing.T) {
	env := newTestEnv(t)
	// У поставщиков УНП больше нет: старое поле запроса игнорируется и в ответ не попадает.
	code, out := env.req(t, "POST", "/api/suppliers", `{"name":"МАВ","unp":" 123 456 789 ","deferral_days":30}`, true)
	if _, hasUNP := out["unp"]; code != http.StatusCreated || hasUNP {
		t.Fatalf("поставщик без УНП: код=%d ответ=%v", code, out)
	}
	if code, _ := env.req(t, "POST", "/api/suppliers", `{"name":"Другой","unp":"123456789"}`, true); code != http.StatusCreated {
		t.Fatalf("второй поставщик с игнорируемым старым unp: код %d", code)
	}
	// Покупатели: УНП больше нет, старое поле запроса игнорируется.
	// Создать, найти поиском по примечанию, изменить, удалить.
	code, buy := env.req(t, "POST", "/api/buyers", `{"name":"СтройДом","unp":"987654321","note":"опт"}`, true)
	if _, hasUNP := buy["unp"]; code != http.StatusCreated || hasUNP {
		t.Fatalf("создание покупателя без УНП: код=%d ответ=%v", code, buy)
	}
	buyID, _ := buy["id"].(string)
	if code, _ := env.req(t, "POST", "/api/buyers", `{"name":"СтройДом"}`, true); code != http.StatusBadRequest {
		t.Fatalf("дубликат наименования покупателя: код %d", code)
	}
	code, list := env.req(t, "GET", "/api/buyers?q=987", "", false)
	buyers, _ := list["buyers"].([]any)
	if code != 200 || len(buyers) != 0 {
		t.Fatalf("поиск по старому УНП не должен находить покупателя: код=%d ответ=%v", code, list)
	}
	code, list = env.req(t, "GET", "/api/buyers?q=%D0%BE%D0%BF%D1%82", "", false)
	buyers, _ = list["buyers"].([]any)
	if code != 200 || len(buyers) != 1 {
		t.Fatalf("поиск покупателей по примечанию: код=%d ответ=%v", code, list)
	}
	if code, upd := env.req(t, "PUT", "/api/buyers/"+buyID, `{"name":"СтройДом ООО","unp":"123"}`, true); code != 200 || upd["name"] != "СтройДом ООО" {
		t.Fatalf("обновление покупателя со старым полем unp: код=%d ответ=%v", code, upd)
	} else if _, hasUNP := upd["unp"]; hasUNP {
		t.Fatalf("ответ покупателя не должен содержать unp: %v", upd)
	}
	if code, _ := env.req(t, "DELETE", "/api/buyers/"+buyID, "", true); code != http.StatusNoContent {
		t.Fatalf("удаление покупателя: код %d", code)
	}
	if code, _ := env.req(t, "DELETE", "/api/buyers/"+buyID, "", true); code != http.StatusNotFound {
		t.Fatalf("повторное удаление покупателя: код %d", code)
	}
}

func TestClearAmountsPreservesCounterparties(t *testing.T) {
	env := newTestEnv(t)
	if code, _ := env.req(t, "POST", "/api/buyers", `{"name":"Покупатель для очистки"}`, true); code != http.StatusCreated {
		t.Fatalf("создание покупателя: код %d", code)
	}
	if code, _ := env.req(t, "POST", "/api/obligations", `{"supplier_id":"s1","due_date":"2025-01-10","amount_cents":10000}`, true); code != http.StatusCreated {
		t.Fatalf("создание прихода: код %d", code)
	}
	if code, _ := env.req(t, "POST", "/api/payments", `{"supplier_id":"s1","date":"2025-01-12","amount_cents":4000}`, true); code != http.StatusCreated {
		t.Fatalf("создание оплаты: код %d", code)
	}
	if code, _ := env.req(t, "POST", "/api/clear-amounts", `{"confirm":"WRONG"}`, true); code != http.StatusBadRequest {
		t.Fatalf("очистка без правильного подтверждения: код %d", code)
	}
	code, ops := env.req(t, "GET", "/api/operations", "", false)
	if code != 200 || ops["total"].(float64) != 2 {
		t.Fatalf("операции после неверного подтверждения должны сохраниться: код=%d ответ=%v", code, ops)
	}

	code, cleared := env.req(t, "POST", "/api/clear-amounts", `{"confirm":"CLEAR_AMOUNTS"}`, true)
	if code != 200 || cleared["removed_obligations"].(float64) != 1 || cleared["removed_payments"].(float64) != 1 {
		t.Fatalf("очистка сумм: код=%d ответ=%v", code, cleared)
	}
	code, ops = env.req(t, "GET", "/api/operations", "", false)
	if code != 200 || ops["total"].(float64) != 0 {
		t.Fatalf("операции после очистки: код=%d ответ=%v", code, ops)
	}
	code, suppliers := env.req(t, "GET", "/api/suppliers", "", false)
	supplierList, _ := suppliers["suppliers"].([]any)
	if code != 200 || len(supplierList) != 1 {
		t.Fatalf("поставщик после очистки должен сохраниться: код=%d ответ=%v", code, suppliers)
	}
	code, buyers := env.req(t, "GET", "/api/buyers", "", false)
	buyerList, _ := buyers["buyers"].([]any)
	if code != 200 || len(buyerList) != 1 {
		t.Fatalf("покупатель после очистки должен сохраниться: код=%d ответ=%v", code, buyers)
	}
	code, dash := env.req(t, "GET", "/api/dashboard?date=2025-02-01", "", false)
	if code != 200 || dash["total_debt_cents"].(float64) != 0 {
		t.Fatalf("задолженность после очистки: код=%d ответ=%v", code, dash)
	}
}

func TestHostGuard(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv := New(st, "test", log.New(io.Discard, "", 0)) // AllowAnyHost=false
	req := httptest.NewRequest("GET", "/api/health", nil)
	req.Host = "evil.example.com"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("чужой Host должен запрещаться, код %d", rec.Code)
	}
	req.Host = "127.0.0.1:8765"
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("локальный Host: код %d", rec.Code)
	}
}
