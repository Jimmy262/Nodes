#!/usr/bin/env bash
# Installs and updates the native systemd distribution of Nodes.
set -Eeuo pipefail
umask 077

repo=https://github.com/Jimmy262/Nodes
app=/opt/nodo
config=/etc/nodo
data=/var/lib/nodo
updater=/var/lib/nodes-updater
units=/etc/systemd/system
tag_pattern='^v(0|[1-9][0-9]{0,7})\.(0|[1-9][0-9]{0,7})\.(0|[1-9][0-9]{0,7})$'
version=
public_url=
port=
queued=0

die() { printf 'Nodes: %s\n' "$*" >&2; exit 1; }

while (($#)); do
    case $1 in
        --version) version=${2:-}; shift 2 ;;
        --public-url) public_url=${2:-}; shift 2 ;;
        --port) port=${2:-}; shift 2 ;;
        --queued) queued=1; shift ;;
        --help)
            printf 'Uso: sudo bash %s --version vX.Y.Z [--port 8080] [--public-url https://nodes.ejemplo.com]\n' "$0"
            exit 0 ;;
        *) die "Opción desconocida: $1" ;;
    esac
done

[[ $EUID == 0 && $(uname -s) == Linux && $(uname -m) == x86_64 ]] ||
    die 'Ejecuta el instalador como root en Linux x86_64.'
for tool in curl openssl ssh systemctl flock sha256sum getent useradd tar sort; do
    command -v "$tool" >/dev/null || die "Instala primero el comando requerido: $tool."
done
systemctl show-environment >/dev/null 2>&1 || die 'Se requiere un sistema iniciado con systemd.'

[[ ! -e /var/lib/nodes-admin/panel.db ]] ||
    die 'Hay una instalación de Nodes con Docker Compose. Usa el procedimiento de esa instalación.'

for dir in "$app" "$config" "$data" "$updater" "$updater/requests" "$app/backups"; do
    [[ ! -L "$dir" ]] || die "No se admiten enlaces simbólicos en $dir."
done
install -d -o root -g root -m 0755 "$app"
exec 9>"$app/.install.lock"
flock -w 120 9 || die 'Hay otra actualización en curso.'

if ((queued)); then
    [[ -f "$config/managed" && -d "$updater/requests" ]] || die 'El servicio de actualización no está configurado.'
    for request in "$updater/requests"/v*; do
        [[ -f "$request" && ! -L "$request" ]] || continue
        candidate=${request##*/}
        [[ $candidate =~ $tag_pattern ]] || continue
        if [[ -z "$version" || $(printf '%s\n' "$candidate" "$version" | sort -V | tail -n1) == "$candidate" ]]; then
            version=$candidate
        fi
    done
    [[ -n "$version" ]] || exit 0
    [[ -z "$port" && -z "$public_url" ]] || die 'La solicitud del panel solo puede seleccionar una versión.'
fi
[[ $version =~ $tag_pattern ]] || die 'Indica una versión publicada, por ejemplo --version v0.1.0.'

work=$(mktemp -d)
result=failed
switched=0
stopped=0
existing=0
configured=0
completed=0
cleanup() {
    code=$?
    trap - EXIT
    if ((code != 0 && existing)); then
        if ((switched)); then
            install -m 0755 "$work/previous" "$app/.nodo.rollback" &&
                mv -fT "$app/.nodo.rollback" "$app/nodo" || true
        fi
        if ((stopped || switched)); then
            systemctl restart nodo.service || true
        fi
    elif ((code != 0 && switched && !completed)); then
        systemctl stop nodo.service || true
    fi
    if [[ -d "$updater/requests" && -f "$config/managed" ]]; then
        printf '%s %s\n' "$result" "$version" > "$updater/.status"
        chmod 0644 "$updater/.status"
        mv -fT "$updater/.status" "$updater/status"
        if ((queued)); then
            rm -f -- "$updater/requests/$version"
        fi
    fi
    rm -rf -- "$work"
    exit "$code"
}
trap cleanup EXIT

if [[ -f "$config/managed" ]]; then
    [[ -f "$units/nodo.service" && -f "$config/nodo.env" ]] ||
        die 'La instalación de Nodes está incompleta. Revisa el servicio antes de continuar.'
    configured=1
    [[ -z "$port" && -z "$public_url" ]] || die 'La actualización conserva la URL y el puerto instalados.'
    if [[ -f "$app/version" ]]; then
        [[ -f "$app/nodo" ]] || die 'No se encontró el binario de Nodes.'
        existing=1
        current=$(<"$app/version")
        [[ $current =~ $tag_pattern ]] || die 'La versión instalada no es válida.'
        [[ "$version" != "$current" ]] || { result=ok; printf 'Nodes %s ya está instalado.\n' "$version"; exit 0; }
        [[ $(printf '%s\n' "$version" "$current" | sort -V | tail -n1) == "$version" ]] ||
            die 'La versión solicitada es anterior a la instalada.'
    fi
    mapfile -t settings < "$config/managed"
    port=${settings[0]:-}
    public_url=${settings[1]:-}
else
    [[ ! -e "$config/nodo.env" && ! -e "$app/nodo" && ! -e "$data/panel.db" &&
       ! -e "$units/nodo.service" && $(systemctl show -p LoadState --value nodo.service) == not-found ]] ||
        die 'Ya existe una instalación manual de Nodes. Se requiere una migración explícita.'
    port=${port:-8080}
fi
[[ $port =~ ^[1-9][0-9]{0,4}$ ]] && ((port <= 65535)) || die 'Puerto inválido.'
[[ -z "$public_url" || $public_url =~ ^https://[a-zA-Z0-9.-]+(:[0-9]{1,5})?$ ]] ||
    die 'Indica un dominio HTTPS sin ruta.'

release="$repo/releases/download/$version"
download() {
    curl --fail --silent --show-error --location --retry 2 --max-time 120 \
        --proto '=https' --proto-redir '=https' --output "$2" "$release/$1"
}
download SHA256SUMS "$work/SHA256SUMS"
download nodo-linux-amd64 "$work/nodo-linux-amd64"
digest=$(awk '$2=="nodo-linux-amd64" { print $1 }' "$work/SHA256SUMS")
[[ $digest =~ ^[a-fA-F0-9]{64}$ ]] || die 'Falta el SHA-256 del binario Linux.'
(cd "$work" && printf '%s  nodo-linux-amd64\n' "$digest" | sha256sum --check --status) ||
    die 'La suma del binario descargado no coincide.'

if ((existing)); then
    install -d -o root -g root -m 0700 "$app/backups"
    install -m 0700 "$app/nodo" "$work/previous"
    systemctl stop nodo.service
    stopped=1
    backup=$(mktemp "$app/backups/nodes-$current-XXXXXXXX")
    tar -C / -czf "$backup" etc/nodo var/lib/nodo
    printf 'Respaldo privado en %s\n' "$backup"
elif ((!configured)); then
    getent passwd nodo >/dev/null && die 'Ya existe el usuario nodo. Se requiere una instalación limpia.'
    useradd --system --user-group --home-dir "$data" --shell /usr/sbin/nologin nodo
    install -d -o nodo -g nodo -m 0700 "$data"
    install -d -o root -g root -m 0700 "$config"
    install -d -o root -g root -m 0755 "$updater"
    install -d -o nodo -g nodo -m 0700 "$updater/requests"
    printf 'NODO_MASTER_KEY=%s\n' "$(openssl rand -hex 32)" > "$config/nodo.env"
    install -o root -g root -m 0755 "$(realpath "$0")" "$app/install-linux.sh"
    public_argument=
    [[ -z "$public_url" ]] || public_argument=" -public-url $public_url"
    cat > "$units/nodo.service" <<EOF
[Unit]
Description=Nodes
After=network-online.target
Wants=network-online.target

[Service]
User=nodo
Group=nodo
WorkingDirectory=$data
EnvironmentFile=$config/nodo.env
Environment=NODES_SELF_UPDATE=1
ExecStart=$app/nodo -listen 127.0.0.1 -port $port -data $data/panel.db$public_argument
Restart=on-failure
RestartSec=5
PrivateTmp=true
NoNewPrivileges=true
UMask=0077

[Install]
WantedBy=multi-user.target
EOF
    cat > "$units/nodes-update.service" <<EOF
[Unit]
Description=Actualizar Nodes desde su release oficial en GitHub
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=$app/install-linux.sh --queued
TimeoutStartSec=10min
UMask=0077
EOF
    cat > "$units/nodes-update.path" <<EOF
[Unit]
Description=Recibir solicitudes de actualización de Nodes

[Path]
PathChanged=$updater/requests
Unit=nodes-update.service

[Install]
WantedBy=multi-user.target
EOF
    chmod 0644 "$units/nodo.service" "$units/nodes-update.service" "$units/nodes-update.path"
    printf '%s\n%s\n' "$port" "$public_url" > "$config/managed"
    systemctl daemon-reload
fi

install -m 0755 "$work/nodo-linux-amd64" "$app/.nodo.new"
mv -fT "$app/.nodo.new" "$app/nodo"
switched=1
systemctl restart nodo.service
host=${public_url#https://}
[[ -n "$host" ]] || host="localhost:$port"
healthy=0
for ((attempt=0; attempt<30; attempt++)); do
    if systemctl is-active --quiet nodo.service &&
        curl -fsS --max-time 2 -H "Host: $host" "http://127.0.0.1:$port/api/auth/status" > "$work/health" &&
        grep -q '"passkeyAvailable":true' "$work/health"; then
        healthy=1
        break
    fi
    sleep 1
done
((healthy)) || die 'La nueva versión no respondió. Consulta journalctl -u nodo.'
systemctl enable nodo.service
systemctl enable --now nodes-update.path
printf '%s\n' "$version" > "$app/.version"
mv -fT "$app/.version" "$app/version"
result=ok
completed=1
printf 'Nodes %s disponible en %s\n' "$version" "${public_url:-http://localhost:$port}"
if grep -q '"configured":false' "$work/health"; then
    printf 'Consulta el enlace inicial privado con: journalctl -u nodo -n 30 --no-pager\n'
fi
