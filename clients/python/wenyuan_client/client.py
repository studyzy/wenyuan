"""A small, pythonic façade over the generated WenYuan API client.

This module is maintained by hand (it is not generated). It offers the
operations most callers reach for — retain, recall, reflect, bank management and
configuration, mental models, the knowledge base, directives, and bank transfer —
in both a synchronous and an asyncio flavour, on top of the generated
``wenyuan_client_api`` package.

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

import aiohttp
from yarl import URL

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
from wenyuan_client_api.models.bank_config_update import BankConfigUpdate
from wenyuan_client_api.models.bank_list_response import BankListResponse
from wenyuan_client_api.models.bank_profile_response import BankProfileResponse
from wenyuan_client_api.models.content import Content
from wenyuan_client_api.models.create_bank_request import CreateBankRequest
from wenyuan_client_api.models.create_directive_request import CreateDirectiveRequest
from wenyuan_client_api.models.create_folder_request import CreateFolderRequest
from wenyuan_client_api.models.create_mental_model_request import CreateMentalModelRequest
from wenyuan_client_api.models.create_page_request import CreatePageRequest
from wenyuan_client_api.models.file_retain_response import FileRetainResponse
from wenyuan_client_api.models.list_memory_units_response import ListMemoryUnitsResponse
from wenyuan_client_api.models.memory_item import MemoryItem
from wenyuan_client_api.models.recall_request import RecallRequest
from wenyuan_client_api.models.recall_response import RecallResponse
from wenyuan_client_api.models.reflect_request import ReflectRequest
from wenyuan_client_api.models.reflect_response import ReflectResponse
from wenyuan_client_api.models.retain_request import RetainRequest
from wenyuan_client_api.models.retain_response import RetainResponse
from wenyuan_client_api.models.update_directive_request import UpdateDirectiveRequest
from wenyuan_client_api.models.update_mental_model_request import UpdateMentalModelRequest
from wenyuan_client_api.models.update_node_request import UpdateNodeRequest
from wenyuan_client_api.models.version_response import VersionResponse

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


#: Sentinel for "argument not provided", for the cases where ``None`` is itself a
#: meaningful value the server distinguishes from absence (``parent_id=None``
#: means "the root", not "leave the parent alone").
_UNSET: Any = object()


def _trigger_input(trigger: Dict[str, Any]) -> Any:
    """Build a ``MentalModelTriggerInput`` naming ONLY the settings the caller gave.

    Every route that takes a trigger applies it as a patch — over the page
    defaults when a page is created, over the stored trigger when one is updated
    — and the server reads "named" from the fields the request actually carried.
    The generated model defeats that on its own: ``to_dict`` drops ``None`` but
    keeps a non-``None`` default, so a caller setting one field would also ship
    ``mode="full"``, ``refresh_after_consolidation=False``,
    ``exclude_mental_models=False`` and ``keep_trace=False`` — quietly asking for
    a from-scratch rebuild of everything the caller never mentioned.

    Passing ``None`` for the defaulted fields the caller left out makes
    ``to_dict`` drop them. Only fields whose default is non-``None`` need this: a
    nullable field left unset is already omitted, while passing it explicitly as
    ``None`` is how a stored value (a cron schedule, say) is cleared.
    """
    from wenyuan_client_api.models import mental_model_trigger_input

    model = mental_model_trigger_input.MentalModelTriggerInput
    unnamed_defaults = {
        name: None
        for name, field in model.model_fields.items()
        if name not in trigger and field.default is not None
    }
    return model(**unnamed_defaults, **trigger)


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
        # The generated routes declare no security scheme, so ``access_token``
        # above is never applied on its own. Setting the header as a client
        # default makes ``api_key`` reach every request — including the bank and
        # config routes — with the same attribution the user agent already gives.
        if api_key:
            self._api_client.set_default_header("Authorization", f"Bearer {api_key}")
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

    def create_bank(
        self,
        bank_id: str,
        name: Optional[str] = None,
        mission: Optional[str] = None,
        disposition_skepticism: Optional[int] = None,
        disposition_literalism: Optional[int] = None,
        disposition_empathy: Optional[int] = None,
        disposition: Optional[Dict[str, float]] = None,
        retain_mission: Optional[str] = None,
        retain_extraction_mode: Optional[str] = None,
        retain_custom_instructions: Optional[str] = None,
        retain_chunk_size: Optional[int] = None,
        retain_structured_chunk_size: Optional[int] = None,
        retain_max_attachments_per_chunk: Optional[int] = None,
        enable_observations: Optional[bool] = None,
        observations_mission: Optional[str] = None,
        enable_text_search: Optional[bool] = None,
        enable_temporal_retrieval: Optional[bool] = None,
        enable_graph_retrieval: Optional[bool] = None,
        enable_reranking: Optional[bool] = None,
        reflect_mission: Optional[str] = None,
        background: Optional[str] = None,
        **options: Any,
    ) -> BankProfileResponse:
        """Create (or update) a bank.

        Only the arguments a caller names are sent, so an omitted field keeps the
        server's default rather than overwriting it with ``None``. The three
        ``disposition_*`` fields take priority over the legacy ``disposition``
        mapping; that mapping's ``skepticism``/``literalism``/``empathy`` keys are
        expanded into the individual fields. Any further keyword argument is
        placed on the generated ``CreateBankRequest``, which validates it.
        """
        return _run_async(
            self.acreate_bank(
                bank_id,
                name=name,
                mission=mission,
                disposition_skepticism=disposition_skepticism,
                disposition_literalism=disposition_literalism,
                disposition_empathy=disposition_empathy,
                disposition=disposition,
                retain_mission=retain_mission,
                retain_extraction_mode=retain_extraction_mode,
                retain_custom_instructions=retain_custom_instructions,
                retain_chunk_size=retain_chunk_size,
                retain_structured_chunk_size=retain_structured_chunk_size,
                retain_max_attachments_per_chunk=retain_max_attachments_per_chunk,
                enable_observations=enable_observations,
                observations_mission=observations_mission,
                enable_text_search=enable_text_search,
                enable_temporal_retrieval=enable_temporal_retrieval,
                enable_graph_retrieval=enable_graph_retrieval,
                enable_reranking=enable_reranking,
                reflect_mission=reflect_mission,
                background=background,
                **options,
            )
        )

    async def acreate_bank(
        self,
        bank_id: str,
        name: Optional[str] = None,
        mission: Optional[str] = None,
        disposition_skepticism: Optional[int] = None,
        disposition_literalism: Optional[int] = None,
        disposition_empathy: Optional[int] = None,
        disposition: Optional[Dict[str, float]] = None,
        retain_mission: Optional[str] = None,
        retain_extraction_mode: Optional[str] = None,
        retain_custom_instructions: Optional[str] = None,
        retain_chunk_size: Optional[int] = None,
        retain_structured_chunk_size: Optional[int] = None,
        retain_max_attachments_per_chunk: Optional[int] = None,
        enable_observations: Optional[bool] = None,
        observations_mission: Optional[str] = None,
        enable_text_search: Optional[bool] = None,
        enable_temporal_retrieval: Optional[bool] = None,
        enable_graph_retrieval: Optional[bool] = None,
        enable_reranking: Optional[bool] = None,
        reflect_mission: Optional[str] = None,
        background: Optional[str] = None,
        **options: Any,
    ) -> BankProfileResponse:
        """Awaitable :meth:`create_bank`."""
        body: Dict[str, Any] = {}
        if name is not None:
            body["name"] = name
        if mission is not None:
            body["mission"] = mission
        if reflect_mission is not None:
            body["reflect_mission"] = reflect_mission
        if background is not None:
            body["background"] = background
        # The individual disposition fields win over the legacy mapping.
        if disposition_skepticism is not None:
            body["disposition_skepticism"] = disposition_skepticism
        elif disposition is not None:
            body["disposition_skepticism"] = disposition.get("skepticism")
        if disposition_literalism is not None:
            body["disposition_literalism"] = disposition_literalism
        elif disposition is not None:
            body["disposition_literalism"] = disposition.get("literalism")
        if disposition_empathy is not None:
            body["disposition_empathy"] = disposition_empathy
        elif disposition is not None:
            body["disposition_empathy"] = disposition.get("empathy")
        if retain_mission is not None:
            body["retain_mission"] = retain_mission
        if retain_extraction_mode is not None:
            body["retain_extraction_mode"] = retain_extraction_mode
        if retain_custom_instructions is not None:
            body["retain_custom_instructions"] = retain_custom_instructions
        if retain_chunk_size is not None:
            body["retain_chunk_size"] = retain_chunk_size
        if retain_structured_chunk_size is not None:
            body["retain_structured_chunk_size"] = retain_structured_chunk_size
        if retain_max_attachments_per_chunk is not None:
            body["retain_max_attachments_per_chunk"] = retain_max_attachments_per_chunk
        if enable_observations is not None:
            body["enable_observations"] = enable_observations
        if observations_mission is not None:
            body["observations_mission"] = observations_mission
        if enable_text_search is not None:
            body["enable_text_search"] = enable_text_search
        if enable_temporal_retrieval is not None:
            body["enable_temporal_retrieval"] = enable_temporal_retrieval
        if enable_graph_retrieval is not None:
            body["enable_graph_retrieval"] = enable_graph_retrieval
        if enable_reranking is not None:
            body["enable_reranking"] = enable_reranking
        body.update(options)

        request = CreateBankRequest(**body)
        return await self.banks.create_or_update_bank(
            _bank_id(bank_id), create_bank_request=request, _request_timeout=self._timeout
        )

    def set_mission(self, bank_id: str, mission: str) -> BankProfileResponse:
        """Deprecated: set the mission through :meth:`update_bank_config` instead."""
        return self.create_bank(bank_id, mission=mission)

    async def aset_mission(self, bank_id: str, mission: str) -> BankProfileResponse:
        """Awaitable :meth:`set_mission`."""
        return await self.acreate_bank(bank_id, mission=mission)

    def set_reflect_mission(self, bank_id: str, reflect_mission: str) -> BankProfileResponse:
        """Deprecated alias for :meth:`set_mission`."""
        return self.set_mission(bank_id, reflect_mission)

    async def aset_reflect_mission(self, bank_id: str, reflect_mission: str) -> BankProfileResponse:
        """Awaitable :meth:`set_reflect_mission`."""
        return await self.aset_mission(bank_id, reflect_mission)

    def delete_bank(self, bank_id: str) -> Any:
        """Delete a bank and everything it holds."""
        return _run_async(self.adelete_bank(bank_id))

    async def adelete_bank(self, bank_id: str) -> Any:
        """Awaitable :meth:`delete_bank`."""
        return await self.banks.delete_bank(_bank_id(bank_id), _request_timeout=self._timeout)

    def get_bank_config(self, bank_id: str) -> Dict[str, Any]:
        """Read a bank's resolved configuration.

        Returns a plain dict carrying ``bank_id``, ``config`` (fully resolved) and
        ``overrides`` (bank-level only). Always available: the server's
        enable-config-API switch gates only the writes.
        """
        return _run_async(self.aget_bank_config(bank_id))

    async def aget_bank_config(self, bank_id: str) -> Dict[str, Any]:
        """Awaitable :meth:`get_bank_config`."""
        response = await self.banks.get_bank_config(_bank_id(bank_id), _request_timeout=self._timeout)
        return response.to_dict()

    def update_bank_config(
        self,
        bank_id: str,
        *,
        reflect_mission: Optional[str] = None,
        reflect_source_facts_max_tokens: Optional[int] = None,
        retain_mission: Optional[str] = None,
        retain_extraction_mode: Optional[str] = None,
        retain_custom_instructions: Optional[str] = None,
        retain_chunk_size: Optional[int] = None,
        retain_structured_chunk_size: Optional[int] = None,
        retain_max_attachments_per_chunk: Optional[int] = None,
        retain_default_strategy: Optional[str] = None,
        retain_strategies: Optional[Dict[str, Any]] = None,
        retain_chunk_batch_size: Optional[int] = None,
        store_document_text: Optional[bool] = None,
        entity_labels: Optional[List[Dict[str, Any]]] = None,
        entities_allow_free_form: Optional[bool] = None,
        enable_observations: Optional[bool] = None,
        observations_mission: Optional[str] = None,
        max_observations_per_scope: Optional[int] = None,
        observation_scope_limits: Optional[List[Dict[str, Any]]] = None,
        consolidation_strategies: Optional[List[Dict[str, Any]]] = None,
        enable_auto_consolidation: Optional[bool] = None,
        consolidation_llm_parallelism: Optional[int] = None,
        consolidation_max_memories_per_round: Optional[int] = None,
        mental_model_min_refresh_interval_seconds: Optional[int] = None,
        knowledge_page_default_trigger: Optional[Dict[str, Any]] = None,
        reflect_default_options: Optional[Dict[str, Any]] = None,
        enable_text_search: Optional[bool] = None,
        enable_temporal_retrieval: Optional[bool] = None,
        enable_graph_retrieval: Optional[bool] = None,
        enable_reranking: Optional[bool] = None,
        consolidation_llm_batch_size: Optional[int] = None,
        consolidation_source_facts_max_tokens: Optional[int] = None,
        consolidation_source_facts_max_tokens_per_observation: Optional[int] = None,
        recall_max_tokens: Optional[int] = None,
        recall_include_chunks: Optional[bool] = None,
        recall_chunks_max_tokens: Optional[int] = None,
        recall_budget_function: Optional[str] = None,
        recall_budget_fixed_low: Optional[int] = None,
        recall_budget_fixed_mid: Optional[int] = None,
        recall_budget_fixed_high: Optional[int] = None,
        recall_budget_adaptive_low: Optional[float] = None,
        recall_budget_adaptive_mid: Optional[float] = None,
        recall_budget_adaptive_high: Optional[float] = None,
        recall_budget_min: Optional[int] = None,
        recall_budget_max: Optional[int] = None,
        disposition_skepticism: Optional[int] = None,
        disposition_literalism: Optional[int] = None,
        disposition_empathy: Optional[int] = None,
        mcp_enabled_tools: Optional[List[str]] = None,
        llm_gemini_safety_settings: Optional[List[Dict[str, str]]] = None,
        memory_defense: Optional[Dict[str, Any]] = None,
        audit_log_enabled: Optional[bool] = None,
    ) -> Dict[str, Any]:
        """Update a bank's configuration overrides.

        Only the arguments a caller names are sent — an omitted setting keeps the
        bank's current value rather than being cleared. Returns a plain dict with
        ``bank_id``, ``config`` and ``overrides``.
        """
        return _run_async(
            self.aupdate_bank_config(
                bank_id,
                reflect_mission=reflect_mission,
                reflect_source_facts_max_tokens=reflect_source_facts_max_tokens,
                retain_mission=retain_mission,
                retain_extraction_mode=retain_extraction_mode,
                retain_custom_instructions=retain_custom_instructions,
                retain_chunk_size=retain_chunk_size,
                retain_structured_chunk_size=retain_structured_chunk_size,
                retain_max_attachments_per_chunk=retain_max_attachments_per_chunk,
                retain_default_strategy=retain_default_strategy,
                retain_strategies=retain_strategies,
                retain_chunk_batch_size=retain_chunk_batch_size,
                store_document_text=store_document_text,
                entity_labels=entity_labels,
                entities_allow_free_form=entities_allow_free_form,
                enable_observations=enable_observations,
                observations_mission=observations_mission,
                max_observations_per_scope=max_observations_per_scope,
                observation_scope_limits=observation_scope_limits,
                consolidation_strategies=consolidation_strategies,
                enable_auto_consolidation=enable_auto_consolidation,
                consolidation_llm_parallelism=consolidation_llm_parallelism,
                consolidation_max_memories_per_round=consolidation_max_memories_per_round,
                mental_model_min_refresh_interval_seconds=mental_model_min_refresh_interval_seconds,
                knowledge_page_default_trigger=knowledge_page_default_trigger,
                reflect_default_options=reflect_default_options,
                enable_text_search=enable_text_search,
                enable_temporal_retrieval=enable_temporal_retrieval,
                enable_graph_retrieval=enable_graph_retrieval,
                enable_reranking=enable_reranking,
                consolidation_llm_batch_size=consolidation_llm_batch_size,
                consolidation_source_facts_max_tokens=consolidation_source_facts_max_tokens,
                consolidation_source_facts_max_tokens_per_observation=consolidation_source_facts_max_tokens_per_observation,
                recall_max_tokens=recall_max_tokens,
                recall_include_chunks=recall_include_chunks,
                recall_chunks_max_tokens=recall_chunks_max_tokens,
                recall_budget_function=recall_budget_function,
                recall_budget_fixed_low=recall_budget_fixed_low,
                recall_budget_fixed_mid=recall_budget_fixed_mid,
                recall_budget_fixed_high=recall_budget_fixed_high,
                recall_budget_adaptive_low=recall_budget_adaptive_low,
                recall_budget_adaptive_mid=recall_budget_adaptive_mid,
                recall_budget_adaptive_high=recall_budget_adaptive_high,
                recall_budget_min=recall_budget_min,
                recall_budget_max=recall_budget_max,
                disposition_skepticism=disposition_skepticism,
                disposition_literalism=disposition_literalism,
                disposition_empathy=disposition_empathy,
                mcp_enabled_tools=mcp_enabled_tools,
                llm_gemini_safety_settings=llm_gemini_safety_settings,
                memory_defense=memory_defense,
                audit_log_enabled=audit_log_enabled,
            )
        )

    async def aupdate_bank_config(
        self,
        bank_id: str,
        *,
        reflect_mission: Optional[str] = None,
        reflect_source_facts_max_tokens: Optional[int] = None,
        retain_mission: Optional[str] = None,
        retain_extraction_mode: Optional[str] = None,
        retain_custom_instructions: Optional[str] = None,
        retain_chunk_size: Optional[int] = None,
        retain_structured_chunk_size: Optional[int] = None,
        retain_max_attachments_per_chunk: Optional[int] = None,
        retain_default_strategy: Optional[str] = None,
        retain_strategies: Optional[Dict[str, Any]] = None,
        retain_chunk_batch_size: Optional[int] = None,
        store_document_text: Optional[bool] = None,
        entity_labels: Optional[List[Dict[str, Any]]] = None,
        entities_allow_free_form: Optional[bool] = None,
        enable_observations: Optional[bool] = None,
        observations_mission: Optional[str] = None,
        max_observations_per_scope: Optional[int] = None,
        observation_scope_limits: Optional[List[Dict[str, Any]]] = None,
        consolidation_strategies: Optional[List[Dict[str, Any]]] = None,
        enable_auto_consolidation: Optional[bool] = None,
        consolidation_llm_parallelism: Optional[int] = None,
        consolidation_max_memories_per_round: Optional[int] = None,
        mental_model_min_refresh_interval_seconds: Optional[int] = None,
        knowledge_page_default_trigger: Optional[Dict[str, Any]] = None,
        reflect_default_options: Optional[Dict[str, Any]] = None,
        enable_text_search: Optional[bool] = None,
        enable_temporal_retrieval: Optional[bool] = None,
        enable_graph_retrieval: Optional[bool] = None,
        enable_reranking: Optional[bool] = None,
        consolidation_llm_batch_size: Optional[int] = None,
        consolidation_source_facts_max_tokens: Optional[int] = None,
        consolidation_source_facts_max_tokens_per_observation: Optional[int] = None,
        recall_max_tokens: Optional[int] = None,
        recall_include_chunks: Optional[bool] = None,
        recall_chunks_max_tokens: Optional[int] = None,
        recall_budget_function: Optional[str] = None,
        recall_budget_fixed_low: Optional[int] = None,
        recall_budget_fixed_mid: Optional[int] = None,
        recall_budget_fixed_high: Optional[int] = None,
        recall_budget_adaptive_low: Optional[float] = None,
        recall_budget_adaptive_mid: Optional[float] = None,
        recall_budget_adaptive_high: Optional[float] = None,
        recall_budget_min: Optional[int] = None,
        recall_budget_max: Optional[int] = None,
        disposition_skepticism: Optional[int] = None,
        disposition_literalism: Optional[int] = None,
        disposition_empathy: Optional[int] = None,
        mcp_enabled_tools: Optional[List[str]] = None,
        llm_gemini_safety_settings: Optional[List[Dict[str, str]]] = None,
        memory_defense: Optional[Dict[str, Any]] = None,
        audit_log_enabled: Optional[bool] = None,
    ) -> Dict[str, Any]:
        """Awaitable :meth:`update_bank_config`."""
        updates = {
            key: value
            for key, value in {
                "reflect_mission": reflect_mission,
                "reflect_source_facts_max_tokens": reflect_source_facts_max_tokens,
                "retain_mission": retain_mission,
                "retain_extraction_mode": retain_extraction_mode,
                "retain_custom_instructions": retain_custom_instructions,
                "retain_chunk_size": retain_chunk_size,
                "retain_structured_chunk_size": retain_structured_chunk_size,
                "retain_max_attachments_per_chunk": retain_max_attachments_per_chunk,
                "retain_default_strategy": retain_default_strategy,
                "retain_strategies": retain_strategies,
                "retain_chunk_batch_size": retain_chunk_batch_size,
                "store_document_text": store_document_text,
                "entity_labels": entity_labels,
                "entities_allow_free_form": entities_allow_free_form,
                "enable_observations": enable_observations,
                "observations_mission": observations_mission,
                "max_observations_per_scope": max_observations_per_scope,
                "observation_scope_limits": observation_scope_limits,
                "consolidation_strategies": consolidation_strategies,
                "enable_auto_consolidation": enable_auto_consolidation,
                "consolidation_llm_parallelism": consolidation_llm_parallelism,
                "consolidation_max_memories_per_round": consolidation_max_memories_per_round,
                "mental_model_min_refresh_interval_seconds": mental_model_min_refresh_interval_seconds,
                "knowledge_page_default_trigger": knowledge_page_default_trigger,
                "reflect_default_options": reflect_default_options,
                "enable_text_search": enable_text_search,
                "enable_temporal_retrieval": enable_temporal_retrieval,
                "enable_graph_retrieval": enable_graph_retrieval,
                "enable_reranking": enable_reranking,
                "consolidation_llm_batch_size": consolidation_llm_batch_size,
                "consolidation_source_facts_max_tokens": consolidation_source_facts_max_tokens,
                "consolidation_source_facts_max_tokens_per_observation": consolidation_source_facts_max_tokens_per_observation,
                "recall_max_tokens": recall_max_tokens,
                "recall_include_chunks": recall_include_chunks,
                "recall_chunks_max_tokens": recall_chunks_max_tokens,
                "recall_budget_function": recall_budget_function,
                "recall_budget_fixed_low": recall_budget_fixed_low,
                "recall_budget_fixed_mid": recall_budget_fixed_mid,
                "recall_budget_fixed_high": recall_budget_fixed_high,
                "recall_budget_adaptive_low": recall_budget_adaptive_low,
                "recall_budget_adaptive_mid": recall_budget_adaptive_mid,
                "recall_budget_adaptive_high": recall_budget_adaptive_high,
                "recall_budget_min": recall_budget_min,
                "recall_budget_max": recall_budget_max,
                "disposition_skepticism": disposition_skepticism,
                "disposition_literalism": disposition_literalism,
                "disposition_empathy": disposition_empathy,
                "mcp_enabled_tools": mcp_enabled_tools,
                "llm_gemini_safety_settings": llm_gemini_safety_settings,
                "memory_defense": memory_defense,
                "audit_log_enabled": audit_log_enabled,
            }.items()
            if value is not None
        }
        response = await self.banks.update_bank_config(
            _bank_id(bank_id),
            bank_config_update=BankConfigUpdate(updates=updates),
            _request_timeout=self._timeout,
        )
        return response.to_dict()

    def reset_bank_config(self, bank_id: str) -> Dict[str, Any]:
        """Drop every bank-level configuration override.

        Returns a plain dict with ``bank_id``, ``config`` and an empty ``overrides``.
        """
        return _run_async(self.areset_bank_config(bank_id))

    async def areset_bank_config(self, bank_id: str) -> Dict[str, Any]:
        """Awaitable :meth:`reset_bank_config`."""
        response = await self.banks.reset_bank_config(_bank_id(bank_id), _request_timeout=self._timeout)
        return response.to_dict()

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

    # -- version ------------------------------------------------------------

    def get_version(self) -> VersionResponse:
        """Read the connected server's version and feature flags."""
        return _run_async(self.aget_version())

    async def aget_version(self) -> VersionResponse:
        """Awaitable :meth:`get_version`."""
        return await self.monitoring.get_version(_request_timeout=self._timeout)

    # -- memories -----------------------------------------------------------

    def list_memories(
        self,
        bank_id: str,
        type: Optional[str] = None,
        search_query: Optional[str] = None,
        entity_id: Optional[str] = None,
        time_field: Optional[str] = None,
        start_date: Optional[str] = None,
        end_date: Optional[str] = None,
        limit: int = 100,
        offset: int = 0,
        tags: Optional[List[str]] = None,
        tags_match: Optional[str] = None,
        document_id: Optional[str] = None,
        state: Optional[str] = None,
        consolidation_state: Optional[str] = None,
    ) -> ListMemoryUnitsResponse:
        """List a bank's memory units, paginated and optionally filtered."""
        return _run_async(
            self.alist_memories(
                bank_id=bank_id,
                type=type,
                search_query=search_query,
                entity_id=entity_id,
                time_field=time_field,
                start_date=start_date,
                end_date=end_date,
                limit=limit,
                offset=offset,
                tags=tags,
                tags_match=tags_match,
                document_id=document_id,
                state=state,
                consolidation_state=consolidation_state,
            )
        )

    async def alist_memories(
        self,
        bank_id: str,
        type: Optional[str] = None,
        search_query: Optional[str] = None,
        entity_id: Optional[str] = None,
        time_field: Optional[str] = None,
        start_date: Optional[str] = None,
        end_date: Optional[str] = None,
        limit: int = 100,
        offset: int = 0,
        tags: Optional[List[str]] = None,
        tags_match: Optional[str] = None,
        document_id: Optional[str] = None,
        state: Optional[str] = None,
        consolidation_state: Optional[str] = None,
    ) -> ListMemoryUnitsResponse:
        """Awaitable :meth:`list_memories`.

        ``search_query`` becomes the ``q`` query parameter.

        ``time_field`` / ``start_date`` / ``end_date`` are one window: the named
        axis (``created_at``, ``updated_at``, ``mentioned_at``, ``occurred_start``,
        ``occurred_end``) both filters and orders the results over the half-open
        range ``[start_date, end_date)`` given as ISO-8601 strings. Memories with
        no value on that axis are excluded, so ``total`` counts the window rather
        than the bank. Leaving all three unset keeps the endpoint's own ordering
        and drops nothing.

        ``entity_id`` keeps only units linked to that entity. ``document_id``
        restricts to one retained document. ``state`` selects valid or invalidated
        facts; ``consolidation_state`` selects failed, pending or done
        consolidation. Omitted filters preserve the server defaults.
        """
        return await self.memory.list_memories(
            bank_id=_bank_id(bank_id),
            type=type,
            q=search_query,
            entity_id=entity_id,
            time_field=time_field,
            start_date=start_date,
            end_date=end_date,
            limit=limit,
            offset=offset,
            tags=tags,
            tags_match=tags_match,
            document_id=document_id,
            state=state,
            consolidation_state=consolidation_state,
            _request_timeout=self._timeout,
        )

    # -- mental models ------------------------------------------------------

    def create_mental_model(
        self,
        bank_id: str,
        name: str,
        source_query: str,
        tags: Optional[List[str]] = None,
        max_tokens: Optional[int] = None,
        trigger: Optional[Dict[str, Any]] = None,
        id: Optional[str] = None,
    ) -> Any:
        """Create a mental model; returns the operation id tracking its first build."""
        return _run_async(
            self.acreate_mental_model(
                bank_id=bank_id,
                name=name,
                source_query=source_query,
                tags=tags,
                max_tokens=max_tokens,
                trigger=trigger,
                id=id,
            )
        )

    async def acreate_mental_model(
        self,
        bank_id: str,
        name: str,
        source_query: str,
        tags: Optional[List[str]] = None,
        max_tokens: Optional[int] = None,
        trigger: Optional[Dict[str, Any]] = None,
        id: Optional[str] = None,
    ) -> Any:
        """Awaitable :meth:`create_mental_model`.

        A supplied ``trigger`` is a patch over the server's defaults: only the
        settings named are sent, so the rest keep their default values.
        """
        trigger_obj = _trigger_input(trigger) if trigger else None
        request = CreateMentalModelRequest(
            id=id,
            name=name,
            source_query=source_query,
            tags=tags,
            max_tokens=max_tokens,
            trigger=trigger_obj,
        )
        return await self.mental_models.create_mental_model(
            _bank_id(bank_id), create_mental_model_request=request, _request_timeout=self._timeout
        )

    def list_mental_models(
        self,
        bank_id: str,
        tags: Optional[List[str]] = None,
        tags_match: Optional[str] = None,
        detail: Optional[str] = None,
        limit: Optional[int] = None,
        offset: Optional[int] = None,
    ) -> Any:
        """List a bank's mental models."""
        return _run_async(
            self.alist_mental_models(
                bank_id=bank_id, tags=tags, tags_match=tags_match, detail=detail, limit=limit, offset=offset
            )
        )

    async def alist_mental_models(
        self,
        bank_id: str,
        tags: Optional[List[str]] = None,
        tags_match: Optional[str] = None,
        detail: Optional[str] = None,
        limit: Optional[int] = None,
        offset: Optional[int] = None,
    ) -> Any:
        """Awaitable :meth:`list_mental_models`.

        ``detail`` picks how much comes back: ``metadata`` (names, tags,
        staleness — the server default), ``content`` (adds content and config), or
        ``full`` (adds the large provenance chains).
        """
        return await self.mental_models.list_mental_models(
            _bank_id(bank_id),
            tags=tags,
            tags_match=tags_match,
            detail=detail,
            limit=limit,
            offset=offset,
            _request_timeout=self._timeout,
        )

    def get_mental_model(
        self,
        bank_id: str,
        mental_model_id: str,
        detail: Optional[str] = None,
    ) -> Any:
        """Read one mental model."""
        return _run_async(self.aget_mental_model(bank_id=bank_id, mental_model_id=mental_model_id, detail=detail))

    async def aget_mental_model(
        self,
        bank_id: str,
        mental_model_id: str,
        detail: Optional[str] = None,
    ) -> Any:
        """Awaitable :meth:`get_mental_model`.

        ``detail`` defaults to ``full`` server-side; pass a lighter level to avoid
        pulling provenance chains you do not need.
        """
        return await self.mental_models.get_mental_model(
            _bank_id(bank_id), mental_model_id, detail=detail, _request_timeout=self._timeout
        )

    def refresh_mental_model(self, bank_id: str, mental_model_id: str) -> Any:
        """Refresh a mental model; returns the operation id tracking the refresh."""
        return _run_async(self.arefresh_mental_model(bank_id=bank_id, mental_model_id=mental_model_id))

    async def arefresh_mental_model(self, bank_id: str, mental_model_id: str) -> Any:
        """Awaitable :meth:`refresh_mental_model`."""
        return await self.mental_models.refresh_mental_model(
            _bank_id(bank_id), mental_model_id, _request_timeout=self._timeout
        )

    def dry_run_refresh_mental_model(self, bank_id: str, mental_model_id: str) -> Any:
        """Preview a refresh without changing anything."""
        return _run_async(self.adry_run_refresh_mental_model(bank_id=bank_id, mental_model_id=mental_model_id))

    async def adry_run_refresh_mental_model(self, bank_id: str, mental_model_id: str) -> Any:
        """Awaitable :meth:`dry_run_refresh_mental_model`.

        The production refresh pipeline with the two writes skipped, so it reports
        what the next refresh will do. Not configurable, and costs the same model
        tokens as a real refresh.
        """
        return await self.mental_models.dry_run_refresh_mental_model(
            _bank_id(bank_id), mental_model_id, _request_timeout=self._timeout
        )

    def clear_mental_model(self, bank_id: str, mental_model_id: str) -> Any:
        """Clear a mental model's content so the next refresh re-synthesises it."""
        return _run_async(self.aclear_mental_model(bank_id=bank_id, mental_model_id=mental_model_id))

    async def aclear_mental_model(self, bank_id: str, mental_model_id: str) -> Any:
        """Awaitable :meth:`clear_mental_model`."""
        return await self.mental_models.clear_mental_model(
            _bank_id(bank_id), mental_model_id, _request_timeout=self._timeout
        )

    def update_mental_model(
        self,
        bank_id: str,
        mental_model_id: str,
        name: Optional[str] = None,
        source_query: Optional[str] = None,
        tags: Optional[List[str]] = None,
        max_tokens: Optional[int] = None,
        trigger: Optional[Dict[str, Any]] = None,
    ) -> Any:
        """Update a mental model's metadata."""
        return _run_async(
            self.aupdate_mental_model(
                bank_id=bank_id,
                mental_model_id=mental_model_id,
                name=name,
                source_query=source_query,
                tags=tags,
                max_tokens=max_tokens,
                trigger=trigger,
            )
        )

    async def aupdate_mental_model(
        self,
        bank_id: str,
        mental_model_id: str,
        name: Optional[str] = None,
        source_query: Optional[str] = None,
        tags: Optional[List[str]] = None,
        max_tokens: Optional[int] = None,
        trigger: Optional[Dict[str, Any]] = None,
    ) -> Any:
        """Awaitable :meth:`update_mental_model`.

        A supplied ``trigger`` is applied as a patch: the keys named are updated
        and the model's current trigger keeps the rest.
        """
        trigger_obj = _trigger_input(trigger) if trigger else None
        request = UpdateMentalModelRequest(
            name=name,
            source_query=source_query,
            tags=tags,
            max_tokens=max_tokens,
            trigger=trigger_obj,
        )
        return await self.mental_models.update_mental_model(
            _bank_id(bank_id), mental_model_id, update_mental_model_request=request, _request_timeout=self._timeout
        )

    def delete_mental_model(self, bank_id: str, mental_model_id: str) -> Any:
        """Delete a mental model."""
        return _run_async(self.adelete_mental_model(bank_id=bank_id, mental_model_id=mental_model_id))

    async def adelete_mental_model(self, bank_id: str, mental_model_id: str) -> Any:
        """Awaitable :meth:`delete_mental_model`."""
        return await self.mental_models.delete_mental_model(
            _bank_id(bank_id), mental_model_id, _request_timeout=self._timeout
        )

    def get_mental_model_history(self, bank_id: str, mental_model_id: str) -> Any:
        """Read a mental model's content change history, most recent first."""
        return _run_async(self.aget_mental_model_history(bank_id=bank_id, mental_model_id=mental_model_id))

    async def aget_mental_model_history(self, bank_id: str, mental_model_id: str) -> Any:
        """Awaitable :meth:`get_mental_model_history`."""
        return await self.mental_models.get_mental_model_history(
            _bank_id(bank_id), mental_model_id, _request_timeout=self._timeout
        )

    # -- knowledge base -----------------------------------------------------

    def get_knowledge_base_tree(
        self,
        bank_id: str,
        tags: Optional[List[str]] = None,
        tags_match: Optional[str] = None,
        tag_groups: Optional[List[Dict[str, Any]]] = None,
    ) -> Any:
        """Read the knowledge base as a nested folder/page tree (no page bodies)."""
        return _run_async(
            self.aget_knowledge_base_tree(bank_id=bank_id, tags=tags, tags_match=tags_match, tag_groups=tag_groups)
        )

    async def aget_knowledge_base_tree(
        self,
        bank_id: str,
        tags: Optional[List[str]] = None,
        tags_match: Optional[str] = None,
        tag_groups: Optional[List[Dict[str, Any]]] = None,
    ) -> Any:
        """Awaitable :meth:`get_knowledge_base_tree`.

        ``tag_groups`` is sent as one JSON-encoded query parameter: the endpoint
        is a GET and a boolean filter tree has no flat form.
        """
        return await self.knowledge_base.get_knowledge_base_tree(
            _bank_id(bank_id),
            tags=tags,
            tags_match=tags_match,
            tag_groups=json.dumps(tag_groups) if tag_groups else None,
            _request_timeout=self._timeout,
        )

    def create_knowledge_folder(self, bank_id: str, name: str, parent_id: Optional[str] = None) -> Any:
        """Create a knowledge-base folder."""
        return _run_async(self.acreate_knowledge_folder(bank_id=bank_id, name=name, parent_id=parent_id))

    async def acreate_knowledge_folder(self, bank_id: str, name: str, parent_id: Optional[str] = None) -> Any:
        """Awaitable :meth:`create_knowledge_folder`; ``parent_id=None`` is the root."""
        request = CreateFolderRequest(name=name, parent_id=parent_id)
        return await self.knowledge_base.create_knowledge_folder(
            _bank_id(bank_id), create_folder_request=request, _request_timeout=self._timeout
        )

    def create_knowledge_page(
        self,
        bank_id: str,
        name: str,
        source_query: str,
        parent_id: Optional[str] = None,
        tags: Optional[List[str]] = None,
        max_tokens: Optional[int] = None,
        trigger: Optional[Dict[str, Any]] = None,
    ) -> Any:
        """Create a knowledge-base page; content is built asynchronously."""
        return _run_async(
            self.acreate_knowledge_page(
                bank_id=bank_id,
                name=name,
                source_query=source_query,
                parent_id=parent_id,
                tags=tags,
                max_tokens=max_tokens,
                trigger=trigger,
            )
        )

    async def acreate_knowledge_page(
        self,
        bank_id: str,
        name: str,
        source_query: str,
        parent_id: Optional[str] = None,
        tags: Optional[List[str]] = None,
        max_tokens: Optional[int] = None,
        trigger: Optional[Dict[str, Any]] = None,
    ) -> Any:
        """Awaitable :meth:`create_knowledge_page`; poll the returned operation id.

        A supplied ``trigger`` patches the page defaults, so only the settings
        named are sent and the rest keep their defaults.
        """
        trigger_obj = _trigger_input(trigger) if trigger else None
        request = CreatePageRequest(
            name=name,
            source_query=source_query,
            parent_id=parent_id,
            tags=tags,
            max_tokens=max_tokens,
            trigger=trigger_obj,
        )
        return await self.knowledge_base.create_knowledge_page(
            _bank_id(bank_id), create_page_request=request, _request_timeout=self._timeout
        )

    def get_knowledge_page(self, bank_id: str, page_id: str) -> Any:
        """Read a knowledge page rendered as a markdown document."""
        return _run_async(self.aget_knowledge_page(bank_id=bank_id, page_id=page_id))

    async def aget_knowledge_page(self, bank_id: str, page_id: str) -> Any:
        """Awaitable :meth:`get_knowledge_page`."""
        return await self.knowledge_base.get_knowledge_page(
            _bank_id(bank_id), page_id, _request_timeout=self._timeout
        )

    def search_knowledge_base(
        self,
        bank_id: str,
        q: str,
        limit: Optional[int] = None,
        tags: Optional[List[str]] = None,
        tags_match: Optional[str] = None,
        tag_groups: Optional[List[Dict[str, Any]]] = None,
    ) -> Any:
        """Hybrid search over knowledge pages."""
        return _run_async(
            self.asearch_knowledge_base(
                bank_id=bank_id, q=q, limit=limit, tags=tags, tags_match=tags_match, tag_groups=tag_groups
            )
        )

    async def asearch_knowledge_base(
        self,
        bank_id: str,
        q: str,
        limit: Optional[int] = None,
        tags: Optional[List[str]] = None,
        tags_match: Optional[str] = None,
        tag_groups: Optional[List[Dict[str, Any]]] = None,
    ) -> Any:
        """Awaitable :meth:`search_knowledge_base`; ``limit`` defaults to 10 server-side."""
        return await self.knowledge_base.search_knowledge_base(
            _bank_id(bank_id),
            q,
            limit=limit,
            tags=tags,
            tags_match=tags_match,
            tag_groups=json.dumps(tag_groups) if tag_groups else None,
            _request_timeout=self._timeout,
        )

    def update_knowledge_node(
        self,
        bank_id: str,
        node_id: str,
        name: Optional[str] = None,
        parent_id: Any = _UNSET,
        source_query: Optional[str] = None,
        tags: Optional[List[str]] = None,
        max_tokens: Optional[int] = None,
        trigger: Optional[Dict[str, Any]] = None,
    ) -> Any:
        """Rename/move a knowledge node and/or update a page's options."""
        return _run_async(
            self.aupdate_knowledge_node(
                bank_id=bank_id,
                node_id=node_id,
                name=name,
                parent_id=parent_id,
                source_query=source_query,
                tags=tags,
                max_tokens=max_tokens,
                trigger=trigger,
            )
        )

    async def aupdate_knowledge_node(
        self,
        bank_id: str,
        node_id: str,
        name: Optional[str] = None,
        parent_id: Any = _UNSET,
        source_query: Optional[str] = None,
        tags: Optional[List[str]] = None,
        max_tokens: Optional[int] = None,
        trigger: Optional[Dict[str, Any]] = None,
    ) -> Any:
        """Awaitable :meth:`update_knowledge_node`.

        Only the fields the caller passes are applied. ``parent_id`` is sent only
        when provided, so passing ``None`` explicitly moves the node to the root
        while omitting it leaves the parent alone. A supplied ``trigger`` is a
        patch over the page's current refresh settings.
        """
        fields: Dict[str, Any] = {}
        if name is not None:
            fields["name"] = name
        if parent_id is not _UNSET:
            fields["parent_id"] = parent_id
        if source_query is not None:
            fields["source_query"] = source_query
        if tags is not None:
            fields["tags"] = tags
        if max_tokens is not None:
            fields["max_tokens"] = max_tokens
        if trigger is not None:
            fields["trigger"] = _trigger_input(trigger)
        request = UpdateNodeRequest(**fields)
        return await self.knowledge_base.update_knowledge_node(
            _bank_id(bank_id), node_id, update_node_request=request, _request_timeout=self._timeout
        )

    def delete_knowledge_node(self, bank_id: str, node_id: str) -> Any:
        """Delete a knowledge folder or page and its whole subtree."""
        return _run_async(self.adelete_knowledge_node(bank_id=bank_id, node_id=node_id))

    async def adelete_knowledge_node(self, bank_id: str, node_id: str) -> Any:
        """Awaitable :meth:`delete_knowledge_node`."""
        return await self.knowledge_base.delete_knowledge_node(
            _bank_id(bank_id), node_id, _request_timeout=self._timeout
        )

    def export_knowledge_base(self, bank_id: str) -> Any:
        """Export the knowledge base as a portable markdown bundle."""
        return _run_async(self.aexport_knowledge_base(bank_id=bank_id))

    async def aexport_knowledge_base(self, bank_id: str) -> Any:
        """Awaitable :meth:`export_knowledge_base`."""
        return await self.knowledge_base.export_knowledge_base(_bank_id(bank_id), _request_timeout=self._timeout)

    # -- directives ---------------------------------------------------------

    def create_directive(
        self,
        bank_id: str,
        name: str,
        content: str,
        priority: int = 0,
        is_active: bool = True,
        tags: Optional[List[str]] = None,
    ) -> Any:
        """Create a directive."""
        return _run_async(
            self.acreate_directive(
                bank_id=bank_id, name=name, content=content, priority=priority, is_active=is_active, tags=tags
            )
        )

    async def acreate_directive(
        self,
        bank_id: str,
        name: str,
        content: str,
        priority: int = 0,
        is_active: bool = True,
        tags: Optional[List[str]] = None,
    ) -> Any:
        """Awaitable :meth:`create_directive`; a higher ``priority`` is injected first."""
        request = CreateDirectiveRequest(
            name=name, content=content, priority=priority, is_active=is_active, tags=tags
        )
        return await self.directives.create_directive(
            _bank_id(bank_id), create_directive_request=request, _request_timeout=self._timeout
        )

    def list_directives(
        self,
        bank_id: str,
        tags: Optional[List[str]] = None,
        limit: Optional[int] = None,
        offset: Optional[int] = None,
    ) -> Any:
        """List a bank's directives."""
        return _run_async(self.alist_directives(bank_id=bank_id, tags=tags, limit=limit, offset=offset))

    async def alist_directives(
        self,
        bank_id: str,
        tags: Optional[List[str]] = None,
        limit: Optional[int] = None,
        offset: Optional[int] = None,
    ) -> Any:
        """Awaitable :meth:`list_directives`."""
        return await self.directives.list_directives(
            _bank_id(bank_id), tags=tags, limit=limit, offset=offset, _request_timeout=self._timeout
        )

    def get_directive(self, bank_id: str, directive_id: str) -> Any:
        """Read one directive."""
        return _run_async(self.aget_directive(bank_id=bank_id, directive_id=directive_id))

    async def aget_directive(self, bank_id: str, directive_id: str) -> Any:
        """Awaitable :meth:`get_directive`."""
        return await self.directives.get_directive(
            _bank_id(bank_id), directive_id, _request_timeout=self._timeout
        )

    def update_directive(
        self,
        bank_id: str,
        directive_id: str,
        name: Optional[str] = None,
        content: Optional[str] = None,
        priority: Optional[int] = None,
        is_active: Optional[bool] = None,
        tags: Optional[List[str]] = None,
    ) -> Any:
        """Update a directive."""
        return _run_async(
            self.aupdate_directive(
                bank_id=bank_id,
                directive_id=directive_id,
                name=name,
                content=content,
                priority=priority,
                is_active=is_active,
                tags=tags,
            )
        )

    async def aupdate_directive(
        self,
        bank_id: str,
        directive_id: str,
        name: Optional[str] = None,
        content: Optional[str] = None,
        priority: Optional[int] = None,
        is_active: Optional[bool] = None,
        tags: Optional[List[str]] = None,
    ) -> Any:
        """Awaitable :meth:`update_directive`."""
        request = UpdateDirectiveRequest(
            name=name, content=content, priority=priority, is_active=is_active, tags=tags
        )
        return await self.directives.update_directive(
            _bank_id(bank_id), directive_id, update_directive_request=request, _request_timeout=self._timeout
        )

    def delete_directive(self, bank_id: str, directive_id: str) -> Any:
        """Delete a directive."""
        return _run_async(self.adelete_directive(bank_id=bank_id, directive_id=directive_id))

    async def adelete_directive(self, bank_id: str, directive_id: str) -> Any:
        """Awaitable :meth:`delete_directive`."""
        return await self.directives.delete_directive(
            _bank_id(bank_id), directive_id, _request_timeout=self._timeout
        )

    # -- transfer -----------------------------------------------------------

    def export_documents(
        self,
        bank_id: str,
        document_ids: Optional[List[str]] = None,
        include_observations: bool = False,
        include_knowledge_base: bool = False,
        *,
        poll_interval: float = 2.0,
        timeout: float = 300.0,
    ) -> bytes:
        """Export a bank's documents as a transfer ZIP archive.

        Submits the export, polls the operation to completion, downloads the
        archive and returns its bytes. ``include_observations`` and
        ``include_knowledge_base`` are whole-bank only.
        """
        return _run_async(
            self.aexport_documents(
                bank_id,
                document_ids,
                include_observations,
                include_knowledge_base,
                poll_interval=poll_interval,
                timeout=timeout,
            )
        )

    async def aexport_documents(
        self,
        bank_id: str,
        document_ids: Optional[List[str]] = None,
        include_observations: bool = False,
        include_knowledge_base: bool = False,
        *,
        poll_interval: float = 2.0,
        timeout: float = 300.0,
    ) -> bytes:
        """Awaitable :meth:`export_documents`.

        Raises ``TimeoutError`` if the export does not finish within ``timeout``,
        and ``RuntimeError`` if it fails or completes without an archive.
        """
        submission = await self.document_transfer.export_documents(
            _bank_id(bank_id),
            document_id=document_ids,
            include_observations=include_observations,
            include_knowledge_base=include_knowledge_base,
            _request_timeout=self._timeout,
        )
        return await self._download_operation_archive(bank_id, submission.operation_id, poll_interval, timeout)

    def export_bank(
        self,
        bank_id: str,
        *,
        include_data: bool = True,
        include_bank_config: bool = True,
        include_history: bool = False,
        poll_interval: float = 2.0,
        timeout: float = 300.0,
    ) -> bytes:
        """Export a whole bank as a transfer ZIP archive (blocking convenience)."""
        return _run_async(
            self.aexport_bank(
                bank_id,
                include_data=include_data,
                include_bank_config=include_bank_config,
                include_history=include_history,
                poll_interval=poll_interval,
                timeout=timeout,
            )
        )

    async def aexport_bank(
        self,
        bank_id: str,
        *,
        include_data: bool = True,
        include_bank_config: bool = True,
        include_history: bool = False,
        poll_interval: float = 2.0,
        timeout: float = 300.0,
    ) -> bytes:
        """Awaitable :meth:`export_bank`.

        Three flags decide what the archive carries: ``include_data`` the memories
        and everything backing them; ``include_bank_config`` the bank's config,
        mental models, knowledge pages, directives and webhooks; ``include_history``
        the audit and LLM-request logs. Embeddings never travel — the importing
        instance regenerates them with its own model.
        """
        submission = await self.bank_transfer.export_bank_transfer(
            _bank_id(bank_id),
            include_data=include_data,
            include_bank_config=include_bank_config,
            include_history=include_history,
            _request_timeout=self._timeout,
        )
        return await self._download_operation_archive(bank_id, submission.operation_id, poll_interval, timeout)

    def import_bank(
        self,
        bank_id: str,
        archive: bytes,
        *,
        target_bank_id: Optional[str] = None,
        include_data: bool = True,
        include_bank_config: bool = True,
        include_history: bool = False,
    ) -> str:
        """Restore a bank archive into a fresh bank; returns the operation id."""
        return _run_async(
            self.aimport_bank(
                bank_id,
                archive,
                target_bank_id=target_bank_id,
                include_data=include_data,
                include_bank_config=include_bank_config,
                include_history=include_history,
            )
        )

    async def aimport_bank(
        self,
        bank_id: str,
        archive: bytes,
        *,
        target_bank_id: Optional[str] = None,
        include_data: bool = True,
        include_bank_config: bool = True,
        include_history: bool = False,
    ) -> str:
        """Awaitable :meth:`import_bank`.

        The restore runs in the background; poll
        ``client.operations.get_operation_status`` for its progress. ``bank_id``
        is the bank the operation is recorded against (it must exist);
        ``target_bank_id`` is the bank to create and must not already exist.
        """
        submission = await self.bank_transfer.import_bank_transfer(
            _bank_id(bank_id),
            archive,
            target_bank_id=target_bank_id,
            include_data=include_data,
            include_bank_config=include_bank_config,
            include_history=include_history,
            _request_timeout=self._timeout,
        )
        return submission.operation_id

    def clone_bank(
        self,
        bank_id: str,
        target_bank_id: str,
        *,
        include_data: bool = True,
        include_bank_config: bool = True,
        include_history: bool = False,
    ) -> str:
        """Copy a bank into a new one; returns the clone operation's id."""
        return _run_async(
            self.aclone_bank(
                bank_id,
                target_bank_id,
                include_data=include_data,
                include_bank_config=include_bank_config,
                include_history=include_history,
            )
        )

    async def aclone_bank(
        self,
        bank_id: str,
        target_bank_id: str,
        *,
        include_data: bool = True,
        include_bank_config: bool = True,
        include_history: bool = False,
    ) -> str:
        """Awaitable :meth:`clone_bank`.

        Runs server-side as the export and import back to back, so no archive
        travels over the wire and no model is called. ``target_bank_id`` must not
        already exist. Webhooks ride with ``include_bank_config``: a clone made
        with the defaults will call the source's webhook endpoints.
        """
        submission = await self.bank_transfer.clone_bank(
            _bank_id(bank_id),
            target_bank_id,
            include_data=include_data,
            include_bank_config=include_bank_config,
            include_history=include_history,
            _request_timeout=self._timeout,
        )
        return submission.operation_id

    async def _download_operation_archive(
        self,
        bank_id: str,
        operation_id: str,
        poll_interval: float,
        timeout: float,
    ) -> bytes:
        """Poll an export operation to completion, then download its archive.

        Shared by the document and whole-bank exports: both submit an operation
        whose ``result_metadata`` names the finished archive.
        """
        loop = asyncio.get_running_loop()
        deadline = loop.time() + timeout
        timeout_message = f"Export operation {operation_id} did not complete within {timeout}s"
        while True:
            # The deadline bounds the in-flight status request and the sleep too,
            # so a slow poll or a long poll_interval cannot overrun it.
            remaining = deadline - loop.time()
            if remaining <= 0:
                raise TimeoutError(timeout_message)
            try:
                status = await asyncio.wait_for(
                    self.operations.get_operation_status(
                        _bank_id(bank_id), operation_id, _request_timeout=self._timeout
                    ),
                    remaining,
                )
            except asyncio.TimeoutError:
                raise TimeoutError(timeout_message) from None
            if status.status == "completed":
                break
            if status.status in ("failed", "cancelled"):
                raise RuntimeError(f"Export operation {operation_id} {status.status}: {status.error_message}")
            await asyncio.sleep(min(poll_interval, max(deadline - loop.time(), 0)))

        meta = status.result_metadata or {}
        download_url = meta.get("download_url")
        if not download_url:
            raise RuntimeError(f"Export operation {operation_id} completed without a download_url")
        if download_url.lower().startswith(("https://", "http://")):
            # Object stores hand back signed URLs. Their query string must survive
            # untouched, and this client's credentials must not go to the storage
            # host, so the request is made directly rather than through the API
            # client. ``trust_env`` matches the generated client, so a proxy
            # configured through the environment reaches the storage host too.
            async with aiohttp.ClientSession(trust_env=True) as session:
                async with session.get(
                    URL(download_url, encoded=True), timeout=aiohttp.ClientTimeout(total=self._timeout)
                ) as response:
                    response.raise_for_status()
                    return await response.read()
        # A server-relative URL carries the raw storage key, slashes and all.
        # Fetching it through the generated download route would percent-encode
        # those slashes, which fronting proxies often reject; serializing the path
        # directly keeps them. ``auth_settings=[]`` leaves the client's auth on it,
        # as the API expects, while the signed-URL branch above sends none.
        request = self._api_client.param_serialize(
            method="GET",
            resource_path=download_url,
            header_params={"Accept": "application/zip"},
            auth_settings=[],
        )
        response = await self._api_client.call_api(*request, _request_timeout=self._timeout)
        archive = await response.read()
        # Called only for its status check: it raises on a non-2XX download, which
        # would otherwise hand the error body back as the archive.
        self._api_client.response_deserialize(response, response_types_map={"2XX": "bytearray"})
        return bytes(archive)