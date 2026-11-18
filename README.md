# Nodes

Administrador de VPS Linux con backend Go, interfaz Svelte y SQLite. El panel se ejecuta en tu computadora, conecta tus servidores por OpenSSH y permite consultar recursos, administrar aplicaciones Docker y abrir terminales desde el navegador.

La identidad **Violeta pulso** incluye logo de tres nodos, icono y nueve paletas seleccionables desde **Apariencia**. Consulta el [branding y sus archivos descargables](docs/branding/README.md).

## Instalación local

Consulta la **[guía práctica para Windows y Linux](docs/INSTALACION-LOCAL.md)**: requisitos, clonación, compilación, primera passkey, conexión SSH, respaldos y solución de problemas.

En Windows, con Git, Node.js 22.12 o posterior compatible con Vite 7, Go 1.26 o posterior y OpenSSH instalados:

```powershell
git clone https://github.com/Jimmy262/Nodes.git
cd Nodes
.\start.ps1
```

Abre el enlace privado de configuración que aparece en la consola y registra una passkey con tu dispositivo. En los siguientes accesos entra en `http://localhost:8080`. El ejecutable conserva por ahora el nombre `nodo.exe` y las opciones de configuración usan el prefijo `NODO_`.

Después de compilar puedes iniciar directamente:

```powershell
.\bin\nodo.exe -port 8080 -data .\data\panel.db
```

## Funciones

- Inventario de VPS con CPU, RAM, disco e históricos; selección y comparación de servidores.
- Aplicaciones agrupadas por Compose o Swarm, consumo Docker, enlaces públicos y logs por servicio.
- Terminal SSH y accesos por iconos a Codex y Claude dentro del navegador, con sesiones persistentes mediante tmux. El botón de carpeta permite explorar directorios y abrir una sesión independiente en la ruta elegida; las sesiones anteriores se conservan. El explorador requiere Python 3 en el servidor y muestra únicamente carpetas, con los permisos del usuario SSH.

Si falta un requisito de terminal, su botón **Instalar** prepara y ejecuta la instalación con un clic. El progreso se muestra en la ventana y en Actividad; al terminar se vuelve a comprobar el requisito y se abre la herramienta. El inicio de sesión en Codex o Claude se realiza después, desde la terminal.

En **GitHub**, busca o pega la URL del repositorio, elige el servidor y revisa el acceso público. El nombre y la rama se completan desde el repositorio; el servicio y el puerto interno se detectan cuando su definición es inequívoca. Variables de entorno, nombre, rama, carpeta y ajustes avanzados quedan plegados y se pueden modificar antes de revisar. La revisión muestra qué se preparará antes de ejecutar.

En **Aplicaciones → GitHub → Nueva aplicación**, elige el repositorio y el servidor. Nodes detecta Compose o Dockerfile y muestra los bloqueos de compatibilidad. Para usar un dominio con HTTPS, apunta su registro A a la IP indicada y pulsa **Volver a comprobar DNS** en la revisión. **Desplegar aplicación** instala el ejecutor en la VPS y publica el commit mostrado. Esta operación requiere acceso de lectura al código en el repositorio.

Para desplegar por push, abre **Activar despliegue automático** en la aplicación conectada. El token de alcance detallado necesita acceso a ese repositorio, **Contents**, **Workflows** y **Secrets** con lectura y escritura. **Actions** con lectura permite consultar el historial. Nodes crea una clave SSH exclusiva para el ejecutor y guarda los secretos cifrados antes de publicar el workflow. Puedes abrir el workflow en GitHub para verificar su ejecución. **Desplegar ahora** y **Actualizar desde GitHub** preparan un despliegue manual del último commit.

Puedes **Actualizar repositorios** y **Actualizar token** sin desconectar la cuenta. La lista también se recarga al crear una aplicación y al volver a la pestaña. Si falta un repositorio, pega su URL en el buscador para comprobar su acceso. Los tokens granulares limitados a repositorios concretos necesitan que autorices los nuevos en GitHub; actualizar la lista no modifica esos permisos.

**Actualizar Nodes**, encima de **Apariencia**, consulta las versiones del panel publicadas en GitHub. En Linux, las instalaciones hechas con el instalador de systemd pueden aplicar una versión nueva desde ese diálogo. Las aplicaciones externas se siguen actualizando desde su propia ficha.

- Despliegues desde ZIP y repositorios GitHub, con revisión previa, variables de entorno y configuración de dominios.
- Biblioteca Compose con PostgreSQL, Redis, n8n y Supabase, con credenciales generadas para cada instalación.
- Edición de aplicaciones compatibles y operaciones de eliminación o traslado con revisión y confirmación.

La [edición directa de configuración](docs/EDICION-DIRECTA.md) utiliza SSH, Docker Swarm o los archivos Compose originales; no requiere una clave API de Dokploy.

Las funciones remotas requieren las herramientas y permisos correspondientes en cada VPS. El traslado automático tiene restricciones de arquitectura, redes, volúmenes y tipo de aplicación; la revisión indica qué se admite antes de ejecutar. No traslada DNS, certificados ni datos externos.

En **GitHub → Desplegar → Código y nombre**, **Archivo Compose** permite elegir un
`.yml` o `.yaml` en la carpeta del proyecto. Déjalo vacío para conservar la
detección automática. El archivo elegido debe definir todos los servicios y usar
redes y volúmenes propios del proyecto. En modo **IP y puerto**, declara en ese
archivo los puertos que quieras publicar. La revisión muestra el archivo que se
usará también en los despliegues siguientes.

## Conectar un servidor

Pulsa **Conectar servidor**, indica su IP o dominio, usuario y puerto, y verifica la huella SSH con la consola de tu proveedor o un canal de confianza. Introduce su contraseña SSH actual una sola vez: Nodes genera una clave Ed25519 exclusiva, añade únicamente su parte pública a la cuenta remota y comprueba el acceso con esa clave antes de guardar el servidor. No muestra la clave pública ni la privada, no guarda la contraseña y conserva las claves que ya estaban autorizadas.

La nueva clave se guarda cifrada en SQLite mediante DPAPI en Windows o la clave maestra en Linux. OpenSSH utiliza archivos temporales privados; en el contenedor de producción están en tmpfs y se eliminan al cerrar Nodes. La huella del host queda fijada para las conexiones posteriores. Conserva la base y la clave maestra en tus respaldos. **Ya tengo un acceso SSH configurado** mantiene la opción avanzada para conexiones existentes con OpenSSH o una ruta de identidad; no necesitas habilitar contraseñas si ya usas ese acceso.

El usuario SSH necesita permiso para consultar Docker sin sudo interactivo. Los despliegues requieren Compose v2 y tar. El ejecutor de GitHub necesita además Python 3.10 o posterior y PyYAML con `SafeLoader` (validado con PyYAML 6.0.3) para revisar el YAML antes de que Compose pueda leer archivos. La preparación comprueba estos requisitos y no instala paquetes automáticamente. La terminal utiliza tmux y los mismos permisos del usuario remoto; las operaciones de Docker pueden otorgar privilegios administrativos.

## Datos y acceso

`data/` es privado: contiene SQLite, inventario, históricos y credenciales cifradas. Windows protege secretos con DPAPI de tu cuenta; Linux requiere `NODO_MASTER_KEY`, una clave persistente de 32 bytes representada por 64 caracteres hexadecimales. La [guía local](docs/INSTALACION-LOCAL.md) explica cómo generarla y conservarla.

El panel usa exclusivamente passkeys (WebAuthn), con verificación del dispositivo, sesiones en cookies HttpOnly, caducidad y límites de intentos. «Recordarme» conserva la sesión durante 30 días; sin esa opción dura hasta 12 horas y la cookie no es persistente. En **Acceso y seguridad** puedes añadir hasta diez passkeys y revocarlas. No hay permisos separados por persona.

Registra una segunda passkey como respaldo. Si pierdes todas, detén Nodes y ejecútalo con `-reset-access` y la misma ruta `-data` y configuración de inicio; conserva el inventario y revoca el acceso. Las passkeys se vinculan al dominio: usa `localhost` en tu computadora y tu dominio HTTPS definitivo en producción. Al actualizar desde una instalación anterior se elimina su configuración de acceso y se invalidan sus sesiones; registra tu primera passkey mediante el nuevo enlace privado de la consola.

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


El enlace para registrar la primera passkey caduca a los 15 minutos del arranque y solo puede canjearse una vez. Al abrirlo se autoriza únicamente ese navegador, sin ampliar el plazo. Completa el registro antes de la caducidad; si el enlace caduca o lo abriste en otro navegador, reinicia Nodes para emitir uno nuevo. Con el acceso ya configurado, reiniciar no permite registrar otra passkey sin autenticarse.
