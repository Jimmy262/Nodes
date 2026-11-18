#!/usr/bin/env bash
# Host-side updater for a Nodes Compose installation with a separate gateway.
set -Eeuo pipefail
umask 077

root=/var/lib/nodes-updater
managed=/opt/nodes-compose-updater
units=/etc/systemd/system
repository=https://github.com/Jimmy262/Nodes
tag_pattern='^v(0|[1-9][0-9]{0,7})\.(0|[1-9][0-9]{0,7})\.(0|[1-9][0-9]{0,7})$'
version=
base=
queued=0

die() { printf 'Nodes: %s\n' "$*" >&2; exit 1; }
while (($#)); do
    case $1 in
        --version) (($# >= 2)) || die 'Falta la versión.'; version=$2; shift 2 ;;
        --compose-file) (($# >= 2)) || die 'Falta el archivo Compose.'; base=$2; shift 2 ;;
        --queued) queued=1; shift ;;
        --help)
            printf 'Uso: sudo bash %s --version vX.Y.Z [--compose-file /ruta/compose.yaml]\n' "$0"
            exit 0 ;;
        *) die "Opción desconocida: $1" ;;
    esac
done

[[ $EUID == 0 && $(uname -s) == Linux && $(uname -m) == x86_64 ]] ||
    die 'Ejecuta este instalador como root en Linux x86_64.'
systemctl show-environment >/dev/null 2>&1 || die 'Se requiere systemd.'
docker compose version >/dev/null || die 'Se requiere Docker Compose.'
[[ ! -e /etc/nodo/managed ]] || die 'Esta máquina ya tiene un Nodes gestionado como servicio nativo.'
for directory in "$root" "$root/requests" "$managed"; do
    [[ ! -L "$directory" ]] || die "No se admiten enlaces simbólicos en $directory."
done
install -d -o root -g root -m 0700 "$managed"
exec 9>"$managed/lock"
flock -w 120 9 || die 'Ya hay una actualización en curso.'

if [[ -f "$managed/base" ]]; then
    [[ -z "$base" ]] || die 'El archivo Compose ya está configurado.'
    base=$(<"$managed/base")
else
    ((queued == 0)) && [[ "$base" == /* ]] || die 'Indica el archivo del Nodes que ya está en ejecución con --compose-file.'
    base=$(realpath -e "$base") || die 'No se encontró el archivo Compose.'
fi
# This configuration is consumed later by a root systemd service. Operators
# must keep the original file, its .env and its parent directories protected.
admin_owned() {
    local owner mode
    read -r owner mode < <(stat -c '%u %a' -- "$1")
    [[ "$owner" == 0 && "$mode" =~ ^[0-7]{3,4}$ ]] && (( (8#$mode & 022) == 0 ))
}
[[ -f "$base" && ! -L "$base" ]] && admin_owned "$base" ||
    die 'El archivo Compose debe pertenecer a root y estar protegido contra escritura.'
directory=$(dirname "$base")
if [[ -e "$directory/.env" || -L "$directory/.env" ]]; then
    [[ -f "$directory/.env" && ! -L "$directory/.env" ]] && admin_owned "$directory/.env" ||
        die 'El archivo .env de Compose debe estar protegido por root.'
fi
while [[ "$directory" != / ]]; do
    admin_owned "$directory" || die "Protege contra escritura el directorio $directory."
    directory=$(dirname "$directory")
done

if ((queued)); then
    [[ -z "$version" ]] || die 'La versión se lee de la cola.'
    for request in "$root/requests"/v*; do
        [[ -f "$request" && ! -L "$request" ]] || continue
        candidate=${request##*/}
        [[ $candidate =~ $tag_pattern ]] || continue
        if [[ -z "$version" || $(printf '%s\n' "$candidate" "$version" | sort -V | tail -n1) == "$candidate" ]]; then
            version=$candidate
        fi
    done
    [[ -n "$version" ]] || exit 0
fi
[[ $version =~ $tag_pattern ]] || die 'Indica una versión publicada con --version vX.Y.Z.'

original=(docker compose -f "$base")
if [[ -f "$managed/override.yaml" ]]; then
    original+=(-f "$managed/override.yaml")
fi
work=$(mktemp -d)
changed=0
stopped=0
result=failed
cleanup() {
    code=$?
    trap - EXIT
    if ((code != 0 && (changed || stopped))); then
        if [[ -f "$work/previous.yaml" ]]; then
            install -m 0600 "$work/previous.yaml" "$managed/.restore.yaml"
            mv -fT "$managed/.restore.yaml" "$managed/override.yaml"
        else
            rm -f -- "$managed/override.yaml"
        fi
        "${original[@]}" up -d --no-deps --no-build --pull never --force-recreate --wait --wait-timeout 90 app ||
            printf 'Revisa el estado de Nodes y el respaldo en %s/backups.\n' "$managed" >&2
    fi
    if [[ -d "$root/requests" ]]; then
        printf '%s %s\n' "$result" "$version" > "$root/.status"
        chmod 0644 "$root/.status"
        mv -fT "$root/.status" "$root/status"
        if ((queued)); then
            rm -f -- "$root/requests/$version"
        fi
    fi
    rm -rf -- "$work"
    exit "$code"
}
trap cleanup EXIT

if [[ -f "$managed/version" ]]; then
    current=$(<"$managed/version")
    [[ $current =~ $tag_pattern ]] || die 'Versión instalada no válida.'
    [[ "$version" != "$current" ]] || { result=ok; printf 'Nodes %s ya está instalado.\n' "$version"; exit 0; }
    [[ $(printf '%s\n' "$version" "$current" | sort -V | tail -n1) == "$version" ]] ||
        die 'No se permite instalar una versión anterior.'
fi

container=$("${original[@]}" ps --status running -q app)
[[ $container =~ ^[a-f0-9]{12,64}$ ]] || die 'Debe haber un contenedor app de Nodes en ejecución.'
[[ $(docker inspect -f '{{json .Config.Entrypoint}}' "$container") == '["/usr/local/bin/nodo-entrypoint"]' ]] ||
    die 'El contenedor no usa la imagen compatible de Nodes.'
identity=$(docker inspect -f '{{.Config.User}}' "$container")
[[ $identity =~ ^([1-9][0-9]*):([1-9][0-9]*)$ ]] || die 'Se requiere un UID y un GID numéricos sin privilegios para app.'
[[ $(docker inspect -f '{{.State.Health.Status}}' "$container") == healthy ]] ||
    die 'La instalación actual debe estar saludable antes de actualizar.'
data=$(docker inspect -f '{{range .Mounts}}{{if and (eq .Destination "/var/lib/nodo") (eq .Type "bind")}}{{.Source}}{{end}}{{end}}' "$container")
[[ "$data" == /* && -d "$data" && -f "$data/panel.db" ]] ||
    die 'El contenedor debe conservar panel.db en un directorio montado del host.'

release="$repository/releases/download/$version"
for asset in SHA256SUMS nodo-linux-amd64; do
    curl --fail --silent --show-error --location --retry 2 --max-time 120 \
        --proto '=https' --proto-redir '=https' --output "$work/$asset" "$release/$asset"
done
digest=$(awk '$2=="nodo-linux-amd64" { print $1 }' "$work/SHA256SUMS")
[[ $digest =~ ^[a-fA-F0-9]{64}$ ]] || die 'Falta el SHA-256 del binario Linux.'
(cd "$work" && printf '%s  nodo-linux-amd64\n' "$digest" | sha256sum --check --status) ||
    die 'La suma del binario descargado no coincide.'

install -d -o root -g root -m 0755 "$root" "$managed/releases" "$managed/releases/$version"
# The application may traverse release directories to execute its bound binary.
chmod 0755 "$managed"
install -d -o "${identity%:*}" -g "${identity#*:}" -m 0700 "$root/requests"
binary="$managed/releases/$version/nodo"
install -m 0555 "$work/nodo-linux-amd64" "$binary"
cat > "$work/override.yaml" <<EOF
services:
  app:
    environment:
      NODES_SELF_UPDATE: "1"
    volumes:
      - $binary:/usr/local/bin/nodo:ro
      - $root:$root:ro
      - $root/requests:$root/requests:rw
EOF
docker compose -f "$base" -f "$work/override.yaml" config --quiet
if [[ -f "$managed/override.yaml" ]]; then
    cp "$managed/override.yaml" "$work/previous.yaml"
fi
install -d -o root -g root -m 0700 "$managed/backups"
backup=$(mktemp "$managed/backups/nodes-XXXXXXXX")
"${original[@]}" stop -t 20 app
stopped=1
tar -C "$data" -czf "$backup" .
tar -tzf "$backup" >/dev/null
printf 'Respaldo privado en %s\n' "$backup"
install -m 0600 "$work/override.yaml" "$managed/.override.yaml"
mv -fT "$managed/.override.yaml" "$managed/override.yaml"
changed=1
docker compose -f "$base" -f "$managed/override.yaml" up -d --no-deps --no-build --pull never \
    --force-recreate --wait --wait-timeout 120 app

if [[ ! -f "$managed/base" ]]; then
    install -o root -g root -m 0755 "$(realpath "$0")" "$managed/install.sh"
    printf '%s\n' "$base" > "$managed/base"
    cat > "$units/nodes-update-compose.service" <<EOF
[Unit]
Description=Actualizar el panel Nodes de Docker Compose
After=docker.service

[Service]
Type=oneshot
ExecStart=$managed/install.sh --queued
TimeoutStartSec=10min
UMask=0077
EOF
    cat > "$units/nodes-update-compose.path" <<EOF
[Unit]
Description=Recibir solicitudes de actualización de Nodes

[Path]
PathChanged=$root/requests
Unit=nodes-update-compose.service

[Install]
WantedBy=multi-user.target
EOF
    chmod 0644 "$units/nodes-update-compose.service" "$units/nodes-update-compose.path"
    systemctl daemon-reload
fi
systemctl enable --now nodes-update-compose.path
printf '%s\n' "$version" > "$managed/.version"
mv -fT "$managed/.version" "$managed/version"
result=ok
printf 'Nodes %s está saludable. El panel puede solicitar futuras actualizaciones.\n' "$version"
