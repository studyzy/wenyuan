"""Run the reference's own system-test stories against this service.

The reference ships a blackbox suite (`hindsight-system-tests`): 77 stories that
drive a real server through the published Python client, with the language model,
the embedding model and the reranker pointed at an in-process stub. Nothing in
the suite is special-cased for a particular server, so pointing it at this one is
the closest thing to an executable definition of "the same service".

What the plugin changes: the suite boots the reference server itself
(`uv run --project <api> hindsight-api`). This plugin replaces that one call with
a process that starts this service's binary, translating the environment the
suite sets into the configuration this service reads:

* ``HINDSIGHT_API_DATABASE_URL`` arrives as ``pg0://<instance>:<port>`` (the
  suite's embedded PostgreSQL). Resolve it first with ``resolve_pg0`` and pass the
  result in ``WENYUAN_DATABASE_URL``; the plugin hands this service the resolved
  URL.
* this service runs its own migrations at startup, so
  ``HINDSIGHT_API_RUN_MIGRATIONS_ON_STARTUP=true`` is set.

Everything else — the stub's provider configuration, the cache and worker
tunables — is passed through untouched, which is the point: a name this service
does not understand is a finding, not something to paper over here.

Usage:

    # 1. Resolve the suite's database URL (starts its embedded PostgreSQL).
    .venv/bin/python -c 'import asyncio,sys; from hindsight_api.pg0 import resolve_database_url; \\
        print(asyncio.run(resolve_database_url("pg0://hindsight-systest:15499")))'

    # 2. Run the suite against this service.
    WENYUAN_BIN=/tmp/wenyuan \\
    WENYUAN_DATABASE_URL=postgresql://hindsight:hindsight@127.0.0.1:15499/hindsight \\
    PYTHONPATH=<repo>/scripts \\
    <system-tests-venv>/bin/python -m pytest <system-tests>/tests \\
        -p system_parity_plugin -x -q
"""

import os
import subprocess
import sys
import time
from pathlib import Path

import hindsight_system_tests
import hindsight_system_tests.server as server_module

#: The line this service logs on startup. The harness requires it in the log, so
#: a run that somehow started the reference instead fails instead of passing.
STARTUP_MARKER = "entrypoint=wenyuan"

#: Set once the plugin's launcher has actually been used. A run that never uses
#: it is not a parity run, and is failed rather than reported as one.
_STARTED = False

REQUIRED = ("WENYUAN_BIN", "WENYUAN_DATABASE_URL")

STARTUP_TIMEOUT = float(os.environ.get("WENYUAN_STARTUP_TIMEOUT", "120"))


def _require_env() -> None:
    missing = [name for name in REQUIRED if not os.environ.get(name)]
    if missing:
        raise RuntimeError(
            "the parity harness needs %s in the environment" % ", ".join(missing)
        )


def _start_service(*, stub_url, log_path, extra_env=None):
    _require_env()
    port = server_module.free_port()

    env = {key: value for key, value in os.environ.items() if not key.startswith("HINDSIGHT_API_")}
    env.update(server_module.stub_environment(stub_url))
    env.update(extra_env or {})
    env.update(
        {
            "HINDSIGHT_API_DATABASE_URL": os.environ["WENYUAN_DATABASE_URL"],
            "HINDSIGHT_API_HOST": "127.0.0.1",
            "HINDSIGHT_API_PORT": str(port),
            "HINDSIGHT_API_LOG_LEVEL": "info",
            # The reference's server migrates on startup; this one is asked to.
            "HINDSIGHT_API_RUN_MIGRATIONS_ON_STARTUP": "true",
        }
    )

    # A scratch run directory with an empty .env, so nothing in the repository
    # leaks into the configuration the suite passes.
    run_dir = Path(log_path).parent / "run"
    run_dir.mkdir(parents=True, exist_ok=True)
    (run_dir / ".env").write_text("# intentionally empty: the harness passes the whole config\n")

    log_file = open(log_path, "w")
    process = subprocess.Popen(
        [os.environ["WENYUAN_BIN"]],
        cwd=run_dir,
        env=env,
        stdout=log_file,
        stderr=subprocess.STDOUT,
    )

    url = f"http://127.0.0.1:{port}"
    deadline = time.monotonic() + STARTUP_TIMEOUT
    import httpx

    while time.monotonic() < deadline:
        if process.poll() is not None:
            log_file.flush()
            raise RuntimeError(
                "the service exited during startup (%s); log:\n%s"
                % (process.returncode, Path(log_path).read_text()[-4000:])
            )
        try:
            if httpx.get(f"{url}/health/live", timeout=2).status_code == 200:
                break
        except Exception:
            time.sleep(0.2)
    else:
        process.kill()
        raise RuntimeError(
            "the service did not become ready within %ss; log:\n%s"
            % (STARTUP_TIMEOUT, Path(log_path).read_text()[-4000:])
        )

    if STARTUP_MARKER not in Path(log_path).read_text():
        process.kill()
        raise RuntimeError(
            "the process answering on %s is not this service (no %r in its log); log:\n%s"
            % (url, STARTUP_MARKER, Path(log_path).read_text()[-4000:])
        )

    return server_module.HindsightServer(url=url, log_path=Path(log_path), _process=process)


def pytest_sessionfinish(session, exitstatus):
    """Fail the run if the suite never started this service.

    A harness that silently falls back to the reference's own server would report
    the reference's health as this service's parity, which is the one failure
    mode worth failing the whole run over.
    """
    if not _STARTED:
        print(
            "\nPARITY HARNESS ERROR: the suite never started this service through the "
            "plugin, so these results do not describe this build.\n",
            file=sys.stderr,
        )
        session.exitstatus = 1


# The replacement has to happen when this module is imported, not from a pytest
# hook: the suite's `tests/conftest.py` does `from hindsight_system_tests import
# start_hindsight_server` at module import time, and pytest imports that conftest
# while it is still parsing arguments — before `pytest_configure` runs. Patching
# in a hook therefore leaves the conftest bound to the reference's own launcher,
# and the suite silently tests the reference server while reporting success.
def start_wenyuan_server(*, stub_url, log_path, extra_env=None):
    """Start this service where the suite expects the reference server."""
    global _STARTED
    _STARTED = True
    return _start_service(stub_url=stub_url, log_path=log_path, extra_env=extra_env)


hindsight_system_tests.start_hindsight_server = start_wenyuan_server
server_module.start_hindsight_server = start_wenyuan_server


def pytest_configure(config):
    """Refuse to run if this module is not the launcher the suite will use."""
    if hindsight_system_tests.start_hindsight_server is not start_wenyuan_server:
        raise RuntimeError("the parity plugin did not take over the server launcher")
    config._parity_started_marker = True


def resolve_pg0(url: str) -> str:
    """Resolve a pg0:// URL to a postgresql:// one (helper for the shell runner)."""
    import asyncio

    from hindsight_api.pg0 import resolve_database_url

    return asyncio.run(resolve_database_url(url))


if __name__ == "__main__":
    print(resolve_pg0(sys.argv[1] if len(sys.argv) > 1 else "pg0://hindsight-systest:15499"))