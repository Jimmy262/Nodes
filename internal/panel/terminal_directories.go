package panel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

type terminalDirectoryEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
type terminalDirectory struct {
	Path      string                   `json:"path"`
	Parent    string                   `json:"parent"`
	Entries   []terminalDirectoryEntry `json:"entries"`
	Truncated bool                     `json:"truncated"`
}

func validTerminalDirectory(value string) bool {
	return len(value) <= 4096 && !strings.ContainsAny(value, "\x00\r\n") && (value == "" || strings.HasPrefix(value, "/"))
}

// Only directory metadata is read. Paths are quoted data, never shell syntax.
const terminalDirectoryScript = `import os,sys,json
p=os.path.realpath(sys.argv[1] or os.path.expanduser("~"))
if not os.path.isdir(p): sys.exit(2)
entries=[]; truncated=False
with os.scandir(p) as source:
 for entry in source:
  if any(c in entry.name for c in "\r\n"): continue
  try:
   if not entry.is_dir(): continue
  except OSError: continue
  if len(entries)==500: truncated=True; break
  entries.append({"name":entry.name,"path":os.path.join(p,entry.name)})
entries.sort(key=lambda item:item["name"].casefold())
print(json.dumps({"path":p,"parent":os.path.dirname(p),"entries":entries,"truncated":truncated}))`

func (a *App) readTerminalDirectory(ctx context.Context, server Server, path string) (terminalDirectory, error) {
	var result terminalDirectory
	if !validTerminalDirectory(path) {
		return result, errors.New("Usa una ruta absoluta de hasta 4096 caracteres")
	}
	raw, err := a.run(ctx, server, "python3 -c "+shellQuote(terminalDirectoryScript)+" "+shellQuote(path))
	if err != nil || json.Unmarshal([]byte(raw), &result) != nil || result.Path == "" || !validTerminalDirectory(result.Path) || !validTerminalDirectory(result.Parent) || len(result.Entries) > 500 {
		return result, errors.New("No se pudo abrir la carpeta. Comprueba la ruta, los permisos y que Python 3 esté disponible")
	}
	for _, entry := range result.Entries {
		if entry.Name == "" || !validTerminalDirectory(entry.Path) {
			return terminalDirectory{}, errors.New("La lista de carpetas no es válida")
		}
	}
	if result.Entries == nil {
		result.Entries = []terminalDirectoryEntry{}
	}
	return result, nil
}

func (a *App) terminalDirectories(w http.ResponseWriter, r *http.Request) {
	server, err := a.server(r)
	if err != nil || validate(server) != nil {
		fail(w, 404, "Servidor no encontrado")
		return
	}
	if !validTerminalDirectory(r.URL.Query().Get("path")) {
		fail(w, 400, "Usa una ruta absoluta válida")
		return
	}
	result, err := a.readTerminalDirectory(r.Context(), server, r.URL.Query().Get("path"))
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	reply(w, 200, result)
}

func (a *App) storedTerminal(id string, serverID int64) (kind, binding, directory string, err error) {
	err = a.db.QueryRow(`SELECT kind,connection_hash,'' FROM terminal_sessions WHERE id=? AND server_id=?
 UNION ALL SELECT kind,connection_hash,directory FROM terminal_workspace_sessions WHERE id=? AND server_id=?`, id, serverID, id, serverID).Scan(&kind, &binding, &directory)
	return
}
