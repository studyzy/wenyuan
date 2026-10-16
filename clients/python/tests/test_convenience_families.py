"""The convenience families translate their arguments into the request models.

These are mapping tests, not HTTP tests: each one replaces a generated sub-client
method with a recorder and asserts what the wrapper built. The point is the part
the wrapper owns — the shape of the request body, the patch semantics of a
partial update, and the sync/async pairing — rather than the wire behaviour,
which belongs to the generated client.

Run from ``clients/python``::

    python3 -m unittest discover -s tests -v
"""

from __future__ import annotations

import inspect
import json
import pathlib
import sys
import unittest
from unittest.mock import AsyncMock, MagicMock

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))

from wenyuan_client import WenYuan  # noqa: E402
from wenyuan_client_api.models.features_info import FeaturesInfo  # noqa: E402
from wenyuan_client_api.models.version_response import VersionResponse  # noqa: E402


def _client() -> WenYuan:
    return WenYuan("http://127.0.0.1:1")


def _version_response() -> VersionResponse:
    features = FeaturesInfo(**dict.fromkeys(FeaturesInfo.model_fields, False))
    return VersionResponse(api_version="0.8.2", features=features)


class _RecordingClientTestCase(unittest.TestCase):
    def setUp(self):
        self.client = _client()
        self.addCleanup(self.client.close)
        self.captured: dict = {}

    def _record(self, api: str, method: str) -> None:
        """Replace ``client.<api>.<method>`` with a recorder returning a MagicMock."""

        async def fake(*args, **kwargs):
            self.captured["args"] = args
            self.captured["kwargs"] = kwargs
            return MagicMock()

        setattr(getattr(self.client, api), method, fake)

    def _request(self):
        """The one request model the wrapper passed to the recorded call.

        The wrapper hands the generated client its request by keyword, so the
        model sits in ``kwargs`` next to the timeout; anything else is positional.
        """
        for key, value in self.captured["kwargs"].items():
            if key != "_request_timeout":
                return value
        return self.captured["args"][-1]


# --------------------------------------------------------------------------- #
# version
# --------------------------------------------------------------------------- #


class VersionTest(unittest.TestCase):
    def setUp(self):
        self.client = _client()
        self.addCleanup(self.client.close)

    def test_get_version_delegates_to_monitoring_api(self):
        self.client._monitoring_api.get_version = AsyncMock(return_value=_version_response())

        version = self.client.get_version()

        self.assertEqual(version.api_version, "0.8.2")
        self.client._monitoring_api.get_version.assert_called_once()


class AsyncVersionTest(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.client = _client()

    async def asyncTearDown(self):
        await self.client.aclose()

    async def test_aget_version_delegates_to_monitoring_api(self):
        self.client._monitoring_api.get_version = AsyncMock(return_value=_version_response())

        version = await self.client.aget_version()

        self.assertEqual(version.api_version, "0.8.2")
        self.client._monitoring_api.get_version.assert_awaited_once()


# --------------------------------------------------------------------------- #
# list_memories
# --------------------------------------------------------------------------- #


class ListMemoriesTest(_RecordingClientTestCase):
    def test_forwards_the_time_window(self):
        self._record("_memory_api", "list_memories")

        self.client.list_memories(
            "test-bank",
            time_field="mentioned_at",
            start_date="2024-01-01T00:00:00Z",
            end_date="2024-02-01T00:00:00Z",
        )

        kwargs = self.captured["kwargs"]
        self.assertEqual(kwargs["time_field"], "mentioned_at")
        self.assertEqual(kwargs["start_date"], "2024-01-01T00:00:00Z")
        self.assertEqual(kwargs["end_date"], "2024-02-01T00:00:00Z")

    def test_omits_the_window_by_default(self):
        self._record("_memory_api", "list_memories")

        self.client.list_memories("test-bank")

        kwargs = self.captured["kwargs"]
        # None rather than a default axis: the endpoint keeps its own ordering and
        # drops nothing unless a caller asks for a window.
        self.assertIsNone(kwargs["time_field"])
        self.assertIsNone(kwargs["start_date"])
        self.assertIsNone(kwargs["end_date"])
        self.assertEqual(kwargs["limit"], 100)
        self.assertEqual(kwargs["offset"], 0)

    def test_forwards_the_tag_and_document_filters(self):
        self._record("_memory_api", "list_memories")

        self.client.list_memories(
            "test-bank",
            tags=["user:dan"],
            tags_match="any_strict",
            document_id="doc + revision&2",
            state="invalidated",
            consolidation_state="done",
        )

        kwargs = self.captured["kwargs"]
        self.assertEqual(kwargs["tags"], ["user:dan"])
        self.assertEqual(kwargs["tags_match"], "any_strict")
        self.assertEqual(kwargs["document_id"], "doc + revision&2")
        self.assertEqual(kwargs["state"], "invalidated")
        self.assertEqual(kwargs["consolidation_state"], "done")

    def test_search_query_becomes_the_q_parameter(self):
        self._record("_memory_api", "list_memories")

        self.client.list_memories("test-bank", search_query="deploy policy", type="world", entity_id="ent-1")

        kwargs = self.captured["kwargs"]
        self.assertEqual(kwargs["q"], "deploy policy")
        self.assertEqual(kwargs["type"], "world")
        self.assertEqual(kwargs["entity_id"], "ent-1")

    def test_listing_family_exposes_the_existing_filters(self):
        for method in ("list_memories", "alist_memories"):
            parameters = inspect.signature(getattr(WenYuan, method)).parameters
            with self.subTest(method=method):
                self.assertLessEqual(
                    {"document_id", "state", "consolidation_state", "entity_id", "time_field"},
                    set(parameters),
                )


# --------------------------------------------------------------------------- #
# banks: create, mission, config, delete
# --------------------------------------------------------------------------- #


class CreateBankTest(_RecordingClientTestCase):
    def test_disposition_mapping_expands_into_individual_fields(self):
        self._record("_banks_api", "create_or_update_bank")

        self.client.create_bank(
            "test-bank",
            name="Test",
            disposition={"skepticism": 4, "literalism": 2, "empathy": 5},
        )

        self.assertEqual(self.captured["args"][0], "test-bank")
        body = self.captured["kwargs"]["create_bank_request"].to_dict()
        self.assertEqual(
            body,
            {
                "name": "Test",
                "disposition_skepticism": 4,
                "disposition_literalism": 2,
                "disposition_empathy": 5,
            },
        )

    def test_individual_disposition_fields_win_over_the_mapping(self):
        self._record("_banks_api", "create_or_update_bank")

        self.client.create_bank(
            "test-bank",
            disposition={"skepticism": 1, "literalism": 1, "empathy": 1},
            disposition_skepticism=5,
        )

        body = self.captured["kwargs"]["create_bank_request"].to_dict()
        self.assertEqual(body["disposition_skepticism"], 5)
        self.assertEqual(body["disposition_literalism"], 1)

    def test_unset_fields_stay_out_of_the_body(self):
        self._record("_banks_api", "create_or_update_bank")

        self.client.create_bank("test-bank", retain_extraction_mode="chunks", enable_reranking=False)

        body = self.captured["kwargs"]["create_bank_request"].to_dict()
        self.assertEqual(body["retain_extraction_mode"], "chunks")
        self.assertIs(body["enable_reranking"], False)
        for name in ("name", "mission", "reflect_mission", "background", "disposition", "enable_text_search"):
            self.assertNotIn(name, body)

    def test_set_mission_and_reflect_mission_alias(self):
        self._record("_banks_api", "create_or_update_bank")

        self.client.set_mission("test-bank", "be terse")
        self.assertEqual(self.captured["kwargs"]["create_bank_request"].to_dict(), {"mission": "be terse"})

        self.client.set_reflect_mission("test-bank", "be curious")
        self.assertEqual(self.captured["kwargs"]["create_bank_request"].to_dict(), {"mission": "be curious"})


class BankConfigTest(_RecordingClientTestCase):
    def _record_config(self, method: str, response: dict) -> None:
        async def fake(bank_id, *args, **kwargs):
            self.captured["args"] = args
            self.captured["bank_id"] = bank_id
            return MagicMock(to_dict=lambda: response)

        setattr(self.client._banks_api, method, fake)

    def test_get_bank_config_returns_a_plain_dict(self):
        self._record_config("get_bank_config", {"bank_id": "test-bank", "config": {"a": 1}, "overrides": {}})

        result = self.client.get_bank_config("test-bank")

        self.assertEqual(result, {"bank_id": "test-bank", "config": {"a": 1}, "overrides": {}})
        self.assertEqual(self.captured["bank_id"], "test-bank")

    def test_update_bank_config_sends_only_the_named_settings(self):
        captured: dict = {}

        async def fake(bank_id, bank_config_update, **kwargs):
            captured["bank_id"] = bank_id
            captured["updates"] = bank_config_update.updates
            return MagicMock(to_dict=lambda: {"bank_id": bank_id, "config": {}, "overrides": bank_config_update.updates})

        self.client._banks_api.update_bank_config = fake

        result = self.client.update_bank_config(
            "test-bank", enable_text_search=False, retain_structured_chunk_size=12000
        )

        self.assertEqual(
            captured["updates"], {"enable_text_search": False, "retain_structured_chunk_size": 12000}
        )
        self.assertEqual(result["overrides"], captured["updates"])

    def test_update_bank_config_with_no_settings_sends_an_empty_mapping(self):
        captured: dict = {}

        async def fake(bank_id, bank_config_update, **kwargs):
            captured["updates"] = bank_config_update.updates
            return MagicMock(to_dict=lambda: {"bank_id": bank_id, "config": {}, "overrides": {}})

        self.client._banks_api.update_bank_config = fake

        self.client.update_bank_config("test-bank")

        self.assertEqual(captured["updates"], {})

    def test_update_bank_config_forwards_recall_pipeline_toggles(self):
        captured: dict = {}

        async def fake(bank_id, bank_config_update, **kwargs):
            captured["updates"] = bank_config_update.updates
            return MagicMock(to_dict=lambda: {"bank_id": bank_id, "config": {}, "overrides": {}})

        self.client._banks_api.update_bank_config = fake

        self.client.update_bank_config(
            "test-bank",
            enable_text_search=False,
            enable_temporal_retrieval=False,
            enable_graph_retrieval=False,
            enable_reranking=False,
        )

        self.assertEqual(
            captured["updates"],
            {
                "enable_text_search": False,
                "enable_temporal_retrieval": False,
                "enable_graph_retrieval": False,
                "enable_reranking": False,
            },
        )

    def test_reset_bank_config_delegates(self):
        self._record_config("reset_bank_config", {"bank_id": "test-bank", "config": {}, "overrides": {}})

        result = self.client.reset_bank_config("test-bank")

        self.assertEqual(result["overrides"], {})

    def test_delete_bank_delegates(self):
        self._record("_banks_api", "delete_bank")

        self.client.delete_bank("test-bank")

        self.assertEqual(self.captured["args"][0], "test-bank")


# --------------------------------------------------------------------------- #
# mental models: trigger patch semantics
# --------------------------------------------------------------------------- #

#: Fields whose model default is not None — the ones that leaked into every
#: request before the wrapper blanked them.
DEFAULTED_TRIGGER_FIELDS = {"mode", "refresh_after_consolidation", "exclude_mental_models", "keep_trace"}


class MentalModelTriggerTest(_RecordingClientTestCase):
    def test_update_sends_only_the_named_setting(self):
        self._record("_mental_models_api", "update_mental_model")

        self.client.update_mental_model("bank-1", "mm-1", trigger={"mode": "delta"})

        request = self._request()
        self.assertEqual(request.trigger.to_dict(), {"mode": "delta"})

    def test_update_keeps_an_explicit_false(self):
        self._record("_mental_models_api", "update_mental_model")

        self.client.update_mental_model("bank-1", "mm-1", trigger={"refresh_after_consolidation": False})

        request = self._request()
        self.assertEqual(request.trigger.to_dict(), {"refresh_after_consolidation": False})

    def test_update_clears_a_nullable_field(self):
        self._record("_mental_models_api", "update_mental_model")

        self.client.update_mental_model("bank-1", "mm-1", trigger={"refresh_cron": None, "keep_trace": True})

        request = self._request()
        self.assertEqual(request.trigger.to_dict(), {"refresh_cron": None, "keep_trace": True})

    def test_a_defaulted_field_is_sent_only_when_named(self):
        for field in sorted(DEFAULTED_TRIGGER_FIELDS):
            with self.subTest(field=field):
                self.captured.clear()
                self._record("_mental_models_api", "update_mental_model")
                self.client.update_mental_model("bank-1", "mm-1", trigger={"tags_match": "any"})
                request = self._request()
                self.assertNotIn(field, request.trigger.to_dict())

    def test_no_trigger_sends_no_trigger(self):
        self._record("_mental_models_api", "update_mental_model")

        self.client.update_mental_model("bank-1", "mm-1", name="Renamed")

        request = self._request()
        self.assertIsNone(request.trigger)

    def test_create_sends_only_the_named_settings(self):
        self._record("_mental_models_api", "create_mental_model")

        self.client.create_mental_model(
            "bank-1", name="Housing", source_query="Where?", trigger={"refresh_after_consolidation": True}
        )

        request = self._request()
        self.assertEqual(request.trigger.to_dict(), {"refresh_after_consolidation": True})
        self.assertEqual(request.name, "Housing")
        self.assertEqual(request.source_query, "Where?")

    def test_the_whole_family_delegates(self):
        for sync_name, api_method, args in (
            ("list_mental_models", "list_mental_models", ("bank-1",)),
            ("get_mental_model", "get_mental_model", ("bank-1", "mm-1")),
            ("refresh_mental_model", "refresh_mental_model", ("bank-1", "mm-1")),
            ("dry_run_refresh_mental_model", "dry_run_refresh_mental_model", ("bank-1", "mm-1")),
            ("clear_mental_model", "clear_mental_model", ("bank-1", "mm-1")),
            ("delete_mental_model", "delete_mental_model", ("bank-1", "mm-1")),
            ("get_mental_model_history", "get_mental_model_history", ("bank-1", "mm-1")),
        ):
            with self.subTest(method=sync_name):
                self.captured.clear()
                self._record("_mental_models_api", api_method)
                getattr(self.client, sync_name)(*args)
                self.assertEqual(self.captured["args"][: len(args)], args)


# --------------------------------------------------------------------------- #
# knowledge base
# --------------------------------------------------------------------------- #


class KnowledgeBaseTest(_RecordingClientTestCase):
    def test_create_page_maps_every_option(self):
        self._record("_knowledge_base_api", "create_knowledge_page")

        self.client.create_knowledge_page(
            "bank-1",
            name="Deploying the API",
            source_query="How is the API deployed?",
            parent_id="kf-1",
            tags=["ops", "type:runbook"],
            max_tokens=8192,
        )

        request = self._request()
        self.assertEqual(request.name, "Deploying the API")
        self.assertEqual(request.source_query, "How is the API deployed?")
        self.assertEqual(request.parent_id, "kf-1")
        self.assertEqual(request.tags, ["ops", "type:runbook"])
        self.assertEqual(request.max_tokens, 8192)
        # Omitting trigger leaves the server-side page defaults in place.
        self.assertIsNone(request.trigger)

    def test_create_folder_maps_parent(self):
        self._record("_knowledge_base_api", "create_knowledge_folder")

        self.client.create_knowledge_folder("bank-1", "Operations", parent_id=None)

        request = self._request()
        self.assertEqual(request.name, "Operations")
        self.assertIsNone(request.parent_id)

    def test_update_node_sends_only_provided_fields(self):
        self._record("_knowledge_base_api", "update_knowledge_node")

        self.client.update_knowledge_node("bank-1", "kp-1", name="Renamed")

        request = self._request()
        self.assertEqual(request.name, "Renamed")
        self.assertNotIn("parent_id", request.model_fields_set)

    def test_update_node_sends_explicit_null_parent(self):
        self._record("_knowledge_base_api", "update_knowledge_node")

        self.client.update_knowledge_node("bank-1", "kp-1", parent_id=None)

        request = self._request()
        self.assertIn("parent_id", request.model_fields_set)
        self.assertIsNone(request.parent_id)

    def test_update_node_maps_page_options(self):
        self._record("_knowledge_base_api", "update_knowledge_node")

        self.client.update_knowledge_node("bank-1", "kp-1", source_query="New question?", tags=[], max_tokens=2048)

        request = self._request()
        self.assertEqual(request.source_query, "New question?")
        self.assertEqual(request.tags, [])
        self.assertEqual(request.max_tokens, 2048)
        self.assertNotIn("trigger", request.model_fields_set)

    def test_update_node_forwards_a_partial_trigger(self):
        self._record("_knowledge_base_api", "update_knowledge_node")

        self.client.update_knowledge_node("bank-1", "kp-1", trigger={"refresh_cron": "0 3 * * *"})

        request = self._request()
        self.assertEqual(request.trigger.to_dict(), {"refresh_cron": "0 3 * * *"})

    def test_tree_and_search_forward_the_tag_filter(self):
        groups = [{"or": [{"tags": ["user:kate"], "match": "all_strict"}, {"tags": ["team"]}]}]
        for api_method, call in (
            ("get_knowledge_base_tree", lambda **kw: self.client.get_knowledge_base_tree("bank-1", **kw)),
            ("search_knowledge_base", lambda **kw: self.client.search_knowledge_base("bank-1", "q", **kw)),
        ):
            with self.subTest(method=api_method):
                self.captured.clear()
                self._record("_knowledge_base_api", api_method)
                call(tags=["user:kate"], tags_match="any_strict", tag_groups=groups)
                kwargs = self.captured["kwargs"]
                self.assertEqual(kwargs["tags"], ["user:kate"])
                self.assertEqual(kwargs["tags_match"], "any_strict")
                self.assertEqual(json.loads(kwargs["tag_groups"]), groups)

                self.captured.clear()
                self._record("_knowledge_base_api", api_method)
                call()
                self.assertIsNone(self.captured["kwargs"]["tags"])
                self.assertIsNone(self.captured["kwargs"]["tag_groups"])

    def test_page_and_export_delegate(self):
        for sync_name, api_method, args in (
            ("get_knowledge_page", "get_knowledge_page", ("bank-1", "kp-1")),
            ("delete_knowledge_node", "delete_knowledge_node", ("bank-1", "kp-1")),
            ("export_knowledge_base", "export_knowledge_base", ("bank-1",)),
        ):
            with self.subTest(method=sync_name):
                self.captured.clear()
                self._record("_knowledge_base_api", api_method)
                getattr(self.client, sync_name)(*args)
                self.assertEqual(self.captured["args"][: len(args)], args)


# --------------------------------------------------------------------------- #
# directives
# --------------------------------------------------------------------------- #


class DirectiveTest(_RecordingClientTestCase):
    def test_create_maps_every_option(self):
        self._record("_directives_api", "create_directive")

        self.client.create_directive("bank-1", name="Tone", content="Be terse", priority=3, tags=["ops"])

        self.assertEqual(self.captured["args"][0], "bank-1")
        request = self._request()
        self.assertEqual(request.name, "Tone")
        self.assertEqual(request.content, "Be terse")
        self.assertEqual(request.priority, 3)
        self.assertIs(request.is_active, True)
        self.assertEqual(request.tags, ["ops"])

    def test_update_maps_provided_fields(self):
        self._record("_directives_api", "update_directive")

        self.client.update_directive("bank-1", "d-1", is_active=False)

        self.assertEqual(self.captured["args"][:2], ("bank-1", "d-1"))
        request = self._request()
        self.assertIs(request.is_active, False)
        # The unnamed fields are sent as explicit nulls, which is the shape this
        # request model has always had: it is built from every argument.
        self.assertEqual(
            request.to_dict(),
            {"name": None, "content": None, "priority": None, "is_active": False, "tags": None},
        )

    def test_list_get_delete_delegate(self):
        for sync_name, api_method, args in (
            ("list_directives", "list_directives", ("bank-1",)),
            ("get_directive", "get_directive", ("bank-1", "d-1")),
            ("delete_directive", "delete_directive", ("bank-1", "d-1")),
        ):
            with self.subTest(method=sync_name):
                self.captured.clear()
                self._record("_directives_api", api_method)
                getattr(self.client, sync_name)(*args)
                self.assertEqual(self.captured["args"][: len(args)], args)


# --------------------------------------------------------------------------- #
# sync/async pairing
# --------------------------------------------------------------------------- #

#: The convenience families this file covers, as (sync, async) name pairs.
CONVENIENCE_PAIRS = [
    ("get_version", "aget_version"),
    ("list_memories", "alist_memories"),
    ("create_bank", "acreate_bank"),
    ("set_mission", "aset_mission"),
    ("set_reflect_mission", "aset_reflect_mission"),
    ("delete_bank", "adelete_bank"),
    ("get_bank_config", "aget_bank_config"),
    ("update_bank_config", "aupdate_bank_config"),
    ("reset_bank_config", "areset_bank_config"),
    ("create_mental_model", "acreate_mental_model"),
    ("list_mental_models", "alist_mental_models"),
    ("get_mental_model", "aget_mental_model"),
    ("refresh_mental_model", "arefresh_mental_model"),
    ("dry_run_refresh_mental_model", "adry_run_refresh_mental_model"),
    ("clear_mental_model", "aclear_mental_model"),
    ("update_mental_model", "aupdate_mental_model"),
    ("delete_mental_model", "adelete_mental_model"),
    ("get_mental_model_history", "aget_mental_model_history"),
    ("get_knowledge_base_tree", "aget_knowledge_base_tree"),
    ("create_knowledge_folder", "acreate_knowledge_folder"),
    ("create_knowledge_page", "acreate_knowledge_page"),
    ("get_knowledge_page", "aget_knowledge_page"),
    ("search_knowledge_base", "asearch_knowledge_base"),
    ("update_knowledge_node", "aupdate_knowledge_node"),
    ("delete_knowledge_node", "adelete_knowledge_node"),
    ("export_knowledge_base", "aexport_knowledge_base"),
    ("create_directive", "acreate_directive"),
    ("list_directives", "alist_directives"),
    ("get_directive", "aget_directive"),
    ("update_directive", "aupdate_directive"),
    ("delete_directive", "adelete_directive"),
    ("export_documents", "aexport_documents"),
    ("export_bank", "aexport_bank"),
    ("import_bank", "aimport_bank"),
    ("clone_bank", "aclone_bank"),
]


class SyncAsyncParityTest(unittest.TestCase):
    def test_the_pairs_are_actually_sync_and_async(self):
        for sync_name, async_name in CONVENIENCE_PAIRS:
            sync_fn = getattr(WenYuan, sync_name)
            async_fn = getattr(WenYuan, async_name)
            with self.subTest(pair=sync_name):
                self.assertFalse(inspect.iscoroutinefunction(sync_fn), f"{sync_name} should be sync")
                self.assertTrue(inspect.iscoroutinefunction(async_fn), f"{async_name} should be async")

    def test_pair_signatures_match(self):
        """The twins must accept the same arguments, or a caller silently loses one."""
        for sync_name, async_name in CONVENIENCE_PAIRS:
            sync_params = inspect.signature(getattr(WenYuan, sync_name)).parameters
            async_params = inspect.signature(getattr(WenYuan, async_name)).parameters
            with self.subTest(pair=sync_name):
                self.assertEqual(list(sync_params), list(async_params))
                for name, parameter in sync_params.items():
                    other = async_params[name]
                    self.assertEqual(parameter.kind, other.kind, name)
                    self.assertEqual(parameter.default, other.default, name)


if __name__ == "__main__":
    unittest.main()