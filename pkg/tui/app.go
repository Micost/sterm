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
	"k8s.io/client-go/tools/remotecommand"
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
	pageHelp
	pageShell
	pageContextPicker
)

type contPickState struct {
	containers []string
	cursor     int
	action     string
	podRow     k8s.TableRow
}

type shellState struct {
	ns        string
	pod       string
	container string
	term      *Terminal
	done      chan struct{}
	started   bool
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
	yamlLines  int
	descLines  int
	showYAML   bool
	scroll     int
	err        error

	filter    string
	filterOn  bool
	matches   []int
	matchIdx  int
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
	client     *k8s.Client
	kubeconfig string
	screen     tcell.Screen
	done       chan struct{}

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

	// context picker
	contexts    []k8s.ContextInfo
	ctxCursor   int
	ctxPrevPage page

	// auto-refresh
	refreshStop chan struct{}

	// container picker
	contPick *contPickState
	shell    *shellState

	popupTerm *Terminal
	popupOpen bool

	helpPrevPage page

	curr    page
	width   int
	height  int
	bReady  bool
	bErr    error
}

func NewApp(client *k8s.Client, kubeconfig string) *App {
	return &App{client: client, kubeconfig: kubeconfig, done: make(chan struct{}), namespace: "default"}
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
	a.stopRefresh()
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
	a.startRefresh()
}

func (a *App) startRefresh() {
	a.stopRefresh()
	a.refreshStop = make(chan struct{})
	go func() {
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ns := ""
				if a.list != nil && a.list.meta.Namespaced {
					ns = a.namespace
				}
				data, err := a.client.List(context.Background(), a.list.meta.GVR, ns)
				if err == nil {
					a.list.data = data
				}
				a.screen.PostEvent(&renderEvent{})
			case <-a.refreshStop:
				return
			}
		}
	}()
}

func (a *App) stopRefresh() {
	if a.refreshStop != nil {
		close(a.refreshStop)
		a.refreshStop = nil
	}
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
		seen := make(map[schema.GroupVersionResource]bool)
		out := make([]k8s.ResourceMeta, 0, len(a.recent)+len(a.resources))
		for _, r := range a.recent {
			if strings.Contains(strings.ToLower(r.GVR.Resource), f) ||
				strings.Contains(strings.ToLower(r.ShortName), f) {
				out = append(out, r)
				seen[r.GVR] = true
			}
		}
		for _, r := range a.resources {
			if seen[r.GVR] {
				continue
			}
			if strings.Contains(strings.ToLower(r.GVR.Resource), f) ||
				strings.Contains(strings.ToLower(r.ShortName), f) {
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

func (a *App) jumpToResource(name string) {
	items := a.browserList()
	for i, r := range items {
		if r.GVR.Resource == name {
			a.addRecent(r)
			a.list = &listPageState{meta: r}
			a.curr = pageList
			a.selected = i
			go a.loadList()
			return
		}
	}
}

func (a *App) enterContextPicker() {
	a.ctxPrevPage = a.curr
	contexts, err := k8s.ListContexts(a.kubeconfig)
	if err != nil {
		return
	}
	a.contexts = contexts
	a.ctxCursor = 0
	a.curr = pageContextPicker
}

func (a *App) switchContext(name string) {
	config, err := k8s.BuildConfigForContext(a.kubeconfig, name)
	if err != nil {
		return
	}
	client, err := k8s.NewClient(config)
	if err != nil {
		return
	}
	a.client = client
	a.bReady = false
	a.resources = nil
	a.recent = nil
	a.previewData = nil
	a.bErr = nil
	a.list = nil
	a.detail = nil
	a.log = nil
	a.shell = nil
	a.curr = pageBrowser
	go a.loadResources()
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

	termRows := a.height - 4
	if termRows < 5 {
		termRows = 10
	}
	term := NewTerminal(termRows, a.width)

	resizeCh := make(chan remotecommand.TerminalSize, 1)
	resizeCh <- remotecommand.TerminalSize{Width: uint16(a.width), Height: uint16(termRows)}

	stdin, stdout, err := a.client.ExecTTY(ns, pod, container, []string{"sh"}, resizeCh)
	if err != nil {
		a.listErr = fmt.Sprintf("shell: %v", err)
		a.render()
		return
	}

	term.StartStream(stdout)
	term.stdin = stdin
	term.stdout = stdout

	sh := &shellState{
		ns:        ns,
		pod:       pod,
		container: container,
		term:      term,
		done:      make(chan struct{}),
	}
	a.shell = sh
	a.curr = pageShell
	a.screen.SetCursorStyle(tcell.CursorStyleBlinkingBlock)
	a.render()
	sh.started = true

	go func() {
		defer close(sh.done)
		buf := make([]byte, 4096)
		for {
			n, err := term.Read(buf)
			if n > 0 {
				term.Process(buf[:n])
				a.screen.PostEvent(&renderEvent{})
			}
			if err != nil {
				if sh.started {
					a.screen.HideCursor()
					a.curr = pageList
					a.shell = nil
				}
				a.screen.PostEvent(&renderEvent{})
				return
			}
		}
	}()
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
			a.render()
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
	if e.Key() == tcell.KeyCtrlC {
		if a.curr == pageShell && a.shell != nil {
			a.shell.term.Write([]byte{0x03})
			return
		}
		a.quit()
		return
	}
	if e.Key() == tcell.KeyCtrlQ {
		a.quit()
		return
	}
	if e.Key() == tcell.KeyCtrlJ {
		a.togglePopupShell()
		return
	}

	if a.popupOpen && a.popupTerm != nil {
		switch e.Key() {
		case tcell.KeyEscape:
			a.togglePopupShell()
			return
		case tcell.KeyCtrlC:
			a.popupTerm.Write([]byte{0x03})
			return
		case tcell.KeyEnter:
			a.popupTerm.Write([]byte{'\r'})
			return
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			a.popupTerm.Write([]byte{0x7f})
			return
		case tcell.KeyTab:
			a.popupTerm.Write([]byte{'\t'})
			return
		default:
			if e.Rune() != 0 {
				a.popupTerm.Write([]byte(string(e.Rune())))
			}
			return
		}
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
	case pageHelp:
		a.handleHelpKey(e)
	case pageShell:
		a.handleShellKey(e)
	case pageContextPicker:
		a.handleContextPickerKey(e)
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
		case '?':
			a.showHelp()
		case 'p':
			a.jumpToResource("pods")
		case 'c':
			a.enterContextPicker()
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

	rows := a.filteredRows()

	switch e.Key() {
	case tcell.KeyEscape, tcell.KeyCtrlC:
		a.stopRefresh()
		a.curr = pageBrowser
		a.list = nil
	case tcell.KeyEnter:
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
			a.stopRefresh()
			a.curr = pageBrowser
			a.list = nil
		case 'x', 'X':
			if a.list.meta.GVR.Resource == "pods" && len(rows) > 0 {
				a.list.deleteConfirm = true
			}
		case 'l', 'L':
			if a.list.selected >= 0 && a.list.selected < len(rows) {
				row := rows[a.list.selected]
				if row.Obj.GetKind() == "Pod" {
					a.pickContainer(row, "logs")
				} else {
					a.openLogs(row)
				}
			}
		case 's', 'S':
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
			if a.list.selected >= 0 && a.list.selected < len(rows) {
				a.openDetailMode(rows[a.list.selected], false)
			}
		case 'y', 'Y':
			if a.list.selected >= 0 && a.list.selected < len(rows) {
				a.openDetailMode(rows[a.list.selected], true)
			}
		case 'e', 'E':
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
		case '?':
			a.showHelp()
		case 'c':
			a.enterContextPicker()
		case 'o':
			if a.list.meta.GVR.Resource == "nodes" && a.list.selected >= 0 && a.list.selected < len(rows) {
				name := rows[a.list.selected].Cells[0]
				go func() {
					if err := a.client.CordonNode(name); err != nil {
						a.listErr = fmt.Sprintf("cordon: %v", err)
					} else {
						a.loadList()
					}
				}()
			}
		case 'u':
			if a.list.meta.GVR.Resource == "nodes" && a.list.selected >= 0 && a.list.selected < len(rows) {
				name := rows[a.list.selected].Cells[0]
				go func() {
					if err := a.client.UncordonNode(name); err != nil {
						a.listErr = fmt.Sprintf("uncordon: %v", err)
					} else {
						a.loadList()
					}
				}()
			}
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

	if a.detail.filterOn {
		a.handleDetailFilterKey(e)
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
			a.editResource()
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
		case '/':
			a.detail.filter = ""
			a.detail.filterOn = true
		case 'n':
			a.detailJumpMatch(1)
		case 'N':
			a.detailJumpMatch(-1)
		case '?':
			a.showHelp()
		}
	}
}

func (a *App) detailLines() int {
	if a.detail == nil {
		return 0
	}
	if a.detail.showYAML {
		return a.detail.yamlLines
	}
	return a.detail.descLines
}

func (a *App) detailText() string {
	if a.detail == nil {
		return ""
	}
	if a.detail.showYAML {
		return a.detail.yamlText
	}
	return a.detail.descText
}

func (a *App) detailFindMatches() {
	a.detail.matches = nil
	a.detail.matchIdx = 0
	if a.detail.filter == "" {
		return
	}
	f := strings.ToLower(a.detail.filter)
	lines := splitLines(a.detailText())
	for i, line := range lines {
		if strings.Contains(strings.ToLower(line), f) {
			a.detail.matches = append(a.detail.matches, i)
		}
	}
	if len(a.detail.matches) > 0 {
		a.detail.scroll = a.detail.matches[0]
		if a.detail.scroll > a.detailLines() {
			a.detail.scroll = a.detailLines()
		}
	}
}

func (a *App) detailJumpMatch(dir int) {
	if len(a.detail.matches) == 0 {
		return
	}
	a.detail.matchIdx += dir
	if a.detail.matchIdx < 0 {
		a.detail.matchIdx = len(a.detail.matches) - 1
	}
	if a.detail.matchIdx >= len(a.detail.matches) {
		a.detail.matchIdx = 0
	}
	a.detail.scroll = a.detail.matches[a.detail.matchIdx]
	if a.detail.scroll > a.detailLines() {
		a.detail.scroll = a.detailLines()
	}
}

func (a *App) handleDetailFilterKey(e *tcell.EventKey) {
	switch e.Key() {
	case tcell.KeyEscape:
		a.detail.filter = ""
		a.detail.filterOn = false
		a.detail.matches = nil
	case tcell.KeyEnter:
		a.detail.filterOn = false
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if len(a.detail.filter) > 0 {
			a.detail.filter = a.detail.filter[:len(a.detail.filter)-1]
			a.detailFindMatches()
		}
	case tcell.KeyRune:
		a.detail.filter += string(e.Rune())
		a.detailFindMatches()
	}
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
	a.stopRefresh()
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
		descText:   a.client.Describe(row.Obj),
		showYAML:   showYAML,
	}
	a.detail.yamlLines = countLines(yamlText)
	a.detail.descLines = countLines(a.detail.descText)
	a.curr = pageDetail
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := 0
	for _, ch := range s {
		if ch == '\n' {
			n++
		}
	}
	return n
}

func (a *App) openLogs(row k8s.TableRow) {
	a.stopRefresh()
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
		case '?':
			a.showHelp()
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
	a.stopRefresh()
	containers := a.client.PodContainers(row.Obj)
	if len(containers) == 0 {
		return
	}
	a.execContainerShell(row, containers[0])
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
	editors := []string{editor, "vim", "vi", "nano"}
	found := ""
	for _, e := range editors {
		if e == "" {
			continue
		}
		if _, err := exec.LookPath(e); err == nil {
			found = e
			break
		}
	}
	if found == "" {
		a.screen.Resume()
		a.listErr = "no editor found (vim, vi, nano)"
		a.curr = pageList
		a.render()
		return
	}

	cmd := exec.Command(found, tmpPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		a.screen.Resume()
		a.listErr = fmt.Sprintf("editor error: %v", err)
		a.curr = pageList
		a.render()
		return
	}

	a.screen.Resume()

	raw, err := os.ReadFile(tmpPath)
	if err != nil {
		a.listErr = fmt.Sprintf("read error: %v", err)
		a.curr = pageList
		a.render()
		return
	}

	if string(raw) == a.detail.yamlText {
		a.listErr = "no changes made"
		a.curr = pageList
		a.render()
		return
	}

	var updated map[string]interface{}
	if err := yaml.Unmarshal(raw, &updated); err != nil {
		a.listErr = fmt.Sprintf("yaml error: %v", err)
		a.curr = pageList
		a.render()
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
			return
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			if len(a.nsFilter) > 0 {
				a.nsFilter = a.nsFilter[:len(a.nsFilter)-1]
			}
			a.nsCursor = 0
		case tcell.KeyUp:
			if a.nsCursor > 0 {
				a.nsCursor--
			}
		case tcell.KeyDown:
			if a.nsCursor < a.nsFilteredCount()-1 {
				a.nsCursor++
			}
		case tcell.KeyRune:
			switch e.Rune() {
			case 'k', 'K':
				if a.nsCursor > 0 {
					a.nsCursor--
				}
			case 'j', 'J':
				if a.nsCursor < a.nsFilteredCount()-1 {
					a.nsCursor++
				}
			default:
				a.nsFilter += string(e.Rune())
				a.nsCursor = 0
			}
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
		case '?':
			a.showHelp()
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
	if a.curr != pageShell {
		a.screen.Clear()
	}
	if !a.popupOpen {
		a.screen.HideCursor()
	}

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
	case pageHelp:
		a.renderHelp()
	case pageShell:
		a.renderShell()
	case pageContextPicker:
		a.renderContextPicker()
	}

	a.renderPopupShell()

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

	helpKeys := []string{"j/k", "Enter", "/", "n", "Ctrl+J", "Ctrl+Q"}
	helpDesc := []string{"Navigate", "List resources", "Search", "Namespace", "Terminal", "Quit"}
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
	info := fmt.Sprintf(" ↑↓:nav  Enter:list  n:ns(%s)  /:search  ?:help  ESC:quit%s", ns, filterInfo)
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
	for i, row := range rows {
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
			cellStyle := rowStyle
			if ci < len(a.list.data.Columns) && a.list.data.Columns[ci] == "STATUS" {
				st := strings.ToLower(cell)
				if strings.Contains(st, "error") || strings.Contains(st, "terminating") ||
					strings.Contains(st, "crashloop") || strings.Contains(st, "errimage") ||
					strings.Contains(st, "unknown") && a.list.meta.GVR.Resource == "nodes" {
					cellStyle = cellStyle.Foreground(tcell.ColorRed)
				} else if strings.Contains(st, "pending") || strings.Contains(st, "init:") ||
					strings.Contains(st, "containercreating") || strings.Contains(st, "evicted") ||
					strings.Contains(st, "schedulingdisabled") {
					cellStyle = cellStyle.Foreground(tcell.ColorYellow)
				}
			}
			a.drawText(x, line, text, cellStyle)
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
		name := "?"
		if a.list.selected >= 0 && a.list.selected < len(rows) {
			name = rows[a.list.selected].Cells[0]
		}
		info = fmt.Sprintf(" Delete %s? (y/N)", name)
	} else {
		ns := a.nsDisplayName(a.namespace)
		podOps := ""
		nodeOps := ""
		if a.list.meta.GVR.Resource == "pods" {
			podOps = "  s:shell  l:logs  x:del"
		}
		if a.list.meta.GVR.Resource == "nodes" {
			nodeOps = "  o:cordon  u:uncordon"
		}
		info = fmt.Sprintf(" d:desc  y:yaml  e:edit%s%s  n:ns(%s)  ESC:back  [%d/%d] %s", podOps, nodeOps, ns, sel, total, filterInfo)
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
	title := fmt.Sprintf(" %s/%s [%s]  d:toggle  e:edit", kind, name, mode)
	if a.detail.filterOn {
		title = fmt.Sprintf(" [/] %s_", a.detail.filter)
	} else if a.detail.filter != "" {
		title = fmt.Sprintf(" %s/%s [%s]  /:%s  n/N:next/prev", kind, name, mode, a.detail.filter)
	}
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
	info := fmt.Sprintf(" ↑↓:nav  g/G:top/bot  /:search  n/N:match  d:toggle  e:edit  s:shell  ESC:back  [%d/%d]", a.detail.scroll+1, total)
	if len(a.detail.matches) > 0 {
		info += fmt.Sprintf("  match %d/%d", a.detail.matchIdx+1, len(a.detail.matches))
	}
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
		case '?':
			a.showHelp()
		}
	}
}

// --- context picker ---

func (a *App) renderContextPicker() {
	style := tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorDefault)

	headerStyle := style.Bold(true).Foreground(tcell.ColorAqua)
	a.fillLine(0, 0, ' ', headerStyle)
	a.drawText(1, 0, " CONTEXTS", headerStyle)

	sepStyle := style.Foreground(tcell.ColorGray)
	a.fillLine(0, 1, '─', sepStyle)

	line := 2
	for i, ctx := range a.contexts {
		if line >= a.height-1 {
			break
		}
		rowStyle := style
		if i == a.ctxCursor {
			rowStyle = style.Foreground(tcell.ColorBlack).Background(tcell.ColorWhite)
		}
		a.fillLine(0, line, ' ', rowStyle)
		a.drawText(1, line, fmt.Sprintf("  %-40s  cluster: %s", ctx.Name, ctx.Cluster), rowStyle)
		line++
	}

	footerStyle := style.Foreground(tcell.ColorGray)
	a.fillLine(0, a.height-1, ' ', footerStyle)
	a.drawText(1, a.height-1, " ↑↓:nav  Enter:select  ESC:back", footerStyle)
}

func (a *App) handleContextPickerKey(e *tcell.EventKey) {
	switch e.Key() {
	case tcell.KeyEscape:
		a.curr = a.ctxPrevPage
	case tcell.KeyEnter:
		if a.ctxCursor >= 0 && a.ctxCursor < len(a.contexts) {
			a.switchContext(a.contexts[a.ctxCursor].Name)
		}
	case tcell.KeyUp:
		if a.ctxCursor > 0 {
			a.ctxCursor--
		}
	case tcell.KeyDown:
		if a.ctxCursor < len(a.contexts)-1 {
			a.ctxCursor++
		}
	case tcell.KeyRune:
		switch e.Rune() {
		case 'k', 'K':
			if a.ctxCursor > 0 {
				a.ctxCursor--
			}
		case 'j', 'J':
			if a.ctxCursor < len(a.contexts)-1 {
				a.ctxCursor++
			}
		case '?':
			a.showHelp()
		}
	}
}

// --- help ---

var helpText = []string{
	"",
	"  STERM - Kubernetes TUI Manager",
	"",
	"  GLOBAL",
	"    Ctrl+Q / Ctrl+C    Quit from any page",
	"    Ctrl+J             Toggle local terminal popup",
	"    ?                  This help page",
	"",
	"  RESOURCE BROWSER (browser page)",
	"    j/k / Up/Down      Navigate resource types",
	"    g / Home           Go to top",
	"    G / End            Go to bottom",
	"    Enter              Enter resource list",
	"    n                  Switch namespace",
	"    /                  Search by kind or resource name",
	"                        ESC clears search, Enter confirms",
	"    ESC                (top level, no-op)",
	"",
	"  RESOURCE LIST (list page)",
	"    j/k / Up/Down      Navigate items",
	"    g / Home           Go to top",
	"    G / End            Go to bottom",
	"    Enter              View detail (YAML)",
	"    d                  Describe resource",
	"    y                  View YAML",
	"    e                  Edit resource (external $EDITOR)",
	"    s                  Shell into container (pods only)",
	"    l                  Stream logs (pods only)",
	"    x                  Delete resource (confirm y/N)",
	"    /                  Filter items",
	"    n                  Switch namespace",
	"    ESC                Back to browser",
	"",
	"  DETAIL PAGE",
	"    j/k / Up/Down      Scroll",
	"    g / Home           Go to top",
	"    G / End            Go to bottom",
	"    d                  Toggle Describe / YAML",
	"    e                  Edit YAML (external $EDITOR)",
	"    s                  Shell into container (pods only)",
	"    ESC                Back to list",
	"",
	"  LOGS PAGE",
	"    j/k / Up/Down      Scroll (stops auto-follow)",
	"    End                Resume auto-follow",
	"    ESC                Back to list",
	"",
	"  CONTAINER PICKER (multi-container pods)",
	"    j/k / Up/Down      Navigate containers",
	"    Enter              Select",
	"    ESC                Cancel",
	"",
	"  NAMESPACE PICKER",
	"    j/k / Up/Down      Navigate namespaces",
	"    /                  Filter",
	"    Enter              Select",
	"    ESC                Cancel",
	"",
	"  SEARCH MODE",
	"    Type to filter by kind or resource name",
	"    j/k / Up/Down      Navigate matches",
	"    /                  Reopen search input",
	"    ESC                Exit search",
	"    Enter              Confirm filter (stay on browser)",
}

func (a *App) renderHelp() {
	style := tcell.StyleDefault.
		Foreground(tcell.ColorWhite).
		Background(tcell.ColorDefault)

	headerStyle := style.Bold(true).Foreground(tcell.ColorAqua)
	a.fillLine(0, 0, ' ', headerStyle)
	a.drawText(1, 0, " HELP  (ESC to go back)", headerStyle)

	sepStyle := style.Foreground(tcell.ColorGray)
	a.fillLine(0, 1, '─', sepStyle)

	line := 2
	for _, text := range helpText {
		if line >= a.height-1 {
			break
		}
		a.drawText(1, line, text, style)
		line++
	}

	footerStyle := style.Foreground(tcell.ColorGray)
	a.fillLine(0, a.height-1, ' ', footerStyle)
	a.drawText(1, a.height-1, " ESC:back", footerStyle)
}

func (a *App) handleHelpKey(e *tcell.EventKey) {
	if e.Key() == tcell.KeyEscape {
		a.curr = a.helpPrevPage
	}
}

func (a *App) togglePopupShell() {
	if a.popupOpen {
		a.popupOpen = false
		a.screen.HideCursor()
		if a.popupTerm != nil {
			a.popupTerm.Close()
			a.popupTerm = nil
		}
		a.screen.Clear()
		a.render()
		return
	}
	w := a.width / 3
	h := a.height / 3
	if w < 40 {
		w = 40
	}
	if h < 10 {
		h = 10
	}
	term := NewTerminal(h, w)
	if err := term.StartLocal(); err != nil {
		return
	}
	a.popupTerm = term
	a.popupOpen = true
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := term.Read(buf)
			if n > 0 {
				term.Process(buf[:n])
				a.screen.PostEvent(&renderEvent{})
			}
			if err != nil {
				a.popupOpen = false
				term.Close()
				a.popupTerm = nil
				a.screen.PostEvent(&renderEvent{})
				return
			}
		}
	}()
}

func (a *App) renderPopupShell() {
	if a.popupTerm == nil || !a.popupOpen {
		return
	}
	w := a.width / 3
	h := a.height / 3
	if w < 40 {
		w = 40
	}
	if h < 10 {
		h = 10
	}
	x0 := (a.width - w) / 2
	y0 := (a.height - h) / 2

	borderStyle := tcell.StyleDefault.Foreground(tcell.ColorAqua)
	// top border
	for x := 0; x < w; x++ {
		a.screen.SetContent(x0+x, y0-1, '─', nil, borderStyle)
	}
	// bottom border
	for x := 0; x < w; x++ {
		a.screen.SetContent(x0+x, y0+h, '─', nil, borderStyle)
	}
	// left/right borders
	for y := 0; y < h; y++ {
		a.screen.SetContent(x0-1, y0+y, '│', nil, borderStyle)
		a.screen.SetContent(x0+w, y0+y, '│', nil, borderStyle)
	}
	// corners
	a.screen.SetContent(x0-1, y0-1, '┌', nil, borderStyle)
	a.screen.SetContent(x0+w, y0-1, '┐', nil, borderStyle)
	a.screen.SetContent(x0-1, y0+h, '└', nil, borderStyle)
	a.screen.SetContent(x0+w, y0+h, '┘', nil, borderStyle)

	a.popupTerm.RenderAt(a.screen, x0, y0, w, h)
}

func (a *App) showHelp() {
	a.helpPrevPage = a.curr
	a.curr = pageHelp
}

// --- shell ---

func (a *App) renderShell() {
	if a.shell == nil {
		return
	}
	sh := a.shell

	headerStyle := tcell.StyleDefault.Bold(true).Foreground(tcell.ColorAqua)
	a.fillLine(0, 0, ' ', headerStyle)
	a.drawText(1, 0, fmt.Sprintf(" %s/%s  c:%s", sh.ns, sh.pod, sh.container), headerStyle)

	sepStyle := tcell.StyleDefault.Foreground(tcell.ColorGray)
	a.fillLine(0, 1, '─', sepStyle)

	sh.term.Render(a.screen, 2)

	footerStyle := tcell.StyleDefault.Foreground(tcell.ColorGray)
	a.fillLine(0, a.height-1, ' ', footerStyle)
}

func (a *App) handleShellKey(e *tcell.EventKey) {
	if a.shell == nil {
		return
	}
	sh := a.shell

	switch e.Key() {
	case tcell.KeyEscape:
		a.screen.HideCursor()
		sh.term.Close()
		select {
		case <-sh.done:
		default:
		}
		a.curr = pageList
		a.shell = nil

	case tcell.KeyEnter:
		sh.term.Write([]byte{'\r'})
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		sh.term.Write([]byte{0x7f})
	case tcell.KeyTab:
		sh.term.Write([]byte{'\t'})
	case tcell.KeyCtrlW:
		sh.term.Write([]byte{0x17})
	case tcell.KeyCtrlU:
		sh.term.Write([]byte{0x15})
	case tcell.KeyCtrlK:
		sh.term.Write([]byte{0x0b})
	case tcell.KeyCtrlA:
		sh.term.Write([]byte{0x01})
	case tcell.KeyCtrlE:
		sh.term.Write([]byte{0x05})
	case tcell.KeyCtrlL:
		sh.term.Write([]byte{0x0c})
	case tcell.KeyCtrlD:
		sh.term.Write([]byte{0x04})
	case tcell.KeyCtrlR:
		sh.term.Write([]byte{0x12})
	case tcell.KeyUp:
		sh.term.Write([]byte{0x1b, '[', 'A'})
	case tcell.KeyDown:
		sh.term.Write([]byte{0x1b, '[', 'B'})
	case tcell.KeyRight:
		sh.term.Write([]byte{0x1b, '[', 'C'})
	case tcell.KeyLeft:
		sh.term.Write([]byte{0x1b, '[', 'D'})
	case tcell.KeyRune:
		sh.term.Write([]byte(string(e.Rune())))
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
