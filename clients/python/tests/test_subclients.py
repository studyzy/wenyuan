"""Every area the generated package exposes is reachable as a sub-client.

The façade wraps only the handful of operations most callers reach for first;
the rest of the surface is the generated API objects themselves, one property per
area.
"""

from __future__ import annotations

import pathlib
import sys
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))

from wenyuan_client import WenYuan  # noqa: E402
from wenyuan_client_api.api import (  # noqa: E402
    AuditApi,
    BankTemplatesApi,
    BankTransferApi,
    BanksApi,
    DirectivesApi,
    DocumentTransferApi,
    DocumentsApi,
    EntitiesApi,
    FilesApi,
    KnowledgeBaseApi,
    LLMTracesApi,
    MemoryApi,
    MentalModelsApi,
    MonitoringApi,
    OperationsApi,
    WebhooksApi,
)

#: property name -> the generated API class it must hand back.
AREAS = {
    "memory": MemoryApi,
    "banks": BanksApi,
    "documents": DocumentsApi,
    "entities": EntitiesApi,
    "mental_models": MentalModelsApi,
    "knowledge_base": KnowledgeBaseApi,
    "directives": DirectivesApi,
    "operations": OperationsApi,
    "webhooks": WebhooksApi,
    "files": FilesApi,
    "monitoring": MonitoringApi,
    "document_transfer": DocumentTransferApi,
    "bank_transfer": BankTransferApi,
    "audit": AuditApi,
    "bank_templates": BankTemplatesApi,
    "llm_traces": LLMTracesApi,
}


class SubClientTest(unittest.TestCase):
    def setUp(self):
        self.client = WenYuan("http://127.0.0.1:1")
        self.addCleanup(self.client.close)

    def test_every_area_is_exposed(self):
        for name, expected in AREAS.items():
            with self.subTest(area=name):
                sub_client = getattr(self.client, name)
                self.assertIsInstance(sub_client, expected)

    def test_sub_clients_share_the_one_api_client(self):
        self.assertIs(self.client.memory.api_client, self.client.banks.api_client)

    def test_sub_client_identity_is_stable(self):
        self.assertIs(self.client.memory, self.client.memory)


if __name__ == "__main__":
    unittest.main()