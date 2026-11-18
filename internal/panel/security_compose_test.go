package panel

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func securityComposeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var data bytes.Buffer
	archive := zip.NewWriter(&data)
	for name, content := range files {
		file, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestGithubInspectionReportsCompatibleComposeAlternatives(t *testing.T) {
	const portable = "services:\n  web:\n    image: fixture:local\n    ports: ['8080:3000']\n"
	choices := inspectGithubChoices(securityComposeZip(t, map[string]string{
		"compose.yml":          "include: [./backend.yml]\n",
		"compose.dokploy.yml":  portable + "networks:\n  deploy:\n    external: true\n",
		"compose.portable.yml": portable,
		"deployment.yml":       portable,
		"other/compose.yml":    portable,
	}), "deployment.yml")
	if len(choices) != 5 || !strings.Contains(choices[0].Problem, "include") {
		t.Fatalf("expected an incompatible default and four root choices, got %+v", choices)
	}
	if choices[1].File != "compose.dokploy.yml" || choices[1].Problem == "" {
		t.Fatalf("expected the external-network variant to be incompatible, got %+v", choices[1])
	}
	for _, index := range []int{2, 4} {
		if choice := choices[index]; choice.Kind != "compose" || choice.Service != "web" || choice.Port != 3000 {
			t.Errorf("expected Compose metadata for choice %d, got %+v", index, choice)
		}
	}
}

func TestDeploymentRejectsUnreviewedLabelFiles(t *testing.T) {
	_, err := inspectZip(securityComposeZip(t, map[string]string{
		"compose.yaml":  "services:\n  web:\n    image: fixture:local\n    label_file: ./routes.labels\n",
		"routes.labels": "traefik.http.routers.extra.rule=Host(`extra.example.com`)\n",
	}), 0, 0)
	if err == nil {
		t.Fatal("label_file can add routers outside the reviewed labels")
	}
}

func TestDeploymentRejectsHomeReferences(t *testing.T) {
	for _, source := range []string{"~", "~/.fixture-home", "~someone/file"} {
		if relativeProjectPath(source) {
			t.Errorf("HOME shorthand accepted: %s", source)
		}
	}
	for _, source := range []string{".", "./config/.env", "./~/literal", "data"} {
		if !relativeProjectPath(source) {
			t.Errorf("project-local path rejected: %s", source)
		}
	}
	for _, field := range []string{
		"    volumes: ['~/.fixture-home:/fixture:ro']\n",
		"    volumes:\n      - type: bind\n        source: ~/.fixture-home\n        target: /fixture\n",
		"    env_file: ~/.fixture-home\n",
		"    build: {context: ~/.fixture-home}\n",
		"    build: {context: ., dockerfile: ~/.fixture-home}\n",
	} {
		if _, err := inspectZip(securityComposeZip(t, map[string]string{"compose.yaml": "services:\n  web:\n    image: fixture:local\n" + field}), 0, 0); err == nil {
			t.Errorf("HOME reference accepted: %s", field)
		}
	}
	for _, kind := range []string{"secrets", "configs"} {
		definition := "services:\n  web:\n    image: fixture:local\n" + kind + ":\n  file:\n    file: ~/.fixture-home\n"
		if _, err := inspectZip(securityComposeZip(t, map[string]string{"compose.yaml": definition}), 0, 0); err == nil {
			t.Errorf("HOME resource file accepted: %s", kind)
		}
	}
}
