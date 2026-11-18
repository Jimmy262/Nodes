# Nodo en Linux y acceso desde celular

## Instalar o actualizar Nodes desde GitHub

En Linux x86-64 con systemd, ejecuta como root:

```bash
tmp=$(mktemp)
curl -fsSL https://github.com/Jimmy262/Nodes/releases/download/v0.1.0/install-linux.sh -o "$tmp" &&
  bash "$tmp" --version v0.1.0
rm -f "$tmp"
```

Usa el mismo comando con el tag correspondiente para instalar una versión más
reciente. El instalador descarga el binario de ese tag, comprueba su SHA-256 y
mantiene `/var/lib/nodo` y `/etc/nodo`. Antes de actualizar, guarda un respaldo
privado bajo `/opt/nodo/backups`. Si falla el arranque, intenta volver al binario
anterior. El panel se ejecuta como el usuario de servicio `nodo`.

El puerto predeterminado es 8080 en loopback. Para registrar la primera passkey
desde otro equipo, abre un túnel SSH para el mismo puerto o configura tu proxy
HTTPS y pasa `--public-url https://nodes.ejemplo.com` en la primera instalación.
Consulta el enlace privado con `journalctl -u nodo -n 30 --no-pager`.

Cuando la instalación está gestionada por este script, **Actualizar Nodes**
permite solicitar una nueva versión confirmando con passkey. El servicio del
host realiza la instalación. Los registros se consultan con
`journalctl -u nodes-update`. El instalador se detiene si detecta la instalación
Docker Compose de producción o un servicio nativo configurado a mano.

Para publicar otra versión, construye el frontend con `npm ci && npm run build`
y compila el binario Linux amd64 con
`go build -trimpath -ldflags "-X admin-servidores/internal/panel.nodesVersion=vX.Y.Z" -o nodo-linux-amd64 ./cmd/panel`.
Publica `nodo-linux-amd64`, `scripts/install-linux.sh` y la suma del binario en
`SHA256SUMS` en una release estable con ese mismo tag.

El panel incluye dos sesiones independientes por VPS: **Codex** y **Terminal**. Se muestran dentro del navegador mediante xterm.js, WebSocket autenticado y SSH con PTY. Ambas usan `tmux -L nodo-web` en la VPS. Cerrar la vista, cambiar de red o reiniciar Nodo desconecta el cliente; el proceso remoto continúa. Reiniciar la VPS o terminar el proceso dentro de tmux finaliza esa sesión. Volver a pulsar el botón crea o retoma la sesión del mismo tipo.

## Requisitos

- Linux amd64 para el binario `bin/nodo-linux-amd64`, OpenSSH cliente y certificados CA en el equipo que aloja Nodo.
- `tmux` en cada VPS y Codex instalado/autenticado para el usuario SSH de Codex.
- Para despliegues desde GitHub: Python 3.10 o posterior y PyYAML con `SafeLoader` disponibles para la cuenta SSH. En Ubuntu/Debian el paquete es `python3-yaml`; la preparación verifica el import y bloquea el despliegue si falta. El ejecutor valida rutas y estructura antes de invocar Compose; no se admiten referencias externas mediante `include`, `extends`, `label_file` o rutas `~/`.
- DNS y HTTPS para entrar desde el celular. Puedes usar Caddy, Nginx o tu proxy existente, conservando `Host`, `Origin` y la actualización WebSocket. No hace falta un puerto público separado para la terminal.
- Claves SSH, `~/.ssh/config` y `known_hosts` accesibles por el usuario del servicio. Verifica las huellas mediante un canal confiable. Las rutas de clave de Windows deben cambiarse por sus rutas Linux.

## Actualizar ejecutores de GitHub existentes

Los ejecutores ya instalados son copias independientes en `~/.local/share/nodo/github/<id>/deploy.py`. Actualizar el panel no reemplaza esas copias. Antes de reactivar sus workflows, el operador debe verificar PyYAML para la cuenta SSH y sustituir cada `deploy.py` por `internal/panel/github_deploy.py` de esta versión, con respaldo privado, permisos `0600` y reemplazo atómico. Conserva `policy.json`, `ssh_gate.py`, las claves y los volúmenes de la aplicación. Esta corrección local no realiza actualizaciones remotas automáticas.

## Clave de protección en Linux

Windows conserva DPAPI. En Linux se requiere `NODO_MASTER_KEY`: exactamente 32 bytes aleatorios representados por 64 caracteres hexadecimales. Protege con AES-256-GCM las claves de integraciones. Nodo falla al arrancar si no está configurada. Las passkeys almacenan únicamente la credencial pública en el servidor.

Crea una vez el archivo privado del servicio, en el servidor Linux:

```sh
sudo install -d -m 700 /etc/nodo
sudo sh -c 'umask 077; test ! -e /etc/nodo/nodo.env && printf "NODO_MASTER_KEY=%s\n" "$(openssl rand -hex 32)" > /etc/nodo/nodo.env'
```

No regeneres esa clave al reiniciar ni la guardes en el repositorio o junto a la base de datos. Conserva un respaldo privado separado. Sin la clave original no se pueden descifrar las credenciales.

Las claves de integraciones protegidas por DPAPI no se pueden trasladar directamente desde Windows. Respalda la base, corrige las rutas SSH y vuelve a conectar las integraciones cifradas al migrar a Linux. Para un dominio nuevo registra una passkey en ese dominio; si migras una base con acceso configurado en otro dominio, usa `-reset-access` con la misma base de datos y configuración de inicio. Restablecer el acceso no convierte las credenciales cifradas.

## Servicio y HTTPS

1. Crea un usuario de servicio `nodo`, su carpeta personal SSH y `/var/lib/nodo` con acceso exclusivo para ese usuario. Copia el binario a `/opt/nodo/nodo`, dale permiso de ejecución y verifica primero SSH con esa misma cuenta.
2. Copia [nodo.service](nodo.service) a `/etc/systemd/system/nodo.service`. Sustituye `https://nodo.ejemplo.com` por tu dominio real. `EnvironmentFile` lo lee systemd como root; debe mantenerse con permisos 0600.
3. Configura tu proxy HTTPS. [Caddyfile.example](Caddyfile.example) muestra la ruta al proceso local. DNS debe apuntar al servidor y el proxy debe poder emitir su certificado. Si ya usas Traefik/Dokploy, reutiliza ese proxy para evitar competir por 80/443.
4. Ejecuta `sudo systemctl daemon-reload` y `sudo systemctl enable --now nodo`. Abre tú mismo el enlace privado de configuración que muestra `sudo journalctl -u nodo`, y registra tu passkey.
5. Desde el celular, entra en tu dominio HTTPS, inicia sesión y abre **Servidores → Codex** o **Terminal**.

Con proxy local, Nodo permanece en `127.0.0.1:8080`. `-public-url` habilita únicamente el Host y Origin HTTPS configurados; las cookies llevan Secure. No se confía en cabeceras de forwarding arbitrarias. El proxy debe estar en la misma máquina y acceder por loopback; un proxy en otro contenedor/red necesita TLS directo o una arquitectura de red configurada expresamente.

También se admite TLS directo con `-listen 0.0.0.0 -public-url https://tu-dominio -tls-cert /ruta/fullchain.pem -tls-key /ruta/privkey.pem`. Las conexiones fuera de loopback sin TLS se rechazan. La renovación del certificado y el reinicio para cargarlo quedan a cargo del operador.

## Controles y alcance

Al abrir una sesión se comprueban los requisitos. Si falta alguno, la vista ofrece **Revisar instalación → aceptar cambios → Confirmar e instalar**. La revisión caduca en diez minutos, pertenece a la sesión autenticada y no se puede reutilizar. La instalación se sigue en Actividad y continúa si cierras la vista, mientras Nodo permanezca abierto.

El instalador admite tmux y Node.js/npm mediante apt-get, dnf, yum o apk, con root o sudo sin contraseña. Codex se descarga desde el registro oficial npm (`@openai/codex`) en `~/.local` para ese usuario; primero deben estar disponibles Node.js y npm. No inicia sesión en OpenAI por ti, no actualiza deliberadamente un requisito ya detectado como instalado y no acepta paquetes o comandos arbitrarios desde el navegador. Si faltan permisos o la distribución no es compatible, indica que debes prepararlo mediante su administrador. Una instalación fallida puede dejar dependencias parciales; comprueba el estado antes de reintentar.

- La barra incluye **Ctrl+C, Esc, Tab, ↑, ↓, ←, →, Enter**, botón de teclado y **Reconectar**. El cuadro inferior permite enviar texto sin depender del teclado de terminal del móvil. No se reenvía texto automáticamente después de una reconexión.
- Una sesión verificada con passkey abre acceso administrativo al shell del usuario SSH. La sesión de terminal se comprueba antes de cada entrada y cada 15 segundos en reposo; salir, revocar sesiones o restablecer el acceso corta ese acceso. El proceso remoto permanece en tmux para retomarlo al autenticarte otra vez.
- Una sesión de cada tipo por servidor; se comparte entre pestañas/dispositivos autorizados del mismo panel. Máximo ocho conexiones de terminal simultáneas. No es un sistema multiusuario con aislamiento por persona.
- No se guardan entradas ni salidas de terminal en Actividad, SQLite o localStorage. La pantalla e historial de tmux permanecen en la VPS. Codex conserva sus propios archivos según su configuración.
- `exit` termina el shell; salir de Codex termina su proceso. Ctrl+C puede interrumpir una tarea. Cerrar la vista únicamente desconecta. Para finalizar una sesión por SSH, identifica la correcta con `tmux -L nodo-web list-sessions` y usa `kill-session -t NOMBRE` solo sobre ella.
- La reconexión automática hace hasta tres intentos por caída; después usa **Reconectar**. No sobrevive al reinicio de la VPS ni recupera procesos que hayan terminado.

Documentación de referencia: [xterm.js y addons](https://xtermjs.org/docs/guides/using-addons/), [tmux: sesiones y desconexión](https://github.com/tmux/tmux/wiki/Getting-Started), [WebSocket en Caddy](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy).

Esta entrega prepara el software y los ejemplos de configuración. No publica el panel ni cambia DNS, proxy o servicios de las VPS automáticamente.


El enlace para registrar la primera passkey caduca a los 15 minutos del arranque y solo puede canjearse una vez. Al abrirlo se autoriza únicamente ese navegador, sin ampliar el plazo. Completa el registro antes de la caducidad; si el enlace caduca o lo abriste en otro navegador, reinicia Nodes para emitir uno nuevo. Con el acceso ya configurado, reiniciar no permite registrar otra passkey sin autenticarse.
