package panel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var nodesVersion = "dev"

const nodesReleaseAPI = "https://api.github.com/repos/Jimmy262/Nodes/releases/latest"
const nodesUpdateRoot = "/var/lib/nodes-updater"

var nodesReleaseTag = regexp.MustCompile(`^v(0|[1-9][0-9]{0,7})\.(0|[1-9][0-9]{0,7})\.(0|[1-9][0-9]{0,7})$`)

func newerNodesVersion(candidate, current string) bool {
	next, installed := nodesReleaseTag.FindStringSubmatch(candidate), nodesReleaseTag.FindStringSubmatch(current)
	if next == nil || installed == nil {
		return false
	}
	for i := 1; i < len(next); i++ {
		a, _ := strconv.Atoi(next[i])
		b, _ := strconv.Atoi(installed[i])
		if a != b {
			return a > b
		}
	}
	return false
}

func (a *App) latestNodesVersion(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nodesReleaseAPI, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := a.githubClient().Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", errors.New("GitHub no devolvió una versión publicada")
	}
	var release struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&release); err != nil {
		return "", err
	}
	assets := make(map[string]bool)
	for _, asset := range release.Assets {
		assets[asset.Name] = true
	}
	if !nodesReleaseTag.MatchString(release.Tag) || !assets["nodo-linux-amd64"] || !assets["SHA256SUMS"] {
		return "", errors.New("La versión publicada no incluye un paquete de Nodes compatible")
	}
	return release.Tag, nil
}

func nodesUpdateEnabled() bool {
	return os.Getenv("NODES_SELF_UPDATE") == "1" && nodesReleaseTag.MatchString(nodesVersion)
}

func (a *App) nodesUpdateStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]any{"current": nodesVersion, "enabled": nodesUpdateEnabled()}
	if status["enabled"] == true {
		if entries, err := os.ReadDir(filepath.Join(nodesUpdateRoot, "requests")); err == nil {
			for _, entry := range entries {
				if nodesReleaseTag.MatchString(entry.Name()) {
					status["pending"] = entry.Name()
					break
				}
			}
		}
		if info, err := os.Stat(filepath.Join(nodesUpdateRoot, "status")); err == nil && info.Size() <= 128 {
			if content, err := os.ReadFile(filepath.Join(nodesUpdateRoot, "status")); err == nil {
				status["lastResult"] = strings.TrimSpace(string(content))
			}
		}
	}
	if r.URL.Query().Get("check") == "1" {
		latest, err := a.latestNodesVersion(r.Context())
		if err != nil {
			fail(w, 502, "No se pudo consultar una versión de Nodes en GitHub")
			return
		}
		status["latest"] = latest
		status["available"] = newerNodesVersion(latest, nodesVersion)
	}
	reply(w, 200, status)
}

func (a *App) requestNodesUpdate(w http.ResponseWriter, r *http.Request) {
	if !nodesUpdateEnabled() {
		fail(w, 409, "Esta instalación no tiene un actualizador de Nodes configurado")
		return
	}
	if _, err := a.recentSession(r); err != nil {
		fail(w, 403, err.Error())
		return
	}
	var input struct {
		Version string `json:"version"`
	}
	if decode(w, r, &input) != nil || !newerNodesVersion(input.Version, nodesVersion) {
		fail(w, 400, "Selecciona una versión posterior de Nodes")
		return
	}
	latest, err := a.latestNodesVersion(r.Context())
	if err != nil || input.Version != latest {
		fail(w, 409, "Consulta de nuevo la versión publicada antes de actualizar")
		return
	}
	file, err := os.OpenFile(filepath.Join(nodesUpdateRoot, "requests", latest), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		reply(w, 202, map[string]string{"pending": latest})
		return
	}
	if err != nil {
		fail(w, 503, "No se pudo solicitar la actualización al servicio del servidor")
		return
	}
	file.Close()
	reply(w, 202, map[string]string{"pending": latest})
}
