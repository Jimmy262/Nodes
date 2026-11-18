package panel

const codexEnvironment = `export PATH="$HOME/.local/bin:$HOME/.npm-global/bin:$PATH"; if { ! command -v codex >/dev/null 2>&1 || ! command -v claude >/dev/null 2>&1; } && [ -s "$HOME/.nvm/nvm.sh" ]; then . "$HOME/.nvm/nvm.sh"; fi`
const codexRemoteScript = codexEnvironment + `; if ! command -v codex >/dev/null 2>&1; then printf '\nNo se encontro Codex para este usuario SSH. Revisa su instalacion y PATH.\n' >&2; exit 127; fi; cd "$HOME" || exit 1; exec codex`

func agentRemoteScript(kind, directory string) string {
	if kind != "claude" {
		kind = "codex"
	}
	path := `"$HOME"`
	if directory != "" {
		path = shellQuote(directory)
	}
	return codexEnvironment + `; if ! command -v ` + kind + ` >/dev/null 2>&1; then printf 'No se encontro ` + kind + ` para este usuario SSH.\n' >&2; exit 127; fi; cd -- ` + path + ` || exit 1; exec ` + kind
}
