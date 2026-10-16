"""Bank routes keep a bank id as one URL path component, and keep the client's identity.

The wrapper does not hand-write the bank routes; it calls the generated client,
which substitutes the path parameter through ``quote(..., safe=configuration
.safe_chars_for_path_param)`` with the safe set empty. That is the same
constraint the rest of the surface relies on, so this pins it where a bank id is
actually used: a bank id holding reserved characters must arrive at the server
percent-encoded — a ``#`` or ``?`` must not truncate or start a query — and the
configured ``User-Agent`` and ``Authorization`` must ride along.

Run from ``clients/python``::

    python3 -m unittest discover -s tests -v
"""

from __future__ import annotations

import pathlib
import sys
import unittest
from urllib.parse import quote

from aiohttp import web
from aiohttp.test_utils import TestServer

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))

from wenyuan_client import WenYuan  # noqa: E402
from wenyuan_client.client import DEFAULT_USER_AGENT  # noqa: E402

#: Bank ids that must survive as a single path component. Each combines a
#: character that would change the URL's structure if it leaked through.
BANK_IDS = ["team?draft#notes", "team%2Farchive", "team + café", "a/b/c"]


def _profile(bank_id: str) -> dict:
    return {
        "bank_id": bank_id,
        "name": bank_id,
        "mission": "",
        "disposition": {"skepticism": 3, "literalism": 3, "empathy": 3},
    }


class BankRouteEncodingTest(unittest.IsolatedAsyncioTestCase):
    async def _exercise(self, bank_id: str, user_agent: str | None) -> list[dict]:
        seen: list[dict] = []

        async def handler(request: web.Request) -> web.Response:
            seen.append(
                {
                    "method": request.method,
                    "raw_path": request.raw_path,
                    "query_string": request.query_string,
                    "user_agent": request.headers.get("User-Agent"),
                    "authorization": request.headers.get("Authorization"),
                }
            )
            if request.method == "DELETE":
                return web.json_response({"success": True})
            if request.path.endswith("/config"):
                return web.json_response({"bank_id": bank_id, "config": {}, "overrides": {}})
            return web.json_response(_profile(bank_id))

        app = web.Application()
        app.router.add_route("*", "/{path:.*}", handler)
        async with TestServer(app) as server:
            client = WenYuan(
                base_url=str(server.make_url("")).rstrip("/"), api_key="test-key", user_agent=user_agent
            )
            try:
                await client.acreate_bank(bank_id, name=bank_id)
                await client.aget_bank_config(bank_id)
                await client.adelete_bank(bank_id)
            finally:
                await client.aclose()
        return seen

    async def test_reserved_characters_stay_inside_one_path_component(self):
        for bank_id in BANK_IDS:
            with self.subTest(bank_id=bank_id):
                seen = await self._exercise(bank_id, None)
                self.assertEqual([entry["method"] for entry in seen], ["PUT", "GET", "DELETE"])
                encoded = quote(bank_id, safe="")
                for entry, suffix in zip(seen, ("", "/config", "")):
                    self.assertEqual(entry["raw_path"], f"/v1/default/banks/{encoded}{suffix}")
                    self.assertEqual(entry["query_string"], "")

    async def test_the_bank_routes_carry_the_client_identity(self):
        seen = await self._exercise("plain-bank", "wenyuan-integration-test/1.2.3")
        for entry in seen:
            self.assertEqual(entry["user_agent"], f"wenyuan-integration-test/1.2.3 {DEFAULT_USER_AGENT}")
            self.assertEqual(entry["authorization"], "Bearer test-key")


if __name__ == "__main__":
    unittest.main()