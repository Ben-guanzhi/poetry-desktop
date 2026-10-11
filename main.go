package main

import (
	"fyne.io/fyne/v2/app"
	"poetry-desktop/internal/ui"
)

func main() {
	a := app.NewWithID("com.poetry.desktop")
	w := a.NewWindow("中华诗词 · 桌面客户端")
	ui.NewAppUI(a, w, a.Storage().RootURI().Path())
	w.ShowAndRun()
}
