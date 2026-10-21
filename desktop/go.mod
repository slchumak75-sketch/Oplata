module github.com/slchumak75-sketch/oplata/desktop

go 1.25

require (
	github.com/slchumak75-sketch/oplata v0.0.0
	github.com/wailsapp/wails/v2 v2.17.0
)

// Ядро берётся из родительского каталога репозитория (тот же код, что у cmd/oplata).
replace github.com/slchumak75-sketch/oplata => ../
