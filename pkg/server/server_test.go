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
	st, err := store.Open(filepath.Join(t.TempDir(), "data.json"), false)
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
	// Удаление начисления.
	if code, _ := env.req(t, "DELETE", "/api/obligations/"+oblID, "", true); code != http.StatusNoContent {
		t.Fatalf("delete: код %d", code)
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

func TestHostGuard(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "data.json"), false)
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
