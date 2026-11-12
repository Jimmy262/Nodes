"""Nodo's VPS executor. Standard-library only; Docker Compose provides YAML parsing.

Receives a GitHub tarball over SSH stdin. Credentials and application values never
go to stdout. Release directories and diagnostics are private to the SSH user.
"""
import fcntl
import gzip
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import sys
import tarfile
import time
import traceback
import uuid


def require(condition, message):
    if not condition:
        raise ValueError(message)


def relative(value):
    return (isinstance(value, str) and value != "" and
            not any(c in value for c in "\\:\x00\r\n$") and
            not value.startswith("/") and
            ".." not in PurePosixPath(value).parts)


class ExpandedArchive:
    """Bound TAR headers (including hidden PAX data) as well as file contents."""
    def __init__(self, source, limit=264 * 1024 * 1024):
        self.source, self.remaining = source, limit

    def read(self, size=-1):
        requested = self.remaining + 1 if size < 0 else min(size, self.remaining + 1)
        data = self.source.read(requested)
        self.remaining -= len(data)
        require(self.remaining >= 0, "El archivo expandido supera los límites de tamaño")
        return data


def extract(stream, destination, subdir):
    archive = stream.read(64 * 1024 * 1024 + 1)
    require(len(archive) <= 64 * 1024 * 1024, "Archivo mayor que 64 MiB")
    require(relative(subdir), "Carpeta inválida")
    total, count, seen, root = 0, 0, set(), None
    with gzip.GzipFile(fileobj=io.BytesIO(archive)) as expanded, tarfile.open(fileobj=ExpandedArchive(expanded), mode="r|") as source:
        for member in source:
            count += 1
            require(count <= 6000, "Demasiados archivos")
            name = member.name.rstrip("/")
            require(relative(name), "Ruta insegura en el repositorio")
            # Bound every member before filtering subdirectories. Otherwise a
            # compressed bomb outside the selected build path still expands
            # while tarfile skips forward to the next member.
            total += member.size
            require(0 <= member.size <= 64 * 1024 * 1024 and total <= 256 * 1024 * 1024,
                    "El archivo completo supera los límites de tamaño")
            require(member.isfile() or member.isdir(), "No se admiten enlaces ni archivos especiales")
            parts = PurePosixPath(name).parts
            if root is None:
                root = parts[0]
            require(parts[0] == root, "Raíces diferentes en el archivo")
            suffix = PurePosixPath(*parts[1:])
            if subdir != ".":
                if not suffix.is_relative_to(subdir):
                    continue
                suffix = suffix.relative_to(subdir)
            if str(suffix) == "." or member.isdir():
                continue
            require(member.isfile(), "No se admiten enlaces ni archivos especiales")
            key = str(suffix).lower()
            require(key not in seen, "Rutas duplicadas")
            seen.add(key)
            target = destination / suffix
            require(target.resolve().is_relative_to(destination.resolve()), "Ruta fuera del proyecto")
            target.parent.mkdir(parents=True, exist_ok=True)
            with source.extractfile(member) as src, target.open("xb") as dst:
                while chunk := src.read(1024 * 1024):
                    dst.write(chunk)
            target.chmod(0o755 if member.mode & 0o111 else 0o644)
    require(seen, "La carpeta del proyecto está vacía")


def validate(config, project):
    services = config.get("services", {})
    require(0 < len(services) <= 50, "Compose debe definir de 1 a 50 servicios")
    for service in services.values():
        for key in ("container_name", "network_mode", "pid", "ipc", "uts", "userns_mode",
                    "devices", "device_cgroup_rules", "volumes_from", "external_links",
                    "cap_add", "post_start", "pre_stop", "develop", "provider", "profiles",
                    "gpus", "use_api_socket"):
            require(key not in service, "Opción de servicio no admitida: " + key)
        require(not service.get("privileged"), "No se permiten contenedores privilegiados")
        build = service.get("build")
        if build:
            require(isinstance(build, dict), "Build inválido")
            require(relative(build.get("context", ".")), "El build debe estar dentro del proyecto")
            require(relative(build.get("dockerfile", "Dockerfile")), "Dockerfile fuera del proyecto")
            for key in ("additional_contexts", "ssh", "entitlements", "network", "secrets"):
                require(not build.get(key), "Opción de build no admitida: " + key)
        for mount in service.get("volumes", []):
            require(mount.get("type") in ("volume", "bind", "tmpfs"), "Montaje no admitido")
            require(relative(mount.get("source", ".")), "Montaje fuera del proyecto")
        for network in (service.get("networks") or {}).values():
            require(not network or not (network.get("ipv4_address") or network.get("ipv6_address")),
                    "No se admiten direcciones de red fijas")
    for kind in ("volumes", "networks", "secrets", "configs"):
        for key, resource in (config.get(kind) or {}).items():
            resource = resource or {}
            require(not resource.get("external") and not resource.get("driver_opts") and
                    not resource.get("ipam"), "Recurso externo no admitido")
            require(not resource.get("name") or resource["name"] == project + "_" + key,
                    "No se admiten nombres globales de recursos")
            require(resource.get("driver", "local" if kind == "volumes" else "bridge") in ("local", "bridge"),
                    "Controlador no admitido")
            require(relative(resource.get("file", ".")), "Archivo fuera del proyecto")


def configure(config, policy):
    services = config["services"]
    selected = policy["service"]
    require(selected in services, "El servicio público ya no existe en el Compose")
    for name, service in services.items():
        # Repo changes cannot add unintended public routers. New services are private.
        labels = {k: v for k, v in service.get("labels", {}).items() if not k.startswith("traefik.")}
        labels["traefik.enable"] = "false"
        if name == selected:
            labels.update(policy["labels"].get(selected, {}))
            environment = service.setdefault("environment", {})
            for variable in policy.get("environment", []):
                environment[variable["name"]] = variable["value"].replace("$", "$$")
        service["labels"] = labels
        if policy["proxy"]:
            service.pop("ports", None)
    if policy["proxy"]:
        key = "nodo_proxy"
        require(key not in config.get("networks", {}), "Nombre de red reservado: nodo_proxy")
        config.setdefault("networks", {})[key] = {"external": True, "name": policy["network"]}
        networks = services[selected].setdefault("networks", {"default": None})
        networks[key] = None
    # `docker compose config` already serializes literal dollars as $$. Keep
    # that encoding; only the newly injected policy values need escaping.
    return config


def deploy(base, policy, commit, stream):
    require(re.fullmatch(r"[a-f0-9]{40}", commit), "Commit inválido")
    release = base / "releases" / (str(time.time_ns()) + "-" + commit[:12] + "-" + uuid.uuid4().hex[:8])
    source = release / "source"
    source.mkdir(parents=True, mode=0o700)
    extract(stream, source, policy["buildPath"])
    runtime = release / "compose.json"
    project = policy["project"]
    prefix = ["docker", "compose", "--project-name", project, "--project-directory", str(source)]
    # The policy is outside the build context. Its values may be used for both
    # Compose interpolation and the selected service's runtime environment.
    variables = release / "variables.env"
    with variables.open("w") as dst:
        for item in policy.get("environment", []):
            value = json.dumps(item["value"], ensure_ascii=False).replace("$", "$$")
            dst.write(item["name"] + "=" + value + "\n")
    if (source / ".env").is_file():
        for line in (source / ".env").read_text().splitlines():
            key = line.strip().removeprefix("export ").split("=", 1)[0].strip()
            require(not key.startswith(("COMPOSE_", "DOCKER_")), "Variable reservada en .env")
        prefix += ["--env-file", str(source / ".env")]
    prefix += ["--env-file", str(variables)]
    if policy["dockerfile"]:
        require((source / "Dockerfile").is_file(), "Dockerfile no encontrado")
        config = {"services": {"app": {"build": {"context": "."}, "restart": "unless-stopped",
                 "ports": [{"target": policy["port"], "published": str(policy["hostPort"]), "protocol": "tcp"}]}}}
        if (source / ".env").is_file():
            config["services"]["app"]["env_file"] = [".env"]
        definition = source / "nodo-compose.json"
        definition.write_text(json.dumps(config))
    else:
        definition = source / policy["compose"]
    require(definition.is_file(), "Compose no encontrado")
    print("Validando el commit " + commit + "…", flush=True)
    with (release / "deploy.log").open("wb") as log:
        rendered = release / "rendered.json"
        with rendered.open("wb") as dst:
            subprocess.run(prefix + ["-f", str(definition), "config", "--format", "json", "--no-path-resolution"],
                           stdout=dst, stderr=log, check=True, timeout=90, cwd=source)
        require(rendered.stat().st_size <= 4 * 1024 * 1024, "Compose demasiado grande")
        config = json.loads(rendered.read_text())
        validate(config, project)
        config = configure(config, policy)
        runtime.write_text(json.dumps(config))
        print("Construyendo e iniciando los servicios…", flush=True)
        subprocess.run(prefix + ["-f", str(runtime), "up", "-d", "--build", "--wait", "--wait-timeout", "120"],
                       stdout=log, stderr=log, check=True, timeout=1500, cwd=source)
    result = {"commit": commit, "release": str(release), "at": time.time(), "status": "succeeded"}
    temporary = base / "last-success.tmp"
    temporary.write_text(json.dumps(result))
    temporary.replace(base / "last-success.json")
    print("Despliegue completado: " + commit, flush=True)


def main():
    os.umask(0o077)
    base = Path(__file__).resolve().parent
    with (base / "deploy.lock").open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            print("Ya hay un despliegue en curso. Espera a que termine y vuelve a ejecutar.", file=sys.stderr)
            return 75
        try:
            policy = json.loads((base / "policy.json").read_text())
            require(len(sys.argv) == 2, "Falta el commit")
            deploy(base, policy, sys.argv[1], sys.stdin.buffer)
            return 0
        except Exception:
            # Exception strings and Docker logs can contain application secrets.
            # They stay on the VPS. Actions receives a generic actionable result.
            (base / "last-error.log").write_text(traceback.format_exc())
            print("El despliegue falló. Puede haber servicios parcialmente actualizados. Revisa last-error.log y los registros privados en "
                  + str(base / "releases") + "/<release>/deploy.log antes de reintentar.", file=sys.stderr)
            return 1


if __name__ == "__main__":
    sys.exit(main())
