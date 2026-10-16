"""Retry policy for idempotent calls when the server is at capacity.

The server's admission control refuses with 429/503 when it is saturated.
Retrying is only safe if it honours the ``Retry-After`` hint and spreads the
retries out: a burst of clients that all obey ``Retry-After: 1`` exactly comes
back in lockstep and rebuilds the spike that caused the refusal.
"""

from __future__ import annotations

import pathlib
import random
import sys
import unittest
from unittest.mock import AsyncMock, MagicMock, patch

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))

from wenyuan_client import WenYuan  # noqa: E402
from wenyuan_client.client import _retry_after_seconds, _retry_on_capacity  # noqa: E402
from wenyuan_client_api.exceptions import ApiException  # noqa: E402


def _at_capacity(status: int = 503, retry_after="2") -> ApiException:
    exception = ApiException(status=status)
    exception.headers = {"Retry-After": retry_after} if retry_after is not None else {}
    return exception


class RetryAfterParsingTest(unittest.TestCase):
    def test_reads_delta_seconds(self):
        self.assertEqual(_retry_after_seconds(_at_capacity(retry_after="7")), 7.0)

    def test_missing_header_returns_none(self):
        self.assertIsNone(_retry_after_seconds(_at_capacity(retry_after=None)))

    def test_missing_headers_attribute_returns_none(self):
        self.assertIsNone(_retry_after_seconds(ApiException(status=503)))

    def test_http_date_form_falls_back(self):
        """The date form is legal but rarer; the caller's backoff is better."""
        self.assertIsNone(_retry_after_seconds(_at_capacity(retry_after="Wed, 21 Oct 2026 07:28:00 GMT")))

    def test_negative_is_clamped(self):
        self.assertEqual(_retry_after_seconds(_at_capacity(retry_after="-5")), 0.0)

    def test_non_finite_is_ignored(self):
        self.assertIsNone(_retry_after_seconds(_at_capacity(retry_after="inf")))

    def test_reads_a_sequence_of_pairs(self):
        """The generated client may hand the headers over as a list of pairs."""
        exception = ApiException(status=503)
        exception.headers = [("Content-Type", "application/json"), ("retry-after", "3")]
        self.assertEqual(_retry_after_seconds(exception), 3.0)


class RetryBehaviourTest(unittest.IsolatedAsyncioTestCase):
    async def test_succeeds_without_retrying(self):
        calls = []

        async def call():
            calls.append(1)
            return "ok"

        self.assertEqual(await _retry_on_capacity(call, 3, random.Random(0)), "ok")
        self.assertEqual(len(calls), 1)

    async def test_retries_503_then_succeeds(self):
        calls = []

        async def call():
            calls.append(1)
            if len(calls) < 3:
                raise _at_capacity(503, "0")
            return "ok"

        self.assertEqual(await _retry_on_capacity(call, 3, random.Random(0)), "ok")
        self.assertEqual(len(calls), 3)

    async def test_retries_429_too(self):
        calls = []

        async def call():
            calls.append(1)
            if len(calls) < 2:
                raise _at_capacity(429, "0")
            return "ok"

        self.assertEqual(await _retry_on_capacity(call, 3, random.Random(0)), "ok")
        self.assertEqual(len(calls), 2)

    async def test_gives_up_after_max_attempts_and_reraises(self):
        calls = []

        async def call():
            calls.append(1)
            raise _at_capacity(503, "0")

        with self.assertRaises(ApiException) as caught:
            await _retry_on_capacity(call, 3, random.Random(0))
        self.assertEqual(caught.exception.status, 503)
        self.assertEqual(len(calls), 3)

    async def test_does_not_retry_other_errors(self):
        """A 400 is the caller's problem; repeating it wastes a round trip."""
        calls = []

        async def call():
            calls.append(1)
            raise ApiException(status=400)

        with self.assertRaises(ApiException):
            await _retry_on_capacity(call, 3, random.Random(0))
        self.assertEqual(len(calls), 1)

    async def test_max_attempts_of_one_disables_retrying(self):
        calls = []

        async def call():
            calls.append(1)
            raise _at_capacity(503, "0")

        with self.assertRaises(ApiException):
            await _retry_on_capacity(call, 1, random.Random(0))
        self.assertEqual(len(calls), 1)

    async def test_wait_is_jittered_within_retry_after(self):
        """Two clients given the same Retry-After must not wake together."""
        slept: list = []

        async def fake_sleep(seconds):
            slept.append(seconds)

        async def call():
            raise _at_capacity(503, "4")

        with patch("wenyuan_client.client.asyncio.sleep", fake_sleep):
            for seed in (1, 2, 3):
                with self.assertRaises(ApiException):
                    await _retry_on_capacity(call, 2, random.Random(seed))

        self.assertTrue(all(0 <= value <= 4 for value in slept), slept)
        self.assertGreater(len(set(slept)), 1, "identical waits: jitter is not being applied")

    async def test_fallback_backoff_when_no_retry_after(self):
        slept: list = []

        async def fake_sleep(seconds):
            slept.append(seconds)

        async def call():
            raise _at_capacity(503, None)

        with patch("wenyuan_client.client.asyncio.sleep", fake_sleep):
            with self.assertRaises(ApiException):
                await _retry_on_capacity(call, 3, random.Random(0))

        # First retry: within [0, 0.5]; second: within [0, 1.0].
        self.assertEqual(len(slept), 2)
        self.assertTrue(0 <= slept[0] <= 0.5, slept)
        self.assertTrue(0 <= slept[1] <= 1.0, slept)


class ClientRetryTest(unittest.IsolatedAsyncioTestCase):
    """The wrapper wires the retry policy to reads, and only to reads."""

    def _client(self, max_attempts=3):
        client = WenYuan("http://127.0.0.1:1", max_attempts=max_attempts)
        self.addCleanup(client.close)
        return client

    async def test_recall_retries_then_succeeds(self):
        client = self._client()
        client._memory_api = MagicMock()
        client._memory_api.recall_memories = AsyncMock(
            side_effect=[_at_capacity(503, "0"), "recall-ok"]
        )

        async def fake_sleep(_seconds):
            return None

        with patch("wenyuan_client.client.asyncio.sleep", fake_sleep):
            result = await client.arecall("bank", "q")
        self.assertEqual(result, "recall-ok")
        self.assertEqual(client._memory_api.recall_memories.await_count, 2)

    async def test_reflect_retries_then_succeeds(self):
        client = self._client()
        client._memory_api = MagicMock()
        client._memory_api.reflect = AsyncMock(side_effect=[_at_capacity(429, "0"), "reflect-ok"])

        async def fake_sleep(_seconds):
            return None

        with patch("wenyuan_client.client.asyncio.sleep", fake_sleep):
            result = await client.areflect("bank", "q")
        self.assertEqual(result, "reflect-ok")
        self.assertEqual(client._memory_api.reflect.await_count, 2)

    async def test_recall_surfaces_the_original_error_after_the_last_attempt(self):
        client = self._client(max_attempts=2)
        client._memory_api = MagicMock()
        client._memory_api.recall_memories = AsyncMock(side_effect=_at_capacity(503, "0"))

        async def fake_sleep(_seconds):
            return None

        with patch("wenyuan_client.client.asyncio.sleep", fake_sleep):
            with self.assertRaises(ApiException) as caught:
                await client.arecall("bank", "q")
        self.assertEqual(caught.exception.status, 503)
        self.assertEqual(client._memory_api.recall_memories.await_count, 2)

    async def test_retain_is_not_retried(self):
        """A write must survive only once; a retry there could duplicate it."""
        client = self._client()
        client._memory_api = MagicMock()
        client._memory_api.retain_memories = AsyncMock(side_effect=_at_capacity(503, "0"))

        with self.assertRaises(ApiException):
            await client.aretain("bank", "text")
        self.assertEqual(client._memory_api.retain_memories.await_count, 1)


if __name__ == "__main__":
    unittest.main()