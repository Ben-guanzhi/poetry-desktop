package ui

import (
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"poetry-desktop/internal/fetcher"
	"poetry-desktop/internal/model"
	"poetry-desktop/internal/store"
)

type AppUI struct {
	app     fyne.App
	window  fyne.Window
	store   *store.Store
	dataDir string

	navList     *widget.List
	topBar      *fyne.Container
	searchEntry *widget.Entry

	currentNav      int
	poems           []model.Poem
	listIndex       int
	listRebuild     func()
	selectedDynasty model.Dynasty
	selectedAuthor  string

	fontSize  int
	themeName string
}

var favIcon = fyne.NewStaticResource("star", []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path fill="black" d="M12 17.27L18.18 21l-1.64-7.03L22 9.24l-7.19-.61L12 2 9.19 8.63 2 9.24l5.46 4.73L5.82 21z"/></svg>`))

var brightnessIcon = fyne.NewStaticResource("brightness", []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path fill="black" d="M20 8.69V4h-4.69L12 .69 8.69 4H4v4.69L.69 12 4 15.31V20h4.69L12 23.31 15.31 20H20v-4.69L23.31 12 20 8.69zM12 18a6 6 0 1 1 0-12 6 6 0 0 1 0 12z"/></svg>`))

// navKind says what a sidebar entry does. Entries carry their intent instead
// of being reached by position, so inserting, moving or renaming an entry
// cannot silently stop routing to the right page.
type navKind int

const (
	navHome navKind = iota
	navDynasty
	navDataset
	navSearch
	navCollections
	navSettings
)

type navItem struct {
	Title   string
	Kind    navKind
	Dynasty model.Dynasty
	Dataset string
}

// iconFor keeps the sidebar icon policy in one place: only the four special
// pages have their own icon, every dynasty and dataset entry shares one.
func iconFor(k navKind) fyne.Resource {
	switch k {
	case navHome:
		return theme.HomeIcon()
	case navSearch:
		return theme.SearchIcon()
	case navCollections:
		return favIcon
	case navSettings:
		return theme.SettingsIcon()
	}
	return theme.DocumentIcon()
}

var navItems = []navItem{
	{Title: "今日推荐", Kind: navHome},
	{Title: "唐诗", Kind: navDynasty, Dynasty: model.DynastyTang},
	{Title: "宋词", Kind: navDynasty, Dynasty: model.DynastySong},
	{Title: "元曲", Kind: navDynasty, Dynasty: model.DynastyYuan},
	{Title: "五代诗词", Kind: navDynasty, Dynasty: model.DynastyFive},
	{Title: "花间集", Kind: navDataset, Dataset: "花间集"},
	{Title: "纳兰性德", Kind: navDataset, Dataset: "纳兰性德"},
	{Title: "四书（部分）", Kind: navDataset, Dataset: "四书（部分）"},
	{Title: "论语", Kind: navDataset, Dataset: "论语"},
	{Title: "诗经", Kind: navDataset, Dataset: "诗经"},
	{Title: "蒙学", Kind: navDataset, Dataset: "蒙学"},
	{Title: "楚辞", Kind: navDataset, Dataset: "楚辞"},
	{Title: "曹操诗集", Kind: navDataset, Dataset: "曹操诗集"},
	{Title: "幽梦影", Kind: navDataset, Dataset: "幽梦影"},
	{Title: "水墨唐诗", Kind: navDataset, Dataset: "水墨唐诗"},
	{Title: "御定全唐詩", Kind: navDataset, Dataset: "御定全唐詩"},
	{Title: "维基文库", Kind: navDataset, Dataset: "维基文库"},
	{Title: "搜索", Kind: navSearch},
	{Title: "收藏", Kind: navCollections},
	{Title: "设置", Kind: navSettings},
}

// navAt returns the entry at id. Out-of-range ids report ok=false rather than
// panicking, so a stale currentNav degrades to a no-op.
func navAt(id int) (navItem, bool) {
	if id < 0 || id >= len(navItems) {
		return navItem{}, false
	}
	return navItems[id], true
}

// navIndex is the reverse lookup the jump buttons use. An unknown title yields
// -1, which navAt turns into a no-op.
func navIndex(title string) int {
	for i, it := range navItems {
		if it.Title == title {
			return i
		}
	}
	return -1
}

// 列表取数上限集中在这一处。同一语义的上限散落在各调用点时，改一页忘一页
// 就会让浏览、收藏和导出可见的数据量悄悄不一致，且没有一处能看出「这是同一
// 个策略」。
const (
	browsePageSize  = 200   // 按朝代浏览的第一页
	datasetPageSize = 500   // 按数据集浏览的第一页
	auxPageSize     = 1000  // 作者列表 / 书目列表 / 单作者或单书目的明细
	collectionLimit = 1000  // 收藏页可见上限
	exportLimit     = 10000 // 导出用：接近全量
)

func NewAppUI(a fyne.App, w fyne.Window, dataDir string) *AppUI {
	s, err := store.New(dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open store: %v\n", err)
		os.Exit(1)
	}

	ui := &AppUI{
		app:       a,
		window:    w,
		store:     s,
		dataDir:   dataDir,
		fontSize:  s.GetFontSize(),
		themeName: s.GetTheme(),
	}
	ui.applyTheme()
	ui.buildUI()
	ui.loadHome()
	return ui
}

func (ui *AppUI) applyTheme() {
	if ui.themeName == "dark" {
		ui.app.Settings().SetTheme(theme.DarkTheme())
	} else {
		ui.app.Settings().SetTheme(theme.LightTheme())
	}
}

func (ui *AppUI) toggleTheme() {
	if ui.themeName == "light" {
		ui.themeName = "dark"
	} else {
		ui.themeName = "light"
	}
	ui.applyTheme()
	_ = ui.store.SetTheme(ui.themeName)
	ui.refreshCurrentView()
}

func (ui *AppUI) refreshCurrentView() {
	ui.navigateTo(ui.currentNav)
}

func (ui *AppUI) navigateTo(id int) {
	ui.currentNav = id
	it, ok := navAt(id)
	if !ok {
		return
	}
	switch it.Kind {
	case navHome:
		ui.loadHome()
	case navDynasty:
		ui.loadBrowse(it.Dynasty)
	case navDataset:
		ui.loadDatasetByName(it.Dataset)
	case navSearch:
		ui.loadSearch()
	case navCollections:
		ui.loadCollections()
	case navSettings:
		ui.loadSettings()
	}
}

// isOn reports whether the sidebar is currently showing a page of kind k.
func (ui *AppUI) isOn(k navKind) bool {
	it, ok := navAt(ui.currentNav)
	return ok && it.Kind == k
}

func (ui *AppUI) buildUI() {
	ui.navList = widget.NewList(
		func() int { return len(navItems) },
		func() fyne.CanvasObject {
			return container.NewHBox(
				widget.NewIcon(theme.DocumentIcon()),
				widget.NewLabel("Template"),
			)
		},
		func(id widget.ListItemID, item fyne.CanvasObject) {
			box := item.(*fyne.Container)
			icon := box.Objects[0].(*widget.Icon)
			label := box.Objects[1].(*widget.Label)
			it, ok := navAt(id)
			if !ok {
				return
			}
			icon.SetResource(iconFor(it.Kind))
			label.SetText(it.Title)
		},
	)
	ui.navList.OnSelected = func(id widget.ListItemID) {
		ui.navigateTo(id)
	}

	ui.searchEntry = widget.NewEntry()
	ui.searchEntry.SetPlaceHolder("搜索诗词、作者...")
	ui.searchEntry.OnSubmitted = func(q string) {
		ui.navigateTo(navIndex("维基文库"))
		ui.performSearch(q)
	}

	themeBtn := widget.NewButtonWithIcon("", brightnessIcon, func() {
		ui.toggleTheme()
	})

	clearBtn := widget.NewButtonWithIcon("", theme.ContentClearIcon(), func() {
		ui.searchEntry.SetText("")
	})

	searchBtn := widget.NewButtonWithIcon("", theme.SearchIcon(), func() {
		ui.navigateTo(navIndex("维基文库"))
		ui.performSearch(ui.searchEntry.Text)
	})

	ui.topBar = container.NewBorder(
		nil, nil,
		themeBtn,
		container.NewHBox(searchBtn, clearBtn),
		ui.searchEntry,
	)

	split := container.NewHSplit(
		container.NewPadded(ui.navList),
		container.NewBorder(ui.topBar, nil, nil, nil, container.NewCenter(widget.NewLabel("加载中..."))),
	)
	split.Offset = 0.15
	ui.window.SetContent(split)
	ui.window.Resize(fyne.NewSize(1200, 800))
}

func (ui *AppUI) setContent(obj fyne.CanvasObject) {
	mainArea := container.NewBorder(ui.topBar, nil, nil, nil, obj)
	split := container.NewHSplit(container.NewPadded(ui.navList), mainArea)
	split.Offset = 0.15
	ui.window.SetContent(split)
}

// ---------- Home ----------

func (ui *AppUI) loadHome() {
	ui.poems = nil
	ui.listIndex = 0
	ui.listRebuild = func() { ui.loadHome() }

	poem, err := ui.store.RandomPoem()
	if err != nil || poem == nil {
		poem = &model.Poem{
			Title:   "暂无数据",
			Content: "请先在设置中下载数据。\n点击左侧\"设置\"开始下载诗词数据。",
		}
	}
	ui.setContent(ui.makeReaderView(poem))
}

// ---------- Browse ----------

func (ui *AppUI) loadDatasetByName(name string) {
	ds, ok := model.GetDataset(name)
	if !ok {
		ui.setContent(container.NewCenter(widget.NewLabel("数据集不存在")))
		return
	}
	ui.loadBrowseByDataset(ds)
}

func (ui *AppUI) loadBrowse(dynasty model.Dynasty) {
	ui.selectedDynasty = dynasty
	ui.listRebuild = func() { ui.loadBrowse(dynasty) }
	poems, _ := ui.store.ListPoemsByDynasty(dynasty, browsePageSize, 0)
	ui.poems = poems
	list := ui.makePoemList()

	total, _ := ui.store.PoemCount(dynasty)
	label := widget.NewLabel(fmt.Sprintf("%s 诗词 · 共 %d 首", dynasty, total))
	label.TextStyle = fyne.TextStyle{Bold: true}
	ui.setContent(container.NewBorder(label, nil, nil, nil, list))
}

func (ui *AppUI) loadBrowseByDataset(ds model.Dataset) {
	ui.selectedDynasty = ds.Dynasty
	ui.listRebuild = func() { ui.loadBrowseByDataset(ds) }

	poems, _ := ui.store.ListPoemsByDataset(ds.Dir, datasetPageSize, 0)
	ui.poems = poems
	list := ui.makePoemList()

	var toolbar fyne.CanvasObject
	buttons := []*widget.Button{}
	if len(ui.poems) > 0 && ds.Type != "classic" {
		buttons = append(buttons, widget.NewButtonWithIcon("按作者", theme.ListIcon(), func() {
			ui.loadAuthorList(ds.Dynasty)
		}))
	}
	// 经典类数据集（含维基文库抓取的书）支持按书目分组浏览
	if len(ui.poems) > 0 && ds.Type == "classic" {
		buttons = append(buttons, widget.NewButtonWithIcon("按书目", theme.ListIcon(), func() {
			ui.loadBookList(ds)
		}))
	}
	if len(buttons) == 1 {
		toolbar = buttons[0]
	} else if len(buttons) > 1 {
		items := make([]fyne.CanvasObject, len(buttons))
		for i, b := range buttons {
			items[i] = b
		}
		toolbar = container.NewHBox(items...)
	}

	title := widget.NewLabel(fmt.Sprintf("%s · %d 篇", ds.Name, len(ui.poems)))
	title.TextStyle = fyne.TextStyle{Bold: true}
	top := container.NewBorder(nil, nil, title, toolbar, nil)
	ui.setContent(container.NewBorder(top, nil, nil, nil, list))
}

// loadBookList shows the distinct books of a classic dataset (维基文库 crawl
// output groups chapters under a book name).
func (ui *AppUI) loadBookList(ds model.Dataset) {
	ui.listRebuild = func() { ui.loadBookList(ds) }
	books, _ := ui.store.ListBooksFromDataset(ds.Dir, auxPageSize, 0)

	if len(books) == 0 {
		ui.setContent(container.NewCenter(widget.NewLabel("暂无书目\n\n请先在设置中导入数据")))
		return
	}

	list := widget.NewList(
		func() int { return len(books) },
		func() fyne.CanvasObject {
			return container.NewHBox(
				widget.NewIcon(theme.FolderIcon()),
				widget.NewLabel("Book"),
			)
		},
		func(id widget.ListItemID, item fyne.CanvasObject) {
			if id < len(books) {
				box := item.(*fyne.Container)
				box.Objects[1].(*widget.Label).SetText(books[id])
			}
		},
	)
	list.OnSelected = func(id widget.ListItemID) {
		if id >= 0 && id < len(books) {
			ui.loadBookPoems(ds, books[id])
		}
	}

	backBtn := widget.NewButtonWithIcon("返回", theme.NavigateBackIcon(), func() {
		ui.loadBrowseByDataset(ds)
	})
	label := widget.NewLabel(fmt.Sprintf("%s · 书目 · %d 本", ds.Name, len(books)))
	label.TextStyle = fyne.TextStyle{Bold: true}
	top := container.NewHBox(backBtn, label)
	ui.setContent(container.NewBorder(top, nil, nil, nil, list))
}

func (ui *AppUI) loadBookPoems(ds model.Dataset, book string) {
	ui.listRebuild = func() { ui.loadBookPoems(ds, book) }
	poems, _ := ui.store.ListPoemsByBookInDataset(ds.Dir, book, auxPageSize, 0)
	ui.poems = poems
	list := ui.makePoemList()

	backBtn := widget.NewButtonWithIcon("返回", theme.NavigateBackIcon(), func() {
		ui.loadBookList(ds)
	})
	label := widget.NewLabel(fmt.Sprintf("%s · %d 章", book, len(poems)))
	label.TextStyle = fyne.TextStyle{Bold: true}
	top := container.NewHBox(backBtn, label)
	ui.setContent(container.NewBorder(top, nil, nil, nil, list))
}

func (ui *AppUI) loadAuthorList(dynasty model.Dynasty) {
	ui.listRebuild = func() { ui.loadAuthorList(dynasty) }
	authors, _ := ui.store.ListAuthorsFromPoems(dynasty, auxPageSize, 0)

	list := widget.NewList(
		func() int { return len(authors) },
		func() fyne.CanvasObject { return widget.NewLabel("Author") },
		func(id widget.ListItemID, item fyne.CanvasObject) {
			if id < len(authors) {
				item.(*widget.Label).SetText(authors[id])
			}
		},
	)
	list.OnSelected = func(id widget.ListItemID) {
		if id >= 0 && id < len(authors) {
			ui.selectedAuthor = authors[id]
			ui.loadAuthorPoems(authors[id])
		}
	}

	backBtn := widget.NewButtonWithIcon("返回", theme.NavigateBackIcon(), func() {
		ui.loadBrowse(dynasty)
	})
	label := widget.NewLabel(fmt.Sprintf("%s · 诗人列表 · %d 位", dynasty, len(authors)))
	label.TextStyle = fyne.TextStyle{Bold: true}
	top := container.NewHBox(backBtn, label)
	ui.setContent(container.NewBorder(top, nil, nil, nil, list))
}

func (ui *AppUI) loadAuthorPoems(author string) {
	ui.listRebuild = func() { ui.loadAuthorPoems(author) }
	poems, _ := ui.store.ListPoemsByAuthor(author, auxPageSize, 0)
	ui.poems = poems
	list := ui.makePoemList()

	backBtn := widget.NewButtonWithIcon("返回", theme.NavigateBackIcon(), func() {
		ui.loadAuthorList(ui.selectedDynasty)
	})
	label := widget.NewLabel(fmt.Sprintf("%s · %d 首", author, len(poems)))
	label.TextStyle = fyne.TextStyle{Bold: true}
	top := container.NewHBox(backBtn, label)
	ui.setContent(container.NewBorder(top, nil, nil, nil, list))
}

// ---------- Poem List ----------

func (ui *AppUI) makePoemList() *widget.List {
	list := widget.NewList(
		func() int { return len(ui.poems) },
		func() fyne.CanvasObject {
			return container.NewHBox(
				widget.NewLabel("Title"),
				widget.NewLabel("Preview"),
				widget.NewLabel("Author"),
			)
		},
		func(id widget.ListItemID, item fyne.CanvasObject) {
			if id >= len(ui.poems) {
				return
			}
			box := item.(*fyne.Container)
			title := box.Objects[0].(*widget.Label)
			preview := box.Objects[1].(*widget.Label)
			author := box.Objects[2].(*widget.Label)
			p := ui.poems[id]
			title.SetText(p.Title)
			if p.Author != "" {
				author.SetText(p.Author)
			} else if p.Chapter != "" {
				author.SetText(p.Chapter)
			} else {
				author.SetText("")
			}
			preview.SetText(p.Preview(30))
		},
	)
	list.OnSelected = func(id widget.ListItemID) {
		if id >= 0 && id < len(ui.poems) {
			ui.listIndex = id
			ui.setContent(ui.makeReaderView(&ui.poems[id]))
		}
	}
	return list
}

// ---------- Reader ----------

func (ui *AppUI) makeReaderView(poem *model.Poem) fyne.CanvasObject {
	title := canvas.NewText(poem.Title, theme.ForegroundColor())
	title.TextSize = 24
	title.TextStyle = fyne.TextStyle{Bold: true}
	title.Alignment = fyne.TextAlignCenter

	var subtitleStr string
	if poem.Chapter != "" && poem.Section != "" {
		subtitleStr = fmt.Sprintf("%s · %s", poem.Section, poem.Chapter)
	} else if poem.Chapter != "" {
		subtitleStr = poem.Chapter
	} else if poem.Dynasty != "" {
		subtitleStr = fmt.Sprintf("[%s] %s", poem.Dynasty, poem.Author)
	} else {
		subtitleStr = poem.Author
	}
	if poem.Rhythmic != "" && poem.Rhythmic != poem.Title {
		subtitleStr += " · " + poem.Rhythmic
	}
	subtitle := canvas.NewText(subtitleStr, color.NRGBA{R: 128, G: 128, B: 128, A: 255})
	subtitle.TextSize = 14
	subtitle.Alignment = fyne.TextAlignCenter

	contentBox := container.NewVBox()
	lines := splitLines(poem.Content)
	for _, line := range lines {
		lbl := canvas.NewText(line, theme.ForegroundColor())
		lbl.TextSize = float32(ui.fontSize)
		lbl.Alignment = fyne.TextAlignCenter
		contentBox.Add(lbl)
	}

	var tagBox fyne.CanvasObject
	if poem.Tags != "" {
		tagBox = widget.NewLabel("标签: " + poem.Tags)
	} else {
		tagBox = widget.NewLabel("")
	}

	// Font size controls
	fontSizeLabel := widget.NewLabel(fmt.Sprintf("%dpx", ui.fontSize))
	fontBox := container.NewHBox(
		widget.NewButtonWithIcon("", theme.ContentRemoveIcon(), func() {
			if ui.fontSize > 12 {
				ui.fontSize--
			}
			_ = ui.store.SetFontSize(ui.fontSize)
			ui.setContent(ui.makeReaderView(poem))
		}),
		container.NewCenter(container.NewVBox(
			widget.NewLabel("字号"),
			fontSizeLabel,
		)),
		widget.NewButtonWithIcon("", theme.ContentAddIcon(), func() {
			if ui.fontSize < 48 {
				ui.fontSize++
			}
			_ = ui.store.SetFontSize(ui.fontSize)
			ui.setContent(ui.makeReaderView(poem))
		}),
	)
	fontBox = container.NewCenter(fontBox)

	// Copy button
	copyBtn := widget.NewButtonWithIcon("复制全文", theme.ContentCopyIcon(), func() {
		ui.app.Clipboard().SetContent(formatPoem(poem))
		dialog.ShowInformation("已复制", "诗词内容已复制到剪贴板", ui.window)
	})

	// Navigation
	hasList := len(ui.poems) > 0
	var navBox fyne.CanvasObject
	if hasList {
		prevBtn := widget.NewButtonWithIcon("上一首", theme.NavigateBackIcon(), func() {
			if ui.listIndex > 0 {
				ui.listIndex--
				ui.setContent(ui.makeReaderView(&ui.poems[ui.listIndex]))
			}
		})
		if ui.listIndex == 0 {
			prevBtn.Disable()
		}
		nextBtn := widget.NewButtonWithIcon("下一首", theme.NavigateNextIcon(), func() {
			if ui.listIndex < len(ui.poems)-1 {
				ui.listIndex++
				ui.setContent(ui.makeReaderView(&ui.poems[ui.listIndex]))
			}
		})
		if ui.listIndex >= len(ui.poems)-1 {
			nextBtn.Disable()
		}
		navBox = container.NewCenter(container.NewHBox(prevBtn, nextBtn))
	} else {
		navBox = container.NewCenter(
			widget.NewButtonWithIcon("换一首", theme.ViewRefreshIcon(), func() {
				ui.loadHome()
			}),
		)
	}

	// Favorite button
	collected, _ := ui.store.IsCollected(poem.ID)
	favText := "收藏"
	if collected {
		favText = "取消收藏"
	}
	favBtn := widget.NewButtonWithIcon(favText, favIcon, func() {
		ui.store.ToggleCollection(poem.ID)
		ui.setContent(ui.makeReaderView(poem))
	})

	// Action bar
	var actionBox fyne.CanvasObject
	if hasList && ui.listRebuild != nil {
		backBtn := widget.NewButtonWithIcon("返回列表", theme.NavigateBackIcon(), func() {
			ui.listRebuild()
		})
		actionBox = container.NewHBox(backBtn, favBtn, copyBtn)
	} else {
		actionBox = container.NewHBox(favBtn, copyBtn)
	}

	centerBox := container.NewVBox(
		container.NewPadded(title),
		subtitle,
		widget.NewSeparator(),
		contentBox,
		widget.NewSeparator(),
		tagBox,
		widget.NewSeparator(),
		container.NewPadded(navBox),
		widget.NewSeparator(),
		container.NewPadded(fontBox),
		widget.NewSeparator(),
		container.NewPadded(actionBox),
	)

	return container.NewScroll(centerBox)
}

// ---------- Search ----------

func (ui *AppUI) loadSearch() {
	ui.poems = nil
	ui.listRebuild = func() { ui.loadSearch() }

	var items []fyne.CanvasObject

	entry := widget.NewEntry()
	entry.SetPlaceHolder("输入关键词，按回车搜索...")
	entry.OnSubmitted = func(q string) {
		ui.performSearch(q)
	}
	searchBtn := widget.NewButtonWithIcon("搜索", theme.SearchIcon(), func() {
		ui.performSearch(entry.Text)
	})
	items = append(items, container.NewBorder(nil, nil, nil, searchBtn, entry))
	items = append(items, widget.NewSeparator())

	history, _ := ui.store.ListSearchHistory()
	histLabel := widget.NewLabel("搜索历史")
	histLabel.TextStyle = fyne.TextStyle{Bold: true}
	items = append(items, histLabel)

	if len(history) > 0 {
		histBox := container.NewVBox()
		for _, h := range history {
			h := h
			histBox.Add(widget.NewButtonWithIcon(h, theme.SearchIcon(), func() {
				ui.performSearch(h)
			}))
		}
		items = append(items, histBox)
		clearBtn := widget.NewButtonWithIcon("清空搜索历史", theme.DeleteIcon(), func() {
			_ = ui.store.ClearSearchHistory()
			ui.loadSearch()
		})
		items = append(items, container.NewCenter(clearBtn))
	} else {
		items = append(items, widget.NewLabel("暂无搜索历史"))
	}

	ui.setContent(container.NewScroll(container.NewVBox(items...)))
}

func (ui *AppUI) performSearch(q string) {
	q = strings.TrimSpace(q)
	if q == "" {
		return
	}
	_ = ui.store.AddSearchHistory(q)
	poems, err := ui.store.SearchPoems(q, 200, 0)
	if err != nil {
		dialog.ShowError(err, ui.window)
		return
	}
	ui.poems = poems
	ui.listRebuild = func() { ui.performSearch(q) }
	list := ui.makePoemList()

	resultLabel := widget.NewLabel(fmt.Sprintf("\"%s\" · 搜索结果: %d 首", q, len(poems)))
	resultLabel.TextStyle = fyne.TextStyle{Bold: true}

	backBtn := widget.NewButtonWithIcon("返回搜索", theme.NavigateBackIcon(), func() {
		ui.loadSearch()
	})
	top := container.NewHBox(backBtn, resultLabel)

	ui.setContent(container.NewBorder(top, nil, nil, nil, list))
}

// ---------- Collections ----------

func (ui *AppUI) loadCollections() {
	ui.listRebuild = func() { ui.loadCollections() }
	ui.poems = nil

	poems, _ := ui.store.ListCollections(collectionLimit, 0)
	ui.poems = poems

	if len(poems) == 0 {
		items := []fyne.CanvasObject{
			widget.NewLabel("暂无收藏\n\n在诗词阅读页面点击\"收藏\"按钮即可收藏诗词"),
			widget.NewSeparator(),
			widget.NewButtonWithIcon("浏览诗词", theme.HomeIcon(), func() {
				ui.navigateTo(navIndex("今日推荐"))
			}),
		}
		ui.setContent(container.NewScroll(container.NewVBox(items...)))
		return
	}

	list := ui.makePoemList()

	exportBtn := widget.NewButtonWithIcon("导出收藏", theme.DocumentSaveIcon(), func() {
		ui.exportCollections()
	})
	deleteAllBtn := widget.NewButtonWithIcon("清空收藏", theme.DeleteIcon(), func() {
		dialog.ShowConfirm("确认清空所有收藏？", "此操作不可恢复。", func(confirmed bool) {
			if !confirmed {
				return
			}
			for _, p := range poems {
				ui.store.ToggleCollection(p.ID)
			}
			ui.loadCollections()
		}, ui.window)
	})

	label := widget.NewLabel(fmt.Sprintf("我的收藏 · %d 首", len(poems)))
	label.TextStyle = fyne.TextStyle{Bold: true}

	ui.setContent(container.NewBorder(label, container.NewHBox(exportBtn, deleteAllBtn), nil, nil, list))
}

func (ui *AppUI) exportCollections() {
	poems, _ := ui.store.ListCollections(exportLimit, 0)
	if len(poems) == 0 {
		dialog.ShowInformation("提示", "暂无收藏，无法导出", ui.window)
		return
	}

	var sb strings.Builder
	sb.WriteString("中华诗词收藏\n")
	sb.WriteString(strings.Repeat("=", 40) + "\n\n")
	for _, p := range poems {
		sb.WriteString(formatPoem(&p))
		sb.WriteString("\n\n")
		sb.WriteString(strings.Repeat("-", 40) + "\n\n")
	}

	path := filepath.Join(ui.dataDir, "collections_export.txt")
	err := os.WriteFile(path, []byte(sb.String()), 0644)
	if err != nil {
		dialog.ShowError(err, ui.window)
		return
	}
	dialog.ShowInformation("导出成功", fmt.Sprintf("已导出 %d 首诗词到:\n%s", len(poems), path), ui.window)
}

// ---------- Settings ----------

func (ui *AppUI) loadSettings() {
	ui.poems = nil
	ui.listRebuild = func() { ui.loadSettings() }

	var items []fyne.CanvasObject

	// Display settings
	titleLabel := widget.NewLabel("显示设置")
	titleLabel.TextStyle = fyne.TextStyle{Bold: true}
	items = append(items, titleLabel, widget.NewSeparator())

	// Font size
	fontVal := widget.NewLabel(fmt.Sprintf("%dpx", ui.fontSize))
	items = append(items, container.NewHBox(widget.NewLabel("字体大小"), fontVal))
	items = append(items, container.NewHBox(
		widget.NewButtonWithIcon("", theme.ContentRemoveIcon(), func() {
			if ui.fontSize > 12 {
				ui.fontSize--
			}
			_ = ui.store.SetFontSize(ui.fontSize)
			ui.loadSettings()
		}),
		widget.NewButtonWithIcon("", theme.ContentAddIcon(), func() {
			if ui.fontSize < 48 {
				ui.fontSize++
			}
			_ = ui.store.SetFontSize(ui.fontSize)
			ui.loadSettings()
		}),
	))

	// Theme
	themeText := "亮色"
	if ui.themeName == "dark" {
		themeText = "暗色"
	}
	items = append(items, container.NewHBox(widget.NewLabel("主题"), widget.NewLabel(themeText)))
	items = append(items, container.NewCenter(
		widget.NewButtonWithIcon("切换主题", brightnessIcon, func() {
			ui.toggleTheme()
		}),
	))

	items = append(items, widget.NewSeparator())

	// Data management
	dataLabel := widget.NewLabel("数据管理")
	dataLabel.TextStyle = fyne.TextStyle{Bold: true}
	items = append(items, dataLabel, widget.NewSeparator())

	count, _ := ui.store.PoemCount("")
	items = append(items, widget.NewLabel(fmt.Sprintf("本地诗词总数: %d", count)))

	for _, ds := range model.Datasets {
		ds := ds
		dsCount, _ := ui.store.PoemCountByDataset(ds.Dir)
		action := "下载"
		if ds.AutoDir {
			action = "导入"
		}
		info := widget.NewLabel(fmt.Sprintf("%s: %d 篇", ds.Name, dsCount))
		btn := widget.NewButton(action, func() {
			go ui.downloadDataset(ds)
		})
		items = append(items, container.NewBorder(nil, nil, nil, btn, info))
	}

	// Crawled wikisource books need to be placed into the dataset dir by hand
	items = append(items, widget.NewLabel("维基文库数据请用 tools/crawler 抓取，"+
		"将生成的 JSON 放入数据目录的 \"维基文库\" 文件夹后再导入。"))

	items = append(items, widget.NewSeparator())

	// Download all
	items = append(items, container.NewCenter(
		widget.NewButtonWithIcon("下载全部数据集", theme.DownloadIcon(), func() {
			go func() {
				for _, ds := range model.Datasets {
					fmt.Printf("Downloading %s...\n", ds.Name)
					f := fetcher.New(ui.dataDir, ui.store, nil)
					_ = f.DownloadAndImport(ds)
				}
				fyne.Do(func() {
					if ui.isOn(navSettings) {
						ui.loadSettings()
					}
				})
			}()
		}),
	))

	items = append(items, widget.NewSeparator())

	// Export collections
	items = append(items, container.NewCenter(
		widget.NewButtonWithIcon("导出收藏到文件", theme.DocumentSaveIcon(), func() {
			ui.exportCollections()
		}),
	))

	// Clear search history
	items = append(items, container.NewCenter(
		widget.NewButtonWithIcon("清空搜索历史", theme.DeleteIcon(), func() {
			_ = ui.store.ClearSearchHistory()
			dialog.ShowInformation("已清空", "搜索历史已清空", ui.window)
		}),
	))

	// Reset all data
	resetBtn := widget.NewButtonWithIcon("重置所有数据", theme.DeleteIcon(), func() {
		dialog.ShowConfirm("确认重置所有数据？", "此操作将删除所有已下载的诗词，且无法恢复。", func(confirmed bool) {
			if !confirmed {
				return
			}
			if err := ui.store.ClearAllPoems(); err != nil {
				dialog.ShowError(err, ui.window)
				return
			}
			ui.loadSettings()
		}, ui.window)
	})
	resetBtn.Importance = widget.DangerImportance
	items = append(items, container.NewCenter(resetBtn))

	items = append(items, widget.NewSeparator())

	// Data directory
	items = append(items, widget.NewLabel("数据目录: "+ui.dataDir))
	items = append(items, widget.NewButton("打开数据目录", func() {
		openDirectory(ui.dataDir)
	}))

	items = append(items, widget.NewSeparator())

	// About
	aboutLabel := widget.NewLabel("关于")
	aboutLabel.TextStyle = fyne.TextStyle{Bold: true}
	items = append(items, aboutLabel, widget.NewSeparator())
	items = append(items, widget.NewLabel("中华诗词桌面客户端 v1.0"))
	items = append(items, widget.NewLabel("数据来源: chinese-poetry-simplify"))

	ui.setContent(container.NewScroll(container.NewVBox(items...)))
}

func (ui *AppUI) downloadDataset(ds model.Dataset) {
	f := fetcher.New(ui.dataDir, ui.store, func(p fetcher.Progress) {
		fmt.Printf("[%s] %s %d/%d %s\n", p.Dataset, p.File, p.Current, p.Total, p.Message)
	})
	if err := f.DownloadAndImport(ds); err != nil {
		fmt.Printf("download error: %v\n", err)
	}
	fyne.Do(func() {
		if ui.isOn(navSettings) {
			ui.loadSettings()
		}
	})
}

// ---------- Utils ----------

func openDirectory(path string) {
	switch runtime.GOOS {
	case "windows":
		exec.Command("explorer", path).Start()
	case "darwin":
		exec.Command("open", path).Start()
	default:
		exec.Command("xdg-open", path).Start()
	}
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func formatPoem(p *model.Poem) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("《%s》", p.Title))
	if p.Chapter != "" && p.Section != "" {
		sb.WriteString(fmt.Sprintf("（%s·%s）", p.Section, p.Chapter))
	} else if p.Chapter != "" {
		sb.WriteString(fmt.Sprintf("（%s）", p.Chapter))
	}
	sb.WriteString("\n")
	if p.Dynasty != "" {
		sb.WriteString(fmt.Sprintf("[%s] ", p.Dynasty))
	}
	if p.Author != "" {
		sb.WriteString(p.Author)
	}
	if p.Rhythmic != "" && p.Rhythmic != p.Title {
		sb.WriteString(fmt.Sprintf(" · %s", p.Rhythmic))
	}
	sb.WriteString("\n\n")
	sb.WriteString(p.Content)
	if p.Tags != "" {
		sb.WriteString(fmt.Sprintf("\n\n标签: %s", p.Tags))
	}
	return sb.String()
}
