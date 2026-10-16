"""Tests for the maintained WenYuan wrapper against a local stub server.

No external service is contacted: each test spins up a small ``http.server`` on
an ephemeral loopback port, answers with canned JSON, and drives the wrapper
end-to-end so both directions of the decode path are exercised — the request
body the wrapper serialises and the response models it parses back.

Run from ``clients/python``::

    python3 -m unittest discover -s tests -v
"""

from __future__ import annotations

import asyncio
import json
import os
import pathlib
import sys
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))

from wenyuan_client import DEFAULT_BASE_URL, BASE_URL_ENV_VAR, WenYuan  # noqa: E402


class _Handler(BaseHTTPRequestHandler):
    """Canned responses plus a log of what the client actually sent."""

    protocol_version = "HTTP/1.1"
    server: "_StubServer"

    def log_message(self, *args):  # silence the test output
        pass

    # -- helpers ------------------------------------------------------------

    def _read_json(self):
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b""
        return json.loads(raw) if raw else None

    def _reply(self, payload, status=200):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _record(self):
        self.server.requests.append(
            {
                "method": self.command,
                "path": self.path,
                "headers": dict(self.headers),
                "body": self._read_json() if self.command in ("POST", "PUT", "PATCH") else None,
            }
        )
        return self.server.requests[-1]

    # -- routes -------------------------------------------------------------

    def do_PUT(self):
        request = self._record()
        bank_id = request["path"].rsplit("/", 1)[-1]
        self._reply(
            {
                "bank_id": bank_id,
                "name": bank_id,
                "disposition": {"skepticism": 3, "literalism": 3, "empathy": 3},
                "mission": "",
            }
        )

    def do_GET(self):
        request = self._record()
        if request["path"].startswith("/v1/default/banks"):
            self._reply({"banks": [], "total": 0, "limit": 10, "offset": 0})
        else:
            self._reply({"error": "not found"}, status=404)

    def do_POST(self):
        request = self._record()
        path = request["path"]
        if path.endswith("/memories/recall"):
            self._reply(
                {
                    "results": [
                        {
                            "id": "mem-1",
                            "text": "we deploy on Fridays",
                            "type": "world",
                            "tags": ["ops"],
                        }
                    ]
                }
            )
        elif path.endswith("/reflect"):
            self._reply({"text": "Deploys happen on Fridays.", "based_on": {}})
        elif path.endswith("/memories"):
            body = request["body"]
            self._reply(
                {
                    "success": True,
                    "bank_id": "test-bank",
                    "items_count": len(body["items"]),
                    "async": bool(body.get("async")),
                }
            )
        else:
            self._reply({"error": "not found"}, status=404)


class _StubServer(ThreadingHTTPServer):
    requests: list

    def __init__(self):
        super().__init__(("127.0.0.1", 0), _Handler)
        self.requests = []


class ClientTestCase(unittest.TestCase):
    def setUp(self):
        self.server = _StubServer()
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.base_url = f"http://127.0.0.1:{self.server.server_address[1]}"
        self.addCleanup(self._stop)

    def _stop(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)

    def client(self, **kwargs):
        client = WenYuan(self.base_url, **kwargs)
        self.addCleanup(client.close)
        return client

    # -- sync operations ----------------------------------------------------

    def test_retain_round_trip(self):
        client = self.client()
        response = client.retain("test-bank", "we deploy on Fridays")

        self.assertTrue(response.success)
        self.assertEqual(response.items_count, 1)
        self.assertEqual(len(self.server.requests), 1)
        sent = self.server.requests[0]
        self.assertEqual(sent["method"], "POST")
        self.assertEqual(sent["path"], "/v1/default/banks/test-bank/memories")
        self.assertEqual(sent["body"]["items"][0]["content"], "we deploy on Fridays")
        self.assertIs(sent["body"]["async"], False)

    def test_retain_content_blocks_are_serialised(self):
        client = self.client()
        blocks = [
            {"type": "text", "text": "look at this:"},
            {"type": "text", "text": "then reconnect."},
        ]
        client.retain("test-bank", blocks)

        body = self.server.requests[0]["body"]
        self.assertEqual(body["items"][0]["content"], blocks)

    def test_recall_decodes_results(self):
        client = self.client()
        response = client.recall("test-bank", "when do we deploy?", max_tokens=128)

        self.assertEqual(len(response.results), 1)
        self.assertEqual(response.results[0].text, "we deploy on Fridays")
        sent = self.server.requests[0]
        self.assertEqual(sent["path"], "/v1/default/banks/test-bank/memories/recall")
        self.assertEqual(sent["body"]["query"], "when do we deploy?")
        self.assertEqual(sent["body"]["max_tokens"], 128)

    def test_reflect_decodes_text(self):
        client = self.client()
        response = client.reflect("test-bank", "summarise the deploy policy")

        self.assertEqual(response.text, "Deploys happen on Fridays.")
        self.assertEqual(self.server.requests[0]["path"], "/v1/default/banks/test-bank/reflect")

    def test_recall_rejects_malformed_option(self):
        client = self.client()
        # The generated request model validates option values, so a wrong type
        # fails locally before any request is made.
        with self.assertRaises(Exception):
            client.recall("test-bank", "q", max_tokens="not-an-int")

    # -- banks --------------------------------------------------------------

    def test_create_bank_round_trip(self):
        client = self.client()
        profile = client.create_bank("test-bank", name="Test Bank")

        self.assertEqual(profile.bank_id, "test-bank")
        sent = self.server.requests[0]
        self.assertEqual(sent["method"], "PUT")
        self.assertEqual(sent["path"], "/v1/default/banks/test-bank")
        self.assertEqual(sent["body"]["name"], "Test Bank")

    def test_list_banks_decodes_page(self):
        client = self.client()
        page = client.list_banks(q="test", limit=5, offset=0)

        self.assertEqual(page.total, 0)
        self.assertEqual(page.banks, [])
        self.assertIn("q=test", self.server.requests[0]["path"])

    # -- async variants -----------------------------------------------------

    def test_async_recall(self):
        client = WenYuan(self.base_url)

        async def run():
            try:
                return await client.arecall("test-bank", "when do we deploy?")
            finally:
                await client.aclose()

        response = asyncio.run(run())
        self.assertEqual(response.results[0].id, "mem-1")

    def test_sync_call_inside_running_loop_is_refused(self):
        client = self.client()

        async def attempt():
            with self.assertRaises(RuntimeError):
                client.recall("test-bank", "q")

        asyncio.run(attempt())

    # -- configuration ------------------------------------------------------

    def test_base_url_from_environment(self):
        previous = os.environ.get(BASE_URL_ENV_VAR)
        os.environ[BASE_URL_ENV_VAR] = self.base_url + "/"
        self.addCleanup(self._restore_env, previous)

        client = WenYuan()
        self.addCleanup(client.close)
        self.assertEqual(client.base_url, self.base_url)

    def _restore_env(self, previous):
        if previous is None:
            os.environ.pop(BASE_URL_ENV_VAR, None)
        else:
            os.environ[BASE_URL_ENV_VAR] = previous

    def test_default_base_url(self):
        previous = os.environ.pop(BASE_URL_ENV_VAR, None)
        self.addCleanup(self._restore_env, previous)
        client = WenYuan()
        self.addCleanup(client.close)
        self.assertEqual(client.base_url, DEFAULT_BASE_URL)


if __name__ == "__main__":
    unittest.main()