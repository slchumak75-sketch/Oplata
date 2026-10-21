// Пакет webui встраивает HTML GUI (assets/) в исполняемый файл oplata.exe.
// Фронтенд — чистый HTML/CSS/JS без сборки и внешних зависимостей (работает офлайн).
package webui

import (
	"embed"
	"io/fs"
)

// content содержит каталог assets целиком.
//
//go:embed all:assets
var content embed.FS

// FS возвращает файловую систему с корнем в assets/ (index.html, app.css, app.js).
func FS() fs.FS {
	sub, err := fs.Sub(content, "assets")
	if err != nil {
		panic(err) // embed проверяется при компиляции, ошибка здесь невозможна
	}
	return sub
}
