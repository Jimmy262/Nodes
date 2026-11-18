package panel

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

func localSSHArgs(s Server, directory string) []string {
	args := []string{"-t", "-p", strconv.Itoa(s.Port), "-l", s.User, "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "PermitLocalCommand=no"}
	if s.sshIdentity != "" {
		args = append(args, "-F", "none", "-i", s.sshIdentity, "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none", "-o", "UserKnownHostsFile="+s.sshKnownHosts, "-o", "GlobalKnownHostsFile="+s.sshKnownHosts, "-o", "UpdateHostKeys=no")
	} else if s.Identity != "" {
		args = append(args, "-i", s.Identity, "-o", "IdentitiesOnly=yes")
	}
	args = append(args, "--", s.Host)
	if directory != "" {
		// Avoid embedded double quotes for Windows PowerShell's legacy native
		// argument marshalling, while keeping the remote path a shell literal.
		quoted := "'" + strings.ReplaceAll(directory, "'", "'\\''") + "'"
		args = append(args, "cd -- "+quoted+" && exec /bin/sh -l")
	}
	return args
}

func powershellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

func localSSHCommand(s Server, directory string) string {
	parts := []string{"ssh"}
	for _, arg := range localSSHArgs(s, directory) {
		if runtime.GOOS == "windows" {
			parts = append(parts, powershellQuote(arg))
		} else {
			parts = append(parts, shellQuote(arg))
		}
	}
	if runtime.GOOS == "windows" {
		return "& " + strings.Join(parts, " ")
	}
	return strings.Join(parts, " ")
}

func localTerminalAllowed(r *http.Request) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(peer).IsLoopback() {
		return false
	}
	host := r.Host
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

func (a *App) localTerminalOptions(w http.ResponseWriter, r *http.Request) {
	s, err := a.server(r)
	if err != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	directory := r.URL.Query().Get("directory")
	if validate(s) != nil || !validTerminalDirectory(directory) {
		fail(w, 400, "Conexión SSH o carpeta inválida")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	reply(w, 200, map[string]any{"available": localTerminalAllowed(r), "command": localSSHCommand(s, directory)})
}

func encodedPowerShell(script string) string {
	units := utf16.Encode([]rune(script))
	data := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(data[i*2:], unit)
	}
	return base64.StdEncoding.EncodeToString(data)
}

func launchLocalSSH(s Server, directory string) error {
	if runtime.GOOS != "windows" {
		return errors.New("Apertura local no disponible")
	}
	if _, err := exec.LookPath("ssh.exe"); err != nil {
		return err
	}
	if strings.ContainsRune(s.Identity, '"') {
		return errors.New("Ruta de clave inválida")
	}
	// The detached, visible PowerShell owns its interactive console. Arguments
	// are encoded and quoted as PowerShell literals; no cmd.exe interpolation.
	inner := encodedPowerShell(localSSHCommand(s, directory))
	script := "Start-Process -WindowStyle Normal -FilePath 'powershell.exe' -ArgumentList '-NoLogo -NoProfile -NoExit -EncodedCommand " + inner + "'"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", encodedPowerShell(script))
	hideLocalTerminalHelper(cmd)
	cmd.Env = sshEnvironment()
	return cmd.Run()
}

func (a *App) openLocalTerminal(w http.ResponseWriter, r *http.Request) {
	if !localTerminalAllowed(r) {
		fail(w, 403, "Para abrir PowerShell, accede a Nodes desde este PC por localhost. También puedes copiar el comando SSH.")
		return
	}
	s, err := a.server(r)
	if err != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	var input struct {
		Directory string `json:"directory"`
	}
	if decode(w, r, &input) != nil || validate(s) != nil || !validTerminalDirectory(input.Directory) {
		fail(w, 400, "Conexión SSH o carpeta inválida")
		return
	}
	launch := a.launchLocalTerminal
	if launch == nil {
		launch = launchLocalSSH
	}
	if err := launch(s, input.Directory); err != nil {
		fail(w, 500, "No se pudo abrir PowerShell. Copia el comando SSH y ejecútalo en tu terminal.")
		return
	}
	reply(w, 200, map[string]bool{"opened": true})
}
