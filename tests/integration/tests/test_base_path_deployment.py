"""
Integration tests for base path deployment.

Ported from the reference's hindsight-integration-tests base-path suite. The
test starts this repository's own server binary, so it exercises the real
process an operator runs, not an in-process handler.

Tests:
1. API with base path (direct, no proxy)
2. Full stack via docker-compose with Nginx reverse proxy
3. Regression: API without base path still works
4. Full retain/recall workflow

Requirements:
- Go toolchain (the server is built once per session)
- Docker and docker compose (reverse proxy test only)
- No nginx required on the host

The server runs with the process-local "memory" database and the "none" model
provider: retain stores each item verbatim and recall serves it from the lexical
arm, so the suite needs no PostgreSQL, model download or API key.
"""

from __future__ import annotations

import os
import platform
import shutil
import socket
import subprocess
import tempfile
import time
from pathlib import Path

import httpx
import pytest

REPO_ROOT = Path(__file__).resolve().parents[3]
BASE_PATH = "/hindsight"


def free_port() -> int:
    """Return a port the kernel says is currently free."""
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def run_command(cmd: list[str], cwd: str | Path | None = None) -> subprocess.CompletedProcess:
    return subprocess.run(cmd, cwd=cwd, capture_output=True, text=True)


def check_docker_available() -> bool:
    return run_command(["docker", "info"]).returncode == 0


def get_docker_compose_command() -> list[str]:
    if shutil.which("docker"):
        result = run_command(["docker", "compose", "version"])
        if result.returncode == 0:
            return ["docker", "compose"]
    if shutil.which("docker-compose"):
        return ["docker-compose"]
    raise RuntimeError("docker-compose not available")


def check_docker_compose_available() -> bool:
    try:
        get_docker_compose_command()
        return True
    except RuntimeError:
        return False


@pytest.fixture(scope="session")
def wenyuan_binary(tmp_path_factory: pytest.TempPathFactory) -> Path:
    """Build the server once for the whole session."""
    if shutil.which("go") is None:
        pytest.skip("go toolchain not available")

    output = tmp_path_factory.mktemp("wenyuan-bin") / "wenyuan"
    env = {**os.environ, "CGO_ENABLED": "0"}
    result = subprocess.run(
        ["go", "build", "-tags", "pgx", "-o", str(output), "./cmd/server"],
        cwd=REPO_ROOT,
        env=env,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        pytest.fail(f"go build failed:\nSTDOUT: {result.stdout}\nSTDERR: {result.stderr}")
    return output


class APIServer:
    """Manage one server process for the duration of a test."""

    def __init__(self, binary: Path, base_path: str | None = None, port: int | None = None):
        self.binary = binary
        self.base_path = base_path
        self.port = port or free_port()
        self.process: subprocess.Popen | None = None
        self.run_dir: Path | None = None
        self.log_file = None

    def start(self) -> None:
        # A scratch run directory with an empty .env, so a developer's own .env
        # cannot leak into the configuration the test passes.
        self.run_dir = Path(tempfile.mkdtemp(prefix=f"wenyuan-base-path-{self.port}-"))
        (self.run_dir / ".env").write_text("# intentionally empty\n")
        self.log_file = open(self.run_dir / "server.log", "w")

        env = {key: value for key, value in os.environ.items() if not key.startswith("WENYUAN_API_")}
        env.update(
            {
                "WENYUAN_API_DATABASE_URL": "memory",
                "WENYUAN_API_LLM_PROVIDER": "none",
                "WENYUAN_API_HOST": "127.0.0.1",
                "WENYUAN_API_PORT": str(self.port),
                "WENYUAN_API_LOG_LEVEL": "info",
            }
        )
        if self.base_path:
            env["WENYUAN_API_BASE_PATH"] = self.base_path

        self.process = subprocess.Popen(
            [str(self.binary)],
            cwd=self.run_dir,
            env=env,
            stdout=self.log_file,
            stderr=subprocess.STDOUT,
        )

        health_url = f"http://127.0.0.1:{self.port}{self.base_path or ''}/health"
        for _ in range(60):
            if self.process.poll() is not None:
                break
            try:
                if httpx.get(health_url, timeout=2.0).status_code == 200:
                    return
            except (httpx.ConnectError, httpx.ReadTimeout):
                pass
            time.sleep(1)

        self.log_file.flush()
        with open(self.run_dir / "server.log") as log:
            print(f"API server failed to start. Logs:\n{log.read()}")
        raise RuntimeError(f"API server failed to start on port {self.port}")

    def stop(self) -> None:
        if self.process:
            self.process.terminate()
            try:
                self.process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait()
            self.process = None

        if self.log_file:
            self.log_file.close()
            self.log_file = None

        if self.run_dir:
            shutil.rmtree(self.run_dir, ignore_errors=True)
            self.run_dir = None

    def __enter__(self) -> "APIServer":
        self.start()
        return self

    def __exit__(self, exc_type, exc_val, exc_tb) -> None:
        self.stop()


class DockerComposeStack:
    """Manage one docker-compose stack for the duration of a test."""

    def __init__(self, compose_file: Path, project_name: str):
        self.compose_file = compose_file
        self.project_name = project_name
        self.compose_cmd = get_docker_compose_command()

    def start(self) -> None:
        result = run_command(
            self.compose_cmd + ["-f", str(self.compose_file), "-p", self.project_name, "up", "-d"],
            cwd=self.compose_file.parent,
        )
        if result.returncode != 0:
            raise RuntimeError(
                f"Failed to start docker-compose:\nSTDOUT: {result.stdout}\nSTDERR: {result.stderr}"
            )

    def stop(self) -> None:
        result = run_command(
            self.compose_cmd + ["-f", str(self.compose_file), "-p", self.project_name, "down", "-v"],
            cwd=self.compose_file.parent,
        )
        if result.returncode != 0:
            print(f"Warning: Failed to stop docker-compose:\n{result.stderr}")

    def __enter__(self) -> "DockerComposeStack":
        self.start()
        return self

    def __exit__(self, exc_type, exc_val, exc_tb) -> None:
        self.stop()


def test_api_without_base_path(wenyuan_binary: Path) -> None:
    """Regression test: API works at the root path."""
    with APIServer(wenyuan_binary) as server:
        base_url = f"http://127.0.0.1:{server.port}"

        response = httpx.get(f"{base_url}/health")
        assert response.status_code == 200
        assert "status" in response.json()

        response = httpx.get(f"{base_url}/v1/default/banks")
        assert response.status_code == 200
        assert "banks" in response.json()

        response = httpx.get(f"{base_url}/docs")
        assert response.status_code == 200


def test_api_with_base_path_direct(wenyuan_binary: Path) -> None:
    """Test the API with a base path (direct, no proxy)."""
    with APIServer(wenyuan_binary, base_path=BASE_PATH) as server:
        base_url = f"http://127.0.0.1:{server.port}"

        response = httpx.get(f"{base_url}{BASE_PATH}/health")
        assert response.status_code == 200
        assert "status" in response.json()

        response = httpx.get(f"{base_url}{BASE_PATH}/v1/default/banks")
        assert response.status_code == 200
        assert "banks" in response.json()

        response = httpx.get(f"{base_url}{BASE_PATH}/docs")
        assert response.status_code == 200

        response = httpx.get(f"{base_url}{BASE_PATH}/openapi.json")
        assert response.status_code == 200
        openapi = response.json()
        assert "servers" in openapi
        assert openapi["servers"][0]["url"] == BASE_PATH


@pytest.mark.skipif(
    not check_docker_available() or not check_docker_compose_available(),
    reason="docker and docker-compose are required",
)
def test_reverse_proxy_simple_config(wenyuan_binary: Path, tmp_path: Path) -> None:
    """Test a reverse proxy deployment with Nginx in front of the API."""
    api_port = free_port()
    nginx_port = free_port()
    while nginx_port == api_port:
        nginx_port = free_port()

    with APIServer(wenyuan_binary, base_path=BASE_PATH, port=api_port):
        if platform.system() == "Linux":
            network_mode = "host"
            api_host = "localhost"
            listen_port = nginx_port
            port_mapping = ""
        else:
            network_mode = "bridge"
            api_host = "host.docker.internal"
            listen_port = 80
            port_mapping = f"""    ports:
      - "{nginx_port}:80"
"""

        compose_file = tmp_path / "test-reverse-proxy.yml"
        compose_file.write_text(
            f"""services:
  nginx:
    image: nginx:alpine
{port_mapping}    volumes:
      - ./test-nginx.conf:/etc/nginx/nginx.conf:ro
    network_mode: {network_mode}
"""
        )
        (tmp_path / "test-nginx.conf").write_text(
            f"""events {{
    worker_connections 1024;
}}

http {{
    server {{
        listen {listen_port};
        server_name localhost;

        location {BASE_PATH}/ {{
            proxy_pass http://{api_host}:{api_port};
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
        }}
    }}
}}
"""
        )

        with DockerComposeStack(compose_file, project_name="wenyuan-base-path-test"):
            proxy_url = f"http://localhost:{nginx_port}"
            deadline = time.monotonic() + 30
            while time.monotonic() < deadline:
                try:
                    if httpx.get(f"{proxy_url}{BASE_PATH}/health", timeout=2.0).status_code == 200:
                        break
                except (httpx.ConnectError, httpx.ReadTimeout):
                    pass
                time.sleep(1)
            else:
                raise RuntimeError("nginx did not become ready within 30s")

            response = httpx.get(f"{proxy_url}{BASE_PATH}/health", timeout=10.0)
            assert response.status_code == 200
            assert "status" in response.json()

            response = httpx.get(f"{proxy_url}{BASE_PATH}/v1/default/banks", timeout=10.0)
            assert response.status_code == 200
            assert "banks" in response.json()

            response = httpx.get(f"{proxy_url}{BASE_PATH}/docs", timeout=10.0)
            assert response.status_code == 200


@pytest.mark.asyncio
async def test_full_workflow_with_base_path(wenyuan_binary: Path) -> None:
    """Test the full retain/recall workflow through a base path."""
    bank_id = "integration_test_bank"

    with APIServer(wenyuan_binary, base_path=BASE_PATH) as server:
        base_url = f"http://127.0.0.1:{server.port}{BASE_PATH}"

        async with httpx.AsyncClient(base_url=base_url, timeout=30.0) as client:
            response = await client.post(
                f"/v1/default/banks/{bank_id}/memories",
                json={
                    "items": [
                        {
                            "content": "Wenyuan supports deployment under custom base paths for reverse proxy scenarios.",
                            "context": "integration test",
                        }
                    ]
                },
            )
            assert response.status_code == 200
            result = response.json()
            assert result["success"] is True

            response = await client.post(
                f"/v1/default/banks/{bank_id}/memories/recall",
                json={"query": "base path deployment"},
            )
            assert response.status_code == 200
            recall_result = response.json()
            assert "results" in recall_result
            assert len(recall_result["results"]) > 0
