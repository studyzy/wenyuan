"""WenYuan client for Python.

A thin, pythonic layer over the generated ``wenyuan_client_api`` package::

    from wenyuan_client import WenYuan

    with WenYuan() as client:                 # reads HINDSIGHT_API_URL
        client.retain("my-bank", "the deploy runs on Fridays")
        hits = client.recall("my-bank", "when do we deploy?")
        answer = client.reflect("my-bank", "summarise the deploy policy")

Every operation has an awaitable counterpart (``aretain``, ``arecall``,
``areflect``, ``acreate_bank``, ``alist_banks``) and the generated sub-clients
are reachable as properties: ``client.memory``, ``client.banks``,
``client.documents``, ``client.entities``, ``client.mental_models``,
``client.knowledge_base``, ``client.directives``, ``client.operations``,
``client.webhooks``, ``client.files``, ``client.monitoring``,
``client.document_transfer``, ``client.bank_transfer`` (plus the ``audit``,
``bank_templates`` and ``llm_traces`` areas).
"""

from wenyuan_client_api.models.bank_list_response import BankListResponse
from wenyuan_client_api.models.bank_profile_response import BankProfileResponse
from wenyuan_client_api.models.recall_response import RecallResponse
from wenyuan_client_api.models.recall_result import RecallResult
from wenyuan_client_api.models.reflect_response import ReflectResponse
from wenyuan_client_api.models.retain_response import RetainResponse

from . import recall
from .client import (
    BASE_URL_ENV_VAR,
    DEFAULT_BASE_URL,
    DEFAULT_MAX_ATTEMPTS,
    DEFAULT_USER_AGENT,
    ContentInput,
    WenYuan,
)
from .recall import (
    iter_results,
    result_at,
    result_count,
    to_prompt_string,
)

# Attaching the recall conveniences lives with the wrapper, not the generated
# package, so it happens on import of this package rather than in the models.
recall.install()

__all__ = [
    "WenYuan",
    "BASE_URL_ENV_VAR",
    "DEFAULT_BASE_URL",
    "DEFAULT_MAX_ATTEMPTS",
    "DEFAULT_USER_AGENT",
    "ContentInput",
    "BankListResponse",
    "BankProfileResponse",
    "RecallResponse",
    "RecallResult",
    "ReflectResponse",
    "RetainResponse",
    # Recall conveniences (also installed as methods on RecallResponse).
    "iter_results",
    "result_at",
    "result_count",
    "to_prompt_string",
]

__version__ = "0.1.0"