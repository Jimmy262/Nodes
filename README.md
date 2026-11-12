# Nodes

Administrador de VPS Linux con backend Go, interfaz Svelte y SQLite. El panel se ejecuta en tu computadora, conecta tus servidores por OpenSSH y permite consultar recursos, administrar aplicaciones Docker y abrir terminales desde el navegador.

La identidad **Violeta pulso** incluye logo de tres nodos, icono y nueve paletas seleccionables desde **Apariencia**. Consulta el [branding y sus archivos descargables](docs/branding/README.md).

## Instalación local

Consulta la **[guía práctica para Windows y Linux](docs/INSTALACION-LOCAL.md)**: requisitos, clonación, compilación, primer PIN, conexión SSH, respaldos y solución de problemas.

En Windows, con Git, Node.js 22.12 o posterior compatible con Vite 7, Go 1.26 o posterior y OpenSSH instalados:

```powershell
git clone https://github.com/Jimmy262/Nodes.git
cd Nodes
.\start.ps1
```

Abre el enlace privado de configuración que aparece en la consola y crea un PIN de ocho números. En los siguientes accesos entra en `http://127.0.0.1:8080`. El ejecutable conserva por ahora el nombre `nodo.exe` y las opciones de configuración usan el prefijo `NODO_`.

Después de compilar puedes iniciar directamente:

```powershell
.\bin\nodo.exe -port 8080 -data .\data\panel.db
```

## Funciones

- Inventario de VPS con CPU, RAM, disco e históricos; selección y comparación de servidores.
- Aplicaciones agrupadas por Compose o Swarm, consumo Docker, enlaces públicos y logs por servicio.
- Terminal SSH y acceso a Codex dentro del navegador, con sesiones persistentes mediante tmux.
- Despliegues desde ZIP y repositorios GitHub, con revisión previa, variables de entorno y configuración de dominios.
- Biblioteca Compose con PostgreSQL, Redis, n8n y Supabase, con credenciales generadas para cada instalación.
- Edición de aplicaciones compatibles y operaciones de eliminación o traslado con revisión y confirmación.

La [edición directa de configuración](docs/EDICION-DIRECTA.md) utiliza SSH, Docker Swarm o los archivos Compose originales; no requiere una clave API de Dokploy.

Las funciones remotas requieren las herramientas y permisos correspondientes en cada VPS. El traslado automático tiene restricciones de arquitectura, redes, volúmenes y tipo de aplicación; la revisión indica qué se admite antes de ejecutar. No traslada DNS, certificados ni datos externos.

## Conectar un servidor

Comprueba primero `ssh -p 22 deploy@tu-servidor` y verifica la huella SSH. Añade nombre, host, usuario y puerto en **Servidores**; si hace falta, indica una ruta absoluta de clave privada. Nodo usa OpenSSH, SSH Agent y `known_hosts` de tu cuenta. No acepta huellas desconocidas automáticamente.

El usuario SSH necesita permiso para consultar Docker sin sudo interactivo. Los despliegues requieren Compose v2 y tar. La terminal utiliza tmux y los mismos permisos del usuario remoto; las operaciones de Docker pueden otorgar privilegios administrativos.

## Datos y acceso

`data/` es privado: contiene SQLite, inventario, históricos y credenciales cifradas. Windows protege secretos con DPAPI de tu cuenta; Linux requiere `NODO_MASTER_KEY`, una clave persistente de 32 bytes representada por 64 caracteres hexadecimales. La [guía local](docs/INSTALACION-LOCAL.md) explica cómo generarla y conservarla.

El panel usa un PIN de ocho números, sesiones en cookies HttpOnly, caducidad y límites de intentos. La opción «Recordarme» conserva la sesión durante 30 días. El PIN habilita acceso administrativo; no hay permisos separados por persona.

Por defecto escucha únicamente en `127.0.0.1`. Para alojarlo en Linux y acceder desde otro equipo o celular, sigue la [guía de servicio y HTTPS](deploy/README.md), con [systemd](deploy/nodo.service) y [Caddy](deploy/Caddyfile.example).

## Desarrollo

```sh
npm ci
npm run check
npm run build
go mod download
go vet ./...
go run ./cmd/panel
```

En Linux carga primero la clave maestra como indica la guía. Para recarga de la interfaz, ejecuta `npm run dev` en otra terminal y abre `http://127.0.0.1:5173`; el proxy se comunica con el backend en 8080. `?preview=design` muestra el diseño en desarrollo sin llamadas API.

Los archivos de terceros incluidos para la plantilla Supabase conservan su [licencia](internal/panel/templates/supabase/LICENSE) y [avisos](internal/panel/templates/supabase/NOTICE).
