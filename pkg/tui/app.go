package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Micost/sterm/pkg/k8s"

	"github.com/gdamore/tcell/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

type renderEvent struct{}

func (e *renderEvent) When() time.Time { return time.Now() }

type page int

const (
	pageBrowser page = iota
	pageList
	pageDetail
	pageLogs
	pageNamespace
	pageContainerPicker
)

type contPickState struct {
	containers []string
	cursor     int
	action     string
	podRow     k8s.TableRow
}

type listPageState struct {
	meta          k8s.ResourceMeta
	data          *k8s.TableData
	selected      int
	offset        int
	filter        string
	filterOn      bool
	deleteConfirm bool
}

type detailPageState struct {
	obj        *unstructured.Unstructured
	gvr        schema.GroupVersionResource
	namespaced bool
	yamlText   string
	descText   string
	showYAML   bool
	scroll     int
	err        error
}

type logPageState struct {
	ns, podName, container string
	lines                  []string
	maxLines               int
	scroll                 int
	autoScroll             bool
	cancel                 context.CancelFunc
}

type App struct {
	client *k8s.Client
	screen tcell.Screen
	done   chan struct{}

	// browser page
	resources []k8s.ResourceMeta
	selected  int
	offset    int
	recent    []k8s.ResourceMeta

	browserFilter   string
	browserFilterOn bool

	// preview (right pane)
	previewData *k8s.TableData
	previewErr  error

	// list page
	list    *listPageState
	listErr string

	// detail page
	detail *detailPageState

	// log page
	log *logPageState

	// namespace picker
	namespace  string   // "" = all namespaces
	namespaces []string
	nsCursor   int
	nsOffset   int
	nsFilter   string
	nsFilterOn bool
	nsPrevPage page     // page to return to on ESC

	// container picker
	contPick *contPickState

	curr    page
	width   int
	height  int
	bReady  bool
	bErr    error
}

func NewApp(client *k8s.Client) *App {
	return &App{client: client, done: make(chan struct{}), namespace: "default"}
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
		ns = a.namespace
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
	if a.bReady {
		a.loadPreview()
	}
}

func (a *App) addRecent(r k8s.ResourceMeta) {
	if r.Category == "crd" {
		return
	}
	for i, rr := range a.recent {
		if rr.GVR == r.GVR {
			a.recent = append(a.recent[:i], a.recent[i+1:]...)
			break
		}
	}
	a.recent = append([]k8s.ResourceMeta{r}, a.recent...)
	if len(a.recent) > 5 {
		a.recent = a.recent[:5]
	}
}

func (a *App) browserList() []k8s.ResourceMeta {
	if a.browserFilter != "" {
		f := strings.ToLower(a.browserFilter)
		out := make([]k8s.ResourceMeta, 0, len(a.recent)+len(a.resources))
		out = append(out, a.recent...)
		for _, r := range a.resources {
			if strings.Contains(strings.ToLower(r.Kind), f) ||
				strings.Contains(strings.ToLower(r.Name()), f) ||
				strings.Contains(strings.ToLower(r.GVR.Resource), f) {
				out = append(out, r)
			}
		}
		return out
	}
	seen := make(map[schema.GroupVersionResource]bool)
	out := make([]k8s.ResourceMeta, 0, len(a.recent)+len(a.resources))
	for _, r := range a.recent {
		seen[r.GVR] = true
		out = append(out, r)
	}
	for _, r := range a.resources {
		if r.Category == "common" && !seen[r.GVR] {
			out = append(out, r)
		}
	}
	return out
}

func (a *App) resourceAt(idx int) *k8s.ResourceMeta {
	if idx < len(a.recent) {
		return &a.recent[idx]
	}
	idx2 := idx - len(a.recent)
	if idx2 < len(a.resources) {
		return &a.resources[idx2]
	}
	return nil
}

func (a *App) totalResources() int {
	return len(a.recent) + len(a.resources)
}

func (a *App) loadPreview() {
	items := a.browserList()
	if a.selected >= len(items) {
		return
	}
	r := items[a.selected]
	gvr := r.GVR
	ns := ""
	if r.Namespaced {
		ns = a.namespace
	}
	go func() {
		data, err := a.client.List(context.Background(), gvr, ns)
		if err != nil {
			a.previewErr = err
			a.previewData = nil
		} else {
			items := a.browserList()
			if a.selected >= len(items) || items[a.selected].GVR != gvr {
				return
			}
			a.previewData = data
			a.previewErr = nil
		}
		a.screen.PostEvent(&renderEvent{})
	}()
}

func (a *App) enterNamespacePicker() {
	a.nsPrevPage = a.curr
	a.curr = pageNamespace
	a.nsCursor = 0
	a.nsOffset = 0
	a.nsFilter = ""
	a.nsFilterOn = false
	go a.loadNamespaces()
}

func (a *App) pickContainer(row k8s.TableRow, action string) {
	containers := a.client.PodContainers(row.Obj)
	if len(containers) == 0 {
		return
	}
	if len(containers) == 1 {
		if action == "shell" {
			a.execContainerShell(row, containers[0])
		} else {
			a.openContainerLogs(row, containers[0])
		}
		return
	}
	a.contPick = &contPickState{
		containers: containers,
		cursor:     0,
		action:     action,
		podRow:     row,
	}
	a.curr = pageContainerPicker
}

func (a *App) execContainerShell(row k8s.TableRow, container string) {
	ns := row.Obj.GetNamespace()
	pod := row.Obj.GetName()

	a.screen.Suspend()

	cmd := exec.Command("kubectl", "exec", "-it", "-n", ns, pod, "-c", container, "--", "sh")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()

	a.screen.Resume()
}

func (a *App) openContainerLogs(row k8s.TableRow, container string) {
	ns := row.Obj.GetNamespace()
	podName := row.Obj.GetName()

	ctx, cancel := context.WithCancel(context.Background())
	a.log = &logPageState{
		ns:         ns,
		podName:    podName,
		container:  container,
		maxLines:   2000,
		autoScroll: true,
		cancel:     cancel,
	}
	a.curr = pageLogs

	go func() {
		ch, err := a.client.StreamLogs(ctx, ns, podName, container, 200)
		if err != nil {
			a.log.lines = append(a.log.lines, fmt.Sprintf("error: %v", err))
			a.screen.PostEvent(&renderEvent{})
			return
		}
		for line := range ch {
			a.log.lines = append(a.log.lines, line)
			if len(a.log.lines) > a.log.maxLines {
				a.log.lines = a.log.lines[len(a.log.lines)-a.log.maxLines:]
			}
		}
	}()
}

func (a *App) loadNamespaces() {
	nss, err := a.client.Namespaces(context.Background())
	if err != nil {
		return
	}
	a.namespaces = nss
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
		default:
			a.handleNonKey(ev)
		}
	}
}

func (a *App) handleKey(e *tcell.EventKey) {
	if e.Key() == tcell.KeyCtrlQ || e.Key() == tcell.KeyCtrlC {
		a.quit()
		return
	}
	switch a.curr {
	case pageBrowser:
		a.handleBrowserKey(e)
	case pageList:
		a.handleListKey(e)
	case pageDetail:
		a.handleDetailKey(e)
	case pageLogs:
		a.handleLogKey(e)
	case pageNamespace:
		a.handleNamespaceKey(e)
	case pageContainerPicker:
		a.handleContainerPickerKey(e)
	}
	a.render()
}

func (a *App) handleNonKey(ev tcell.Event) {
	switch ev.(type) {
	case *renderEvent:
		a.render()
	}
}

func (a *App) handleBrowserKey(e *tcell.EventKey) {
	if a.browserFilterOn {
		a.handleBrowserFilterKey(e)
		return
	}

	total := a.visibleBrowser()
	switch e.Key() {
	case tcell.KeyEscape:
		// top level, no-op
	case tcell.KeyEnter:
		if !a.bReady || total == 0 {
			return
		}
		items := a.browserList()
		if a.selected >= len(items) {
			return
		}
		r := items[a.selected]
		a.addRecent(r)
		a.list = &listPageState{meta: r}
		a.curr = pageList
		go a.loadList()
	case tcell.KeyUp:
		if a.selected > 0 {
			a.selected--
			a.loadPreview()
		}
	case tcell.KeyDown:
		if a.selected < total-1 {
			a.selected++
			a.loadPreview()
		}
	case tcell.KeyHome:
		a.selected = 0
		a.loadPreview()
	case tcell.KeyEnd:
		a.selected = total - 1
		a.loadPreview()
	case tcell.KeyRune:
		switch e.Rune() {
		case 'n', 'N':
			a.enterNamespacePicker()
		case 'k', 'K':
			if a.selected > 0 {
				a.selected--
				a.loadPreview()
			}
		case 'j', 'J':
			if a.selected < total-1 {
				a.selected++
				a.loadPreview()
			}
		case 'g':
			a.selected = 0
			a.loadPreview()
		case 'G':
			a.selected = total - 1
			a.loadPreview()
		case '/':
			a.browserFilterOn = true
		}
	}
}

func (a *App) handleBrowserFilterKey(e *tcell.EventKey) {
	switch e.Key() {
	case tcell.KeyEscape:
		a.browserFilter = ""
		a.browserFilterOn = false
		a.selected = 0
	case tcell.KeyEnter:
		a.browserFilterOn = false
	case tcell.KeyUp:
		if a.selected > 0 {
			a.selected--
		}
	case tcell.KeyDown:
		items := a.browserList()
		if a.selected < len(items)-1 {
			a.selected++
		}
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if len(a.browserFilter) > 0 {
			a.browserFilter = a.browserFilter[:len(a.browserFilter)-1]
		}
		a.selected = 0
	case tcell.KeyRune:
		switch e.Rune() {
		case 'k', 'K':
			if a.selected > 0 {
				a.selected--
			}
		case 'j', 'J':
			items := a.browserList()
			if a.selected < len(items)-1 {
				a.selected++
			}
		case '/':
			a.browserFilterOn = true
		default:
			a.browserFilter += string(e.Rune())
			a.selected = 0
		}
	}
	a.loadPreview()
}

func (a *App) visibleBrowser() int {
	return len(a.browserList())
}

func (a *App) handleListKey(e *tcell.EventKey) {
	if a.list == nil || a.list.data == nil {
		return
	}
	a.listErr = ""

	if a.list.filterOn {
		a.handleFilterKey(e)
		return
	}

	if a.list.deleteConfirm {
		a.handleDeleteConfirm(e)
		return
	}

	switch e.Key() {
	case tcell.KeyEscape, tcell.KeyCtrlC:
		a.curr = pageBrowser
		a.list = nil
	case tcell.KeyEnter:
		rows := a.filteredRows()
		if a.list.selected >= 0 && a.list.selected < len(rows) {
			row := rows[a.list.selected]
			a.openDetail(row)
		}
	case tcell.KeyRune:
		switch e.Rune() {
		case '/':
			a.list.filter = ""
			a.list.filterOn = true
		case 'q', 'Q':
			a.curr = pageBrowser
			a.list = nil
		case 'x', 'X':
			if len(a.filteredRows()) > 0 {
				a.list.deleteConfirm = true
			}
		case 'l', 'L':
			rows := a.filteredRows()
			if a.list.selected >= 0 && a.list.selected < len(rows) {
				row := rows[a.list.selected]
				if row.Obj.GetKind() == "Pod" {
					a.pickContainer(row, "logs")
				} else {
					a.openLogs(row)
				}
			}
		case 's', 'S':
			rows := a.filteredRows()
			if a.list.selected >= 0 && a.list.selected < len(rows) {
				row := rows[a.list.selected]
				if row.Obj.GetKind() == "Pod" {
					a.pickContainer(row, "shell")
				} else {
					a.execShell(row)
				}
			}
		case 'n', 'N':
			a.enterNamespacePicker()
		case 'd', 'D':
			rows := a.filteredRows()
			if a.list.selected >= 0 && a.list.selected < len(rows) {
				a.openDetailMode(rows[a.list.selected], false)
			}
		case 'y', 'Y':
			rows := a.filteredRows()
			if a.list.selected >= 0 && a.list.selected < len(rows) {
				a.openDetailMode(rows[a.list.selected], true)
			}
		case 'e', 'E':
			rows := a.filteredRows()
			if a.list.selected >= 0 && a.list.selected < len(rows) {
				a.openDetailMode(rows[a.list.selected], true)
				a.editResource()
			}
		case 'k', 'K':
			if a.list.selected > 0 {
				a.list.selected--
			}
		case 'j', 'J':
			max := a.filteredCount() - 1
			if a.list.selected < max {
				a.list.selected++
			}
		case 'g':
			a.list.selected = 0
		case 'G':
			a.list.selected = a.filteredCount() - 1
		}
	case tcell.KeyUp:
		if a.list.selected > 0 {
			a.list.selected--
		}
	case tcell.KeyDown:
		max := a.filteredCount() - 1
		if a.list.selected < max {
			a.list.selected++
		}
	case tcell.KeyPgUp, tcell.KeyCtrlU:
		a.list.selected -= a.visibleRows()
		if a.list.selected < 0 {
			a.list.selected = 0
		}
	case tcell.KeyPgDn, tcell.KeyCtrlD:
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

func (a *App) handleDetailKey(e *tcell.EventKey) {
	if a.detail == nil {
		return
	}

	switch e.Key() {
	case tcell.KeyEscape, tcell.KeyCtrlC:
		a.detail = nil
		a.curr = pageList
	case tcell.KeyUp:
		if a.detail.scroll > 0 {
			a.detail.scroll--
		}
	case tcell.KeyDown:
		max := a.detailLines()
		if a.detail.scroll < max {
			a.detail.scroll++
		}
	case tcell.KeyPgUp, tcell.KeyCtrlU:
		a.detail.scroll -= a.visibleRows()
		if a.detail.scroll < 0 {
			a.detail.scroll = 0
		}
	case tcell.KeyPgDn, tcell.KeyCtrlD:
		a.detail.scroll += a.visibleRows()
		if max := a.detailLines(); a.detail.scroll > max {
			a.detail.scroll = max
		}
	case tcell.KeyHome:
		a.detail.scroll = 0
	case tcell.KeyEnd:
		a.detail.scroll = a.detailLines()
	case tcell.KeyRune:
		switch e.Rune() {
		case 'd', 'D':
			a.detail.showYAML = !a.detail.showYAML
			a.detail.scroll = 0
		case 'e', 'E':
			go a.editResource()
		case 's', 'S':
			go a.execDetail()
		case 'k', 'K':
			if a.detail.scroll > 0 {
				a.detail.scroll--
			}
		case 'j', 'J':
			max := a.detailLines()
			if a.detail.scroll < max {
				a.detail.scroll++
			}
		case 'g':
			a.detail.scroll = 0
		case 'G':
			a.detail.scroll = a.detailLines()
		}
	}
}

func (a *App) detailLines() int {
	if a.detail == nil {
		return 0
	}
	text := a.detail.descText
	if a.detail.showYAML {
		text = a.detail.yamlText
	}
	if text == "" {
		return 0
	}
	lines := 0
	for _, ch := range text {
		if ch == '\n' {
			lines++
		}
	}
	return lines
}

func (a *App) handleDeleteConfirm(e *tcell.EventKey) {
	switch e.Key() {
	case tcell.KeyEscape:
		a.list.deleteConfirm = false
	case tcell.KeyRune:
		switch e.Rune() {
		case 'y', 'Y':
			a.list.deleteConfirm = false
			go a.doDelete()
		case 'n', 'N':
			a.list.deleteConfirm = false
		}
	}
}

func (a *App) doDelete() {
	gvr := a.list.meta.GVR
	rows := a.filteredRows()
	if a.list.selected < 0 || a.list.selected >= len(rows) {
		return
	}
	row := rows[a.list.selected]
	ns := row.Obj.GetNamespace()
	name := row.Obj.GetName()

	if err := a.client.Delete(context.Background(), gvr, ns, name); err != nil {
		_ = err
	}
	a.loadList()
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

func (a *App) openDetail(row k8s.TableRow) {
	a.openDetailMode(row, true)
}

func (a *App) openDetailMode(row k8s.TableRow, showYAML bool) {
	yamlText, err := k8s.ToYAML(row.Obj)
	if err != nil {
		a.detail = &detailPageState{err: err}
		a.curr = pageDetail
		return
	}

	a.detail = &detailPageState{
		obj:        row.Obj,
		gvr:        a.list.meta.GVR,
		namespaced: a.list.meta.Namespaced,
		yamlText:   yamlText,
		descText:   k8s.Describe(row.Obj),
		showYAML:   showYAML,
	}
	a.curr = pageDetail
}

func (a *App) openLogs(row k8s.TableRow) {
	ns := row.Obj.GetNamespace()
	podName := row.Obj.GetName()
	containers := a.client.PodContainers(row.Obj)
	if len(containers) == 0 {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.log = &logPageState{
		ns:         ns,
		podName:    podName,
		container:  containers[0],
		maxLines:   2000,
		autoScroll: true,
		cancel:     cancel,
	}
	a.curr = pageLogs

	go func() {
		ch, err := a.client.StreamLogs(ctx, ns, podName, containers[0], 200)
		if err != nil {
			a.log.lines = append(a.log.lines, fmt.Sprintf("error: %v", err))
			a.screen.PostEvent(&renderEvent{})
			return
		}
		for line := range ch {
			a.log.lines = append(a.log.lines, line)
			if len(a.log.lines) > a.log.maxLines {
				a.log.lines = a.log.lines[len(a.log.lines)-a.log.maxLines:]
			}
			a.screen.PostEvent(&renderEvent{})
		}
	}()
}

func (a *App) handleLogKey(e *tcell.EventKey) {
	if a.log == nil {
		return
	}

	switch e.Key() {
	case tcell.KeyEscape, tcell.KeyCtrlC:
		a.log.cancel()
		a.log = nil
		a.curr = pageList
	case tcell.KeyUp:
		a.log.autoScroll = false
		if a.log.scroll > 0 {
			a.log.scroll--
		}
	case tcell.KeyDown:
		contentLines := a.height - 3
		max := len(a.log.lines) - contentLines
		if a.log.scroll < max {
			a.log.scroll++
		} else {
			a.log.autoScroll = true
		}
	case tcell.KeyPgUp, tcell.KeyCtrlU:
		a.log.autoScroll = false
		a.log.scroll -= a.visibleRows()
		if a.log.scroll < 0 {
			a.log.scroll = 0
		}
	case tcell.KeyPgDn, tcell.KeyCtrlD:
		a.log.scroll += a.visibleRows()
		contentLines := a.height - 3
		if max := len(a.log.lines) - contentLines; a.log.scroll >= max {
			a.log.scroll = max
			a.log.autoScroll = true
		}
	case tcell.KeyHome:
		a.log.autoScroll = false
		a.log.scroll = 0
	case tcell.KeyEnd:
		a.log.autoScroll = true
	case tcell.KeyRune:
		switch e.Rune() {
		case 'k', 'K':
			a.log.autoScroll = false
			if a.log.scroll > 0 {
				a.log.scroll--
			}
		case 'j', 'J':
			contentLines := a.height - 3
			max := len(a.log.lines) - contentLines
			if a.log.scroll < max {
				a.log.scroll++
			} else {
				a.log.autoScroll = true
			}
		case 'g':
			a.log.autoScroll = false
			a.log.scroll = 0
		case 'G':
			a.log.autoScroll = true
		}
	}
}

func (a *App) renderLogs() {
	style := tcell.StyleDefault.
		Foreground(tcell.ColorWhite).
		Background(tcell.ColorDefault)

	if a.log == nil {
		return
	}

	// header
	headerStyle := style.Bold(true).Foreground(tcell.ColorAqua)
	title := fmt.Sprintf(" %s/%s c:%s", a.log.ns, a.log.podName, a.log.container)
	a.fillLine(0, 0, ' ', headerStyle)
	a.drawText(1, 0, title, headerStyle)

	sepStyle := style.Foreground(tcell.ColorGray)
	a.fillLine(0, 1, '─', sepStyle)

	// adjust scroll for auto-follow
	contentLines := a.height - 3
	if a.log.autoScroll {
		a.log.scroll = len(a.log.lines) - contentLines
		if a.log.scroll < 0 {
			a.log.scroll = 0
		}
	}

	// render lines
	for line := 0; line < contentLines && line+a.log.scroll < len(a.log.lines); line++ {
		idx := line + a.log.scroll
		text := a.log.lines[idx]
		if len(text) > a.width {
			text = text[:a.width]
		}
		a.drawText(0, 2+line, text, style)
	}

	// footer
	footerStyle := style.Foreground(tcell.ColorGray)
	a.fillLine(0, a.height-1, ' ', footerStyle)

	total := len(a.log.lines)
	follow := " [F]"
	if !a.log.autoScroll {
		follow = ""
	}
	info := fmt.Sprintf(" ↑↓:scroll  End:follow  ESC:back  lines:%d%s", total, follow)
	if len(a.log.lines) == 0 {
		info = " Waiting for logs..."
	}
	a.drawText(1, a.height-1, info, footerStyle)
}

func (a *App) execShell(row k8s.TableRow) {
	ns := row.Obj.GetNamespace()
	pod := row.Obj.GetName()
	containers := a.client.PodContainers(row.Obj)
	if len(containers) == 0 {
		return
	}

	a.screen.Suspend()

	cmd := exec.Command("kubectl", "exec", "-it", "-n", ns, pod, "-c", containers[0], "--", "sh")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()

	a.screen.Resume()
	a.render()
}

func (a *App) execDetail() {
	if a.detail == nil || a.detail.obj == nil {
		return
	}
	row := k8s.TableRow{Obj: a.detail.obj}
	a.execShell(row)
}

func (a *App) editResource() {
	a.screen.Suspend()

	tmp, err := os.CreateTemp("", "sterm-*.yaml")
	if err != nil {
		a.screen.Resume()
		return
	}
	tmpPath := tmp.Name()
	tmp.WriteString(a.detail.yamlText)
	tmp.Close()
	defer os.Remove(tmpPath)

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	cmd := exec.Command(editor, tmpPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()

	a.screen.Resume()

	raw, err := os.ReadFile(tmpPath)
	if err != nil {
		return
	}

	var updated map[string]interface{}
	if err := yaml.Unmarshal(raw, &updated); err != nil {
		return
	}

	updatedObj := &unstructured.Unstructured{Object: updated}
	if err := a.client.Update(context.Background(), a.detail.gvr, updatedObj); err != nil {
		a.listErr = err.Error()
		a.curr = pageList
		a.render()
		return
	}

	a.listErr = ""
	a.curr = pageList
	if a.list != nil {
		go a.loadList()
	}
	a.render()
}

func (a *App) handleNamespaceKey(e *tcell.EventKey) {
	if a.nsFilterOn {
		switch e.Key() {
		case tcell.KeyEscape:
			a.nsFilter = ""
			a.nsFilterOn = false
			a.nsCursor = 0
		case tcell.KeyEnter:
			a.nsFilterOn = false
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			if len(a.nsFilter) > 0 {
				a.nsFilter = a.nsFilter[:len(a.nsFilter)-1]
			}
			a.nsCursor = 0
		case tcell.KeyRune:
			a.nsFilter += string(e.Rune())
			a.nsCursor = 0
		}
		return
	}

	switch e.Key() {
	case tcell.KeyEscape, tcell.KeyCtrlC:
		a.curr = a.nsPrevPage
	case tcell.KeyEnter:
		items := a.nsFilteredItems()
		if a.nsCursor >= 0 && a.nsCursor < len(items) {
			a.namespace = items[a.nsCursor]
			a.curr = a.nsPrevPage
			if a.curr == pageList {
				go a.loadList()
			} else if a.curr == pageBrowser {
				go a.loadPreview()
			}
		}
	case tcell.KeyUp:
		if a.nsCursor > 0 {
			a.nsCursor--
		}
	case tcell.KeyDown:
		if a.nsCursor < a.nsFilteredCount()-1 {
			a.nsCursor++
		}
	case tcell.KeyPgUp, tcell.KeyCtrlU:
		a.nsCursor -= a.visibleRows()
		if a.nsCursor < 0 {
			a.nsCursor = 0
		}
	case tcell.KeyPgDn, tcell.KeyCtrlD:
		a.nsCursor += a.visibleRows()
		if max := a.nsFilteredCount() - 1; a.nsCursor > max {
			a.nsCursor = max
		}
	case tcell.KeyHome:
		a.nsCursor = 0
	case tcell.KeyEnd:
		a.nsCursor = a.nsFilteredCount() - 1
	case tcell.KeyRune:
		switch e.Rune() {
		case '/':
			a.nsFilter = ""
			a.nsFilterOn = true
		case 'k', 'K':
			if a.nsCursor > 0 {
				a.nsCursor--
			}
		case 'j', 'J':
			if a.nsCursor < a.nsFilteredCount()-1 {
				a.nsCursor++
			}
		case 'g':
			a.nsCursor = 0
		case 'G':
			a.nsCursor = a.nsFilteredCount() - 1
		}
	}
}

func (a *App) nsFilteredCount() int {
	if a.nsFilter == "" {
		return len(a.namespaces) + 1 // +1 for "all"
	}
	f := strings.ToLower(a.nsFilter)
	count := 1 // "all" always shown
	for _, ns := range a.namespaces {
		if strings.Contains(strings.ToLower(ns), f) {
			count++
		}
	}
	return count
}

func (a *App) nsFilteredItems() []string {
	items := []string{""} // "all"
	if a.nsFilter == "" {
		items = append(items, a.namespaces...)
		return items
	}
	f := strings.ToLower(a.nsFilter)
	for _, ns := range a.namespaces {
		if strings.Contains(strings.ToLower(ns), f) {
			items = append(items, ns)
		}
	}
	return items
}

func (a *App) nsDisplayName(ns string) string {
	if ns == "" {
		return "all"
	}
	return ns
}

func (a *App) quit() {
	close(a.done)
}

func (a *App) render() {
	a.screen.Clear()

	a.height--

	switch a.curr {
	case pageBrowser:
		a.renderBrowser()
	case pageList:
		a.renderList()
	case pageDetail:
		a.renderDetail()
	case pageLogs:
		a.renderLogs()
	case pageNamespace:
		a.renderNamespace()
	case pageContainerPicker:
		a.renderContainerPicker()
	}

	a.height++
	a.screen.Show()
}

// --- namespace picker ---

func (a *App) renderNamespace() {
	style := tcell.StyleDefault.
		Foreground(tcell.ColorWhite).
		Background(tcell.ColorDefault)

	// header
	headerStyle := style.Bold(true).Foreground(tcell.ColorAqua)
	a.fillLine(0, 0, ' ', headerStyle)
	title := " NAMESPACES"
	if a.nsFilterOn {
		title = fmt.Sprintf(" [/] %s_", a.nsFilter)
	} else if a.nsFilter != "" {
		title = fmt.Sprintf(" [/] %s", a.nsFilter)
	}
	a.drawText(1, 0, title, headerStyle)

	sepStyle := style.Foreground(tcell.ColorGray)
	a.fillLine(0, 1, '─', sepStyle)

	items := a.nsFilteredItems()
	maxRows := a.visibleRows()

	if a.nsCursor < a.nsOffset {
		a.nsOffset = a.nsCursor
	}
	if a.nsCursor >= a.nsOffset+maxRows {
		a.nsOffset = a.nsCursor - maxRows + 1
	}

	prevCat := ""
	line := 2
	for i := a.nsOffset; i < len(items) && line < a.height-1; i++ {
		ns := items[i]
		display := a.nsDisplayName(ns)

		cat := "namespaces"
		if ns == "" {
			cat = "all"
		}
		if cat != prevCat && prevCat != "" {
			if line < a.height-1 {
				a.fillLine(0, line, '·', sepStyle)
				line++
			}
		}
		prevCat = cat

		if line >= a.height-1 {
			break
		}

		rowStyle := style
		if i == a.nsCursor {
			rowStyle = style.
				Foreground(tcell.ColorBlack).
				Background(tcell.ColorWhite)
		}

		a.fillLine(0, line, ' ', rowStyle)
		mark := " "
		if ns == a.namespace {
			mark = "✓"
		}
		a.drawText(1, line, fmt.Sprintf("%s %-50s", mark, display), rowStyle)
		line++
	}

	// footer
	footerStyle := style.Foreground(tcell.ColorGray)
	a.fillLine(0, a.height-1, ' ', footerStyle)
	info := fmt.Sprintf(" ↑↓:nav  Enter:select  /:filter  ESC:back  [%d/%d]", a.nsCursor+1, len(items))
	a.drawText(1, a.height-1, info, footerStyle)
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

	items := a.browserList()
	total := len(items)
	if total > 0 && a.selected >= total {
		a.selected = total - 1
	}

	// split layout
	sepCol := a.width / 2
	leftW := sepCol - 1
	rightW := a.width - sepCol - 1
	nsW := 3

	// compute dynamic column widths
	resW := 12
	for _, r := range items {
		if len(r.GVR.Resource) > resW {
			resW = len(r.GVR.Resource)
		}
	}
	resW += 3
	shortW := leftW - resW - nsW - 3 // remaining, leaves 1 space before separator
	if shortW < 3 {
		shortW = 3
	}

	nameW := rightW - 16

	headerStyle := style.Bold(true).Foreground(tcell.ColorAqua)
	a.fillLine(0, 0, ' ', headerStyle)
	title := fmt.Sprintf("%-*s %*s %*s", resW, "RESOURCE", shortW, "SHORT", nsW, "NS")
	if a.browserFilterOn {
		title = fmt.Sprintf(" [/] %s_", a.browserFilter)
	} else if a.browserFilter != "" {
		title = fmt.Sprintf(" [/] %s", a.browserFilter)
	}
	a.drawText(1, 0, title, headerStyle)
	if nameW < 10 {
		nameW = rightW
	}
	if nameW >= 10 {
		a.drawText(sepCol+1, 0, fmt.Sprintf("%-*s %s", nameW, "NAME", "STATUS"), headerStyle)
	} else {
		a.drawText(sepCol+1, 0, "NAME", headerStyle)
	}

	sepStyle := style.Foreground(tcell.ColorGray)
	a.fillLine(0, 1, '─', sepStyle)
	// vertical separator full height
	for y := 0; y < a.height-1; y++ {
		a.screen.SetContent(sepCol, y, '│', nil, sepStyle)
	}
	// namespace indicator on right side header
	nsLabel := fmt.Sprintf(" ns: %s ", a.nsDisplayName(a.namespace))
	a.drawText(sepCol+1, 1, nsLabel, style)

	a.offset = 0

	helpKeys := []string{"j/k", "Enter", "/", "n", "Ctrl+Q"}
	helpDesc := []string{"Navigate", "List resources", "Search", "Namespace", "Quit"}
	contentRows := a.height - 2 - len(helpKeys) - 1 // header+sep, help, footer
	if contentRows < 3 {
		contentRows = 3
	}

	line := 2
	maxItems := contentRows
	if total < maxItems {
		maxItems = total
	}
	for i := 0; i < maxItems && line <= 2+contentRows; i++ {
		r := &items[i]

		rowStyle := style
		if i == a.selected {
			rowStyle = style.
				Foreground(tcell.ColorBlack).
				Background(tcell.ColorWhite)
		}

		a.fillLineTo(0, line, sepCol, ' ', rowStyle)
		ns := "✓"
		if !r.Namespaced {
			ns = "✗"
		}
		a.drawText(1, line, fmt.Sprintf("%-*s %*s %*s", resW, r.GVR.Resource, shortW, r.ShortName, nsW, ns), rowStyle)
		line++
	}

	// right pane: clear then draw preview (same height as left)
	for y := 2; y < 2+contentRows; y++ {
		a.fillLineTo(sepCol+1, y, a.width, ' ', style)
	}
	if a.previewData != nil && rightW >= 10 {
		maxRows := contentRows
		for i := 0; i < maxRows && i < len(a.previewData.Rows); i++ {
			row := a.previewData.Rows[i]
			rowY := 2 + i
			name := truncate(row.Cells[0], nameW)
			a.drawText(sepCol+1, rowY, name, style)
			if nameW < rightW {
				st := ""
				if len(row.Cells) >= 5 {
					st = truncate(row.Cells[4], rightW-nameW-1)
				}
				a.drawText(sepCol+1+nameW+1, rowY, st, style)
			}
		}
	} else if a.previewData == nil && a.previewErr == nil && rightW >= 10 {
		if a.selected < maxItems {
			r := items[a.selected]
			a.drawText(sepCol+2, 2, fmt.Sprintf("Loading %s...", r.Name()), style)
		}
	}

	// help text on left, blank on right
	helpStyle := style.Foreground(tcell.ColorGray)
	for hi := range helpKeys {
		y := 2 + contentRows + hi
		a.fillLineTo(0, y, sepCol, ' ', helpStyle)
		a.drawText(1, y, fmt.Sprintf("  %-12s %s", helpKeys[hi], helpDesc[hi]), helpStyle)
		a.fillLineTo(sepCol+1, y, a.width, ' ', style)
	}

	footerStyle := style.Foreground(tcell.ColorGray)
	a.fillLine(0, a.height-1, ' ', footerStyle)
	ns := a.nsDisplayName(a.namespace)
	filterInfo := ""
	if a.browserFilterOn {
		filterInfo = fmt.Sprintf(" [/] %s_", a.browserFilter)
	} else if a.browserFilter != "" {
		filterInfo = fmt.Sprintf(" [/] %s", a.browserFilter)
	}
	info := fmt.Sprintf(" ↑↓:nav  Enter:list  n:ns(%s)  /:search  ESC:quit%s", ns, filterInfo)
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

	colWs := make([]int, len(a.list.data.Columns))
	for ci, col := range a.list.data.Columns {
		colWs[ci] = len(col)
	}
	for _, row := range rows {
		for ci, cell := range row.Cells {
			if ci < len(colWs) && len(cell) > colWs[ci] {
				colWs[ci] = len(cell)
			}
		}
	}
	for ci := range colWs {
		minW := columnWidth(a.list.data.Columns, ci)
		if colWs[ci] < minW {
			colWs[ci] = minW
		}
		colWs[ci] += 2
	}

	// header
	headerStyle := style.Bold(true).Foreground(tcell.ColorAqua)
	a.fillLine(0, 0, ' ', headerStyle)
	x := 1
	for ci, col := range a.list.data.Columns {
		w := colWs[ci]
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
			w := colWs[ci]
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
	var info string
	if a.list.deleteConfirm {
		row := a.filteredRows()
		name := "?"
		if a.list.selected >= 0 && a.list.selected < len(row) {
			name = row[a.list.selected].Cells[0]
		}
		info = fmt.Sprintf(" Delete %s? (y/N)", name)
	} else {
		ns := a.nsDisplayName(a.namespace)
		info = fmt.Sprintf(" d:desc  y:yaml  e:edit  s:shell  l:logs  x:del  n:ns(%s)  ESC:back  [%d/%d] %s", ns, sel, total, filterInfo)
	}
	if a.listErr != "" {
		errStyle := style.Foreground(tcell.ColorRed)
		a.drawText(1, a.height-2, a.listErr, errStyle)
		a.drawText(1, a.height-1, info, footerStyle)
	} else {
		a.drawText(1, a.height-1, info, footerStyle)
	}
}

// --- detail ---

func (a *App) renderDetail() {
	style := tcell.StyleDefault.
		Foreground(tcell.ColorWhite).
		Background(tcell.ColorDefault)

	if a.detail == nil || a.detail.err != nil {
		msg := "Error loading detail"
		if a.detail != nil && a.detail.err != nil {
			msg = fmt.Sprintf("Error: %v", a.detail.err)
		}
		a.drawText(a.width/2-len(msg)/2, a.height/2, msg, style)
		return
	}

	text := a.detail.descText
	mode := "DESCRIBE"
	if a.detail.showYAML {
		text = a.detail.yamlText
		mode = "YAML"
	}

	lines := splitLines(text)

	// header
	headerStyle := style.Bold(true).Foreground(tcell.ColorAqua)
	name := a.detail.obj.GetName()
	kind := a.detail.obj.GetKind()
	title := fmt.Sprintf(" %s/%s [%s]  d:toggle", kind, name, mode)
	a.fillLine(0, 0, ' ', headerStyle)
	a.drawText(1, 0, title, headerStyle)

	sepStyle := style.Foreground(tcell.ColorGray)
	a.fillLine(0, 1, '─', sepStyle)

	// content area
	contentLines := a.height - 3

	if a.detail.scroll > len(lines)-contentLines {
		a.detail.scroll = len(lines) - contentLines
	}
	if a.detail.scroll < 0 {
		a.detail.scroll = 0
	}

	for line := 0; line < contentLines && line+a.detail.scroll < len(lines); line++ {
		idx := line + a.detail.scroll
		a.drawText(0, 2+line, lines[idx], style)
	}

	// footer
	footerStyle := style.Foreground(tcell.ColorGray)
	a.fillLine(0, a.height-1, ' ', footerStyle)

	total := len(lines)
	info := fmt.Sprintf(" ↑↓:nav  d:toggle  s:shell  ESC:back  [%d/%d]", a.detail.scroll+1, total)
	a.drawText(1, a.height-1, info, footerStyle)
}

// --- container picker ---

func (a *App) renderContainerPicker() {
	style := tcell.StyleDefault.
		Foreground(tcell.ColorWhite).
		Background(tcell.ColorDefault)

	if a.contPick == nil {
		return
	}

	headerStyle := style.Bold(true).Foreground(tcell.ColorAqua)
	a.fillLine(0, 0, ' ', headerStyle)
	a.drawText(1, 0, fmt.Sprintf(" CONTAINERS  %s: %s/%s", a.contPick.action, a.contPick.podRow.Obj.GetNamespace(), a.contPick.podRow.Obj.GetName()), headerStyle)

	sepStyle := style.Foreground(tcell.ColorGray)
	a.fillLine(0, 1, '─', sepStyle)

	line := 2
	for i, c := range a.contPick.containers {
		if line >= a.height-1 {
			break
		}
		rowStyle := style
		if i == a.contPick.cursor {
			rowStyle = style.Foreground(tcell.ColorBlack).Background(tcell.ColorWhite)
		}
		a.fillLine(0, line, ' ', rowStyle)
		a.drawText(1, line, fmt.Sprintf("  %s", c), rowStyle)
		line++
	}

	footerStyle := style.Foreground(tcell.ColorGray)
	a.fillLine(0, a.height-1, ' ', footerStyle)
	a.drawText(1, a.height-1, " ↑↓:nav  Enter:select  ESC:back", footerStyle)
}

func (a *App) handleContainerPickerKey(e *tcell.EventKey) {
	if a.contPick == nil {
		return
	}
	switch e.Key() {
	case tcell.KeyEscape:
		a.contPick = nil
		a.curr = pageList
	case tcell.KeyEnter:
		idx := a.contPick.cursor
		if idx >= 0 && idx < len(a.contPick.containers) {
			c := a.contPick.containers[idx]
			row := a.contPick.podRow
			if a.contPick.action == "shell" {
				a.contPick = nil
				a.curr = pageList
				a.execContainerShell(row, c)
			} else {
				a.contPick = nil
				a.openContainerLogs(row, c)
			}
		}
	case tcell.KeyUp:
		if a.contPick.cursor > 0 {
			a.contPick.cursor--
		}
	case tcell.KeyDown:
		if a.contPick.cursor < len(a.contPick.containers)-1 {
			a.contPick.cursor++
		}
	case tcell.KeyRune:
		switch e.Rune() {
		case 'k', 'K':
			if a.contPick.cursor > 0 {
				a.contPick.cursor--
			}
		case 'j', 'J':
			if a.contPick.cursor < len(a.contPick.containers)-1 {
				a.contPick.cursor++
			}
		}
	}
}

func splitLines(s string) []string {
	if s == "" {
		return []string{""}
	}
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start <= len(s) {
		lines = append(lines, s[start:])
	}
	return lines
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

func (a *App) setCell(x, y int, ch rune, style tcell.Style) {
	if x < 0 || x >= a.width || y < 0 || y >= a.height {
		return
	}
	a.screen.SetContent(x, y, ch, nil, style)
}

func (a *App) fillLine(x, y int, ch rune, style tcell.Style) {
	if y < 0 || y >= a.height {
		return
	}
	for i := x; i < a.width; i++ {
		a.setCell(i, y, ch, style)
	}
}

func (a *App) fillLineTo(x, y, end int, ch rune, style tcell.Style) {
	if y < 0 || y >= a.height {
		return
	}
	if end > a.width {
		end = a.width
	}
	for i := x; i < end; i++ {
		a.setCell(i, y, ch, style)
	}
}

func (a *App) drawText(x, y int, text string, style tcell.Style) {
	if y < 0 || y >= a.height {
		return
	}
	for i, ch := range text {
		a.setCell(x+i, y, ch, style)
	}
}
