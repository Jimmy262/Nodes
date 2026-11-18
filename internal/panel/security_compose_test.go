package panel

import (
	"archive/zip"
	"bytes"
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
