#!/usr/bin/env python3
"""Re-apply the hand patches to the freshly generated Python client.

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

3. ``wenyuan_client_api/models/content_any_of_inner.py`` is the ``oneOf`` union
   of the three content-block models. The generated ``__init__`` hands whatever
   it is given straight to ``actual_instance``, and its validator accepts only a
   block *instance*, so a plain ``{"type": "text", "text": ...}`` dict — the form
   the contract itself uses, and the form a caller of ``Content.from_dict`` or
   ``MemoryItem.from_dict`` supplies — is turned into an empty member and
   serialises as ``null``. The patch dispatches a dict on its ``type`` to the
   matching block model first, so ``from_dict`` round-trips the block instead of
   dropping it.

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


# --------------------------------------------------------------------------
# 3. content_any_of_inner.py: dispatch a plain block dict to its model.
# --------------------------------------------------------------------------

CONTENT_BLOCK_STOCK = '''    def __init__(self, *args, **kwargs) -> None:
        if args:
            if len(args) > 1:
                raise ValueError("If a position argument is used, only 1 is allowed to set `actual_instance`")
            if kwargs:
                raise ValueError("If a position argument is used, keyword arguments cannot be used.")
            super().__init__(actual_instance=args[0])
        else:
            super().__init__(**kwargs)
'''

CONTENT_BLOCK_PATCHED = '''    def __init__(self, *args, **kwargs) -> None:
        if args:
            if len(args) > 1:
                raise ValueError("If a position argument is used, only 1 is allowed to set `actual_instance`")
            if kwargs:
                raise ValueError("If a position argument is used, keyword arguments cannot be used.")
            value = args[0]
        elif "actual_instance" in kwargs:
            super().__init__(**kwargs)
            return
        elif kwargs:
            value = kwargs
        else:
            super().__init__()
            return

        if isinstance(value, dict):
            # The contract spells a content block as a plain object
            # (``{"type": "text", "text": ...}``), which is also what the
            # ``from_dict`` path hands over. Build the matching block model so
            # the member carries the block instead of serialising as null.
            block_class = {
                "text": TextContentBlock,
                "image": ImageContentBlock,
                "file": FileContentBlock,
            }.get(value.get("type"))
            if block_class is None:
                raise ValueError(f"Unknown content block type: {value.get('type')!r}")
            value = block_class.from_dict(value)
        super().__init__(actual_instance=value)
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
        "models/content_any_of_inner.py": patch(
            PACKAGE / "models" / "content_any_of_inner.py", CONTENT_BLOCK_STOCK, CONTENT_BLOCK_PATCHED
        ),
    }
    for name, state in results.items():
        print(f"{name}: {state}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())