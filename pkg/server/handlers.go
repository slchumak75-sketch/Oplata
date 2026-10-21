package server

// Обработчики бизнес-операций: поставщики, начисления, выплаты,
// сводка, матрица, импорт/экспорт, резервные копии.
// Стиль: чтения — через Store.Snapshot + pkg/calc; записи — через Store.Update
// (валидация model.DB.Validate выполняется автоматически перед сохранением).

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/slchumak75-sketch/oplata/pkg/calc"
	"github.com/slchumak75-sketch/oplata/pkg/excelio"
	"github.com/slchumak75-sketch/oplata/pkg/model"
	"github.com/slchumak75-sketch/oplata/pkg/store"
)

// ---------- поставщики ----------

func (s *Server) handleSuppliersList(w http.ResponseWriter, r *http.Request) {
	asOf, ok := queryDate(r, "date")
	if !ok {
		writeErr(w, http.StatusBadRequest, "некорректная дата в параметре date")
		return
	}
	db := s.Store.Snapshot()
	sums := calc.SummarizeAll(db, asOf)
	if r.URL.Query().Get("include_archived") != "1" {
		sums = slices.DeleteFunc(sums, func(sum calc.SupplierSummary) bool { return sum.Supplier.Archived })
	}
	writeJSON(w, http.StatusOK, map[string]any{"date": asOf, "suppliers": sums})
}

type supplierRequest struct {
	Name         string `json:"name"`
	UNP          string `json:"unp"`
	DeferralDays int    `json:"deferral_days"`
	Note         string `json:"note"`
	Archived     bool   `json:"archived"`
}

func (s *Server) handleSupplierCreate(w http.ResponseWriter, r *http.Request) {
	var req supplierRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "некорректный JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "укажите название поставщика")
		return
	}
	created := model.Supplier{ID: model.NewID("sup"), Name: strings.TrimSpace(req.Name),
		UNP:          model.NormalizeUNP(req.UNP),
		DeferralDays: req.DeferralDays, Note: req.Note, Archived: req.Archived}
	err := s.Store.Update(func(db *model.DB) error {
		db.Suppliers = append(db.Suppliers, created)
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleSupplierUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req supplierRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "некорректный JSON: "+err.Error())
		return
	}
	var updated *model.Supplier
	err := s.Store.Update(func(db *model.DB) error {
		sup := db.SupplierByID(id)
		if sup == nil {
			return errNotFound("поставщик не найден")
		}
		if strings.TrimSpace(req.Name) == "" {
			return fmt.Errorf("укажите название поставщика")
		}
		sup.Name = strings.TrimSpace(req.Name)
		sup.UNP = model.NormalizeUNP(req.UNP)
		sup.DeferralDays = req.DeferralDays
		sup.Note = req.Note
		sup.Archived = req.Archived
		cp := *sup
		updated = &cp
		return nil
	})
	if err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleSupplierDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.Store.Update(func(db *model.DB) error {
		if db.SupplierByID(id) == nil {
			return errNotFound("поставщик не найден")
		}
		for _, o := range db.Obligations {
			if o.SupplierID == id {
				return fmt.Errorf("у поставщика есть начисления — удалите операции или отправьте поставщика в архив")
			}
		}
		for _, p := range db.Payments {
			if p.SupplierID == id {
				return fmt.Errorf("у поставщика есть выплаты — удалите операции или отправьте поставщика в архив")
			}
		}
		db.Suppliers = slices.DeleteFunc(db.Suppliers, func(sup model.Supplier) bool { return sup.ID == id })
		return nil
	})
	if err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSupplierCard(w http.ResponseWriter, r *http.Request) {
	asOf, ok := queryDate(r, "date")
	if !ok {
		writeErr(w, http.StatusBadRequest, "некорректная дата в параметре date")
		return
	}
	db := s.Store.Snapshot()
	card := calc.BuildSupplierCard(db, r.PathValue("id"), asOf)
	if card == nil {
		writeErr(w, http.StatusNotFound, "поставщик не найден")
		return
	}
	writeJSON(w, http.StatusOK, card)
}

// ---------- покупатели (справочник «Карточка покупателя») ----------

func (s *Server) handleBuyersList(w http.ResponseWriter, r *http.Request) {
	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	db := s.Store.Snapshot()
	buyers := []model.Buyer{}
	for _, b := range db.Buyers {
		if search != "" && !strings.Contains(strings.ToLower(b.Name+" "+b.UNP+" "+b.Note), search) {
			continue
		}
		buyers = append(buyers, b)
	}
	writeJSON(w, http.StatusOK, map[string]any{"buyers": buyers})
}

type buyerRequest struct {
	Name string `json:"name"`
	UNP  string `json:"unp"`
	Note string `json:"note"`
}

func (s *Server) handleBuyerCreate(w http.ResponseWriter, r *http.Request) {
	var req buyerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "некорректный JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "укажите наименование покупателя")
		return
	}
	created := model.Buyer{ID: model.NewID("buy"), Name: strings.TrimSpace(req.Name),
		UNP: model.NormalizeUNP(req.UNP), Note: strings.TrimSpace(req.Note)}
	err := s.Store.Update(func(db *model.DB) error {
		db.Buyers = append(db.Buyers, created)
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleBuyerUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req buyerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "некорректный JSON: "+err.Error())
		return
	}
	var updated *model.Buyer
	err := s.Store.Update(func(db *model.DB) error {
		buy := db.BuyerByID(id)
		if buy == nil {
			return errNotFound("покупатель не найден")
		}
		if strings.TrimSpace(req.Name) == "" {
			return fmt.Errorf("укажите наименование покупателя")
		}
		buy.Name = strings.TrimSpace(req.Name)
		buy.UNP = model.NormalizeUNP(req.UNP)
		buy.Note = strings.TrimSpace(req.Note)
		cp := *buy
		updated = &cp
		return nil
	})
	if err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// handleBuyerDelete удаляет покупателя без ограничений: операций с покупателями
// приложение не ведёт, терять историю нечего (в отличие от поставщиков).
func (s *Server) handleBuyerDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.Store.Update(func(db *model.DB) error {
		if db.BuyerByID(id) == nil {
			return errNotFound("покупатель не найден")
		}
		db.Buyers = slices.DeleteFunc(db.Buyers, func(b model.Buyer) bool { return b.ID == id })
		return nil
	})
	if err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- операции (журнал) ----------

type operationView struct {
	Kind         string     `json:"kind"` // obligation | payment
	ID           string     `json:"id"`
	SupplierID   string     `json:"supplier_id"`
	SupplierName string     `json:"supplier_name"`
	Date         model.Date `json:"date"` // срок оплаты для начисления, дата выплаты для выплаты
	AmountCents  int64      `json:"amount_cents"`
	DeliveryDate model.Date `json:"delivery_date,omitempty"`
	DocNumber    string     `json:"doc_number"`
	Comment      string     `json:"comment"`
}

func (s *Server) handleOperationsList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := q.Get("kind") // "", all, obligation, payment
	supplierID := q.Get("supplier_id")
	from, to := model.Date(q.Get("from")), model.Date(q.Get("to"))
	search := strings.ToLower(strings.TrimSpace(q.Get("q")))
	limit := 500
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= 5000 {
		limit = v
	}
	db := s.Store.Snapshot()
	names := make(map[string]string, len(db.Suppliers))
	for _, sup := range db.Suppliers {
		names[sup.ID] = sup.Name
	}
	match := func(supID string, date model.Date, fields ...string) bool {
		if supplierID != "" && supID != supplierID {
			return false
		}
		if from != "" && date < from {
			return false
		}
		if to != "" && date > to {
			return false
		}
		if search != "" {
			hay := strings.ToLower(names[supID] + " " + strings.Join(fields, " "))
			if !strings.Contains(hay, search) {
				return false
			}
		}
		return true
	}
	ops := []operationView{}
	if kind == "" || kind == "all" || kind == "obligation" {
		for _, o := range db.Obligations {
			if match(o.SupplierID, o.DueDate, o.DocNumber, o.Comment) {
				ops = append(ops, operationView{"obligation", o.ID, o.SupplierID, names[o.SupplierID],
					o.DueDate, o.AmountCents, o.DeliveryDate, o.DocNumber, o.Comment})
			}
		}
	}
	if kind == "" || kind == "all" || kind == "payment" {
		for _, p := range db.Payments {
			if match(p.SupplierID, p.Date, p.DocNumber, p.Comment) {
				ops = append(ops, operationView{"payment", p.ID, p.SupplierID, names[p.SupplierID],
					p.Date, p.AmountCents, "", p.DocNumber, p.Comment})
			}
		}
	}
	sort.SliceStable(ops, func(i, j int) bool {
		if ops[i].Date != ops[j].Date {
			return ops[i].Date > ops[j].Date // новые сверху
		}
		return ops[i].SupplierName < ops[j].SupplierName
	})
	total := len(ops)
	if len(ops) > limit {
		ops = ops[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "operations": ops})
}

// ---------- начисления («Собств») ----------

type obligationRequest struct {
	SupplierID   string `json:"supplier_id"`
	DueDate      string `json:"due_date"`
	AmountCents  int64  `json:"amount_cents"`
	DeliveryDate string `json:"delivery_date"`
	// DeferralDays: если DueDate пуст, а DeliveryDate задан, срок = поставка + отсрочка.
	// nil -> отсрочка поставщика. Фронтенд обычно считает сам и присылает готовый DueDate.
	DeferralDays *int   `json:"deferral_days"`
	DocNumber    string `json:"doc_number"`
	Comment      string `json:"comment"`
}

func resolveObligation(db *model.DB, req obligationRequest) (model.Obligation, error) {
	sup := db.SupplierByID(req.SupplierID)
	if sup == nil {
		return model.Obligation{}, fmt.Errorf("поставщик не найден")
	}
	if req.AmountCents <= 0 {
		return model.Obligation{}, fmt.Errorf("сумма к оплате должна быть положительной")
	}
	var delivery model.Date
	if strings.TrimSpace(req.DeliveryDate) != "" {
		var err error
		delivery, err = model.ParseDate(req.DeliveryDate)
		if err != nil {
			return model.Obligation{}, err
		}
	}
	var due model.Date
	if strings.TrimSpace(req.DueDate) != "" {
		var err error
		due, err = model.ParseDate(req.DueDate)
		if err != nil {
			return model.Obligation{}, err
		}
	} else if delivery != "" {
		deferral := sup.DeferralDays
		if req.DeferralDays != nil {
			deferral = *req.DeferralDays
		}
		due = delivery.AddDays(deferral)
	} else {
		return model.Obligation{}, fmt.Errorf("укажите срок оплаты (due_date) или дату поставки (delivery_date)")
	}
	return model.Obligation{
		SupplierID: req.SupplierID, DueDate: due, AmountCents: req.AmountCents,
		DeliveryDate: delivery, DocNumber: strings.TrimSpace(req.DocNumber), Comment: strings.TrimSpace(req.Comment),
	}, nil
}

func (s *Server) handleObligationCreate(w http.ResponseWriter, r *http.Request) {
	var req obligationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "некорректный JSON: "+err.Error())
		return
	}
	var created model.Obligation
	err := s.Store.Update(func(db *model.DB) error {
		o, err := resolveObligation(db, req)
		if err != nil {
			return err
		}
		o.ID = model.NewID("obl")
		created = o
		db.Obligations = append(db.Obligations, o)
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleObligationUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req obligationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "некорректный JSON: "+err.Error())
		return
	}
	var updated model.Obligation
	err := s.Store.Update(func(db *model.DB) error {
		for i := range db.Obligations {
			if db.Obligations[i].ID == id {
				o, err := resolveObligation(db, req)
				if err != nil {
					return err
				}
				o.ID = id
				db.Obligations[i] = o
				updated = o
				return nil
			}
		}
		return errNotFound("начисление не найдено")
	})
	if err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleObligationDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.Store.Update(func(db *model.DB) error {
		before := len(db.Obligations)
		db.Obligations = slices.DeleteFunc(db.Obligations, func(o model.Obligation) bool { return o.ID == id })
		if len(db.Obligations) == before {
			return errNotFound("начисление не найдено")
		}
		return nil
	})
	if err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- выплаты ----------

type paymentRequest struct {
	SupplierID  string `json:"supplier_id"`
	Date        string `json:"date"`
	AmountCents int64  `json:"amount_cents"`
	DocNumber   string `json:"doc_number"`
	Comment     string `json:"comment"`
}

func resolvePayment(db *model.DB, req paymentRequest) (model.Payment, error) {
	if db.SupplierByID(req.SupplierID) == nil {
		return model.Payment{}, fmt.Errorf("поставщик не найден")
	}
	if req.AmountCents == 0 {
		return model.Payment{}, fmt.Errorf("укажите сумму выплаты")
	}
	date, err := model.ParseDate(req.Date)
	if err != nil {
		return model.Payment{}, err
	}
	return model.Payment{SupplierID: req.SupplierID, Date: date, AmountCents: req.AmountCents,
		DocNumber: strings.TrimSpace(req.DocNumber), Comment: strings.TrimSpace(req.Comment)}, nil
}

func (s *Server) handlePaymentCreate(w http.ResponseWriter, r *http.Request) {
	var req paymentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "некорректный JSON: "+err.Error())
		return
	}
	var created model.Payment
	err := s.Store.Update(func(db *model.DB) error {
		p, err := resolvePayment(db, req)
		if err != nil {
			return err
		}
		p.ID = model.NewID("pay")
		created = p
		db.Payments = append(db.Payments, p)
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handlePaymentUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req paymentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "некорректный JSON: "+err.Error())
		return
	}
	var updated model.Payment
	err := s.Store.Update(func(db *model.DB) error {
		for i := range db.Payments {
			if db.Payments[i].ID == id {
				p, err := resolvePayment(db, req)
				if err != nil {
					return err
				}
				p.ID = id
				db.Payments[i] = p
				updated = p
				return nil
			}
		}
		return errNotFound("выплата не найдена")
	})
	if err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handlePaymentDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.Store.Update(func(db *model.DB) error {
		before := len(db.Payments)
		db.Payments = slices.DeleteFunc(db.Payments, func(p model.Payment) bool { return p.ID == id })
		if len(db.Payments) == before {
			return errNotFound("выплата не найдена")
		}
		return nil
	})
	if err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- сводка и матрица ----------

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	asOf, ok := queryDate(r, "date")
	if !ok {
		writeErr(w, http.StatusBadRequest, "некорректная дата в параметре date")
		return
	}
	db := s.Store.Snapshot()
	writeJSON(w, http.StatusOK, calc.BuildDashboard(db, asOf))
}

func (s *Server) handleMatrix(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// Основной режим вкладки «Таблица»: произвольный диапазон дат (недели пн–вс).
	if q.Get("from") != "" || q.Get("to") != "" {
		from, to := model.Date(q.Get("from")), model.Date(q.Get("to"))
		if !from.Valid() || !to.Valid() {
			writeErr(w, http.StatusBadRequest, "нужны корректные даты from и to (гггг-мм-дд)")
			return
		}
		if to < from {
			writeErr(w, http.StatusBadRequest, "дата to раньше даты from")
			return
		}
		if days := int(to.Time().Sub(from.Time()).Hours()/24) + 1; days > 371 { // не больше 53 недель
			writeErr(w, http.StatusBadRequest, "диапазон слишком большой (максимум 371 день)")
			return
		}
		db := s.Store.Snapshot()
		writeJSON(w, http.StatusOK, calc.BuildMatrixRange(db, from, to))
		return
	}
	// Совместимость: режим «год + месяц».
	now := model.Today()
	defYear, defMonth := now.YearMonth()
	year, month := defYear, defMonth
	if v := q.Get("year"); v != "" {
		y, err := strconv.Atoi(v)
		if err != nil || y < 1900 || y > 2200 {
			writeErr(w, http.StatusBadRequest, "некорректный год")
			return
		}
		year = y
	}
	if v := q.Get("month"); v != "" {
		m, err := strconv.Atoi(v)
		if err != nil || m < 1 || m > 12 {
			writeErr(w, http.StatusBadRequest, "некорректный месяц (1..12)")
			return
		}
		month = m
	}
	db := s.Store.Snapshot()
	writeJSON(w, http.StatusOK, calc.BuildMatrix(db, year, month))
}

// ---------- импорт / экспорт / резервные копии ----------

func readMultipartFile(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, bool) {
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "не удалось прочитать загруженный файл: "+err.Error())
		return nil, false
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "ожидается файл в поле file")
		return nil, false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	if int64(len(data)) > maxBytes {
		writeErr(w, http.StatusBadRequest, "файл слишком большой")
		return nil, false
	}
	return data, true
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	mode := r.URL.Query().Get("mode") // replace (по умолчанию) | merge
	if mode == "" {
		mode = "replace"
	}
	yearOverride := 0
	if v := r.URL.Query().Get("year"); v != "" {
		y, err := strconv.Atoi(v)
		if err != nil || y < 1900 || y > 2200 {
			writeErr(w, http.StatusBadRequest, "некорректный год")
			return
		}
		yearOverride = y
	}
	data, ok := readMultipartFile(w, r, 64<<20)
	if !ok {
		return
	}
	imported, report, err := excelio.ParseXLSX(data, yearOverride)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch mode {
	case "replace":
		current := s.Store.Snapshot()
		imported.Settings = current.Settings
		imported.Settings.SeededFrom = ""
		// В Excel покупателей нет — справочник при замене поставщиков/операций сохраняем.
		imported.Buyers = current.Buyers
		// УНП поставщиков в Excel тоже нет — переносим по совпадению наименования, чтобы не терять карточки.
		unpByName := make(map[string]string, len(current.Suppliers))
		for _, sup := range current.Suppliers {
			if sup.UNP != "" {
				unpByName[model.NormalizeName(sup.Name)] = sup.UNP
			}
		}
		for i := range imported.Suppliers {
			if unp, ok := unpByName[model.NormalizeName(imported.Suppliers[i].Name)]; ok {
				imported.Suppliers[i].UNP = unp
			}
		}
		imported.Warnings = report.Warnings
		err = s.Store.ReplaceAll(imported)
	case "merge":
		err = s.Store.Update(func(db *model.DB) error {
			byName := make(map[string]string, len(db.Suppliers))
			for _, sup := range db.Suppliers {
				byName[model.NormalizeName(sup.Name)] = sup.ID
			}
			remap := make(map[string]string, len(imported.Suppliers))
			for _, sup := range imported.Suppliers {
				if existing, ok := byName[model.NormalizeName(sup.Name)]; ok {
					remap[sup.ID] = existing
				} else {
					db.Suppliers = append(db.Suppliers, sup)
					remap[sup.ID] = sup.ID
					byName[model.NormalizeName(sup.Name)] = sup.ID
				}
			}
			for _, o := range imported.Obligations {
				o.SupplierID = remap[o.SupplierID]
				o.ID = model.NewID("obl")
				db.Obligations = append(db.Obligations, o)
			}
			for _, p := range imported.Payments {
				p.SupplierID = remap[p.SupplierID]
				p.ID = model.NewID("pay")
				db.Payments = append(db.Payments, p)
			}
			db.Warnings = append(db.Warnings, report.Warnings...)
			return nil
		})
	default:
		writeErr(w, http.StatusBadRequest, "mode должен быть replace или merge")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.Log.Printf("импорт Excel (%s): поставщиков=%d начислений=%d выплат=%d долг=%s",
		mode, report.Suppliers, report.Obligations, report.Payments, model.FormatCents(report.TotalDebtCents))
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleExportXLSX(w http.ResponseWriter, r *http.Request) {
	year := time.Now().Year()
	if v := r.URL.Query().Get("year"); v != "" {
		y, err := strconv.Atoi(v)
		if err != nil || y < 1900 || y > 2200 {
			writeErr(w, http.StatusBadRequest, "некорректный год")
			return
		}
		year = y
	}
	data, err := excelio.ExportXLSX(s.Store.Snapshot(), year)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"oplata-%d.xlsx\"", year))
	_, _ = w.Write(data)
}

func (s *Server) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	db := s.Store.Snapshot()
	names := make(map[string]string, len(db.Suppliers))
	for _, sup := range db.Suppliers {
		names[sup.ID] = sup.Name
	}
	var buf bytes.Buffer
	buf.WriteString("\xef\xbb\xbf") // BOM, чтобы Excel RU открыл UTF-8 корректно
	cw := csv.NewWriter(&buf)
	cw.Comma = ';'
	_ = cw.Write([]string{"Тип", "Поставщик", "Дата", "Сумма", "Дата поставки", "№ документа", "Комментарий"})
	money := func(c int64) string { return strings.Replace(model.CentsToDecimal(c), ".", ",", 1) }
	dmy := func(d model.Date) string {
		if d == "" {
			return ""
		}
		return d.Time().Format("02.01.2006")
	}
	type csvRow struct {
		iso   model.Date
		cells []string
	}
	rows := []csvRow{}
	for _, o := range db.Obligations {
		rows = append(rows, csvRow{o.DueDate, []string{"Собств (к оплате)", names[o.SupplierID], dmy(o.DueDate), money(o.AmountCents), dmy(o.DeliveryDate), o.DocNumber, o.Comment}})
	}
	for _, p := range db.Payments {
		rows = append(rows, csvRow{p.Date, []string{"Выплата", names[p.SupplierID], dmy(p.Date), money(p.AmountCents), "", p.DocNumber, p.Comment}})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].iso < rows[j].iso })
	for _, row := range rows {
		_ = cw.Write(row.cells)
	}
	cw.Flush()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"oplata-operations.csv\"")
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	data, err := store.Encode(s.Store.Snapshot())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := "oplata-backup-" + time.Now().Format("20060102-150405") + ".json"
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	_, _ = w.Write(data)
}

func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	data, ok := readMultipartFile(w, r, 64<<20)
	if !ok {
		return
	}
	db, err := store.Decode(data)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "файл резервной копии не подходит: "+err.Error())
		return
	}
	if err := s.Store.ReplaceAll(db); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.Log.Printf("база восстановлена из резервной копии (%d поставщиков)", len(db.Suppliers))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Confirm != "RESET" {
		writeErr(w, http.StatusBadRequest, "для сброса пришлите {\"confirm\":\"RESET\"}")
		return
	}
	current := s.Store.Snapshot()
	db := model.NewEmptyDB()
	db.Settings = current.Settings
	db.Settings.SeededFrom = ""
	if err := s.Store.ReplaceAll(db); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Log.Print("база сброшена к пустой (страховка: папка backups)")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------- ошибки ----------

type notFoundErr struct{ msg string }

func (e notFoundErr) Error() string { return e.msg }

func errNotFound(msg string) error { return notFoundErr{msg} }

func statusForErr(err error) int {
	if _, ok := err.(notFoundErr); ok {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}
