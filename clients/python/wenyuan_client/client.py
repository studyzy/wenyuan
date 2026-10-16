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
import json
import math
import os
import random
from contextlib import contextmanager
from contextvars import ContextVar
from pathlib import Path
from typing import Any, Callable, Awaitable, Dict, Iterator, List, Optional, Union

import wenyuan_client_api
from wenyuan_client_api.api import (
    audit_api,
    bank_templates_api,
    bank_transfer_api,
    banks_api,
    directives_api,
    document_transfer_api,
    documents_api,
    entities_api,
    files_api,
    knowledge_base_api,
    llm_traces_api,
    memory_api,
    mental_models_api,
    monitoring_api,
    operations_api,
    webhooks_api,
)
from wenyuan_client_api.exceptions import ApiException
from wenyuan_client_api.models.bank_list_response import BankListResponse
from wenyuan_client_api.models.bank_profile_response import BankProfileResponse
from wenyuan_client_api.models.content import Content
from wenyuan_client_api.models.create_bank_request import CreateBankRequest
from wenyuan_client_api.models.file_retain_response import FileRetainResponse
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

#: Attempts a retryable call makes in total, including the first.
DEFAULT_MAX_ATTEMPTS = 3

#: Fallback backoff when the server reports it is at capacity without a usable
#: ``Retry-After``.
_FALLBACK_BACKOFF_SECONDS = 0.5


def _retry_after_seconds(exception: ApiException) -> Optional[float]:
    """Parse ``Retry-After`` (delta-seconds form) from an error, if present.

    The generated client attaches the response headers to the exception when it
    has them; they arrive either as a case-insensitive mapping or as a sequence
    of pairs, so both shapes are read. The HTTP-date form is legal but rarer,
    and the caller's own backoff is a better answer than parsing dates.
    """
    headers = getattr(exception, "headers", None)
    if not headers:
        return None
    raw = None
    getter = getattr(headers, "get", None)
    if callable(getter):
        raw = getter("Retry-After") or getter("retry-after")
    else:
        for name, value in headers:
            if str(name).lower() == "retry-after":
                raw = value
                break
    if raw is None:
        return None
    try:
        seconds = float(raw)
    except (TypeError, ValueError):
        return None
    if not math.isfinite(seconds):
        return None
    return max(0.0, seconds)


async def _retry_on_capacity(
    call: "Callable[[], Awaitable[Any]]",
    max_attempts: int,
    rng: random.Random,
) -> Any:
    """Run ``call``, retrying while the server reports it is at capacity.

    Only for **idempotent** operations. Recall and reflect are reads, so a repeat
    is free; a synchronous retain is not — its ``operation_id`` is ignored — and
    is deliberately left out, because a retry there could duplicate a write.

    Two things matter more than the retry itself. ``Retry-After`` is honoured:
    the server sends it precisely because it knows how long its queue is, so
    retrying sooner just earns another refusal. The wait is **jittered**: a burst
    of clients that all obey ``Retry-After: 1`` exactly would come back together
    and rebuild the spike that caused the rejection.
    """
    for attempt in range(1, max_attempts + 1):
        try:
            return await call()
        except ApiException as exception:
            at_capacity = exception.status in (429, 503)
            if not at_capacity or attempt == max_attempts:
                raise
            wait = _retry_after_seconds(exception)
            if wait is None:
                wait = _FALLBACK_BACKOFF_SECONDS * (2 ** (attempt - 1))
            # Full jitter: sleep somewhere in [0, wait], so a synchronised burst
            # spreads out instead of returning together.
            await asyncio.sleep(rng.uniform(0, wait))
    raise AssertionError("unreachable")  # pragma: no cover


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
        max_attempts: Total attempts an *idempotent* call (recall, reflect) makes
            when the server reports it is at capacity (429/503). ``1`` disables
            retrying. Waits honour ``Retry-After`` and are jittered; writes are
            never retried here.
    """

    def __init__(
        self,
        base_url: Optional[str] = None,
        api_key: Optional[str] = None,
        timeout: float = DEFAULT_TIMEOUT,
        user_agent: Optional[str] = None,
        max_attempts: int = DEFAULT_MAX_ATTEMPTS,
    ) -> None:
        if base_url is None:
            base_url = os.environ.get(BASE_URL_ENV_VAR) or DEFAULT_BASE_URL
        base_url = base_url.rstrip("/")

        configuration = wenyuan_client_api.Configuration(host=base_url, access_token=api_key)
        self._api_client = wenyuan_client_api.ApiClient(configuration)
        self._api_client.user_agent = f"{user_agent} {DEFAULT_USER_AGENT}" if user_agent else DEFAULT_USER_AGENT
        self._timeout = timeout
        self._max_attempts = max(1, max_attempts)
        # A per-client RNG keeps the jitter independent of any seeding the
        # calling application does to the global ``random`` module.
        self._retry_rng = random.Random()
        # Task/thread-local, so suspending retains in one scope never silences
        # another concurrent user of the same client.
        self._retain_suspended: ContextVar[bool] = ContextVar("retain_suspended", default=False)

        self.base_url = base_url

        # Generated API objects, exposed as properties so callers can reach the
        # endpoints the façade does not name.
        self._memory_api = memory_api.MemoryApi(self._api_client)
        self._banks_api = banks_api.BanksApi(self._api_client)
        self._documents_api = documents_api.DocumentsApi(self._api_client)
        self._entities_api = entities_api.EntitiesApi(self._api_client)
        self._mental_models_api = mental_models_api.MentalModelsApi(self._api_client)
        self._knowledge_base_api = knowledge_base_api.KnowledgeBaseApi(self._api_client)
        self._directives_api = directives_api.DirectivesApi(self._api_client)
        self._operations_api = operations_api.OperationsApi(self._api_client)
        self._webhooks_api = webhooks_api.WebhooksApi(self._api_client)
        self._files_api = files_api.FilesApi(self._api_client)
        self._monitoring_api = monitoring_api.MonitoringApi(self._api_client)
        self._document_transfer_api = document_transfer_api.DocumentTransferApi(self._api_client)
        self._bank_transfer_api = bank_transfer_api.BankTransferApi(self._api_client)
        self._audit_api = audit_api.AuditApi(self._api_client)
        self._bank_templates_api = bank_templates_api.BankTemplatesApi(self._api_client)
        self._llm_traces_api = llm_traces_api.LLMTracesApi(self._api_client)

    # -- low-level API accessors --------------------------------------------
    # These expose the full generated surface for the areas the façade does not
    # wrap. Every method on them is async.

    @property
    def memory(self) -> memory_api.MemoryApi:
        """Low-level Memory API — retain, recall, reflect, memories, tags, graph."""
        return self._memory_api

    @property
    def banks(self) -> banks_api.BanksApi:
        """Low-level Banks API — create, update, delete banks; stats; config."""
        return self._banks_api

    @property
    def documents(self) -> documents_api.DocumentsApi:
        """Low-level Documents API — list, get, update, delete documents and chunks."""
        return self._documents_api

    @property
    def entities(self) -> entities_api.EntitiesApi:
        """Low-level Entities API — list, get, and regenerate entity observations."""
        return self._entities_api

    @property
    def mental_models(self) -> mental_models_api.MentalModelsApi:
        """Low-level Mental Models API — create, list, get, update, refresh, delete, history."""
        return self._mental_models_api

    @property
    def knowledge_base(self) -> knowledge_base_api.KnowledgeBaseApi:
        """Low-level Knowledge Base API — folder/page tree, page CRUD, search, export."""
        return self._knowledge_base_api

    @property
    def directives(self) -> directives_api.DirectivesApi:
        """Low-level Directives API — create, list, get, update, delete."""
        return self._directives_api

    @property
    def operations(self) -> operations_api.OperationsApi:
        """Low-level Operations API — status, list, cancel, retry async operations."""
        return self._operations_api

    @property
    def webhooks(self) -> webhooks_api.WebhooksApi:
        """Low-level Webhooks API — create, list, update, delete webhooks and deliveries."""
        return self._webhooks_api

    @property
    def files(self) -> files_api.FilesApi:
        """Low-level Files API — upload and retain files."""
        return self._files_api

    @property
    def monitoring(self) -> monitoring_api.MonitoringApi:
        """Low-level Monitoring API — health, liveness, readiness, version, metrics."""
        return self._monitoring_api

    @property
    def document_transfer(self) -> document_transfer_api.DocumentTransferApi:
        """Low-level Document Transfer API — export/import documents, download files."""
        return self._document_transfer_api

    @property
    def bank_transfer(self) -> bank_transfer_api.BankTransferApi:
        """Low-level Bank Transfer API — export/import/clone a whole bank."""
        return self._bank_transfer_api

    @property
    def audit(self) -> audit_api.AuditApi:
        """Low-level Audit API — audit log entries and stats."""
        return self._audit_api

    @property
    def bank_templates(self) -> bank_templates_api.BankTemplatesApi:
        """Low-level Bank Templates API — export/import templates, schema."""
        return self._bank_templates_api

    @property
    def llm_traces(self) -> llm_traces_api.LLMTracesApi:
        """Low-level LLM Traces API — request traces and stats."""
        return self._llm_traces_api

    # -- retain suspension --------------------------------------------------

    @contextmanager
    def suspend_retains(self) -> Iterator[None]:
        """Temporarily suppress retains in the current execution context.

        Recall and reflect are unaffected, while :meth:`retain`,
        :meth:`retain_batch` and :meth:`retain_files` (and their awaitable
        counterparts) send no request and report an empty result —
        ``items_count=0`` and no operation ids. Useful for evaluation runs or
        replaying a transcript against a populated bank: read a bank without
        adding to it.

        The suspension is local to the current synchronous flow or async task, so
        concurrent users of the same client are unaffected. Scopes may be nested
        and are restored when the scope exits, including after an exception.

        The convenience methods are guarded; the low-level accessors
        (:attr:`memory`, :attr:`files`) call the generated API directly and are
        deliberately not intercepted.

        ::

            with client.suspend_retains():
                client.retain(bank_id, "not stored")   # items_count == 0
                client.recall(bank_id, "still works")  # unaffected
        """
        token = self._retain_suspended.set(True)
        try:
            yield
        finally:
            self._retain_suspended.reset(token)

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
        if self._retain_suspended.get():
            return RetainResponse(success=True, bank_id=bank_id, items_count=0, var_async=False)

        request = RetainRequest(
            items=[_memory_item(item) for item in items],
            var_async=retain_async,
            document_tags=document_tags,
            operation_id=operation_id,
        )
        return await self.memory.retain_memories(
            _bank_id(bank_id), retain_request=request, _request_timeout=self._timeout
        )

    # -- retain files -------------------------------------------------------

    def retain_files(
        self,
        bank_id: str,
        files: List[Union[str, Path]],
        *,
        context: Optional[str] = None,
        files_metadata: Optional[List[Dict[str, Any]]] = None,
    ) -> FileRetainResponse:
        """Upload files and retain their contents (sync wrapper)."""
        return _run_async(
            self.aretain_files(bank_id, files, context=context, files_metadata=files_metadata)
        )

    async def aretain_files(
        self,
        bank_id: str,
        files: List[Union[str, Path]],
        *,
        context: Optional[str] = None,
        files_metadata: Optional[List[Dict[str, Any]]] = None,
    ) -> FileRetainResponse:
        """Upload files and retain their contents as memories.

        Files are converted to text server-side (PDF, DOCX, images via OCR, audio
        via transcription, and more) and ingested. Processing is always
        asynchronous: use the returned operation ids to track progress.

        ``files_metadata``, when given, must match ``files`` in length; each
        entry may carry ``context``, ``document_id``, ``tags`` and ``metadata``.
        """
        if self._retain_suspended.get():
            return FileRetainResponse(operation_ids=[])

        file_data: List[tuple] = []
        for file_path in files:
            path = Path(file_path)
            file_data.append((path.name, await asyncio.to_thread(path.read_bytes)))

        meta = files_metadata or [{"context": context} if context else {} for _ in files]
        request_body = json.dumps({"files_metadata": meta})

        return await self.files.file_retain(
            _bank_id(bank_id),
            files=file_data,
            request=request_body,
            _request_timeout=self._timeout,
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
        """Awaitable :meth:`recall`.

        A read, so it is retried while the server reports it is at capacity.
        """
        request = RecallRequest(query=query, **kwargs)
        bank = _bank_id(bank_id)
        return await _retry_on_capacity(
            lambda: self.memory.recall_memories(bank, recall_request=request, _request_timeout=self._timeout),
            self._max_attempts,
            self._retry_rng,
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
        """Awaitable :meth:`reflect`.

        A read, so it is retried while the server reports it is at capacity.
        """
        request = ReflectRequest(query=query, **kwargs)
        bank = _bank_id(bank_id)
        return await _retry_on_capacity(
            lambda: self.memory.reflect(bank, reflect_request=request, _request_timeout=self._timeout),
            self._max_attempts,
            self._retry_rng,
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