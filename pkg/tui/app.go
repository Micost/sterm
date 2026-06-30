package tui

import (
	"fmt"
	"log"

	"github.com/Micost/sterm/pkg/k8s"

	"github.com/gdamore/tcell/v2"
)

type App struct {
	client *k8s.Client
	screen tcell.Screen
	width  int
	height int
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

	s.SetStyle(tcell.StyleDefault.
		Foreground(tcell.ColorWhite).
		Background(tcell.ColorDefault))

	s.Clear()

	go a.render()

	for {
		ev := s.PollEvent()
		switch e := ev.(type) {
		case *tcell.EventKey:
			switch e.Key() {
			case tcell.KeyEscape, tcell.KeyCtrlC:
				return nil
			case tcell.KeyRune:
				log.Printf("rune: %c", e.Rune())
			}
		case *tcell.EventResize:
			a.width, a.height = s.Size()
			s.Sync()
		}
	}
}

func (a *App) render() {
	a.drawText(0, 0, "sterm - Simplified K8s Terminal")
	a.drawText(0, 1, "Press ESC or Ctrl+C to quit")
	a.screen.Show()
}

func (a *App) drawText(x, y int, text string) {
	for i, ch := range text {
		a.screen.SetContent(x+i, y, ch, nil, tcell.StyleDefault)
	}
}
