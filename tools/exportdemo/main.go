// Команда exportdemo пишет XLSX-экспорт встроенных стартовых данных (seed) за 2025 год.
//
// Нужна для независимой проверки совместимости экспорта: в CI результат открывает
// сторонний openpyxl (tools/verify_export.py). Вручную:
//
//	go run ./tools/exportdemo /tmp/oplata-2025.xlsx
package main

import (
	"fmt"
	"os"

	"github.com/slchumak75-sketch/oplata/pkg/excelio"
	"github.com/slchumak75-sketch/oplata/pkg/store"
)

func main() {
	out := "oplata-2025.xlsx"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	db, err := store.Decode(store.SeedJSON)
	if err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
	data, err := excelio.ExportXLSX(db, 2025)
	if err != nil {
		fmt.Fprintln(os.Stderr, "export:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
	fmt.Printf("записан %s (%d байт)\n", out, len(data))
}
