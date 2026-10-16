"""Tests for the request options the wrapper translates for the generated models.

The generated request models accept their union-typed fields (``content``,
``timestamp``, ``observation_scopes``, ``tag_groups``) only as model instances,
and they ignore an option they do not know. Both behaviours make a request pass
the façade while carrying less than the caller asked for, so each translation is
pinned here against the body the wrapper actually puts on the wire, and each
unusable name is pinned as a refusal.

Run from ``clients/python``::

    python3 -m unittest discover -s tests -v
"""

from __future__ import annotations

import json
import pathlib
import sys
import threading
import unittest
import warnings
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))

from wenyuan_client import (  # noqa: E402
    DispositionTraits,
    ReflectFact,
    WenYuan,
    RecallResponse,
)
from wenyuan_client_api.models.content import Content  # noqa: E402
from wenyuan_client_api.models.content_any_of_inner import ContentAnyOfInner  # noqa: E402
from wenyuan_client_api.models.memory_item import MemoryItem  # noqa: E402


TEXT_BLOCK = {"type": "text", "text": "look at this:"}
IMAGE_BLOCK = {
    "type": "image",
    "source": {"type": "base64", "media_type": "image/png", "data": "AA"},
}


class _Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server: "_StubServer"

    def log_message(self, *args):  # silence the test output
        pass

    def _reply(self, payload):
        body = json.dumps(payload).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b""
        body = json.loads(raw) if raw else None
        self.server.requests.append({"path": self.path, "body": body})

        if self.path.endswith("/memories/recall"):
            self._reply({"results": []})
        elif self.path.endswith("/reflect"):
            self._reply({"text": "ok"})
        else:
            self._reply(
                {
                    "success": True,
                    "bank_id": "b",
                    "items_count": len(body["items"]),
                    "async": bool(body.get("async")),
                }
            )


class _StubServer(ThreadingHTTPServer):
    requests: list

    def __init__(self):
        super().__init__(("127.0.0.1", 0), _Handler)
        self.requests = []


class _StubTestCase(unittest.TestCase):
    def setUp(self):
        self.server = _StubServer()
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.addCleanup(self._stop)
        self.client = WenYuan(f"http://127.0.0.1:{self.server.server_address[1]}")
        self.addCleanup(self.client.close)

    def _stop(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)

    def sent(self):
        """The single request the last call made."""
        self.assertEqual(len(self.server.requests), 1, self.server.requests)
        return self.server.requests[0]["body"]


class ContentBlockDecodingTest(unittest.TestCase):
    """The generated content union decodes a block the way the contract spells it."""

    def test_from_dict_keeps_each_block(self):
        decoded = Content.from_dict([TEXT_BLOCK, IMAGE_BLOCK])
        self.assertEqual(decoded.to_dict(), [TEXT_BLOCK, IMAGE_BLOCK])

    def test_memory_item_from_dict_keeps_content(self):
        item = MemoryItem.from_dict({"content": [TEXT_BLOCK, IMAGE_BLOCK]})
        self.assertEqual(item.to_dict()["content"], [TEXT_BLOCK, IMAGE_BLOCK])

    def test_constructor_accepts_a_block_dict(self):
        self.assertEqual(ContentAnyOfInner(TEXT_BLOCK).to_dict(), TEXT_BLOCK)
        self.assertEqual(ContentAnyOfInner(**IMAGE_BLOCK).to_dict(), IMAGE_BLOCK)

    def test_unknown_block_type_is_refused(self):
        with self.assertRaises(ValueError):
            ContentAnyOfInner({"type": "video"})

    def test_string_and_block_list_serialise_alike(self):
        self.assertEqual(Content("plain").to_dict(), "plain")
        self.assertEqual(Content([TEXT_BLOCK]).to_dict(), [TEXT_BLOCK])


class RecallOptionsTest(_StubTestCase):
    def test_tag_groups_are_built_not_dropped(self):
        self.client.recall("b", "q", tag_groups=[{"tags": ["customer"], "match": "all"}])
        self.assertEqual(
            self.sent()["tag_groups"],
            [{"tags": ["customer"], "match": "all", "resolve": "exact"}],
        )

    def test_nested_tag_group_rule_survives(self):
        self.client.recall("b", "q", tag_groups=[{"not": {"tags": ["internal"]}}])
        self.assertEqual(
            self.sent()["tag_groups"],
            [{"not": {"tags": ["internal"], "match": "any_strict", "resolve": "exact"}}],
        )

    def test_include_flags_build_the_include_object(self):
        self.client.recall(
            "b",
            "q",
            include_entities=True,
            max_entity_tokens=100,
            include_chunks=True,
            max_chunk_tokens=64,
        )
        include = self.sent()["include"]
        self.assertEqual(include["entities"], {"max_tokens": 100})
        self.assertEqual(include["chunks"], {"max_tokens": 64})
        self.assertIsNone(include["source_facts"])

    def test_include_absent_when_no_flag_is_set(self):
        self.client.recall("b", "q")
        self.assertNotIn("include", self.sent())

    def test_source_facts_flag(self):
        self.client.recall("b", "q", include_source_facts=True, max_source_facts_tokens=256)
        self.assertEqual(self.sent()["include"]["source_facts"]["max_tokens"], 256)

    def test_min_scores_stage_floors_are_sent(self):
        self.client.recall("b", "q", min_scores={"semantic": 0.2, "final": 0.5})
        self.assertEqual(self.sent()["min_scores"]["semantic"], 0.2)
        self.assertEqual(self.sent()["min_scores"]["final"], 0.5)

    def test_unknown_min_scores_stage_is_refused(self):
        with self.assertRaises(ValueError) as caught:
            self.client.recall("b", "q", min_scores={"sematics": 0.2})
        self.assertIn("sematics", str(caught.exception))
        self.assertEqual(self.server.requests, [])

    def test_include_and_include_flags_are_mutually_exclusive(self):
        with self.assertRaises(ValueError):
            self.client.recall("b", "q", include={"chunks": {}}, include_entities=True)
        self.assertEqual(self.server.requests, [])

    def test_unknown_option_is_refused(self):
        with self.assertRaises(TypeError) as caught:
            self.client.recall("b", "q", prefer_observation=True)
        self.assertIn("prefer_observation", str(caught.exception))
        self.assertEqual(self.server.requests, [])

    def test_schema_field_names_still_reach_the_request(self):
        self.client.recall("b", "q", types=["world"], budget="high", prefer_observations=True)
        body = self.sent()
        self.assertEqual(body["types"], ["world"])
        self.assertEqual(body["budget"], "high")
        self.assertIs(body["prefer_observations"], True)


class ReflectOptionsTest(_StubTestCase):
    def test_include_facts_builds_the_include_object(self):
        self.client.reflect("b", "q", include_facts=True)
        self.assertEqual(self.sent()["include"], {"facts": {}, "tool_calls": None})

    def test_include_tool_calls_carries_the_output_switch(self):
        self.client.reflect("b", "q", include_tool_calls=True, include_tool_call_output=False)
        include = self.sent()["include"]
        self.assertEqual(include["tool_calls"], {"output": False})
        self.assertNotIn("facts", include)

    def test_both_flags_together(self):
        self.client.reflect("b", "q", include_facts=True, include_tool_calls=True)
        self.assertEqual(self.sent()["include"], {"facts": {}, "tool_calls": {"output": True}})

    def test_tag_groups_are_built_not_dropped(self):
        self.client.reflect("b", "q", tag_groups=[{"tags": ["ops"], "match": "all"}])
        self.assertEqual(
            self.sent()["tag_groups"],
            [{"tags": ["ops"], "match": "all", "resolve": "exact"}],
        )

    def test_response_schema_is_passed_through(self):
        schema = {"type": "object", "properties": {"answer": {"type": "string"}}}
        self.client.reflect("b", "q", response_schema=schema)
        self.assertEqual(self.sent()["response_schema"], schema)

    def test_unknown_option_is_refused(self):
        with self.assertRaises(TypeError) as caught:
            self.client.reflect("b", "q", include_fact=True)
        self.assertIn("include_fact", str(caught.exception))
        self.assertEqual(self.server.requests, [])


class RetainOptionsTest(_StubTestCase):
    def test_item_options_reach_the_wire(self):
        self.client.retain(
            "b",
            "x",
            timestamp="2023-05-30T23:40:00",
            context="ctx",
            metadata={"k": "v"},
            entities=[{"text": "Alice", "type": "person"}],
            resolve_entities=False,
            tags=["t1"],
            update_mode="append",
            observation_scopes="per_tag",
            strategy="named",
        )
        item = self.sent()["items"][0]
        self.assertEqual(item["timestamp"], "2023-05-30T23:40:00")
        self.assertEqual(item["context"], "ctx")
        self.assertEqual(item["metadata"], {"k": "v"})
        self.assertEqual(item["entities"], [{"text": "Alice", "type": "person"}])
        self.assertIs(item["resolve_entities"], False)
        self.assertEqual(item["tags"], ["t1"])
        self.assertEqual(item["update_mode"], "append")
        self.assertEqual(item["observation_scopes"], "per_tag")
        self.assertEqual(item["strategy"], "named")

    def test_observation_scopes_accepts_explicit_tag_groups(self):
        self.client.retain("b", "x", observation_scopes=[["a", "b"]])
        self.assertEqual(self.sent()["items"][0]["observation_scopes"], [["a", "b"]])

    def test_entities_accept_bare_names(self):
        self.client.retain("b", "x", entities=["Alice"])
        self.assertEqual(self.sent()["items"][0]["entities"], [{"text": "Alice"}])

    def test_timeless_sentinel_is_kept(self):
        self.client.retain("b", "x", timestamp="unset")
        self.assertEqual(self.sent()["items"][0]["timestamp"], "unset")

    def test_batch_document_id_fills_in_for_items_without_one(self):
        self.client.retain_batch(
            "b",
            [{"content": "a"}, {"content": "b", "document_id": "own"}],
            document_id="batch",
        )
        items = self.sent()["items"]
        self.assertEqual([item["document_id"] for item in items], ["batch", "own"])

    def test_memory_item_instances_are_accepted(self):
        self.client.retain_batch("b", [MemoryItem(content=Content("x"))], document_id="d")
        self.assertEqual(self.sent()["items"][0]["document_id"], "d")

    def test_unknown_item_key_is_refused(self):
        with self.assertRaises(TypeError) as caught:
            self.client.retain_batch("b", [{"content": "x", "observaton_scopes": "per_tag"}])
        self.assertIn("observaton_scopes", str(caught.exception))
        self.assertEqual(self.server.requests, [])

    def test_operation_id_is_sent_only_for_an_async_retain(self):
        self.client.retain("b", "x", retain_async=True, operation_id="abc")
        self.assertEqual(self.sent()["operation_id"], "abc")

    def test_operation_id_on_a_sync_retain_warns_and_is_dropped(self):
        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            self.client.retain("b", "x", operation_id="abc")
        self.assertIsNone(self.sent()["operation_id"])
        self.assertTrue(any("operation_id" in str(warning.message) for warning in caught))


class ExportSurfaceTest(unittest.TestCase):
    def test_response_types_are_re_exported(self):
        self.assertIsNotNone(ReflectFact)
        self.assertIsNotNone(DispositionTraits)
        self.assertIsNotNone(RecallResponse)


if __name__ == "__main__":
    unittest.main()