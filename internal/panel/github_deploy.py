"""Nodo's VPS executor. PyYAML validates input before Docker Compose reads files.

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
import yaml


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


class BoundedLoader(yaml.SafeLoader):
    # Compose uses YAML 1.2 booleans. PyYAML's YAML 1.1 defaults otherwise
    # turn ordinary environment keys such as ON/OFF into non-string keys.
    yaml_implicit_resolvers = {
        first: [(tag, pattern) for tag, pattern in resolvers
                if tag not in ('tag:yaml.org,2002:bool', 'tag:yaml.org,2002:timestamp')]
        for first, resolvers in yaml.SafeLoader.yaml_implicit_resolvers.items()
    }

    def __init__(self, stream):
        super().__init__(stream)
        self.node_count = 0
        self.node_depth = 0
        self.checked_mappings = set()

    def compose_node(self, parent, index):
        self.node_count += 1
        self.node_depth += 1
        require(self.node_count <= 20000 and self.node_depth <= 64,
                "Compose supera los límites de complejidad")
        try:
            return super().compose_node(parent, index)
        finally:
            self.node_depth -= 1

    def check_mapping_keys(self, node):
        if id(node) in self.checked_mappings:
            return
        keys = set()
        for key_node, _ in node.value:
            if key_node.tag == 'tag:yaml.org,2002:merge':
                continue
            require(isinstance(key_node, yaml.ScalarNode) and
                    key_node.tag == 'tag:yaml.org,2002:str' and key_node.value not in keys,
                    "Compose contiene claves inválidas o duplicadas")
            keys.add(key_node.value)
        self.checked_mappings.add(id(node))

    def construct_mapping(self, node, deep=False):
        self.check_mapping_keys(node)
        self.flatten_mapping(node)
        result = {}
        for key_node, value_node in node.value:
            key = self.construct_object(key_node, deep=deep)
            require(isinstance(key, str), "Compose contiene claves inválidas")
            result[key] = self.construct_object(value_node, deep=deep)
        return result


BoundedLoader.add_implicit_resolver('tag:yaml.org,2002:bool',
                                  re.compile(r'^(?:true|True|TRUE|false|False|FALSE)$'), list('tTfF'))


def load_definition(definition):
    with definition.open('rb') as source:
        data = source.read(2 * 1024 * 1024 + 1)
    require(len(data) <= 2 * 1024 * 1024, "Compose demasiado grande")
    loader = BoundedLoader(data)
    try:
        node = loader.get_single_node()
        active = set()
        references = 0
        def check(current, depth=0):
            nonlocal references
            references += 1
            require(depth <= 64 and references <= 50000 and id(current) not in active,
                    "Compose contiene ciclos o supera los límites de complejidad")
            active.add(id(current))
            try:
                if isinstance(current, yaml.MappingNode):
                    # Check original keys before flatten_mapping mutates any
                    # shared anchor. Explicit keys may override merged keys.
                    loader.check_mapping_keys(current)
                    for key, value in current.value:
                        check(key, depth + 1)
                        check(value, depth + 1)
                elif isinstance(current, yaml.SequenceNode):
                    for value in current.value:
                        check(value, depth + 1)
            finally:
                active.remove(id(current))
        require(node is not None, "Compose vacío")
        check(node)
        config = loader.construct_document(node)
        require(isinstance(config, dict), "Compose inválido")
        return config
    finally:
        loader.dispose()


def project_relative(value):
    return relative(value) and not value.startswith("~")


def validate(config, project, raw=False):
    require(isinstance(config, dict), "Compose inválido")
    require(not any(key in config for key in ("include", "extends")),
            "Incluye todos los servicios en un único Compose")
    services = config.get("services", {})
    require(isinstance(services, dict) and 0 < len(services) <= 50, "Compose debe definir de 1 a 50 servicios")
    for service in services.values():
        require(isinstance(service, dict), "Servicio inválido")
        for key in ("extends", "label_file", "container_name", "network_mode", "pid", "ipc", "uts", "userns_mode",
                    "devices", "device_cgroup_rules", "volumes_from", "external_links",
                    "cap_add", "post_start", "pre_stop", "develop", "provider", "profiles",
                    "gpus", "use_api_socket"):
            require(key not in service, "Opción de servicio no admitida: " + key)
        require(not service.get("privileged"), "No se permiten contenedores privilegiados")
        build = service.get("build")
        if build:
            if raw and isinstance(build, str):
                build = {"context": build}
            require(isinstance(build, dict), "Build inválido")
            require(project_relative(build.get("context", ".")), "El build debe estar dentro del proyecto")
            require(project_relative(build.get("dockerfile", "Dockerfile")), "Dockerfile fuera del proyecto")
            for key in ("additional_contexts", "ssh", "entitlements", "network", "secrets"):
                require(not build.get(key), "Opción de build no admitida: " + key)
        files = service.get("env_file", [])
        if not isinstance(files, list):
            files = [files]
        for entry in files:
            filename = entry.get("path") if isinstance(entry, dict) else entry
            require(project_relative(filename), "env_file fuera del proyecto")
        for mount in service.get("volumes", []):
            if raw and isinstance(mount, str):
                parts = mount.split(":")
                require(len(parts) <= 3 and (len(parts) == 1 or project_relative(parts[0])),
                        "Montaje fuera del proyecto")
                continue
            require(isinstance(mount, dict), "Montaje inválido")
            require(mount.get("type") in ("volume", "bind", "tmpfs"), "Montaje no admitido")
            require(project_relative(mount.get("source", ".")), "Montaje fuera del proyecto")
        networks = service.get("networks") or {}
        if raw and isinstance(networks, list):
            networks = {key: None for key in networks}
        for network in networks.values():
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
            require(project_relative(resource.get("file", ".")), "Archivo fuera del proyecto")


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
    # Parse without invoking Compose: include/extends and env_file can read
    # host files during `config`, before the normalized model is available.
    validate(load_definition(definition), project, raw=True)
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
