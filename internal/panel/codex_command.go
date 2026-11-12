package panel

const codexEnvironment = `export PATH="$HOME/.local/bin:$HOME/.npm-global/bin:$PATH"; if ! command -v codex >/dev/null 2>&1 && [ -s "$HOME/.nvm/nvm.sh" ]; then . "$HOME/.nvm/nvm.sh"; fi`
const codexRemoteScript = codexEnvironment + `; if ! command -v codex >/dev/null 2>&1; then printf '\nNo se encontro Codex para este usuario SSH. Revisa su instalacion y PATH.\n' >&2; exit 127; fi; cd "$HOME" || exit 1; exec codex`
