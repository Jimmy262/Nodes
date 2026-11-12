# Nodo en Linux y acceso desde celular

El panel incluye dos sesiones independientes por VPS: **Codex** y **Terminal**. Se muestran dentro del navegador mediante xterm.js, WebSocket autenticado y SSH con PTY. Ambas usan `tmux -L nodo-web` en la VPS. Cerrar la vista, cambiar de red o reiniciar Nodo desconecta el cliente; el proceso remoto continúa. Reiniciar la VPS o terminar el proceso dentro de tmux finaliza esa sesión. Volver a pulsar el botón crea o retoma la sesión del mismo tipo.

## Requisitos

- Linux amd64 para el binario `bin/nodo-linux-amd64`, OpenSSH cliente y certificados CA en el equipo que aloja Nodo.
- `tmux` en cada VPS y Codex instalado/autenticado para el usuario SSH de Codex.
- DNS y HTTPS para entrar desde el celular. Puedes usar Caddy, Nginx o tu proxy existente, conservando `Host`, `Origin` y la actualización WebSocket. No hace falta un puerto público separado para la terminal.
- Claves SSH, `~/.ssh/config` y `known_hosts` accesibles por el usuario del servicio. Verifica las huellas mediante un canal confiable. Las rutas de clave de Windows deben cambiarse por sus rutas Linux.

## Clave de protección en Linux

Windows conserva DPAPI. En Linux se requiere `NODO_MASTER_KEY`: exactamente 32 bytes aleatorios representados por 64 caracteres hexadecimales. Protege con AES-256-GCM la clave adicional del PIN y las claves de integraciones. Nodo falla al arrancar si no está configurada; no se degrada a un PIN sin protección.

Crea una vez el archivo privado del servicio, en el servidor Linux:

```sh
sudo install -d -m 700 /etc/nodo
sudo sh -c 'umask 077; test ! -e /etc/nodo/nodo.env && printf "NODO_MASTER_KEY=%s\n" "$(openssl rand -hex 32)" > /etc/nodo/nodo.env'
```

No regeneres esa clave al reiniciar ni la guardes en el repositorio o junto a la base de datos. Conserva un respaldo privado separado. Sin la clave original no se pueden descifrar las credenciales.

El PIN y las claves de integraciones protegidos por DPAPI no se pueden trasladar directamente desde Windows. Para una instalación nueva, crea una base nueva y configura el PIN e inventario. Para migrar una base existente, respáldala primero y usa `-reset-password` una sola vez en Linux con la nueva clave; el inventario se conserva, pero debes corregir las rutas SSH y volver a conectar GitHub/Dokploy y otras credenciales cifradas de Windows. El reinicio del PIN no convierte esas credenciales.

## Servicio y HTTPS

1. Crea un usuario de servicio `nodo`, su carpeta personal SSH y `/var/lib/nodo` con acceso exclusivo para ese usuario. Copia el binario a `/opt/nodo/nodo`, dale permiso de ejecución y verifica primero SSH con esa misma cuenta.
2. Copia [nodo.service](nodo.service) a `/etc/systemd/system/nodo.service`. Sustituye `https://nodo.ejemplo.com` por tu dominio real. `EnvironmentFile` lo lee systemd como root; debe mantenerse con permisos 0600.
3. Configura tu proxy HTTPS. [Caddyfile.example](Caddyfile.example) muestra la ruta al proceso local. DNS debe apuntar al servidor y el proxy debe poder emitir su certificado. Si ya usas Traefik/Dokploy, reutiliza ese proxy para evitar competir por 80/443.
4. Ejecuta `sudo systemctl daemon-reload` y `sudo systemctl enable --now nodo`. Abre tú mismo el enlace privado de configuración que muestra `sudo journalctl -u nodo`, y crea el PIN.
5. Desde el celular, entra en tu dominio HTTPS, inicia sesión y abre **Servidores → Codex** o **Terminal**.

Con proxy local, Nodo permanece en `127.0.0.1:8080`. `-public-url` habilita únicamente el Host y Origin HTTPS configurados; las cookies llevan Secure. No se confía en cabeceras de forwarding arbitrarias. El proxy debe estar en la misma máquina y acceder por loopback; un proxy en otro contenedor/red necesita TLS directo o una arquitectura de red configurada expresamente.

También se admite TLS directo con `-listen 0.0.0.0 -public-url https://tu-dominio -tls-cert /ruta/fullchain.pem -tls-key /ruta/privkey.pem`. Las conexiones fuera de loopback sin TLS se rechazan. La renovación del certificado y el reinicio para cargarlo quedan a cargo del operador.

## Controles y alcance

Al abrir una sesión se comprueban los requisitos. Si falta alguno, la vista ofrece **Revisar instalación → aceptar cambios → Confirmar e instalar**. La revisión caduca en diez minutos, pertenece a la sesión autenticada y no se puede reutilizar. La instalación se sigue en Actividad y continúa si cierras la vista, mientras Nodo permanezca abierto.

El instalador admite tmux y Node.js/npm mediante apt-get, dnf, yum o apk, con root o sudo sin contraseña. Codex se descarga desde el registro oficial npm (`@openai/codex`) en `~/.local` para ese usuario; primero deben estar disponibles Node.js y npm. No inicia sesión en OpenAI por ti, no actualiza deliberadamente un requisito ya detectado como instalado y no acepta paquetes o comandos arbitrarios desde el navegador. Si faltan permisos o la distribución no es compatible, indica que debes prepararlo mediante su administrador. Una instalación fallida puede dejar dependencias parciales; comprueba el estado antes de reintentar.

- La barra incluye **Ctrl+C, Esc, Tab, ↑, ↓, ←, →, Enter**, botón de teclado y **Reconectar**. El cuadro inferior permite enviar texto sin depender del teclado de terminal del móvil. No se reenvía texto automáticamente después de una reconexión.
- El PIN abre acceso administrativo al shell del usuario SSH. La sesión de terminal se comprueba antes de cada entrada y cada 15 segundos en reposo; salir, revocar sesiones o cambiar PIN corta ese acceso. El proceso remoto permanece en tmux para retomarlo al autenticarte otra vez.
- Una sesión de cada tipo por servidor; se comparte entre pestañas/dispositivos autorizados del mismo panel. Máximo ocho conexiones de terminal simultáneas. No es un sistema multiusuario con aislamiento por persona.
- No se guardan entradas ni salidas de terminal en Actividad, SQLite o localStorage. La pantalla e historial de tmux permanecen en la VPS. Codex conserva sus propios archivos según su configuración.
- `exit` termina el shell; salir de Codex termina su proceso. Ctrl+C puede interrumpir una tarea. Cerrar la vista únicamente desconecta. Para finalizar una sesión por SSH, identifica la correcta con `tmux -L nodo-web list-sessions` y usa `kill-session -t NOMBRE` solo sobre ella.
- La reconexión automática hace hasta tres intentos por caída; después usa **Reconectar**. No sobrevive al reinicio de la VPS ni recupera procesos que hayan terminado.

Documentación de referencia: [xterm.js y addons](https://xtermjs.org/docs/guides/using-addons/), [tmux: sesiones y desconexión](https://github.com/tmux/tmux/wiki/Getting-Started), [WebSocket en Caddy](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy).

Esta entrega prepara el software y los ejemplos de configuración. No publica el panel ni cambia DNS, proxy o servicios de las VPS automáticamente.
