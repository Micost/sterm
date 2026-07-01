package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/Micost/sterm/pkg/k8s"

	"github.com/gdamore/tcell/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type page int

const (
	pageBrowser page = iota
	pageList
)

type listPageState struct {
	meta     k8s.ResourceMeta
	data     *k8s.TableData
	selected int
	offset   int
	filter   string
	filterOn bool
}

type App struct {
	client *k8s.Client
	screen tcell.Screen
	done   chan struct{}

	// browser page
	resources []k8s.ResourceMeta
	selected  int
	offset    int

	// list page
	list *listPageState

	curr    page
	width   int
	height  int
	bReady  bool
	bErr    error
}

func NewApp(client *k8s.Client) *App {
	return &App{client: client, done: make(chan struct{})}
}

func (a *App) Run() error {
	s, err := tcell.NewScreen()
	if err != nil {
		return fmt.Errorf("new screen: %w", err)
	}
	if err := s.Init(); err != nil {
		return fmt.Errorf("init screen: %w", err)
	}
	a.screen = s
	defer s.Fini()

	a.width, a.height = s.Size()

	style := tcell.StyleDefault.
		Foreground(tcell.ColorWhite).
		Background(tcell.ColorDefault)
	s.SetStyle(style)
	s.Clear()

	go a.loadResources()
	a.eventLoop()
	return nil
}

func (a *App) loadList() {
	ns := ""
	if a.list.meta.Namespaced {
		ns = metav1.NamespaceAll
	}
	data, err := a.client.List(context.Background(), a.list.meta.GVR, ns)
	if err != nil {
		_ = err
		return
	}
	a.list.data = data
	a.list.selected = 0
	a.list.offset = 0
	a.render()
}

func (a *App) loadResources() {
	rr, err := a.client.Discover()
	if err != nil {
		a.bErr = err
	} else {
		a.resources = rr
		a.bReady = true
	}
	a.render()
}

func (a *App) eventLoop() {
	for {
		select {
		case <-a.done:
			return
		default:
		}

		ev := a.screen.PollEvent()
		switch e := ev.(type) {
		case *tcell.EventKey:
			a.handleKey(e)
		case *tcell.EventResize:
			a.width, a.height = a.screen.Size()
			a.screen.Sync()
			a.render()
		}
	}
}

func (a *App) handleKey(e *tcell.EventKey) {
	switch a.curr {
	case pageBrowser:
		a.handleBrowserKey(e)
	case pageList:
		a.handleListKey(e)
	}
	a.render()
}

func (a *App) handleBrowserKey(e *tcell.EventKey) {
	switch e.Key() {
	case tcell.KeyEscape, tcell.KeyCtrlC:
		a.quit()
	case tcell.KeyEnter:
		if !a.bReady || len(a.resources) == 0 {
			return
		}
		r := a.resources[a.selected]
		a.list = &listPageState{meta: r}
		a.curr = pageList
		go a.loadList()
	case tcell.KeyUp:
		if a.selected > 0 {
			a.selected--
		}
	case tcell.KeyDown:
		if a.selected < len(a.resources)-1 {
			a.selected++
		}
	case tcell.KeyPgUp:
		a.selected -= a.visibleRows()
		if a.selected < 0 {
			a.selected = 0
		}
	case tcell.KeyPgDn:
		a.selected += a.visibleRows()
		if a.selected >= len(a.resources) {
			a.selected = len(a.resources) - 1
		}
	case tcell.KeyHome:
		a.selected = 0
	case tcell.KeyEnd:
		a.selected = len(a.resources) - 1
	}
}

func (a *App) handleListKey(e *tcell.EventKey) {
	if a.list == nil || a.list.data == nil {
		return
	}

	if a.list.filterOn {
		a.handleFilterKey(e)
		return
	}

	switch e.Key() {
	case tcell.KeyEscape, tcell.KeyCtrlC:
		a.curr = pageBrowser
		a.list = nil
	case tcell.KeyRune:
		if e.Rune() == '/' {
			a.list.filter = ""
			a.list.filterOn = true
		} else if e.Rune() == 'q' || e.Rune() == 'Q' {
			a.curr = pageBrowser
			a.list = nil
		}
	case tcell.KeyUp:
		rows := a.visibleRows()
		_ = rows
		if a.list.selected > 0 {
			a.list.selected--
		}
	case tcell.KeyDown:
		max := a.filteredCount() - 1
		if a.list.selected < max {
			a.list.selected++
		}
	case tcell.KeyPgUp:
		a.list.selected -= a.visibleRows()
		if a.list.selected < 0 {
			a.list.selected = 0
		}
	case tcell.KeyPgDn:
		a.list.selected += a.visibleRows()
		if max := a.filteredCount() - 1; a.list.selected > max {
			a.list.selected = max
		}
	case tcell.KeyHome:
		a.list.selected = 0
	case tcell.KeyEnd:
		a.list.selected = a.filteredCount() - 1
	}
}

func (a *App) handleFilterKey(e *tcell.EventKey) {
	switch e.Key() {
	case tcell.KeyEscape:
		a.list.filter = ""
		a.list.filterOn = false
		a.list.selected = 0
	case tcell.KeyEnter:
		a.list.filterOn = false
		_ = a.filteredCount()
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if len(a.list.filter) > 0 {
			a.list.filter = a.list.filter[:len(a.list.filter)-1]
		}
		a.list.selected = 0
	case tcell.KeyRune:
		a.list.filter += string(e.Rune())
		a.list.selected = 0
	}
}

func (a *App) filteredCount() int {
	if a.list == nil || a.list.data == nil {
		return 0
	}
	if a.list.filter == "" {
		return len(a.list.data.Rows)
	}
	f := strings.ToLower(a.list.filter)
	count := 0
	for _, row := range a.list.data.Rows {
		if matchesFilter(row.Cells, f) {
			count++
		}
	}
	return count
}

func (a *App) filteredRows() []k8s.TableRow {
	if a.list == nil || a.list.data == nil {
		return nil
	}
	if a.list.filter == "" {
		return a.list.data.Rows
	}
	f := strings.ToLower(a.list.filter)
	var out []k8s.TableRow
	for _, row := range a.list.data.Rows {
		if matchesFilter(row.Cells, f) {
			out = append(out, row)
		}
	}
	return out
}

func matchesFilter(cells []string, filter string) bool {
	for _, c := range cells {
		if strings.Contains(strings.ToLower(c), filter) {
			return true
		}
	}
	return false
}

func (a *App) visibleRows() int {
	return a.height - 3
}

func (a *App) quit() {
	close(a.done)
}

func (a *App) render() {
	a.screen.Clear()

	switch a.curr {
	case pageBrowser:
		a.renderBrowser()
	case pageList:
		a.renderList()
	}

	a.screen.Show()
}

// --- browser ---

func (a *App) renderBrowser() {
	style := tcell.StyleDefault.
		Foreground(tcell.ColorWhite).
		Background(tcell.ColorDefault)

	if !a.bReady {
		msg := "Loading resources..."
		if a.bErr != nil {
			msg = fmt.Sprintf("Error: %v", a.bErr)
		}
		a.drawText(a.width/2-len(msg)/2, a.height/2, msg, style)
		return
	}

	if len(a.resources) == 0 {
		a.drawText(a.width/2-10, a.height/2, "No resources found", style)
		return
	}

	headerStyle := style.Bold(true).Foreground(tcell.ColorAqua)
	a.fillLine(0, 0, ' ', headerStyle)
	a.drawText(1, 0, fmt.Sprintf("%-24s %-30s %-12s %s",
		"RESOURCE", "APIVERSION", "KIND", "NAMESPACED"), headerStyle)

	sepStyle := style.Foreground(tcell.ColorGray)
	a.fillLine(0, 1, '─', sepStyle)

	rows := a.visibleRows()
	if a.selected < a.offset {
		a.offset = a.selected
	}
	if a.selected >= a.offset+rows {
		a.offset = a.selected - rows + 1
	}

	prevCat := ""
	line := 2
	for i := a.offset; i < len(a.resources) && line < a.height-1; i++ {
		r := a.resources[i]

		if r.Category != prevCat && prevCat != "" {
			if line < a.height-1 {
				a.fillLine(0, line, '·', sepStyle)
				line++
			}
		}
		prevCat = r.Category

		if line >= a.height-1 {
			break
		}

		rowStyle := style
		if i == a.selected {
			rowStyle = style.
				Foreground(tcell.ColorBlack).
				Background(tcell.ColorWhite)
		}

		a.fillLine(0, line, ' ', rowStyle)
		ns := "✓"
		if !r.Namespaced {
			ns = "✗"
		}
		a.drawText(1, line, fmt.Sprintf("%-24s %-30s %-12s %s",
			r.Name(), r.APIVersion(), r.Kind, ns), rowStyle)
		line++
	}

	footerStyle := style.Foreground(tcell.ColorGray)
	a.fillLine(0, a.height-1, ' ', footerStyle)
	total := len(a.resources)
	info := fmt.Sprintf(" ↑↓:nav  Enter:list  ESC:quit  [%d/%d]", a.selected+1, total)
	a.drawText(1, a.height-1, info, footerStyle)
}

// --- list ---

func (a *App) renderList() {
	style := tcell.StyleDefault.
		Foreground(tcell.ColorWhite).
		Background(tcell.ColorDefault)

	if a.list == nil || a.list.data == nil {
		msg := fmt.Sprintf("Loading %s...", a.list.meta.Name())
		a.drawText(a.width/2-len(msg)/2, a.height/2, msg, style)
		return
	}

	rows := a.filteredRows()
	_ = rows

	// header
	headerStyle := style.Bold(true).Foreground(tcell.ColorAqua)
	a.fillLine(0, 0, ' ', headerStyle)
	x := 1
	for ci, col := range a.list.data.Columns {
		w := columnWidth(a.list.data.Columns, ci)
		fmtStr := fmt.Sprintf("%%-%ds", w)
		header := fmt.Sprintf(fmtStr, col)
		a.drawText(x, 0, header, headerStyle)
		x += w + 1
	}

	sepStyle := style.Foreground(tcell.ColorGray)
	a.fillLine(0, 1, '─', sepStyle)

	maxRows := a.visibleRows()
	if a.list.selected < a.list.offset {
		a.list.offset = a.list.selected
	}
	if a.list.selected >= a.list.offset+maxRows {
		a.list.offset = a.list.selected - maxRows + 1
	}

	line := 2
	rowIdx := 0
	for i, row := range a.filteredRows() {
		if rowIdx < a.list.offset {
			rowIdx++
			continue
		}
		if line >= a.height-1 {
			break
		}

		rowStyle := style
		if i == a.list.selected {
			rowStyle = style.
				Foreground(tcell.ColorBlack).
				Background(tcell.ColorWhite)
		}

		a.fillLine(0, line, ' ', rowStyle)
		x = 1
		for ci, cell := range row.Cells {
			w := columnWidth(a.list.data.Columns, ci)
			fmtStr := fmt.Sprintf("%%-%ds", w)
			text := fmt.Sprintf(fmtStr, truncate(cell, w))
			a.drawText(x, line, text, rowStyle)
			x += w + 1
		}
		line++
	}

	// footer
	footerStyle := style.Foreground(tcell.ColorGray)
	a.fillLine(0, a.height-1, ' ', footerStyle)

	total := a.filteredCount()
	filterInfo := ""
	if a.list.filterOn {
		filterInfo = fmt.Sprintf(" [/] %s_", a.list.filter)
	} else if a.list.filter != "" {
		filterInfo = fmt.Sprintf(" [/] %s", a.list.filter)
	}

	sel := 0
	if total > 0 {
		sel = a.list.selected + 1
	}
	info := fmt.Sprintf(" ↑↓:nav  /:filter  ESC:back  [%d/%d] %s", sel, total, filterInfo)
	a.drawText(1, a.height-1, info, footerStyle)
}

// --- helpers ---

func columnWidth(cols []string, ci int) int {
	if ci < 0 || ci >= len(cols) {
		return 10
	}
	switch cols[ci] {
	case "NAME":
		return 40
	case "NAMESPACE":
		return 20
	case "KIND":
		return 20
	case "AGE":
		return 8
	case "STATUS":
		return 20
	default:
		return 15
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-2] + ".."
}

func (a *App) fillLine(x, y int, ch rune, style tcell.Style) {
	for i := x; i < a.width; i++ {
		a.screen.SetContent(i, y, ch, nil, style)
	}
}

func (a *App) drawText(x, y int, text string, style tcell.Style) {
	for i, ch := range text {
		a.screen.SetContent(x+i, y, ch, nil, style)
	}
}
