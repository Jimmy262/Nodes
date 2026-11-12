package panel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	pty "github.com/aymanbagabas/go-pty"
	"github.com/gorilla/websocket"
)

type terminalBackend interface {
	io.ReadWriteCloser
	Resize(int, int) error
}
type sshTerminal struct {
	pty.Pty
	cancel    context.CancelFunc
	closeOnce sync.Once
	closeErr  error
}

func (t *sshTerminal) Close() error {
	t.closeOnce.Do(func() {
		t.cancel()
		// ClosePseudoConsole can wait for its output pipe to drain. Keep a
		// reader alive while closing even after the WebSocket writer stops.
		if console, ok := t.Pty.(pty.ConPty); ok {
			go io.Copy(io.Discard, console.OutputPipe())
		}
		t.closeErr = t.Pty.Close()
	})
	return t.closeErr
}

func startSSHPTY(parent context.Context, s Server, command string) (terminalBackend, error) {
	ctx, cancel := context.WithCancel(parent)
	t, err := pty.New()
	if err != nil {
		cancel()
		return nil, err
	}
	args := sshCommand(ctx, s, command).Args
	args[1] = "-tt"
	cmd := t.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = append(sshEnvironment(), "TERM=xterm-256color")
	if err = t.Resize(100, 30); err != nil {
		cancel()
		t.Close()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		cancel()
		t.Close()
		return nil, err
	}
	go func() { _ = cmd.Wait() }()
	return &sshTerminal{Pty: t, cancel: cancel}, nil
}

type terminalSession struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

var terminalIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (a *App) prepareTerminal(w http.ResponseWriter, r *http.Request) {
	s, err := a.server(r)
	if err != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	if validate(s) != nil {
		fail(w, 400, "Conexión SSH inválida")
		return
	}
	var input struct {
		Kind string `json:"kind"`
	}
	if decode(w, r, &input) != nil || (input.Kind != "codex" && input.Kind != "shell") {
		fail(w, 400, "Elige Codex o Terminal")
		return
	}
	// Availability checks never create a shell or start Codex.
	options, err := a.terminalRequirements(r.Context(), s)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	if !options.ready(input.Kind) {
		reply(w, 200, map[string]any{"requirements": options})
		return
	}
	id, err := randomToken()
	if err != nil {
		fail(w, 500, "No se pudo preparar la sesión")
		return
	}
	_, err = a.db.Exec(`INSERT INTO terminal_sessions VALUES(?,?,?,?,?) ON CONFLICT(server_id,kind) DO UPDATE SET id=CASE WHEN connection_hash=excluded.connection_hash THEN id ELSE excluded.id END,connection_hash=excluded.connection_hash`, id, s.ID, input.Kind, connectionHash(s), time.Now().Unix())
	if err != nil {
		fail(w, 500, "No se pudo guardar la sesión")
		return
	}
	if err = a.db.QueryRow("SELECT id FROM terminal_sessions WHERE server_id=? AND kind=?", s.ID, input.Kind).Scan(&id); err != nil {
		fail(w, 500, "No se pudo leer la sesión")
		return
	}
	reply(w, 200, terminalSession{id, input.Kind})
}

func remoteTerminalCommand(id, kind string) string {
	command := `exec "${SHELL:-/bin/sh}" -l`
	if kind == "codex" {
		command = `exec "${SHELL:-/bin/sh}" -lc ` + shellQuote(codexRemoteScript)
	}
	// A separate tmux socket isolates these sessions from the user's other tmux
	// sessions. Closing SSH detaches the client without terminating the pane.
	attach := `export TERM=xterm-256color; exec tmux -L nodo-web new-session -A -s ` + shellQuote("nodo-"+id) + ` -x 100 -y 30 ` + shellQuote(command)
	return `exec "${SHELL:-/bin/sh}" -lc ` + shellQuote(attach)
}

type terminalInput struct {
	Type string `json:"type"`
	Data string `json:"data"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

func applyTerminalInput(t terminalBackend, in terminalInput) error {
	switch in.Type {
	case "input":
		if len(in.Data) > 4096 {
			return errors.New("Entrada demasiado larga")
		}
		_, err := io.WriteString(t, in.Data)
		return err
	case "resize":
		if in.Cols < 20 || in.Cols > 500 || in.Rows < 5 || in.Rows > 200 {
			return errors.New("Tamaño inválido")
		}
		return t.Resize(in.Cols, in.Rows)
	default:
		return errors.New("Mensaje de terminal inválido")
	}
}

func (a *App) terminalAuthorized(r *http.Request, id, binding string) bool {
	session, err := a.session(r)
	if err != nil || session == nil {
		return false
	}
	server, err := a.server(r)
	if err != nil || connectionHash(server) != binding {
		return false
	}
	var stored string
	err = a.db.QueryRow("SELECT connection_hash FROM terminal_sessions WHERE id=? AND server_id=?", id, server.ID).Scan(&stored)
	return err == nil && stored == binding
}

func (a *App) terminalSocket(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != a.requestOrigin(r) {
		fail(w, 403, "Origen no permitido")
		return
	}
	session, err := a.session(r)
	if err != nil || session == nil {
		fail(w, 401, "Inicia sesión")
		return
	}
	s, err := a.server(r)
	if err != nil || validate(s) != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	id := r.PathValue("terminal")
	var kind, binding string
	if !terminalIDPattern.MatchString(id) {
		fail(w, 404, "Sesión no encontrada")
		return
	}
	err = a.db.QueryRow("SELECT kind,connection_hash FROM terminal_sessions WHERE id=? AND server_id=?", id, s.ID).Scan(&kind, &binding)
	if err != nil || binding != connectionHash(s) || (kind != "shell" && kind != "codex") {
		fail(w, 404, "Sesión no encontrada")
		return
	}
	select {
	case a.terminalSlots <- struct{}{}:
		defer func() { <-a.terminalSlots }()
	default:
		fail(w, 429, "Hay demasiadas terminales abiertas")
		return
	}
	upgrade := websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 8192, CheckOrigin: func(req *http.Request) bool { return req.Header.Get("Origin") == a.requestOrigin(req) }}
	ws, err := upgrade.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	ctx, cancel := context.WithCancel(a.terminalContext)
	defer cancel()
	start := a.startTerminal
	if start == nil {
		start = startSSHPTY
	}
	tty, err := start(ctx, s, remoteTerminalCommand(id, kind))
	if err != nil {
		_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(1011, "No se pudo iniciar SSH"), time.Now().Add(time.Second))
		return
	}
	defer tty.Close()
	output := make(chan []byte, 8)
	done := make(chan struct{})
	go func() {
		defer close(output)
		buf := make([]byte, 8192)
		for {
			n, e := tty.Read(buf)
			if n > 0 {
				data := append([]byte(nil), buf[:n]...)
				select {
				case output <- data:
				case <-ctx.Done():
					return
				}
			}
			if e != nil {
				return
			}
		}
	}()
	ws.SetReadLimit(8192)
	_ = ws.SetReadDeadline(time.Now().Add(45 * time.Second))
	ws.SetPongHandler(func(string) error { return ws.SetReadDeadline(time.Now().Add(45 * time.Second)) })
	go func() {
		defer close(done)
		for {
			typ, data, e := ws.ReadMessage()
			if e != nil {
				return
			}
			var in terminalInput
			if typ != websocket.TextMessage || json.Unmarshal(data, &in) != nil || !a.terminalAuthorized(r, id, binding) || applyTerminalInput(tty, in) != nil {
				return
			}
		}
	}()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case data, ok := <-output:
			if !ok {
				_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(1000, "Sesión desconectada"), time.Now().Add(time.Second))
				return
			}
			_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if ws.WriteMessage(websocket.BinaryMessage, data) != nil {
				return
			}
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-tick.C:
			if !a.terminalAuthorized(r, id, binding) {
				_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4001, "Inicia sesión de nuevo"), time.Now().Add(time.Second))
				return
			}
			if ws.WriteControl(websocket.PingMessage, []byte(strconv.FormatInt(time.Now().Unix(), 10)), time.Now().Add(5*time.Second)) != nil {
				return
			}
		}
	}
}
