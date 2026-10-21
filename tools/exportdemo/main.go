// Команда exportdemo проверяет связку импорт→экспорт: читает исходный
// «Оплаты Строймаркет.xlsx» через pkg/excelio и пишет экспорт за тот же год.
//
// Нужна для независимой проверки совместимости в CI: результат открывает
// сторонний openpyxl (tools/verify_export.py). Вручную из корня репозитория:
//
//	go run ./tools/exportdemo /tmp/oplata-2025.xlsx
//	go run ./tools/exportdemo /tmp/oplata.xlsx /path/to/input.xlsx
package main

import (
	"fmt"
	"os"

	"github.com/slchumak75-sketch/oplata/pkg/excelio"
)

func main() {
	out := "oplata-2025.xlsx"
	in := "Оплаты Строймаркет.xlsx"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	if len(os.Args) > 2 {
		in = os.Args[2]
	}

	src, err := os.ReadFile(in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read:", err)
		os.Exit(1)
	}
	db, report, err := excelio.ParseXLSX(src, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "import:", err)
		os.Exit(1)
	}
	data, err := excelio.ExportXLSX(db, report.Year)
	if err != nil {
		fmt.Fprintln(os.Stderr, "export:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
	fmt.Printf("записан %s (%d байт) из %s\n", out, len(data), in)
}
