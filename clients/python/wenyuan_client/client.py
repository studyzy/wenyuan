"""A small, pythonic façade over the generated WenYuan API client.

This module is maintained by hand (it is not generated). It offers the handful
of operations most callers reach for first — retain, recall, reflect and bank
management — in both a synchronous and an asyncio flavour, on top of the
generated ``wenyuan_client_api`` package.

Every call is a thin translation into the generated request models; anything the
façade does not name can still be reached through the exposed sub-clients
(``client.memory``, ``client.banks``, ``client.monitoring`` ...), which are the
generated API objects themselves.
"""

from __future__ import annotations

import asyncio
import atexit
import os
from typing import Any, Dict, List, Optional, Union

import wenyuan_client_api
from wenyuan_client_api.api import banks_api, memory_api, monitoring_api
from wenyuan_client_api.models.bank_list_response import BankListResponse
from wenyuan_client_api.models.bank_profile_response import BankProfileResponse
from wenyuan_client_api.models.content import Content
from wenyuan_client_api.models.create_bank_request import CreateBankRequest
from wenyuan_client_api.models.memory_item import MemoryItem
from wenyuan_client_api.models.recall_request import RecallRequest
from wenyuan_client_api.models.recall_response import RecallResponse
from wenyuan_client_api.models.reflect_request import ReflectRequest
from wenyuan_client_api.models.reflect_response import ReflectResponse
from wenyuan_client_api.models.retain_request import RetainRequest
from wenyuan_client_api.models.retain_response import RetainResponse

#: Environment variable a deployment sets to point the client at its server.
#: The name is part of the deployment contract and is read verbatim.
BASE_URL_ENV_VAR = "HINDSIGHT_API_URL"

#: Where the service listens when the environment names no server.
DEFAULT_BASE_URL = "http://localhost:18888"

#: Default request timeout, in seconds. Reflect can take a while.
DEFAULT_TIMEOUT = 300.0

#: A retain item's content: either a plain string, or an ordered list of blocks
#: (``{"type": "text"|"image"|"file", ...}``) so an attachment can sit inline.
ContentInput = Union[str, List[Dict[str, Any]]]

_VERSION_FALLBACK = "0.1.0"

try:  # pragma: no cover - depends on how the distribution was installed.
    from importlib import metadata as _metadata

    _VERSION = _metadata.version("wenyuan-client")
except Exception:  # noqa: BLE001 - not installed as a distribution during tests.
    _VERSION = _VERSION_FALLBACK

DEFAULT_USER_AGENT = f"wenyuan-client-python/{_VERSION}"


#: Event loop the synchronous methods drive. It is created once and kept open:
#: the aiohttp session binds to whichever loop first used it, so closing the loop
#: between calls would strand that session.
_SYNC_LOOP: Optional[asyncio.AbstractEventLoop] = None


def _run_async(coro):
    """Run a coroutine to completion from synchronous code.

    Refuses to nest: calling a synchronous method from inside a coroutine would
    deadlock, so it fails loudly and points at the awaitable variant instead.
    """
    global _SYNC_LOOP

    try:
        asyncio.get_running_loop()
    except RuntimeError:
        pass
    else:
        coro.close()
        raise RuntimeError(
            "synchronous methods cannot run inside an event loop; "
            "use the awaitable method (for example arecall) instead"
        )

    if _SYNC_LOOP is None or _SYNC_LOOP.is_closed():
        _SYNC_LOOP = asyncio.new_event_loop()
        asyncio.set_event_loop(_SYNC_LOOP)
        atexit.register(_close_sync_loop)
    return _SYNC_LOOP.run_until_complete(coro)


def _close_sync_loop() -> None:
    """Close the shared loop when the interpreter exits."""
    global _SYNC_LOOP
    if _SYNC_LOOP is not None and not _SYNC_LOOP.is_closed():
        _SYNC_LOOP.close()
    _SYNC_LOOP = None


def _content(value: ContentInput) -> Content:
    """Wrap a string or a block list in the generated ``Content`` anyOf.

    The positional form is deliberate: it stores the caller's value on
    ``actual_instance`` verbatim, so a block list reaches the server exactly as
    written. ``Content.from_dict`` instead routes through pydantic's
    ``List[ContentAnyOfInner]`` schema, which cannot model a block ``oneOf`` and
    would replace each block with an empty object.
    """
    if isinstance(value, Content):
        return value
    return Content(value)


def _memory_item(item: Union[MemoryItem, Dict[str, Any]]) -> MemoryItem:
    if isinstance(item, MemoryItem):
        return item
    payload = dict(item)
    if "content" in payload and not isinstance(payload["content"], Content):
        payload["content"] = _content(payload["content"])
    return MemoryItem(**payload)


def _bank_id(bank_id: str) -> str:
    if not isinstance(bank_id, str) or not bank_id:
        raise ValueError("bank_id must be a non-empty string")
    return bank_id


class WenYuan:
    """Client for a WenYuan agent-memory server.

    Args:
        base_url: Server root. When omitted, ``HINDSIGHT_API_URL`` is read from
            the environment; when that is unset too, ``http://localhost:18888``
            is used. A trailing slash is tolerated.
        api_key: Optional bearer token sent with every request.
        timeout: Per-request timeout in seconds.
        user_agent: Extra product token placed in front of this client's own in
            the ``User-Agent`` header, so an integration can identify itself.
    """

    def __init__(
        self,
        base_url: Optional[str] = None,
        api_key: Optional[str] = None,
        timeout: float = DEFAULT_TIMEOUT,
        user_agent: Optional[str] = None,
    ) -> None:
        if base_url is None:
            base_url = os.environ.get(BASE_URL_ENV_VAR) or DEFAULT_BASE_URL
        base_url = base_url.rstrip("/")

        configuration = wenyuan_client_api.Configuration(host=base_url, access_token=api_key)
        self._api_client = wenyuan_client_api.ApiClient(configuration)
        self._api_client.user_agent = f"{user_agent} {DEFAULT_USER_AGENT}" if user_agent else DEFAULT_USER_AGENT
        self._timeout = timeout

        self.base_url = base_url

        # Generated API objects, exposed so callers can reach endpoints the
        # façade does not name.
        self.memory = memory_api.MemoryApi(self._api_client)
        self.banks = banks_api.BanksApi(self._api_client)
        self.monitoring = monitoring_api.MonitoringApi(self._api_client)

    # -- lifecycle ----------------------------------------------------------

    def close(self) -> None:
        """Release the underlying HTTP session."""
        _run_async(self._api_client.close())

    async def aclose(self) -> None:
        """Release the underlying HTTP session (awaitable form)."""
        await self._api_client.close()

    def __enter__(self) -> "WenYuan":
        return self

    def __exit__(self, *exc_info: Any) -> None:
        self.close()

    async def __aenter__(self) -> "WenYuan":
        return self

    async def __aexit__(self, *exc_info: Any) -> None:
        await self.aclose()

    def __repr__(self) -> str:
        return f"WenYuan(base_url={self.base_url!r})"

    # -- retain -------------------------------------------------------------

    def retain(
        self,
        bank_id: str,
        content: ContentInput,
        *,
        context: Optional[str] = None,
        metadata: Optional[Dict[str, str]] = None,
        document_id: Optional[str] = None,
        retain_async: bool = False,
        document_tags: Optional[List[str]] = None,
        operation_id: Optional[str] = None,
    ) -> RetainResponse:
        """Retain one piece of content into a bank."""
        return _run_async(
            self.aretain(
                bank_id,
                content,
                context=context,
                metadata=metadata,
                document_id=document_id,
                retain_async=retain_async,
                document_tags=document_tags,
                operation_id=operation_id,
            )
        )

    async def aretain(
        self,
        bank_id: str,
        content: ContentInput,
        *,
        retain_async: bool = False,
        document_tags: Optional[List[str]] = None,
        operation_id: Optional[str] = None,
        **item_fields: Any,
    ) -> RetainResponse:
        """Awaitable :meth:`retain`.

        Extra keyword arguments (``context``, ``metadata``, ``document_id``,
        ``timestamp`` ...) are placed on the generated ``MemoryItem``, which
        validates them.
        """
        return await self.aretain_batch(
            bank_id,
            [{"content": content, **item_fields}],
            retain_async=retain_async,
            document_tags=document_tags,
            operation_id=operation_id,
        )

    def retain_batch(
        self,
        bank_id: str,
        items: List[Union[MemoryItem, Dict[str, Any]]],
        *,
        retain_async: bool = False,
        document_tags: Optional[List[str]] = None,
        operation_id: Optional[str] = None,
    ) -> RetainResponse:
        """Retain several items in one request."""
        return _run_async(
            self.aretain_batch(
                bank_id,
                items,
                retain_async=retain_async,
                document_tags=document_tags,
                operation_id=operation_id,
            )
        )

    async def aretain_batch(
        self,
        bank_id: str,
        items: List[Union[MemoryItem, Dict[str, Any]]],
        *,
        retain_async: bool = False,
        document_tags: Optional[List[str]] = None,
        operation_id: Optional[str] = None,
    ) -> RetainResponse:
        """Awaitable :meth:`retain_batch`."""
        request = RetainRequest(
            items=[_memory_item(item) for item in items],
            var_async=retain_async,
            document_tags=document_tags,
            operation_id=operation_id,
        )
        return await self.memory.retain_memories(
            _bank_id(bank_id), retain_request=request, _request_timeout=self._timeout
        )

    # -- recall -------------------------------------------------------------

    def recall(
        self,
        bank_id: str,
        query: str,
        *,
        types: Optional[List[str]] = None,
        budget: Optional[Any] = None,
        max_tokens: Optional[int] = None,
        trace: bool = False,
        **options: Any,
    ) -> RecallResponse:
        """Retrieve memories relevant to ``query``.

        Extra keyword arguments are passed straight to the generated
        ``RecallRequest``, which validates them.
        """
        return _run_async(
            self.arecall(
                bank_id,
                query,
                types=types,
                budget=budget,
                max_tokens=max_tokens,
                trace=trace,
                **options,
            )
        )

    async def arecall(self, bank_id: str, query: str, **kwargs: Any) -> RecallResponse:
        """Awaitable :meth:`recall`."""
        request = RecallRequest(query=query, **kwargs)
        return await self.memory.recall_memories(
            _bank_id(bank_id), recall_request=request, _request_timeout=self._timeout
        )

    # -- reflect ------------------------------------------------------------

    def reflect(
        self,
        bank_id: str,
        query: str,
        *,
        budget: Optional[Any] = None,
        context: Optional[str] = None,
        max_tokens: Optional[int] = None,
        **options: Any,
    ) -> ReflectResponse:
        """Synthesise an answer from the bank's memories."""
        return _run_async(
            self.areflect(bank_id, query, budget=budget, context=context, max_tokens=max_tokens, **options)
        )

    async def areflect(self, bank_id: str, query: str, **kwargs: Any) -> ReflectResponse:
        """Awaitable :meth:`reflect`."""
        request = ReflectRequest(query=query, **kwargs)
        return await self.memory.reflect(
            _bank_id(bank_id), reflect_request=request, _request_timeout=self._timeout
        )

    # -- banks --------------------------------------------------------------

    def create_bank(self, bank_id: str, **options: Any) -> BankProfileResponse:
        """Create (or update) a bank.

        Extra keyword arguments are passed to the generated ``CreateBankRequest``.
        """
        return _run_async(self.acreate_bank(bank_id, **options))

    async def acreate_bank(self, bank_id: str, **options: Any) -> BankProfileResponse:
        """Awaitable :meth:`create_bank`."""
        request = CreateBankRequest(**options)
        return await self.banks.create_or_update_bank(
            _bank_id(bank_id), create_bank_request=request, _request_timeout=self._timeout
        )

    def list_banks(
        self,
        *,
        q: Optional[str] = None,
        limit: Optional[int] = None,
        offset: Optional[int] = None,
    ) -> BankListResponse:
        """List banks, optionally filtered and paginated."""
        return _run_async(self.alist_banks(q=q, limit=limit, offset=offset))

    async def alist_banks(
        self,
        *,
        q: Optional[str] = None,
        limit: Optional[int] = None,
        offset: Optional[int] = None,
    ) -> BankListResponse:
        """Awaitable :meth:`list_banks`."""
        return await self.banks.list_banks(
            q=q, limit=limit, offset=offset, _request_timeout=self._timeout
        )