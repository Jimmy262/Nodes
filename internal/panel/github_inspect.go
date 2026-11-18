package panel

import (
	"archive/zip"
	"bytes"
	"net/http"
	"path"
	"slices"
	"strings"
)

type githubProjectChoice struct {
	File    string `json:"file"`
	Kind    string `json:"kind,omitempty"`
	Problem string `json:"problem,omitempty"`
	Service string `json:"service,omitempty"`
	Port    int    `json:"port,omitempty"`
}

func inspectGithubChoices(data []byte, selected string) []githubProjectChoice {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return []githubProjectChoice{{File: selected, Problem: "No se pudo leer la carpeta del repositorio"}}
	}
	var files []string
	for _, entry := range archive.File {
		if !entry.Mode().IsRegular() || !validComposeFilename(entry.Name) {
			continue
		}
		name := strings.TrimSuffix(entry.Name, path.Ext(entry.Name))
		if name == "compose" || name == "docker-compose" ||
			strings.HasPrefix(name, "compose.") || strings.HasPrefix(name, "compose-") ||
			strings.HasPrefix(name, "docker-compose.") || strings.HasPrefix(name, "docker-compose-") {
			files = append(files, entry.Name)
		}
	}
	slices.Sort(files)
	if len(files) > 12 {
		files = files[:12]
	}
	if selected != "" && !slices.Contains(files, selected) {
		files = append(files, selected)
	}
	choices := make([]githubProjectChoice, 0, len(files)+1)
	for _, filename := range append([]string{""}, files...) {
		choice := githubProjectChoice{File: filename}
		project, err := inspectZipWithCompose(data, 8080, 80, filename)
		if err != nil {
			choice.Problem = err.Error()
		} else {
			choice.Kind = "compose"
			choice.File = project.compose
			if project.dockerfile {
				choice.Kind = "dockerfile"
				choice.File = ""
			}
			choice.Service = githubDefaultService(project)
			choice.Port = githubDefaultPort(project, choice.Service)
		}
		choices = append(choices, choice)
	}
	return choices
}

func (a *App) inspectGithubDeployment(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Repository string `json:"repository"`
		Branch     string `json:"branch"`
		BuildPath  string `json:"buildPath"`
		Compose    string `json:"composeFile"`
	}
	if decode(w, r, &input) != nil ||
		!validGithubProject(input.Repository, input.Branch, input.BuildPath) ||
		(input.Compose != "" && !validComposeFilename(input.Compose)) {
		fail(w, 400, "Revisa el repositorio, la rama, la carpeta y el nombre del archivo Compose")
		return
	}
	token, err := a.githubToken()
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	defer clear(token)
	commit, err := a.githubBranchCommit(r.Context(), token, input.Repository, input.Branch)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	data, err := a.githubProjectArchive(r.Context(), token, input.Repository, commit, path.Clean(input.BuildPath))
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	choices := inspectGithubChoices(data, input.Compose)
	reply(w, 200, map[string]any{"automatic": choices[0], "files": choices[1:]})
}
