"""Run with Python 3.10+: python -m unittest discover -s scripts -p test_github_deploy.py.

Docker is mocked; these tests never connect to a VPS or mutate real containers.
On Windows fcntl is stubbed for importing the Linux executor's pure functions.
"""
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import types
import unittest
from unittest.mock import patch

if sys.platform == "win32":
    sys.modules["fcntl"] = types.SimpleNamespace(LOCK_EX=2, LOCK_NB=4, flock=lambda *args: None)
spec = importlib.util.spec_from_file_location("github_deploy", Path(__file__).resolve().parents[1] / "internal/panel/github_deploy.py")
executor = importlib.util.module_from_spec(spec)
spec.loader.exec_module(executor)


def archive(files, special=None):
    data = io.BytesIO()
    with tarfile.open(fileobj=data, mode="w:gz") as output:
        for name, value in files.items():
            header = tarfile.TarInfo(name)
            content = value.encode()
            header.size = len(content)
            output.addfile(header, io.BytesIO(content))
        if special:
            header = tarfile.TarInfo(special)
            header.type = tarfile.SYMTYPE
            header.linkname = "/etc/passwd"
            output.addfile(header)
    data.seek(0)
    return data


class DeploymentTests(unittest.TestCase):
    def test_yaml_is_bounded_and_rejects_duplicate_keys_and_cycles(self):
        invalid = [
            'services: {}\nservices: {}\n',
            '{"services": {}, "services": {}}',
            'x-base: &base {image: first, image: second}\nservices: {web: {<<: *base}}',
            'services: &loop {app: *loop}',
            'x: ' + '[' * 70 + '0' + ']' * 70,
            'x: [' + ','.join('0' for _ in range(20001)) + ']',
            'x: ' + 'a' * (2 * 1024 * 1024),
            'services: !!python/object:fixture {}',
        ]
        with tempfile.TemporaryDirectory() as directory:
            definition = Path(directory) / 'compose.yaml'
            for text in invalid:
                definition.write_text(text)
                with self.subTest(text=text[:60]), self.assertRaises(Exception):
                    executor.load_definition(definition)
            definition.write_text('x-default: &defaults {image: fixture:local}\nservices:\n  web:\n    <<: *defaults\n')
            self.assertEqual(executor.load_definition(definition)['services']['web']['image'], 'fixture:local')
            definition.write_text('x-default: &defaults {image: fixture:base}\nservices:\n  web:\n    <<: *defaults\n    image: fixture:override\n')
            self.assertEqual(executor.load_definition(definition)['services']['web']['image'], 'fixture:override')
            definition.write_text('services: {web: {image: fixture:local, environment: {ON: yes, OFF: no}}}')
            self.assertEqual(executor.load_definition(definition)['services']['web']['environment'], {'ON': 'yes', 'OFF': 'no'})

    def test_unsafe_file_references_are_rejected_before_compose_runs(self):
        policy = {"project": "demo", "buildPath": ".", "compose": "compose.yaml", "dockerfile": False,
                  "service": "web", "proxy": False, "labels": {}, "environment": []}
        definitions = [
            'services: {web: {image: fixture:local, env_file: /outside/host.env}}',
            'services: {web: {image: fixture:local, env_file: [{path: ../outside.env}]}}',
            'services: {web: {image: fixture:local, env_file: "${OUTSIDE}"}}',
            'services: {web: {image: fixture:local, env_file: "~/.fixture-home"}}',
            'include: /outside/compose.yaml\nservices: {web: {image: fixture:local}}',
            '"\\u0069nclude": /outside/compose.yaml\nservices: {web: {image: fixture:local}}',
            'services: {web: {extends: {file: /outside/compose.yaml, service: web}}}',
            'services: {web: {image: fixture:local, label_file: ./routes.labels}}',
            'services: {web: {image: fixture:local, volumes: ["~/.fixture-home:/fixture"]}}',
            'services: {web: {image: fixture:local, build: {context: /outside}}}',
            'services: {web: {image: fixture:local}}\nsecrets: {file: {file: /outside/host.env}}',
        ]
        for definition in definitions:
            with self.subTest(definition=definition), tempfile.TemporaryDirectory() as directory:
                with patch.object(executor.subprocess, 'run') as docker, contextlib.redirect_stdout(io.StringIO()):
                    with self.assertRaises(ValueError):
                        executor.deploy(Path(directory), policy, 'a' * 40, archive({'root/compose.yaml': definition}))
                    docker.assert_not_called()

    def test_prevalidation_accepts_local_files_and_short_forms(self):
        config = {'services': {'web': {'build': '.', 'env_file': ['.env', {'path': './config/app.env', 'required': False}],
                  'volumes': ['data:/data', './local:/local:ro', '/anonymous'], 'networks': ['default'],
                  'environment': {'ORIGINAL': '$$literal', 'IMAGE_VALUE': '${VALUE}'}}},
                  'volumes': {'data': {}}, 'configs': {'local': {'file': './config/local.conf'}}}
        executor.validate(config, 'demo', raw=True)

    def test_expanded_stream_limits_headers_and_contents(self):
        limited = executor.ExpandedArchive(io.BytesIO(b'abcdefghij'), limit=8)
        self.assertEqual(limited.read(4), b'abcd')
        with self.assertRaisesRegex(ValueError, 'expandido'):
            limited.read(1000000)

    def test_limits_include_members_outside_selected_directory(self):
        oversized = tarfile.TarInfo('root/excluded/bomb')
        oversized.size = 64 * 1024 * 1024 + 1
        many = []
        for number in range(5):
            member = tarfile.TarInfo(f'root/excluded/{number}')
            member.size = 64 * 1024 * 1024
            many.append(member)
        link = tarfile.TarInfo('root/excluded/link')
        link.type = tarfile.SYMTYPE
        for members in ([oversized], many, [link]):
            with self.subTest(members=len(members)), tempfile.TemporaryDirectory() as directory:
                with patch.object(executor.tarfile, 'open') as opened:
                    opened.return_value.__enter__.return_value = members
                    with self.assertRaisesRegex(ValueError, 'límites|especiales'):
                        executor.extract(io.BytesIO(b'fake'), Path(directory), 'apps/web')
                    self.assertEqual(list(Path(directory).iterdir()), [])

    def test_archive_paths_and_subdirectories(self):
        for unsafe in ("root/../../escape", "/tmp/escape", "root/a\\escape"):
            with self.subTest(unsafe=unsafe), tempfile.TemporaryDirectory() as directory:
                with self.assertRaises(ValueError):
                    executor.extract(archive({unsafe: "bad"}), Path(directory), ".")
        with tempfile.TemporaryDirectory() as directory:
            executor.extract(archive({"root/apps/web/Dockerfile": "FROM scratch", "root/private": "excluded"}), Path(directory), "apps/web")
            self.assertTrue((Path(directory) / "Dockerfile").is_file())
            self.assertFalse((Path(directory) / "private").exists())

    def test_links_and_duplicates_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(ValueError):
                executor.extract(archive({"root/app": "ok"}, "root/escape"), Path(directory), ".")
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(ValueError):
                executor.extract(archive({"root/A": "one", "root/a": "two"}), Path(directory), ".")

    def test_resource_and_privilege_validation(self):
        invalid = [
            {"services": {"app": {"privileged": True}}},
            {"services": {"app": {"build": {"context": "../outside"}}}},
            {"services": {"app": {"volumes": [{"type": "bind", "source": "/etc"}]}}},
            {"services": {"app": {}}, "volumes": {"data": {"external": True}}},
            {"services": {"app": {}}, "volumes": {"data": {"name": "another-project"}}},
        ]
        for config in invalid:
            with self.subTest(config=config), self.assertRaises(ValueError):
                executor.validate(config, "demo")
        executor.validate({"services": {"app": {"build": {"context": "."}}}, "volumes": {"data": {"name": "demo_data"}}}, "demo")

    def test_routing_is_private_and_secrets_are_literals(self):
        config = {"services": {"web": {"ports": [80], "labels": {"traefik.evil": "bad"}},
                               "new": {"ports": [5432], "labels": {"traefik.enable": "true"}}}}
        policy = {"service": "web", "proxy": True, "network": "shared-proxy", "labels": {"web": {"traefik.enable": "true"}},
                  "environment": [{"name": "PASSWORD", "value": "$literal"}]}
        rendered = executor.configure(config, policy)
        self.assertNotIn("ports", rendered["services"]["web"])
        self.assertNotIn("ports", rendered["services"]["new"])
        self.assertEqual(rendered["services"]["new"]["labels"]["traefik.enable"], "false")
        self.assertNotIn("traefik.evil", rendered["services"]["web"]["labels"])
        self.assertEqual(rendered["services"]["web"]["environment"]["PASSWORD"], "$$literal")
        self.assertEqual(rendered["networks"]["nodo_proxy"]["name"], "shared-proxy")

    def test_deploy_reads_each_commit_and_preserves_project_and_last_success(self):
        policy = {"project": "demo", "buildPath": ".", "compose": "compose.yaml", "dockerfile": False,
                  "service": "web", "proxy": False, "labels": {}, "environment": [{"name": "SECRET", "value": "private-value"}]}
        calls = []

        def docker(command, **kwargs):
            calls.append(command)
            if "config" in command:
                definition = Path(command[command.index("-f") + 1])
                kwargs["stdout"].write(definition.read_bytes())
            return subprocess.CompletedProcess(command, 0)

        with tempfile.TemporaryDirectory() as directory, patch.object(executor.subprocess, "run", side_effect=docker), contextlib.redirect_stdout(io.StringIO()) as console:
            base = Path(directory)
            for i, image in enumerate(("example:v1", "example:v2")):
                commit = str(i + 1) * 40
                source = json.dumps({"services": {"web": {"image": image}}, "volumes": {"data": {"name": "demo_data"}}})
                executor.deploy(base, policy, commit, archive({"root/compose.yaml": source}))
                success = json.loads((base / "last-success.json").read_text())
                runtime = json.loads((Path(success["release"]) / "compose.json").read_text())
                self.assertEqual(runtime["services"]["web"]["image"], image)
                self.assertEqual(success["commit"], commit)
                self.assertFalse((Path(success["release"]) / "source" / "variables.env").exists())
            previous = (base / "last-success.json").read_text()
            with patch.object(executor.subprocess, "run", side_effect=subprocess.CalledProcessError(1, "docker")):
                with self.assertRaises(subprocess.CalledProcessError):
                    executor.deploy(base, policy, "a" * 40, archive({"root/compose.yaml": source}))
            self.assertEqual((base / "last-success.json").read_text(), previous)
            self.assertNotIn("private-value", console.getvalue())
        for command in calls:
            self.assertEqual(command[command.index("--project-name") + 1], "demo")
            self.assertNotIn("down", command)
            self.assertNotIn("--volumes", command)
            self.assertNotIn("--no-recreate", command)

    def test_invalid_commit_never_invokes_docker(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(executor.subprocess, "run") as docker:
            with self.assertRaises(ValueError):
                executor.deploy(Path(directory), {}, "$(bad)", io.BytesIO())
            docker.assert_not_called()

    @unittest.skipUnless(os.environ.get("NODO_TEST_COMPOSE") == "1", "Set NODO_TEST_COMPOSE=1 to check with the real Docker Compose CLI")
    def test_real_compose_merge_overrides_match_prevalidation(self):
        with tempfile.TemporaryDirectory() as directory:
            folder = Path(directory)
            definition = folder / 'compose.yaml'
            definition.write_text('x-default: &defaults {image: fixture:base, restart: unless-stopped}\n'
                                  'services:\n  web:\n    <<: *defaults\n    image: fixture:override\n')
            raw = executor.load_definition(definition)
            executor.validate(raw, 'nodo-test', raw=True)
            rendered = json.loads(subprocess.check_output(
                ['docker', 'compose', '--project-name', 'nodo-test', '-f', str(definition),
                 'config', '--format', 'json', '--no-path-resolution'], cwd=folder))
            for key in ('image', 'restart'):
                self.assertEqual(raw['services']['web'][key], rendered['services']['web'][key])

    @unittest.skipUnless(os.environ.get("NODO_TEST_COMPOSE") == "1", "Set NODO_TEST_COMPOSE=1 to check with the real Docker Compose CLI")
    def test_real_compose_roundtrip_preserves_literals_and_volumes(self):
        # `config` does not need a running Docker engine or start containers.
        with tempfile.TemporaryDirectory() as directory:
            folder = Path(directory)
            definition = folder / "compose.json"
            (folder / 'local.env').write_text("FILE_VALUE='$literal'\n")
            definition.write_text(json.dumps({"services": {
                "web": {"build": ".", "ports": ["8080:80"], "env_file": ['./local.env'], "environment": {"ORIGINAL": "$$literal"}, "volumes": ["data:/data"]},
                "db": {"image": "postgres:17", "ports": ["5432:5432"]}}, "volumes": {"data": {}}}))
            executor.validate(executor.load_definition(definition), 'nodo-test', raw=True)
            prefix = ["docker", "compose", "--project-name", "nodo-test", "--project-directory", str(folder)]
            rendered = subprocess.check_output(prefix + ["-f", str(definition), "config", "--format", "json", "--no-path-resolution"], cwd=folder)
            config = json.loads(rendered)
            executor.validate(config, "nodo-test")
            policy = {"service": "web", "proxy": True, "network": "test-proxy",
                      "environment": [{"name": "PASSWORD", "value": "$private-value"}],
                      "labels": {"web": {"traefik.enable": "true"}}}
            runtime = folder / "runtime.json"
            runtime.write_text(json.dumps(executor.configure(config, policy)))
            checked = json.loads(subprocess.check_output(prefix + ["-f", str(runtime), "config", "--format", "json", "--no-path-resolution"], cwd=folder))
            # Compose JSON escapes literal dollars for safe reuse. Rendering a
            # second time must not add extra dollars or expand secret values.
            self.assertEqual(checked["services"]["web"]["environment"]["PASSWORD"], "$$private-value")
            self.assertEqual(checked["services"]["web"]["environment"]["ORIGINAL"], "$$literal")
            self.assertEqual(checked["services"]["web"]["environment"]["FILE_VALUE"], "$$literal")
            self.assertNotIn("ports", checked["services"]["web"])
            self.assertNotIn("ports", checked["services"]["db"])
            self.assertEqual(checked["volumes"]["data"]["name"], "nodo-test_data")
            self.assertEqual(checked["networks"]["nodo_proxy"]["name"], "test-proxy")
            runtime.write_text(json.dumps(checked))
            checked_again = json.loads(subprocess.check_output(prefix + ["-f", str(runtime), "config", "--format", "json", "--no-path-resolution"], cwd=folder))
            self.assertEqual(checked_again["services"]["web"]["environment"], checked["services"]["web"]["environment"])


if __name__ == "__main__":
    unittest.main()
