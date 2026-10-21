// Пакет store — хранение базы в JSON-файле: загрузка, атомарное сохранение,
// резервные копии, стартовые данные (seed) и потокобезопасный доступ.
//
// Файл данных по умолчанию: <UserConfigDir>/Oplata/data.json
// (Windows: %AppData%\Oplata\data.json). Резервные копии: подпапка backups,
// хранятся последние MaxBackups. Формат файла: docs/DATA_MODEL.md.
package store

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/slchumak75-sketch/oplata/pkg/model"
)

// MaxBackups — сколько резервных копий хранить.
const MaxBackups = 20

// SeedJSON — стартовые данные, сгенерированные из «Оплаты Строймаркет.xlsx»
// скриптом tools/excel2seed.py. Загружаются ТОЛЬКО при первом запуске,
// когда файла данных ещё нет (и не задан флаг -no-seed / WithNoSeed).
// Чтобы собрать exe без чужих данных: заменить файл seed.json на пустую базу
// ({"version":2,...} с пустыми списками) и пересобрать — см. docs/BUILD.md.
//
//go:embed seed.json
var SeedJSON []byte

// Store — потокобезопасная обёртка над *model.DB с сохранением в файл при каждом изменении.
type Store struct {
	mu   sync.RWMutex
	path string
	db   *model.DB
}

// Path возвращает путь к файлу данных.
func (s *Store) Path() string { return s.path }

// DefaultDataPath возвращает путь по умолчанию для файла данных.
// portable=true: рядом с исполняемым файлом (oplata-data.json).
func DefaultDataPath(portable bool) (string, error) {
	if portable {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		return filepath.Join(filepath.Dir(exe), "oplata-data.json"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "Oplata", "data.json"), nil
}

// Open загружает базу из файла path. Если файла нет:
//   - useSeed=true  -> база из встроенного SeedJSON (данные Excel 2025);
//   - useSeed=false -> пустая база.
//
// В обоих случаях файл сразу создаётся на диске, чтобы пользователь видел, где живут данные.
func Open(path string, useSeed bool) (*Store, error) {
	st := &Store{path: path}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		db, err := Decode(data)
		if err != nil {
			return nil, fmt.Errorf("файл данных %s повреждён: %w (восстановите из папки backups рядом с файлом)", path, err)
		}
		st.db = db
	case os.IsNotExist(err):
		if useSeed && len(SeedJSON) > 0 {
			db, err := Decode(SeedJSON)
			if err != nil {
				return nil, fmt.Errorf("встроенные стартовые данные повреждены: %w", err)
			}
			st.db = db
		} else {
			st.db = model.NewEmptyDB()
		}
		if err := st.saveLocked(); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	return st, nil
}

// Decode разбирает и валидирует JSON базы. Пустые срезы нормализуются в [] (не null в API).
// Здесь же живут миграции формата: v1 -> v2 добавляет пустой справочник покупателей
// и пустой УНП у поставщиков (поля просто отсутствуют в старом JSON).
func Decode(data []byte) (*model.DB, error) {
	var db model.DB
	if err := json.Unmarshal(data, &db); err != nil {
		return nil, err
	}
	if db.Version == 1 {
		db.Version = 2
	}
	if db.Suppliers == nil {
		db.Suppliers = []model.Supplier{}
	}
	if db.Obligations == nil {
		db.Obligations = []model.Obligation{}
	}
	if db.Payments == nil {
		db.Payments = []model.Payment{}
	}
	if db.Buyers == nil {
		db.Buyers = []model.Buyer{}
	}
	if db.Warnings == nil {
		db.Warnings = []string{}
	}
	if err := db.Validate(); err != nil {
		return nil, err
	}
	return &db, nil
}

// Encode сериализует базу (с отступами — файл можно смотреть и править вручную).
func Encode(db *model.DB) ([]byte, error) {
	return json.MarshalIndent(db, "", " ")
}

// Snapshot возвращает глубокую копию базы; вызывающий код может безопасно её читать.
func (s *Store) Snapshot() *model.DB {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneDB(s.db)
}

// Update выполняет fn под блокировкой и сохраняет результат на диск.
// Если fn вернула ошибку или база не прошла Validate — изменения откатываются.
func (s *Store) Update(fn func(db *model.DB) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneDB(s.db)
	if err := fn(next); err != nil {
		return err
	}
	if err := next.Validate(); err != nil {
		return err
	}
	prev := s.db
	s.db = next
	if err := s.saveLocked(); err != nil {
		s.db = prev
		return err
	}
	return nil
}

// ReplaceAll полностью заменяет базу (импорт replace, восстановление из резервной копии, сброс).
func (s *Store) ReplaceAll(db *model.DB) error {
	if err := db.Validate(); err != nil {
		return err
	}
	db = cloneDB(db)
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.db
	s.db = db
	if err := s.saveLocked(); err != nil {
		s.db = prev
		return err
	}
	return nil
}

func cloneDB(db *model.DB) *model.DB {
	out := *db
	out.Suppliers = slices.Clone(db.Suppliers)
	out.Obligations = slices.Clone(db.Obligations)
	out.Payments = slices.Clone(db.Payments)
	out.Buyers = slices.Clone(db.Buyers)
	out.Warnings = slices.Clone(db.Warnings)
	if out.Suppliers == nil {
		out.Suppliers = []model.Supplier{}
	}
	if out.Obligations == nil {
		out.Obligations = []model.Obligation{}
	}
	if out.Payments == nil {
		out.Payments = []model.Payment{}
	}
	if out.Buyers == nil {
		out.Buyers = []model.Buyer{}
	}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	return &out
}

// saveLocked пишет базу на диск атомарно (tmp + rename) и обновляет резервные копии.
// Вызывать только под s.mu (или до публикации Store из Open).
func (s *Store) saveLocked() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	s.backupLocked(dir)
	data, err := Encode(s.db)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".data-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// backupLocked копирует текущий файл данных в backups/data-<timestamp>.json перед перезаписью.
// Ошибки бэкапа не блокируют сохранение (лучше сохранить без бэкапа, чем потерять правку).
func (s *Store) backupLocked(dir string) {
	cur, err := os.ReadFile(s.path)
	if err != nil || len(cur) == 0 {
		return
	}
	bdir := filepath.Join(dir, "backups")
	if err := os.MkdirAll(bdir, 0o755); err != nil {
		return
	}
	name := "data-" + time.Now().Format("20060102-150405") + ".json"
	_ = os.WriteFile(filepath.Join(bdir, name), cur, 0o644)
	entries, err := os.ReadDir(bdir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "data-") && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	for len(names) > MaxBackups {
		_ = os.Remove(filepath.Join(bdir, names[0]))
		names = names[1:]
	}
}
