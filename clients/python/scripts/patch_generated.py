#!/usr/bin/env python3
"""Re-apply the two hand patches to the freshly generated Python client.

The generator (openapi-generator ``python``/``asyncio``, see
``clients/python/openapi-generator-config.yaml``) emits two things the SDK
cannot ship as-is. Regenerating overwrites them, so this script is invoked by
``scripts/generate_clients.sh python`` right after generation and is the only
place those edits live. It is idempotent: an already-patched file is left alone.

1. ``wenyuan_client_api/rest.py`` builds its ``aiohttp`` connector and session in
   ``RESTClientObject.__init__``. ``aiohttp.TCPConnector`` binds the running
   event loop, so merely constructing an ``ApiClient`` outside a running loop
   raises "no running event loop". The patch stores the configuration in
   ``__init__`` and defers session creation to ``_ensure_session()``, which runs
   inside the first request (always async). ``pool_manager`` and
   ``retry_client`` become read-only properties so the rest of the generated
   file keeps working unchanged.

2. ``wenyuan_client_api/models/content.py`` serialises its ``anyOf``
   (``str | list[ContentAnyOfInner]``) in ``to_dict``. The generated body only
   unwraps a single object; for the list form it returns the list untouched. The
   patch adds a list branch so each element is serialised through its own
   ``to_dict`` when it has one (blocks built as generated models), while a list
   of plain dicts — what the wrapper's ``Content(value)`` positional form
   stores — still passes through unchanged.

Usage:
    python3 clients/python/scripts/patch_generated.py
"""

from __future__ import annotations

import pathlib
import sys

PACKAGE = pathlib.Path(__file__).resolve().parent.parent / "wenyuan_client_api"

# --------------------------------------------------------------------------
# 1. rest.py: defer the aiohttp session to the first request.
# --------------------------------------------------------------------------

REST_STOCK = '''    def __init__(self, configuration) -> None:

        # maxsize is number of requests to host that are allowed in parallel
        maxsize = configuration.connection_pool_maxsize

        ssl_context = ssl.create_default_context(
            cafile=configuration.ssl_ca_cert
        )
        if configuration.cert_file:
            ssl_context.load_cert_chain(
                configuration.cert_file, keyfile=configuration.key_file
            )

        if not configuration.verify_ssl:
            ssl_context.check_hostname = False
            ssl_context.verify_mode = ssl.CERT_NONE

        connector = aiohttp.TCPConnector(
            limit=maxsize,
            ssl=ssl_context
        )

        self.proxy = configuration.proxy
        self.proxy_headers = configuration.proxy_headers

        # https pool manager
        self.pool_manager = aiohttp.ClientSession(
            connector=connector,
            trust_env=True
        )

        retries = configuration.retries
        self.retry_client: Optional[aiohttp_retry.RetryClient]
        if retries is not None:
            self.retry_client = aiohttp_retry.RetryClient(
                client_session=self.pool_manager,
                retry_options=aiohttp_retry.ExponentialRetry(
                    attempts=retries,
                    factor=2.0,
                    start_timeout=0.1,
                    max_timeout=120.0
                )
            )
        else:
            self.retry_client = None

    async def close(self):
        await self.pool_manager.close()
        if self.retry_client is not None:
            await self.retry_client.close()
'''

REST_PATCHED = '''    def __init__(self, configuration) -> None:
        # aiohttp.TCPConnector captures the running event loop, so building it
        # here would fail for any client created outside a loop. Keep the
        # configuration and build the session on first use instead.
        self._configuration = configuration
        self._pool_manager: Optional[aiohttp.ClientSession] = None
        self._retry_client: Optional[aiohttp_retry.RetryClient] = None

        self.proxy = configuration.proxy
        self.proxy_headers = configuration.proxy_headers

    def _ensure_session(self) -> None:
        """Build the aiohttp session on first use (always inside a request loop)."""
        if self._pool_manager is not None:
            return

        configuration = self._configuration
        maxsize = configuration.connection_pool_maxsize

        ssl_context = ssl.create_default_context(
            cafile=configuration.ssl_ca_cert
        )
        if configuration.cert_file:
            ssl_context.load_cert_chain(
                configuration.cert_file, keyfile=configuration.key_file
            )

        if not configuration.verify_ssl:
            ssl_context.check_hostname = False
            ssl_context.verify_mode = ssl.CERT_NONE

        connector = aiohttp.TCPConnector(
            limit=maxsize,
            ssl=ssl_context
        )

        self._pool_manager = aiohttp.ClientSession(
            connector=connector,
            trust_env=True
        )

        retries = configuration.retries
        if retries is not None:
            self._retry_client = aiohttp_retry.RetryClient(
                client_session=self._pool_manager,
                retry_options=aiohttp_retry.ExponentialRetry(
                    attempts=retries,
                    factor=2.0,
                    start_timeout=0.1,
                    max_timeout=120.0
                )
            )
        else:
            self._retry_client = None

    @property
    def pool_manager(self) -> aiohttp.ClientSession:
        """The request session, created on first access."""
        self._ensure_session()
        return self._pool_manager

    @property
    def retry_client(self) -> Optional[aiohttp_retry.RetryClient]:
        """The retry wrapper, created on first access."""
        self._ensure_session()
        return self._retry_client

    async def close(self):
        if self._pool_manager is not None:
            await self._pool_manager.close()
        if self._retry_client is not None:
            await self._retry_client.close()
'''

# --------------------------------------------------------------------------
# 2. content.py: serialise the list form of the anyOf.
# --------------------------------------------------------------------------

CONTENT_STOCK = '''        if hasattr(self.actual_instance, "to_dict") and callable(self.actual_instance.to_dict):
            return self.actual_instance.to_dict()
        else:
            return self.actual_instance
'''

CONTENT_PATCHED = '''        if hasattr(self.actual_instance, "to_dict") and callable(self.actual_instance.to_dict):
            return self.actual_instance.to_dict()
        elif isinstance(self.actual_instance, list):
            return [
                item.to_dict() if hasattr(item, "to_dict") and callable(item.to_dict) else item
                for item in self.actual_instance
            ]
        else:
            return self.actual_instance
'''


def patch(path: pathlib.Path, stock: str, patched: str) -> str:
    text = path.read_text()
    if patched in text:
        return "already patched"
    if text.count(stock) != 1:
        raise SystemExit(f"{path}: expected exactly one match for the stock block, found {text.count(stock)}")
    path.write_text(text.replace(stock, patched))
    return "patched"


def main() -> int:
    if not PACKAGE.is_dir():
        raise SystemExit(f"{PACKAGE} not found; run the generator first")
    results = {
        "rest.py": patch(PACKAGE / "rest.py", REST_STOCK, REST_PATCHED),
        "models/content.py": patch(PACKAGE / "models" / "content.py", CONTENT_STOCK, CONTENT_PATCHED),
    }
    for name, state in results.items():
        print(f"{name}: {state}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())