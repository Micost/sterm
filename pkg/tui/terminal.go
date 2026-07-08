package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/creack/pty"
	"github.com/gdamore/tcell/v2"
)

type TermCell struct {
	Ch    rune
	Style tcell.Style
}

type Terminal struct {
	cells   [][]TermCell
	rows    int
	cols    int
	curX    int
	curY    int
	curVis  bool
	nscroll int // lines scrolled off top

	pty  *os.File
	proc *os.Process
}

func NewTerminal(rows, cols int) *Terminal {
	cells := make([][]TermCell, rows)
	for i := range cells {
		cells[i] = make([]TermCell, cols)
		for j := range cells[i] {
			cells[i][j] = TermCell{Ch: ' ', Style: tcell.StyleDefault}
		}
	}
	return &Terminal{cells: cells, rows: rows, cols: cols, curVis: true}
}

func (t *Terminal) Start(ns, pod, container string) error {
	var err error
	var tty *os.File
	t.pty, tty, err = pty.Open()
	if err != nil {
		return err
	}

	cmd := exec.Command("kubectl", "exec", "-it", "-n", ns, pod, "-c", container, "--", "sh")
	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
	}

	if err := cmd.Start(); err != nil {
		t.pty.Close()
		tty.Close()
		return fmt.Errorf("kubectl exec: %w", err)
	}
	tty.Close()
	t.proc = cmd.Process
	t.Resize(t.rows, t.cols)

	go func() {
		cmd.Wait()
		t.curVis = false
	}()

	return nil
}

func (t *Terminal) Resize(rows, cols int) {
	if t.rows == rows && t.cols == cols {
		if t.pty != nil {
			_ = pty.Setsize(t.pty, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
		}
		return
	}
	newCells := make([][]TermCell, rows)
	for i := range newCells {
		newCells[i] = make([]TermCell, cols)
		for j := range newCells[i] {
			if i < t.rows && j < t.cols {
				newCells[i][j] = t.cells[i][j]
			} else {
				newCells[i][j] = TermCell{Ch: ' ', Style: tcell.StyleDefault}
			}
		}
	}
	t.cells = newCells
	t.rows = rows
	t.cols = cols
	if t.curX >= cols {
		t.curX = cols - 1
	}
	if t.curY >= rows {
		t.curY = rows - 1
	}
	// Resize PTY
	if t.pty != nil {
		_ = pty.Setsize(t.pty, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	}
}

func (t *Terminal) Write(data []byte) (int, error) {
	if t.pty == nil {
		return 0, fmt.Errorf("pty not open")
	}
	return t.pty.Write(data)
}

func (t *Terminal) Close() {
	if t.pty != nil {
		t.pty.Close()
	}
	if t.proc != nil {
		t.proc.Signal(syscall.SIGTERM)
	}
}

func (t *Terminal) Process(data []byte) {
	escBuf := ""
	style := tcell.StyleDefault

	for i := 0; i < len(data); i++ {
		b := data[i]
		if len(escBuf) > 0 {
			escBuf += string(b)
			if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') {
				if handled := t.handleEscape(escBuf, &style); !handled {
					// could not parse, ignore
				}
				escBuf = ""
			}
			continue
		}
		if b == 0x1b {
			escBuf = string(b)
			continue
		}
		if b == '\r' {
			t.curX = 0
			continue
		}
		if b == '\n' {
			t.newline()
			continue
		}
		if b == 0x08 || b == 0x7f {
			if t.curX > 0 {
				t.curX--
			}
			continue
		}
		if b >= 32 || b == '\t' {
			if b == '\t' {
				t.curX = (t.curX + 8) & ^7
				if t.curX >= t.cols {
					t.curX = 0
					t.newline()
				}
			} else {
				t.putChar(rune(b), style)
			}
		}
	}
	t.applyStyle(style)
}

func (t *Terminal) putChar(ch rune, style tcell.Style) {
	if t.curX >= t.cols {
		t.curX = 0
		t.newline()
	}
	if t.curY >= t.rows {
		t.scrollUp()
		t.curY = t.rows - 1
	}
	t.cells[t.curY][t.curX] = TermCell{Ch: ch, Style: style}
	t.curX++
}

func (t *Terminal) newline() {
	t.curY++
	t.curX = 0
	if t.curY >= t.rows {
		t.scrollUp()
		t.curY = t.rows - 1
	}
}

func (t *Terminal) scrollUp() {
	t.nscroll++
	copy(t.cells, t.cells[1:])
	t.cells[t.rows-1] = make([]TermCell, t.cols)
	for j := range t.cells[t.rows-1] {
		t.cells[t.rows-1][j] = TermCell{Ch: ' ', Style: tcell.StyleDefault}
	}
}

func (t *Terminal) applyStyle(style tcell.Style) {}

func (t *Terminal) handleEscape(fullSeq string, style *tcell.Style) bool {
	if len(fullSeq) < 3 || !strings.HasPrefix(fullSeq, "\x1b[") {
		return false
	}
	term := fullSeq[len(fullSeq)-1]
	seq := fullSeq[2 : len(fullSeq)-1]

	// SGR codes (end with 'm')
	if term == 'm' {
		parts := strings.Split(seq, ";")
		for i := 0; i < len(parts); i++ {
			if parts[i] == "" {
				continue
			}
			switch parts[i] {
			case "0":
				*style = tcell.StyleDefault
			case "1":
				*style = style.Bold(true)
			case "30":
				*style = style.Foreground(tcell.ColorBlack)
			case "31":
				*style = style.Foreground(tcell.ColorMaroon)
			case "32":
				*style = style.Foreground(tcell.ColorGreen)
			case "33":
				*style = style.Foreground(tcell.ColorOlive)
			case "34":
				*style = style.Foreground(tcell.ColorNavy)
			case "35":
				*style = style.Foreground(tcell.ColorPurple)
			case "36":
				*style = style.Foreground(tcell.ColorTeal)
			case "37":
				*style = style.Foreground(tcell.ColorSilver)
			}
		}
		return true
	}

	switch {
	case seq == "H" || seq == "f":
		t.curX, t.curY = 0, 0
	case seq == "K":
		for x := t.curX; x < t.cols; x++ {
			t.cells[t.curY][x] = TermCell{Ch: ' ', Style: tcell.StyleDefault}
		}
	case seq == "1K":
		for x := 0; x <= t.curX; x++ {
			t.cells[t.curY][x] = TermCell{Ch: ' ', Style: tcell.StyleDefault}
		}
	case seq == "2K":
		for x := 0; x < t.cols; x++ {
			t.cells[t.curY][x] = TermCell{Ch: ' ', Style: tcell.StyleDefault}
		}
	case seq == "J" || seq == "0J":
		for y := t.curY; y < t.rows; y++ {
			for x := 0; x < t.cols; x++ {
				if y == t.curY && x < t.curX {
					continue
				}
				t.cells[y][x] = TermCell{Ch: ' ', Style: tcell.StyleDefault}
			}
		}
	case seq == "2J":
		for y := 0; y < t.rows; y++ {
			for x := 0; x < t.cols; x++ {
				t.cells[y][x] = TermCell{Ch: ' ', Style: tcell.StyleDefault}
			}
		}
	case seq == "A":
		t.curY--
		if t.curY < 0 {
			t.curY = 0
		}
	case seq == "B":
		t.curY++
		if t.curY >= t.rows {
			t.curY = t.rows - 1
		}
	case seq == "C":
		t.curX++
		if t.curX >= t.cols {
			t.curX = t.cols - 1
		}
	case seq == "D":
		t.curX--
		if t.curX < 0 {
			t.curX = 0
		}
	case seq == "?25h":
		t.curVis = true
	case seq == "?25l":
		t.curVis = false
	case seq == "s":
	case seq == "u":
	}
	return true
}

func (t *Terminal) Render(screen tcell.Screen, startY int) {
	baseStyle := tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorDefault)
	for y := 0; y < t.rows && startY+y < startY+t.rows; y++ {
		for x := 0; x < t.cols; x++ {
			cell := t.cells[y][x]
			style := cell.Style
			if cell.Ch == ' ' {
				style = baseStyle
			}
			screen.SetContent(x, startY+y, cell.Ch, nil, style)
		}
	}
	if t.curVis {
		screen.ShowCursor(t.curX, startY+t.curY)
	}
}
