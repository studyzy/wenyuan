"""The client-level retain suspension seam.

``WenYuan.suspend_retains`` lets a caller run a read-only session against a real
bank: recall and reflect keep working, while every retain entry point becomes a
no-op that sends no request. These tests pin both halves and that leaving the
scope restores normal retains.
"""

from __future__ import annotations

import asyncio
import pathlib
import sys
import unittest
from unittest.mock import AsyncMock, MagicMock

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))

from wenyuan_client import WenYuan  # noqa: E402

BANK = "bank-1"


def _make_client() -> WenYuan:
    client = WenYuan("http://127.0.0.1:1")
    client._memory_api = MagicMock()
    client._memory_api.retain_memories = AsyncMock(return_value=MagicMock())
    client._memory_api.recall_memories = AsyncMock(return_value=MagicMock())
    client._files_api = MagicMock()
    client._files_api.file_retain = AsyncMock(return_value=MagicMock())
    return client


class SuspensionTest(unittest.TestCase):
    def test_retain_is_suppressed_while_suspended(self):
        client = _make_client()
        self.addCleanup(client.close)

        with client.suspend_retains():
            response = client.retain(BANK, "should not be stored")

        self.assertEqual(client._memory_api.retain_memories.await_count, 0)
        self.assertEqual(response.items_count, 0)
        self.assertTrue(response.success)
        self.assertEqual(response.bank_id, BANK)

    def test_retain_batch_is_suppressed_while_suspended(self):
        client = _make_client()
        self.addCleanup(client.close)

        with client.suspend_retains():
            response = client.retain_batch(BANK, [{"content": "a"}, {"content": "b"}])

        self.assertEqual(client._memory_api.retain_memories.await_count, 0)
        self.assertEqual(response.items_count, 0)

    def test_retain_files_is_suppressed_while_suspended(self):
        client = _make_client()
        self.addCleanup(client.close)

        with client.suspend_retains():
            response = client.retain_files(BANK, ["does-not-need-to-exist.txt"])

        self.assertEqual(client._files_api.file_retain.await_count, 0)
        self.assertEqual(response.operation_ids, [])

    def test_recall_still_reaches_the_api_while_suspended(self):
        client = _make_client()
        self.addCleanup(client.close)

        with client.suspend_retains():
            client.recall(BANK, "a question")

        self.assertEqual(client._memory_api.recall_memories.await_count, 1)

    def test_retain_resumes_after_the_block(self):
        client = _make_client()
        self.addCleanup(client.close)

        with client.suspend_retains():
            client.retain(BANK, "dropped")
        client.retain(BANK, "stored")

        self.assertEqual(client._memory_api.retain_memories.await_count, 1)

    def test_retains_are_enabled_outside_a_suspension_scope(self):
        client = _make_client()
        self.addCleanup(client.close)

        client.retain(BANK, "stored")
        self.assertEqual(client._memory_api.retain_memories.await_count, 1)

    def test_suspension_scopes_are_nested(self):
        client = _make_client()
        self.addCleanup(client.close)

        with client.suspend_retains():
            client.retain(BANK, "outer")
            with client.suspend_retains():
                client.retain(BANK, "inner")
            client.retain(BANK, "outer again")
        client.retain(BANK, "stored")

        self.assertEqual(client._memory_api.retain_memories.await_count, 1)

    def test_suspension_is_reset_after_an_exception(self):
        client = _make_client()
        self.addCleanup(client.close)

        with self.assertRaises(RuntimeError):
            with client.suspend_retains():
                raise RuntimeError("stop")
        client.retain(BANK, "stored")

        self.assertEqual(client._memory_api.retain_memories.await_count, 1)


class AsyncSuspensionTest(unittest.IsolatedAsyncioTestCase):
    async def test_suspension_does_not_suppress_another_task(self):
        client = _make_client()
        self.addCleanup(client.close)
        task_a_suspended = asyncio.Event()
        task_b_retained = asyncio.Event()

        async def read_only_task():
            with client.suspend_retains():
                task_a_suspended.set()
                await task_b_retained.wait()
                return await client.aretain(BANK, "task A should not be stored")

        async def normal_task():
            await task_a_suspended.wait()
            response = await client.aretain(BANK, "task B should be stored")
            task_b_retained.set()
            return response

        task_a_response, task_b_response = await asyncio.gather(read_only_task(), normal_task())

        self.assertEqual(task_a_response.items_count, 0)
        self.assertIs(task_b_response, client._memory_api.retain_memories.return_value)
        self.assertEqual(client._memory_api.retain_memories.await_count, 1)


if __name__ == "__main__":
    unittest.main()