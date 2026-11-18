# Ejecutar Nodo en tu equipo

Nodo se ejecuta en tu computadora y administra VPS Linux por SSH. No necesitas una VPS ni Docker para abrir el panel vacío. Docker y Compose se necesitan en los servidores si vas a administrar o desplegar contenedores.

## 1. Requisitos

- Git, para clonar el repositorio.
- Node.js 22.12 o posterior compatible con Vite 7 (por ejemplo Node 24 LTS), con npm.
- Go 1.26 o posterior, según `go.mod`.
- Cliente OpenSSH en PATH, para conectar servidores.
- Acceso a Internet para descargar dependencias durante la compilación.

Descargas: [Git](https://git-scm.com/downloads), [Node.js](https://nodejs.org/en/download) y [Go](https://go.dev/dl/). En Windows, OpenSSH Client es una característica opcional del sistema. Vuelve a abrir la terminal después de instalar.

Comprueba las herramientas:

```text
git --version
node --version
npm --version
go version
ssh -V
```

## 2. Clonar

```sh
git clone https://github.com/Jimmy262/Nodes.git
cd Nodes
```

Ejecuta los comandos siguientes desde esa carpeta. La instalación nueva empieza vacía: el repositorio no contiene servidores, sesiones, passkeys ni claves privadas.

## 3. Windows: compilar e iniciar

En PowerShell:

```powershell
.\start.ps1
```

El script instala dependencias con `npm ci`, comprueba Svelte, analiza Go con `go vet` y compila `bin/nodo.exe` con la interfaz incorporada. Después inicia Nodo en primer plano. La primera compilación puede tardar varios minutos.

Si PowerShell bloquea los scripts y las políticas de tu equipo lo permiten, habilítalos solo en esa terminal:

```powershell
Set-ExecutionPolicy -Scope Process -ExecutionPolicy RemoteSigned
.\start.ps1
```

También puedes compilar con `.\scripts\build.ps1` e iniciar por separado:

```powershell
.\bin\nodo.exe -port 8080 -data .\data\panel.db
```

No necesitas definir `NODO_MASTER_KEY` en Windows: la aplicación usa DPAPI de la cuenta de Windows. Después de compilar, Node.js y Go no son necesarios para ejecutar el binario; OpenSSH sí se necesita para las conexiones remotas.

## 4. Primer acceso

1. Abre el enlace privado `http://localhost:8080/#setup=...` que imprime la consola. El valor se genera en cada arranque sin acceso configurado; no lo compartas ni publiques capturas de él.
2. Pulsa **Crear passkey y entrar** y confirma con tu huella, rostro, Windows Hello o una llave de seguridad.
3. En los siguientes accesos abre `http://localhost:8080` y pulsa **Entrar con passkey**. «Recordarme» conserva la sesión durante 30 días.
4. Mantén la terminal abierta mientras lo usas. Detén el programa con **Ctrl+C**.

Para volver a iniciar basta ejecutar el binario desde la carpeta del proyecto. Para otro puerto usa `-port 8081` y abre la dirección correspondiente. Usa siempre el mismo archivo `-data`; cambiar de carpeta sin una ruta absoluta puede abrir una base nueva.

## 5. Conectar tu primera VPS

1. Desde esta misma computadora, comprueba SSH: `ssh -p 22 deploy@tu-servidor`. Verifica la huella por un canal confiable antes de aceptarla.
2. Usa una clave SSH sin interacción o carga tu clave con contraseña en SSH Agent. Nodo no acepta huellas desconocidas automáticamente ni pide contraseñas SSH interactivas.
3. En **Servidores**, añade nombre, host, usuario y puerto. Si hace falta, indica la ruta absoluta de tu clave; se guarda la ruta, no el contenido.
4. Para ver contenedores, el usuario remoto necesita ejecutar Docker sin sudo interactivo. Para desplegar necesita también Compose v2 y tar. Terminal utiliza tmux; el panel ofrece revisar su instalación si falta.

La conexión SSH usa la configuración y `known_hosts` de tu cuenta. No copies claves privadas al repositorio. El acceso a Docker y a la terminal concede los privilegios del usuario SSH.

## 6. Linux: instalación local

Instala primero las mismas herramientas. Compila desde la raíz:

```sh
npm ci
npm run check
npm run build
go mod download
go vet ./...
mkdir -p bin
go build -trimpath -o bin/nodo ./cmd/panel
```

En Linux necesitas una clave maestra persistente, privada y fuera del repositorio. Créala **una sola vez** con OpenSSL:

```sh
(
  umask 077
  mkdir -p "$HOME/.config/nodo"
  chmod 700 "$HOME/.config/nodo"
  if [ ! -e "$HOME/.config/nodo/master-key" ]; then
    openssl rand -hex 32 > "$HOME/.config/nodo/master-key"
  fi
  chmod 600 "$HOME/.config/nodo/master-key"
)
```

En cada inicio, desde el proyecto:

```sh
export NODO_MASTER_KEY="$(cat "$HOME/.config/nodo/master-key")"
./bin/nodo -port 8080 -data ./data/panel.db
```

Abre el enlace privado de la consola y registra tu passkey. Guarda una copia privada de la clave: cambiarla o perderla impide descifrar las credenciales existentes. No transfieras secretos cifrados por DPAPI de Windows esperando abrirlos con esta clave. Para servicio systemd o acceso por HTTPS consulta [la guía de alojamiento](../deploy/README.md).

## 7. Datos, respaldos y actualizaciones

- `data/` contiene SQLite, inventario, históricos y credenciales cifradas. Es privado y está excluido de Git junto con claves, `.env`, binarios y evidencias locales.
- Para respaldar, detén Nodo y copia `data/` a un lugar privado. En Windows necesitas la misma identidad DPAPI para abrir los secretos; una copia SQLite por sí sola no los hace portátiles. En Linux respalda la clave maestra por separado.
- Para actualizar: detén Nodo, respalda los datos, ejecuta `git pull --ff-only` y vuelve a compilar con los comandos de tu plataforma. Conserva el mismo archivo de datos y la clave maestra de Linux.
- Pérdida de todas las passkeys: con Nodo detenido, ejecuta el binario con `-reset-access -data data/panel.db`. En Linux, carga antes la clave maestra. El comando conserva el inventario, revoca passkeys y sesiones y genera un enlace para registrar una nueva passkey.

## 8. Desarrollo

Para desarrollar con recarga automática, compila primero la interfaz e inicia el backend como se indicó. En otra terminal:

```sh
npm run dev
```

Abre `http://127.0.0.1:5173` e inicia sesión también en ese puerto. Vite reenvía `/api` al backend en 8080. La vista `http://127.0.0.1:5173/?preview=design` muestra únicamente el diseño y bloquea llamadas API.

## Problemas frecuentes

| Síntoma | Qué comprobar |
| --- | --- |
| No se encuentra Go, npm o ssh | Instalación y PATH; vuelve a abrir la terminal. |
| Puerto ocupado | Detén la otra instancia o inicia con `-port 8081`. |
| Panel vacío después de reiniciar | Carpeta de trabajo y ruta `-data`; usa una ruta absoluta. |
| Error de clave maestra en Linux | Carga los 64 caracteres hexadecimales de la clave original. |
| Error DPAPI en Windows | Usa la cuenta y equipo originales; consulta recuperación antes de cambiar credenciales. |
| Falla SSH | Prueba manualmente con la misma cuenta, host, puerto y clave; revisa agente y huella. |
| No aparece la interfaz al compilar Go | Ejecuta `npm run build` antes de `go build`. |

El acceso local escucha en `127.0.0.1`; no necesita abrir puertos en el router. Para entrar desde otro equipo o celular usa HTTPS y la configuración de [alojamiento](../deploy/README.md).


El enlace para registrar la primera passkey caduca a los 15 minutos del arranque y solo puede canjearse una vez. Al abrirlo se autoriza únicamente ese navegador, sin ampliar el plazo. Completa el registro antes de la caducidad; si el enlace caduca o lo abriste en otro navegador, reinicia Nodes para emitir uno nuevo. Con el acceso ya configurado, reiniciar no permite registrar otra passkey sin autenticarse.


## Alta automática de nuevos servidores

En **Conectar servidor**, indica IP o dominio, usuario y puerto. Verifica la huella con una fuente de confianza y usa la contraseña SSH actual una sola vez para instalar una clave Ed25519 dedicada. Nodes conserva las claves autorizadas existentes y verifica el acceso nuevo antes de guardar el servidor. La contraseña no se almacena y el formulario no revela claves. Si la cuenta solo permite claves, utiliza la opción de acceso SSH ya configurado.

Las nuevas claves se guardan cifradas en la base; dependen de DPAPI en Windows o de la clave maestra en Linux. Los archivos que necesita OpenSSH son temporales y privados; el contenedor de producción usa tmpfs. La carpeta SSH existente puede seguir montada en solo lectura. Respaldar únicamente SQLite sin la protección original no basta para recuperar esas claves en otro equipo. Eliminar un servidor de Nodes no revoca automáticamente su autorización en el servidor remoto.
