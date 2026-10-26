// Пакет server — HTTP API и раздача HTML GUI единственного варианта приложения
// (cmd/oplata, http.Server на 127.0.0.1). Wails/консольный варианты отброшены
// владельцем 2026-10-21 — остался только oplata.exe.
//
// Безопасность локального приложения:
//   - по умолчанию слушаем только 127.0.0.1 (флаг -addr в cmd);
//   - проверяем Host (localhost/127.0.0.1), если не включён LAN-режим;
//   - все изменяющие запросы требуют заголовок X-Oplata-Token со случайным токеном запуска.
//     Токен отдаётся в GET /api/bootstrap: чужие сайты прочитать его не могут (Same-Origin),
//     а без токена POST/PUT/DELETE отклоняются. Дополнительно сверяем Origin == Host.
//
// Все суммы в API — целые копейки (*_cents), даты — "гггг-мм-дд". Спецификация: docs/DATA_MODEL.md.
package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/slchumak75-sketch/oplata/pkg/model"
	"github.com/slchumak75-sketch/oplata/pkg/store"
	"github.com/slchumak75-sketch/oplata/pkg/webui"
)

// Server — состояние HTTP-слоя.
type Server struct {
	Store   *store.Store
	Version string
	Log     *log.Logger
	// AllowAnyHost разрешает любой Host (LAN-режим, -addr 0.0.0.0:...). Осторожно:
	// приложение не имеет авторизации пользователей — см. предупреждение в README.
	AllowAnyHost bool
	token        string
}

// New создаёт сервер с новым случайным токеном запуска.
func New(st *store.Store, version string, logger *log.Logger) *Server {
	var b [16]byte
	token := "dev-token"
	if _, err := rand.Read(b[:]); err == nil {
		token = hex.EncodeToString(b[:])
	}
	if logger == nil {
		logger = log.Default()
	}
	if version == "" {
		version = "dev"
	}
	return &Server{Store: st, Version: version, Log: logger, token: token}
}

// Token возвращает токен запуска (используется в тестах и для отладки).
func (s *Server) Token() string { return s.token }

// Handler собирает маршруты API + статику GUI.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/bootstrap", s.handleBootstrap)
	mux.HandleFunc("PUT /api/settings", s.handleSettings)
	mux.HandleFunc("GET /api/suppliers", s.handleSuppliersList)
	mux.HandleFunc("POST /api/suppliers", s.handleSupplierCreate)
	mux.HandleFunc("PUT /api/suppliers/{id}", s.handleSupplierUpdate)
	mux.HandleFunc("DELETE /api/suppliers/{id}", s.handleSupplierDelete)
	mux.HandleFunc("GET /api/suppliers/{id}/card", s.handleSupplierCard)
	mux.HandleFunc("GET /api/buyers", s.handleBuyersList)
	mux.HandleFunc("POST /api/buyers", s.handleBuyerCreate)
	mux.HandleFunc("PUT /api/buyers/{id}", s.handleBuyerUpdate)
	mux.HandleFunc("DELETE /api/buyers/{id}", s.handleBuyerDelete)
	mux.HandleFunc("GET /api/operations", s.handleOperationsList)
	mux.HandleFunc("POST /api/obligations", s.handleObligationCreate)
	mux.HandleFunc("PUT /api/obligations/{id}", s.handleObligationUpdate)
	mux.HandleFunc("DELETE /api/obligations/{id}", s.handleObligationDelete)
	mux.HandleFunc("POST /api/payments", s.handlePaymentCreate)
	mux.HandleFunc("PUT /api/payments/{id}", s.handlePaymentUpdate)
	mux.HandleFunc("DELETE /api/payments/{id}", s.handlePaymentDelete)
	mux.HandleFunc("GET /api/dashboard", s.handleDashboard)
	mux.HandleFunc("GET /api/matrix", s.handleMatrix)
	mux.HandleFunc("POST /api/import", s.handleImport)
	mux.HandleFunc("GET /api/export.xlsx", s.handleExportXLSX)
	mux.HandleFunc("GET /api/export.csv", s.handleExportCSV)
	mux.HandleFunc("GET /api/backup.json", s.handleBackup)
	mux.HandleFunc("POST /api/restore", s.handleRestore)
	mux.HandleFunc("POST /api/reset", s.handleReset)
	mux.HandleFunc("POST /api/clear-amounts", s.handleClearAmounts)
	mux.HandleFunc("/", s.handleStatic)
	return s.guard(mux)
}

var allowedHosts = map[string]bool{
	"127.0.0.1": true, "localhost": true, "::1": true, "[::1]": true,
}

// guard: проверка Host, Origin и токена для изменяющих запросов.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.AllowAnyHost {
			host := r.Host
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			if !allowedHosts[strings.ToLower(host)] {
				http.Error(w, "forbidden host", http.StatusForbidden)
				return
			}
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if u, err := url.Parse(origin); err == nil && u.Host != "" && u.Host != r.Host {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && strings.HasPrefix(r.URL.Path, "/api/") {
			if r.Header.Get("X-Oplata-Token") != s.token {
				writeErr(w, http.StatusForbidden, "нет или неверный заголовок X-Oplata-Token (перезагрузите страницу)")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ---------- хелперы ответов ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	return dec.Decode(v)
}

func queryDate(r *http.Request, name string) (model.Date, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return model.Today(), true
	}
	d, err := model.ParseDate(raw)
	if err != nil {
		return "", false
	}
	return d, true
}

// ---------- базовые обработчики ----------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.Version})
}

type bootstrapResponse struct {
	Version  string          `json:"version"`
	Today    model.Date      `json:"today"`
	Token    string          `json:"token"`
	Settings model.Settings  `json:"settings"`
	Warnings []string        `json:"warnings"`
	DataFile string          `json:"data_file"`
	Counts   bootstrapCounts `json:"counts"`
}

type bootstrapCounts struct {
	Suppliers   int `json:"suppliers"`
	Obligations int `json:"obligations"`
	Payments    int `json:"payments"`
	Buyers      int `json:"buyers"`
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	db := s.Store.Snapshot()
	writeJSON(w, http.StatusOK, bootstrapResponse{
		Version: s.Version, Today: model.Today(), Token: s.token,
		Settings: db.Settings, Warnings: db.Warnings, DataFile: s.Store.Path(),
		Counts: bootstrapCounts{len(db.Suppliers), len(db.Obligations), len(db.Payments), len(db.Buyers)},
	})
}

type settingsRequest struct {
	CompanyName   *string          `json:"company_name"`
	CurrencyLabel *string          `json:"currency_label"`
	WarnDays      *int             `json:"warn_days"`
	Holidays      *[]model.Holiday `json:"holidays"`
	WorkingDays   *[]model.Date    `json:"working_days"`
	ClearWarnings bool             `json:"clear_warnings"`
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	var req settingsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "некорректный JSON: "+err.Error())
		return
	}
	err := s.Store.Update(func(db *model.DB) error {
		if req.CompanyName != nil {
			db.Settings.CompanyName = strings.TrimSpace(*req.CompanyName)
		}
		if req.CurrencyLabel != nil {
			db.Settings.CurrencyLabel = strings.TrimSpace(*req.CurrencyLabel)
		}
		if req.WarnDays != nil {
			if *req.WarnDays < 0 || *req.WarnDays > 365 {
				return fmt.Errorf("warn_days вне диапазона 0..365")
			}
			db.Settings.WarnDays = *req.WarnDays
		}
		if req.Holidays != nil {
			if len(*req.Holidays) > 10000 {
				return fmt.Errorf("слишком много праздничных дней: %d", len(*req.Holidays))
			}
			seen := make(map[model.Date]bool, len(*req.Holidays))
			holidays := make([]model.Holiday, 0, len(*req.Holidays))
			for _, h := range *req.Holidays {
				date, err := model.ParseDate(string(h.Date))
				if err != nil {
					return fmt.Errorf("праздничный день: %w", err)
				}
				if seen[date] {
					return fmt.Errorf("праздничный день %s указан дважды", date)
				}
				seen[date] = true
				holidays = append(holidays, model.Holiday{Date: date, Name: strings.TrimSpace(h.Name)})
			}
			slices.SortFunc(holidays, func(a, b model.Holiday) int {
				return strings.Compare(string(a.Date), string(b.Date))
			})
			db.Settings.Holidays = holidays
		}
		if req.WorkingDays != nil {
			if len(*req.WorkingDays) > 10000 {
				return fmt.Errorf("слишком много рабочих дней: %d", len(*req.WorkingDays))
			}
			seen := make(map[model.Date]bool, len(*req.WorkingDays))
			workingDays := make([]model.Date, 0, len(*req.WorkingDays))
			for _, raw := range *req.WorkingDays {
				date, err := model.ParseDate(string(raw))
				if err != nil {
					return fmt.Errorf("рабочий день: %w", err)
				}
				if seen[date] {
					return fmt.Errorf("рабочий день %s указан дважды", date)
				}
				seen[date] = true
				workingDays = append(workingDays, date)
			}
			slices.Sort(workingDays)
			db.Settings.WorkingDays = workingDays
		}
		if req.ClearWarnings {
			db.Warnings = []string{}
		}
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.handleBootstrap(w, r)
}

// handleStatic раздаёт встроенный GUI; неизвестные пути (hash-роутинг не требует,
// но прямые ссылки вида /suppliers тоже должны работать) — index.html.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeErr(w, http.StatusNotFound, "неизвестный метод API")
		return
	}
	assets := webui.FS()
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	f, err := assets.Open(path)
	if err != nil {
		path = "index.html"
		f, err = assets.Open(path)
		if err != nil {
			http.Error(w, "gui not found", http.StatusInternalServerError)
			return
		}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, path, info.ModTime(), rs)
		return
	}
	// embed.FS всегда даёт ReadSeeker, ветка — на всякий случай.
	data, _ := io.ReadAll(f)
	_, _ = w.Write(data)
}
