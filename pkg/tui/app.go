package tui

import (
	"fmt"

	"github.com/Micost/sterm/pkg/k8s"

	"github.com/gdamore/tcell/v2"
)

type App struct {
	client *k8s.Client
	screen tcell.Screen

	resources []k8s.ResourceMeta
	selected  int
	offset    int

	width  int
	height int
	ready  bool
	err    error
}

func NewApp(client *k8s.Client) *App {
	return &App{client: client}
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

func (a *App) loadResources() {
	rr, err := a.client.Discover()
	if err != nil {
		a.err = err
	} else {
		a.resources = rr
		a.ready = true
	}
	a.render()
}

func (a *App) eventLoop() {
	for {
		ev := a.screen.PollEvent()
		switch e := ev.(type) {
		case *tcell.EventKey:
			switch e.Key() {
			case tcell.KeyEscape, tcell.KeyCtrlC:
				return
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
			a.render()

		case *tcell.EventResize:
			a.width, a.height = a.screen.Size()
			a.screen.Sync()
			a.render()
		}
	}
}

func (a *App) visibleRows() int {
	return a.height - 3
}

func (a *App) render() {
		a.screen.Clear()

		style := tcell.StyleDefault.
			Foreground(tcell.ColorWhite).
			Background(tcell.ColorDefault)

		if !a.ready {
			msg := "Loading resources..."
			if a.err != nil {
				msg = fmt.Sprintf("Error: %v", a.err)
			}
			a.drawText(a.width/2-len(msg)/2, a.height/2, msg, style)
			a.screen.Show()
			return
		}

		if len(a.resources) == 0 {
			a.drawText(a.width/2-10, a.height/2, "No resources found", style)
			a.screen.Show()
			return
		}

		// header
		headerStyle := style.Bold(true).Foreground(tcell.ColorAqua)
		a.fillLine(0, 0, ' ', headerStyle)
		a.drawText(1, 0, fmt.Sprintf("%-24s %-30s %-12s %s",
			"RESOURCE", "APIVERSION", "KIND", "NAMESPACED"), headerStyle)

		sepStyle := style.Foreground(tcell.ColorGray)
		a.fillLine(0, 1, '─', sepStyle)

		// ensure selection is visible
		rows := a.visibleRows()
		if a.selected < a.offset {
			a.offset = a.selected
		}
		if a.selected >= a.offset+rows {
			a.offset = a.selected - rows + 1
		}
		_ = rows

		prevCat := ""
		line := 2
		for i := a.offset; i < len(a.resources) && line < a.height-1; i++ {
			r := a.resources[i]

			if r.Category != prevCat && prevCat != "" {
				if line < a.height-1 {
					sepStyle := style.Foreground(tcell.ColorGray)
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

		// footer
		footerStyle := style.Foreground(tcell.ColorGray)
		a.fillLine(0, a.height-1, ' ', footerStyle)
		total := len(a.resources)
		info := fmt.Sprintf(" ↑↓:nav  PgUp/PgDn:page  ESC:quit  [%d/%d]", a.selected+1, total)
		a.drawText(1, a.height-1, info, footerStyle)

		a.screen.Show()
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
