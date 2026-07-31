package tui

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	"github.com/creack/pty"
	"github.com/gdamore/tcell/v2"
)

type TermCell struct {
	Ch    rune
	Style tcell.Style
}

type Terminal struct {
	mu       sync.Mutex
	cells    [][]TermCell
	dirty    []bool
	allDirty bool
	rows     int
	cols     int
	curX     int
	curY     int
	curVis   bool
	autowrap bool
	scrollTop int
	scrollBot int
	nscroll  int
	utf8Buf  []byte

	scrollback   [][]TermCell
	scrollOffset int

	selStartX, selStartY int
	selEndX, selEndY     int
	selActive            bool

	pty    *os.File
	stdin  io.WriteCloser
	stdout io.ReadCloser
	proc   *os.Process
}

func NewTerminal(rows, cols int) *Terminal {
	cells := make([][]TermCell, rows)
	dirty := make([]bool, rows)
	for i := range cells {
		cells[i] = make([]TermCell, cols)
		for j := range cells[i] {
			cells[i][j] = TermCell{Ch: ' ', Style: tcell.StyleDefault}
		}
		dirty[i] = true
	}
	return &Terminal{cells: cells, dirty: dirty, rows: rows, cols: cols, curVis: true, autowrap: true, scrollTop: 0, scrollBot: rows - 1}
}

func (t *Terminal) StartStream(stdout io.ReadCloser) {
	t.stdout = stdout
}

func (t *Terminal) StartLocal() error {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	return t.startProcess(exec.Command(shell, "-l"))
}

func (t *Terminal) startProcess(cmd *exec.Cmd) error {
	var err error
	var tty *os.File
	t.pty, tty, err = pty.Open()
	if err != nil {
		return err
	}

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
		return fmt.Errorf("start: %w", err)
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
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resize(rows, cols)
}

func (t *Terminal) resize(rows, cols int) {
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
	t.dirty = make([]bool, rows)
	for i := range t.dirty {
		t.dirty[i] = true
	}
	t.rows = rows
	t.cols = cols
	t.scrollTop = 0
	t.scrollBot = rows - 1
	t.scrollback = nil
	t.scrollOffset = 0
	if t.curX >= cols {
		t.curX = cols - 1
	}
	if t.curY >= rows {
		t.curY = rows - 1
	}
	if t.pty != nil {
		_ = pty.Setsize(t.pty, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	}
}

func (t *Terminal) Read(p []byte) (int, error) {
	if t.stdout != nil {
		return t.stdout.Read(p)
	}
	return t.pty.Read(p)
}

func (t *Terminal) Write(p []byte) (int, error) {
	if t.stdin != nil {
		return t.stdin.Write(p)
	}
	return t.pty.Write(p)
}

func (t *Terminal) Close() {
	if t.stdin != nil {
		t.stdin.Close()
	}
	if t.stdout != nil {
		t.stdout.Close()
	}
	if t.pty != nil {
		t.pty.Close()
	}
	if t.proc != nil {
		t.proc.Signal(syscall.SIGTERM)
	}
}

func (t *Terminal) Process(data []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()

	escBuf := ""
	style := tcell.StyleDefault

	for i := 0; i < len(data); i++ {
		b := data[i]
		if len(escBuf) > 0 {
			escBuf += string(b)
			// OSC sequence (\033]...\007 or \033]...\033\\)
			if len(escBuf) == 2 && escBuf[1] == ']' {
				continue
			}
			if escBuf[1] == ']' {
				if b == 0x07 || (b == '\\' && len(escBuf) >= 3 && escBuf[len(escBuf)-2] == 0x1b) {
					escBuf = ""
				}
				continue
			}
			if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') {
				if handled := t.handleEscape(escBuf, &style); !handled {
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
		if b >= 0x80 {
			t.utf8Buf = append(t.utf8Buf, b)
			if utf8.Valid(t.utf8Buf) {
				r, _ := utf8.DecodeRune(t.utf8Buf)
				t.putChar(r, style)
				t.utf8Buf = t.utf8Buf[:0]
			} else if len(t.utf8Buf) >= 4 {
				r, size := utf8.DecodeRune(t.utf8Buf)
				if r != utf8.RuneError {
					t.putChar(r, style)
				}
				t.utf8Buf = t.utf8Buf[size:]
			}
			continue
		}
		if b == 0x08 || b == 0x7f {
			if t.curX > 0 {
				t.curX--
				t.cells[t.curY][t.curX] = TermCell{Ch: ' ', Style: tcell.StyleDefault}
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
}

func (t *Terminal) putChar(ch rune, style tcell.Style) {
	if t.curX >= t.cols {
		if t.autowrap {
			t.curX = 0
			t.newline()
		} else {
			t.curX = t.cols - 1
		}
	}
	if t.curY > t.scrollBot {
		t.scrollUp()
		t.curY = t.scrollBot
	}
	t.cells[t.curY][t.curX] = TermCell{Ch: ch, Style: style}
	t.markDirty(t.curY)
	t.curX++
	if isWide(ch) && t.curX < t.cols {
		t.curX++
	}
}

func (t *Terminal) newline() {
	t.curY++
	t.curX = 0
	if t.curY > t.scrollBot {
		t.scrollUp()
		t.curY = t.scrollBot
	}
}

func (t *Terminal) scrollUp() {
	t.nscroll++
	top := t.scrollTop
	bot := t.scrollBot
	saved := make([]TermCell, t.cols)
	copy(saved, t.cells[top])
	t.scrollback = append(t.scrollback, saved)
	copy(t.cells[top:bot], t.cells[top+1:bot+1])
	t.cells[bot] = make([]TermCell, t.cols)
	for j := range t.cells[bot] {
		t.cells[bot][j] = TermCell{Ch: ' ', Style: tcell.StyleDefault}
	}
	for i := top; i <= bot; i++ {
		t.dirty[i] = true
	}
}

func (t *Terminal) scrollDown() {
	top := t.scrollTop
	bot := t.scrollBot
	copy(t.cells[top+1:bot+1], t.cells[top:bot])
	t.cells[top] = make([]TermCell, t.cols)
	for j := range t.cells[top] {
		t.cells[top][j] = TermCell{Ch: ' ', Style: tcell.StyleDefault}
	}
	for i := top; i <= bot; i++ {
		t.dirty[i] = true
	}
}

func (t *Terminal) insertLine(y int) {
	top := t.scrollTop
	bot := t.scrollBot
	if y < top || y > bot {
		return
	}
	copy(t.cells[y+1:bot+1], t.cells[y:bot])
	t.cells[y] = make([]TermCell, t.cols)
	for j := range t.cells[y] {
		t.cells[y][j] = TermCell{Ch: ' ', Style: tcell.StyleDefault}
	}
	for i := y; i <= bot; i++ {
		t.dirty[i] = true
	}
}

func (t *Terminal) deleteLine(y int) {
	top := t.scrollTop
	bot := t.scrollBot
	if y < top || y > bot {
		return
	}
	copy(t.cells[y:bot], t.cells[y+1:bot+1])
	t.cells[bot] = make([]TermCell, t.cols)
	for j := range t.cells[bot] {
		t.cells[bot][j] = TermCell{Ch: ' ', Style: tcell.StyleDefault}
	}
	for i := y; i <= bot; i++ {
		t.dirty[i] = true
	}
}

func (t *Terminal) markDirty(y int) {
	if y >= 0 && y < len(t.dirty) {
		t.dirty[y] = true
	}
}

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
				parts[i] = "0"
			}
			switch parts[i] {
			case "0":
				*style = tcell.StyleDefault
			case "1":
				*style = style.Bold(true)
			case "2":
				*style = style.Dim(true)
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
			case "38":
				if i+2 < len(parts) && parts[i+1] == "5" {
					*style = style.Foreground(tcell.Color(parseANSI256(parts[i+2])))
					i += 2
				} else if i+4 < len(parts) && parts[i+1] == "2" {
					r, g, b := parseANSI(parts[i+2]), parseANSI(parts[i+3]), parseANSI(parts[i+4])
					*style = style.Foreground(tcell.NewRGBColor(int32(r), int32(g), int32(b)))
					i += 4
				}
			case "48":
				if i+2 < len(parts) && parts[i+1] == "5" {
					*style = style.Background(tcell.Color(parseANSI256(parts[i+2])))
					i += 2
				} else if i+4 < len(parts) && parts[i+1] == "2" {
					r, g, b := parseANSI(parts[i+2]), parseANSI(parts[i+3]), parseANSI(parts[i+4])
					*style = style.Background(tcell.NewRGBColor(int32(r), int32(g), int32(b)))
					i += 4
				}
			}
		}
		return true
	}

	switch term {
	case 'H', 'f':
		row, col := 0, 0
		if seq != "" {
			fmt.Sscanf(seq, "%d;%d", &row, &col)
			row--
			col--
			if row < 0 {
				row = 0
			}
			if col < 0 {
				col = 0
			}
		}
		t.curX, t.curY = col, row
	case 'K':
		switch seq {
		case "1":
			for x := 0; x <= t.curX; x++ {
				t.cells[t.curY][x] = TermCell{Ch: ' ', Style: tcell.StyleDefault}
			}
		case "2":
			for x := 0; x < t.cols; x++ {
				t.cells[t.curY][x] = TermCell{Ch: ' ', Style: tcell.StyleDefault}
			}
		default:
			for x := t.curX; x < t.cols; x++ {
				t.cells[t.curY][x] = TermCell{Ch: ' ', Style: tcell.StyleDefault}
			}
		}
		t.markDirty(t.curY)
	case 'J':
		switch seq {
		case "2":
			for y := 0; y < t.rows; y++ {
				for x := 0; x < t.cols; x++ {
					t.cells[y][x] = TermCell{Ch: ' ', Style: tcell.StyleDefault}
				}
			}
			for i := range t.dirty {
				t.dirty[i] = true
			}
		default:
			for y := t.curY; y < t.rows; y++ {
				for x := 0; x < t.cols; x++ {
					if y == t.curY && x < t.curX {
						continue
					}
					t.cells[y][x] = TermCell{Ch: ' ', Style: tcell.StyleDefault}
				}
				t.markDirty(y)
			}
		}
	case 'A':
		n := 1
		if seq != "" {
			fmt.Sscanf(seq, "%d", &n)
		}
		t.curY -= n
		if t.curY < 0 {
			t.curY = 0
		}
	case 'B':
		n := 1
		if seq != "" {
			fmt.Sscanf(seq, "%d", &n)
		}
		t.curY += n
		if t.curY >= t.rows {
			t.curY = t.rows - 1
		}
	case 'C':
		n := 1
		if seq != "" {
			fmt.Sscanf(seq, "%d", &n)
		}
		t.curX += n
		if t.curX >= t.cols {
			t.curX = t.cols - 1
		}
	case 'D':
		n := 1
		if seq != "" {
			fmt.Sscanf(seq, "%d", &n)
		}
		t.curX -= n
		if t.curX < 0 {
			t.curX = 0
		}
	case 'L': // IL - Insert Lines
		n := 1
		if seq != "" {
			fmt.Sscanf(seq, "%d", &n)
		}
		for i := 0; i < n; i++ {
			t.insertLine(t.curY)
		}
	case 'M': // DL - Delete Lines
		n := 1
		if seq != "" {
			fmt.Sscanf(seq, "%d", &n)
		}
		for i := 0; i < n; i++ {
			t.deleteLine(t.curY)
		}
	case 'S': // SU - Scroll Up
		n := 1
		if seq != "" {
			fmt.Sscanf(seq, "%d", &n)
		}
		for i := 0; i < n; i++ {
			t.scrollUp()
		}
	case 'T': // SD - Scroll Down
		n := 1
		if seq != "" {
			fmt.Sscanf(seq, "%d", &n)
		}
		for i := 0; i < n; i++ {
			t.scrollDown()
		}
	case 'r': // DECSTBM - Set Scroll Region
		top, bot := 0, t.rows-1
		if seq != "" {
			fmt.Sscanf(seq, "%d;%d", &top, &bot)
			top--
			bot--
			if top < 0 {
				top = 0
			}
			if bot >= t.rows {
				bot = t.rows - 1
			}
			if bot < top {
				bot = t.rows - 1
				top = 0
			}
		}
		t.scrollTop = top
		t.scrollBot = bot
		t.curX, t.curY = 0, 0
	}

	if seq == "?25h" {
		t.curVis = true
	}
	if seq == "?25l" {
		t.curVis = false
	}
	if seq == "?7h" {
		t.autowrap = true
	}
	if seq == "?7l" {
		t.autowrap = false
	}
	return true
}

func (t *Terminal) ScrollBack(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	prev := t.scrollOffset
	t.scrollOffset += n
	if t.scrollOffset > len(t.scrollback) {
		t.scrollOffset = len(t.scrollback)
	}
	if t.scrollOffset != prev {
		for y := 0; y < t.rows; y++ {
			t.dirty[y] = true
		}
	}
}

func (t *Terminal) ScrollForward(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	prev := t.scrollOffset
	t.scrollOffset -= n
	if t.scrollOffset < 0 {
		t.scrollOffset = 0
	}
	if t.scrollOffset != prev {
		for y := 0; y < t.rows; y++ {
			t.dirty[y] = true
		}
	}
}

func (t *Terminal) ResetScroll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.scrollOffset > 0 {
		for y := 0; y < t.rows; y++ {
			t.dirty[y] = true
		}
	}
	t.scrollOffset = 0
	t.selActive = false
	t.selStartX, t.selStartY = 0, 0
	t.selEndX, t.selEndY = 0, 0
	for y := 0; y < t.rows; y++ {
		t.dirty[y] = true
	}
}

func (t *Terminal) screenToAbs(screenX, screenY int) (int, int) {
	sbLines := t.scrollOffset
	if sbLines > len(t.scrollback) {
		sbLines = len(t.scrollback)
	}
	sbStart := len(t.scrollback) - sbLines
	if screenY >= 0 && screenY < sbLines {
		return screenX, sbStart + screenY
	}
	if screenY >= sbLines && screenY < t.rows {
		return screenX, len(t.scrollback) + (screenY - sbLines)
	}
	return -1, -1
}

func (t *Terminal) markAllDirty() {
	for y := 0; y < t.rows; y++ {
		t.dirty[y] = true
	}
}

func (t *Terminal) StartSelection(screenX, screenY int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ax, ay := t.screenToAbs(screenX, screenY)
	if ax < 0 {
		return
	}
	t.selStartX, t.selStartY = ax, ay
	t.selEndX, t.selEndY = ax, ay
	t.selActive = true
	t.markAllDirty()
}

func (t *Terminal) UpdateSelection(screenX, screenY int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.selActive {
		return
	}
	ax, ay := t.screenToAbs(screenX, screenY)
	if ax < 0 {
		return
	}
	t.selEndX, t.selEndY = ax, ay
	t.markAllDirty()
}

func (t *Terminal) EndSelection() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.selActive = false
	t.markAllDirty()
}

func (t *Terminal) ClearSelection() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.selActive = false
	t.selStartX, t.selStartY = 0, 0
	t.selEndX, t.selEndY = 0, 0
	t.markAllDirty()
}

func (t *Terminal) HasSelection() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.selStartX != t.selEndX || t.selStartY != t.selEndY
}

func (t *Terminal) SelectedText() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	sx, sy := t.selStartX, t.selStartY
	ex, ey := t.selEndX, t.selEndY
	if sy > ey || (sy == ey && sx > ex) {
		sx, sy, ex, ey = ex, ey, sx, sy
	}

	var buf strings.Builder
	for row := sy; row <= ey; row++ {
		if row != sy {
			buf.WriteByte('\n')
		}
		var rowData []TermCell
		if row < len(t.scrollback) {
			rowData = t.scrollback[row]
		} else {
			cr := row - len(t.scrollback)
			if cr < t.rows {
				rowData = t.cells[cr]
			}
		}
		if rowData == nil {
			continue
		}
		startCol := 0
		endCol := len(rowData)
		if row == sy {
			startCol = sx
		}
		if row == ey {
			endCol = ex + 1
			if endCol > len(rowData) {
				endCol = len(rowData)
			}
		}
		for col := startCol; col < endCol && col < len(rowData); col++ {
			ch := rowData[col].Ch
			if ch == 0 || ch == ' ' && col == endCol-1 {
				continue
			}
			buf.WriteRune(ch)
		}
	}
	return strings.TrimRight(buf.String(), " ")
}

func (t *Terminal) isSelected(absRow, absCol int) bool {
	if !t.selActive && (t.selStartX == t.selEndX && t.selStartY == t.selEndY) {
		return false
	}
	sx, sy := t.selStartX, t.selStartY
	ex, ey := t.selEndX, t.selEndY
	if sy > ey || (sy == ey && sx > ex) {
		sx, sy, ex, ey = ex, ey, sx, sy
	}
	if absRow < sy || absRow > ey {
		return false
	}
	if absRow == sy && absRow == ey {
		return absCol >= sx && absCol <= ex
	}
	if absRow == sy {
		return absCol >= sx
	}
	if absRow == ey {
		return absCol <= ex
	}
	return true
}

func (t *Terminal) Render(screen tcell.Screen, startY int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.curVis && t.scrollOffset == 0 {
		screen.ShowCursor(t.curX, startY+t.curY)
	} else {
		screen.HideCursor()
	}
	baseStyle := tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorDefault)

	sbLines := t.scrollOffset
	if sbLines > len(t.scrollback) {
		sbLines = len(t.scrollback)
	}
	sbStart := len(t.scrollback) - sbLines

	for y := 0; y < t.rows; y++ {
		if y < sbLines {
			row := t.scrollback[sbStart+y]
			absRow := sbStart + y
			for x := 0; x < t.cols; x++ {
				cell := row[x]
				style := cell.Style
				if cell.Ch == ' ' {
					style = baseStyle
				}
				if t.isSelected(absRow, x) {
					style = style.Reverse(true)
				}
				screen.SetContent(x, startY+y, cell.Ch, nil, style)
			}
		} else {
			cy := y - sbLines
			if !t.dirty[cy] {
				continue
			}
			absRow := len(t.scrollback) + cy
			for x := 0; x < t.cols; x++ {
				cell := t.cells[cy][x]
				style := cell.Style
				if cell.Ch == ' ' {
					style = baseStyle
				}
				if t.isSelected(absRow, x) {
					style = style.Reverse(true)
				}
				screen.SetContent(x, startY+y, cell.Ch, nil, style)
			}
			t.dirty[cy] = false
		}
	}
}

func (t *Terminal) RenderAt(screen tcell.Screen, x0, y0, w, h int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.rows != h || t.cols != w {
		t.resize(h, w)
	}
	if t.curVis {
		screen.ShowCursor(x0+t.curX, y0+t.curY)
	}
	baseStyle := tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorDefault)
	for y := 0; y < h; y++ {
		for x := 0; x < w && x < t.cols; x++ {
			cell := t.cells[y][x]
			style := cell.Style
			if cell.Ch == ' ' {
				style = baseStyle
			}
			screen.SetContent(x0+x, y0+y, cell.Ch, nil, style)
		}
		t.dirty[y] = false
	}
}

func parseANSI256(s string) tcell.Color {
	n := parseANSI(s)
	switch {
	case n < 16:
		return tcell.Color(n)
	case n < 232:
		n -= 16
		r := n / 36
		g := (n % 36) / 6
		b := n % 6
		return tcell.NewRGBColor(int32(r*51), int32(g*51), int32(b*51))
	default:
		gray := int32((n - 232) * 10 + 8)
		return tcell.NewRGBColor(gray, gray, gray)
	}
}

func parseANSI(s string) int {
	n := 0
	for _, ch := range s {
		if ch >= '0' && ch <= '9' {
			n = n*10 + int(ch-'0')
		}
	}
	return n
}

func isWide(ch rune) bool {
	if ch < 0x1100 {
		return false
	}
	return (ch <= 0x115F) ||
		(ch >= 0x2E80 && ch <= 0xA4CF) ||
		(ch >= 0xAC00 && ch <= 0xD7AF) ||
		(ch >= 0xF900 && ch <= 0xFAFF) ||
		(ch >= 0xFE30 && ch <= 0xFE6F) ||
		(ch >= 0xFF01 && ch <= 0xFF60) ||
		(ch >= 0xFFE0 && ch <= 0xFFE6)
}
