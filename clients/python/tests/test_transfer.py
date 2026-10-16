"""The transfer conveniences: submit, poll, then fetch the archive.

Two layers, tested separately. The orchestration tests replace the generated
sub-clients and assert the sequence the wrapper adds — submit the export, poll
the operation to a terminal state, then read the archive from the location the
operation named — together with the timeout bounds and the failure paths. The
download tests run a real local server so the credential boundary is exercised
for real: a signed URL is fetched bare, a server-relative URL keeps the client's
auth.

Run from ``clients/python``::

    python3 -m unittest discover -s tests -v
"""

from __future__ import annotations

import asyncio
import pathlib
import sys
import time
import unittest
from unittest.mock import AsyncMock, MagicMock

from aiohttp import web
from aiohttp.test_utils import TestServer

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))

from wenyuan_client import WenYuan  # noqa: E402
from wenyuan_client_api.exceptions import ApiException  # noqa: E402

OPERATION_ID = "123e4567-e89b-12d3-a456-426614174000"
STORAGE_KEY = f"banks/my-bank/exports/{OPERATION_ID}/transfer.zip"
DOWNLOAD_URL = f"/v1/default/files/download/{STORAGE_KEY}"
ARCHIVE_BYTES = b"PK\x03\x04 fake zip bytes"


def _status(status: str, result_metadata: dict | None = None, error_message: str | None = None) -> MagicMock:
    return MagicMock(status=status, result_metadata=result_metadata, error_message=error_message)


class _TransferTestCase(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.client = WenYuan("http://127.0.0.1:1")

    async def asyncTearDown(self):
        await self.client.aclose()

    def _mock_relative_download(self) -> None:
        """Route the API-relative download through a canned generated response."""
        response = MagicMock()
        response.status = 200
        response.data = ARCHIVE_BYTES
        response.getheaders = lambda: {}

        async def read():
            return ARCHIVE_BYTES

        response.read = read
        self.client._api_client.param_serialize = MagicMock(return_value=("GET", DOWNLOAD_URL, {}, None, []))
        self.client._api_client.call_api = AsyncMock(return_value=response)


class ExportOrchestrationTest(_TransferTestCase):
    async def test_export_bank_submits_polls_and_downloads(self):
        self.client._bank_transfer_api.export_bank_transfer = AsyncMock(
            return_value=MagicMock(operation_id=OPERATION_ID)
        )
        self.client._operations_api.get_operation_status = AsyncMock(
            side_effect=[
                _status("processing"),
                _status("completed", result_metadata={"download_url": DOWNLOAD_URL, "storage_key": STORAGE_KEY}),
            ]
        )
        self._mock_relative_download()

        result = await self.client.aexport_bank("my-bank", poll_interval=0)

        self.assertEqual(result, ARCHIVE_BYTES)
        kwargs = self.client._bank_transfer_api.export_bank_transfer.call_args.kwargs
        self.assertIs(kwargs["include_data"], True)
        self.assertIs(kwargs["include_bank_config"], True)
        self.assertIs(kwargs["include_history"], False)
        self.assertEqual(self.client._operations_api.get_operation_status.await_count, 2)

    async def test_export_bank_forwards_a_narrowed_scope(self):
        """A caller asking for the memories only must not silently get the config too."""
        self.client._bank_transfer_api.export_bank_transfer = AsyncMock(
            return_value=MagicMock(operation_id=OPERATION_ID)
        )
        self.client._operations_api.get_operation_status = AsyncMock(
            return_value=_status("completed", result_metadata={"download_url": DOWNLOAD_URL})
        )
        self._mock_relative_download()

        await self.client.aexport_bank("my-bank", include_bank_config=False, poll_interval=0)

        kwargs = self.client._bank_transfer_api.export_bank_transfer.call_args.kwargs
        self.assertIs(kwargs["include_bank_config"], False)
        self.assertIs(kwargs["include_data"], True)

    async def test_export_documents_submits_polls_and_downloads(self):
        self.client._document_transfer_api.export_documents = AsyncMock(
            return_value=MagicMock(operation_id=OPERATION_ID)
        )
        self.client._operations_api.get_operation_status = AsyncMock(
            side_effect=[
                _status("processing"),
                _status("completed", result_metadata={"download_url": DOWNLOAD_URL, "storage_key": STORAGE_KEY}),
            ]
        )
        self._mock_relative_download()

        result = await self.client.aexport_documents("my-bank", poll_interval=0)

        self.assertEqual(result, ARCHIVE_BYTES)
        submit_kwargs = self.client._document_transfer_api.export_documents.call_args.kwargs
        self.assertIsNone(submit_kwargs["document_id"])
        self.assertIs(submit_kwargs["include_observations"], False)
        self.assertEqual(self.client._operations_api.get_operation_status.await_count, 2)

    async def test_export_documents_forwards_a_subset(self):
        self.client._document_transfer_api.export_documents = AsyncMock(
            return_value=MagicMock(operation_id=OPERATION_ID)
        )
        self.client._operations_api.get_operation_status = AsyncMock(
            return_value=_status("completed", result_metadata={"download_url": DOWNLOAD_URL})
        )
        self._mock_relative_download()

        await self.client.aexport_documents("my-bank", document_ids=["doc-1", "doc-2"], poll_interval=0)

        kwargs = self.client._document_transfer_api.export_documents.call_args.kwargs
        self.assertEqual(kwargs["document_id"], ["doc-1", "doc-2"])

    async def test_export_raises_on_a_failed_operation(self):
        self.client._bank_transfer_api.export_bank_transfer = AsyncMock(
            return_value=MagicMock(operation_id=OPERATION_ID)
        )
        self.client._operations_api.get_operation_status = AsyncMock(
            return_value=_status("failed", error_message="disk full")
        )

        with self.assertRaises(RuntimeError) as caught:
            await self.client.aexport_bank("my-bank", poll_interval=0)
        self.assertIn("disk full", str(caught.exception))

    async def test_export_raises_when_no_archive_is_named(self):
        self.client._document_transfer_api.export_documents = AsyncMock(
            return_value=MagicMock(operation_id=OPERATION_ID)
        )
        self.client._operations_api.get_operation_status = AsyncMock(
            return_value=_status("completed", result_metadata={})
        )

        with self.assertRaises(RuntimeError) as caught:
            await self.client.aexport_documents("my-bank", poll_interval=0)
        self.assertIn("download_url", str(caught.exception))

    async def test_timeout_of_zero_fails_before_the_first_poll(self):
        self.client._document_transfer_api.export_documents = AsyncMock(
            return_value=MagicMock(operation_id=OPERATION_ID)
        )
        self.client._operations_api.get_operation_status = AsyncMock(return_value=_status("processing"))

        with self.assertRaises(TimeoutError):
            await self.client.aexport_documents("my-bank", poll_interval=0, timeout=0)

    async def test_timeout_bounds_a_slow_status_request(self):
        self.client._bank_transfer_api.export_bank_transfer = AsyncMock(
            return_value=MagicMock(operation_id=OPERATION_ID)
        )

        async def late_completed(*args, **kwargs):
            await asyncio.sleep(5)
            return _status("completed", result_metadata={"download_url": DOWNLOAD_URL})

        self.client._operations_api.get_operation_status = late_completed
        self.client._api_client.call_api = AsyncMock()

        start = time.monotonic()
        with self.assertRaises(TimeoutError):
            await self.client.aexport_bank("my-bank", poll_interval=0, timeout=0.05)
        self.assertLess(time.monotonic() - start, 1)
        self.client._api_client.call_api.assert_not_awaited()

    async def test_timeout_bounds_the_poll_sleep(self):
        self.client._bank_transfer_api.export_bank_transfer = AsyncMock(
            return_value=MagicMock(operation_id=OPERATION_ID)
        )
        self.client._operations_api.get_operation_status = AsyncMock(return_value=_status("processing"))

        start = time.monotonic()
        with self.assertRaises(TimeoutError):
            await self.client.aexport_bank("my-bank", poll_interval=5, timeout=0.05)
        self.assertLess(time.monotonic() - start, 1)
        self.assertEqual(self.client._operations_api.get_operation_status.await_count, 1)


class ImportAndCloneTest(_TransferTestCase):
    async def test_import_bank_returns_the_operation_id_and_forwards_the_target(self):
        self.client._bank_transfer_api.import_bank_transfer = AsyncMock(
            return_value=MagicMock(operation_id=OPERATION_ID)
        )

        operation_id = await self.client.aimport_bank("my-bank", ARCHIVE_BYTES, target_bank_id="my-bank-copy")

        self.assertEqual(operation_id, OPERATION_ID)
        args, kwargs = self.client._bank_transfer_api.import_bank_transfer.call_args
        self.assertEqual(args[0], "my-bank")
        self.assertEqual(args[1], ARCHIVE_BYTES)
        self.assertEqual(kwargs["target_bank_id"], "my-bank-copy")

    async def test_clone_bank_names_source_and_target_and_returns_the_operation_id(self):
        self.client._bank_transfer_api.clone_bank = AsyncMock(return_value=MagicMock(operation_id=OPERATION_ID))

        operation_id = await self.client.aclone_bank("my-bank", "my-bank-copy")

        self.assertEqual(operation_id, OPERATION_ID)
        args, kwargs = self.client._bank_transfer_api.clone_bank.call_args
        self.assertEqual(args, ("my-bank", "my-bank-copy"))
        self.assertIs(kwargs["include_data"], True)
        self.assertIs(kwargs["include_bank_config"], True)
        self.assertIs(kwargs["include_history"], False)

    async def test_clone_bank_forwards_a_narrowed_scope(self):
        self.client._bank_transfer_api.clone_bank = AsyncMock(return_value=MagicMock(operation_id=OPERATION_ID))

        await self.client.aclone_bank("my-bank", "my-bank-copy", include_bank_config=False)

        self.assertIs(self.client._bank_transfer_api.clone_bank.call_args.kwargs["include_bank_config"], False)


class SignedUrlDownloadTest(_TransferTestCase):
    async def test_export_downloads_a_signed_url_without_api_credentials(self):
        requests = []

        async def archive(request):
            requests.append(request)
            return web.Response(body=ARCHIVE_BYTES)

        app = web.Application()
        app.router.add_get("/archive", archive)
        server = TestServer(app)
        await server.start_server()
        try:
            download_url = str(server.make_url("/archive")) + "?X-Amz-Signature=abc%2Fdef+ghi"
            client = WenYuan("http://127.0.0.1:1", api_key="secret")
            try:
                client._operations_api.get_operation_status = AsyncMock(
                    return_value=_status("completed", result_metadata={"download_url": download_url})
                )
                client._document_transfer_api.export_documents = AsyncMock(
                    return_value=MagicMock(operation_id=OPERATION_ID)
                )
                # A signed URL must not be routed through the API client at all.
                client._api_client.param_serialize = MagicMock(
                    side_effect=AssertionError("API route used for a signed URL")
                )

                result = await client.aexport_documents("my-bank", poll_interval=0)

                self.assertEqual(result, ARCHIVE_BYTES)
                self.assertEqual(len(requests), 1)
                # The signed query string survives untouched.
                self.assertEqual(requests[0].raw_path, "/archive?X-Amz-Signature=abc%2Fdef+ghi")
                self.assertNotIn("Authorization", requests[0].headers)
            finally:
                await client.aclose()
        finally:
            await server.close()


class RelativeDownloadHttpTest(unittest.IsolatedAsyncioTestCase):
    """The server-relative download goes through the generated client, auth and all."""

    async def _run(self, download_status: int):
        download_path = "/v1/default/files/download/banks/test/exports/transfer.zip"
        seen: dict[str, str] = {}

        async def handler(request: web.Request) -> web.Response:
            if request.method == "POST":
                return web.json_response({"operation_id": OPERATION_ID}, status=202)
            if "/operations/" in request.path:
                return web.json_response(
                    {
                        "operation_id": OPERATION_ID,
                        "status": "completed",
                        "result_metadata": {"download_url": download_path},
                    }
                )
            self.assertEqual(request.path, download_path)
            seen["authorization"] = request.headers.get("Authorization", "")
            body = ARCHIVE_BYTES if download_status == 200 else b"archive unavailable"
            return web.Response(status=download_status, body=body)

        app = web.Application()
        app.router.add_route("*", "/{path:.*}", handler)
        async with TestServer(app) as server:
            client = WenYuan(base_url=str(server.make_url("")).rstrip("/"), api_key="test-key")
            try:
                if download_status == 200:
                    result = await client.aexport_bank("test", poll_interval=0)
                    return result, seen
                with self.assertRaises(ApiException) as caught:
                    await client.aexport_bank("test", poll_interval=0)
                return caught.exception, seen
            finally:
                await client.aclose()

    async def test_a_successful_download_returns_the_archive_bytes(self):
        result, seen = await self._run(200)
        self.assertEqual(result, ARCHIVE_BYTES)
        self.assertEqual(seen["authorization"], "Bearer test-key")

    async def test_a_non_2xx_download_raises_rather_than_returning_the_error_body(self):
        for download_status in (404, 500):
            with self.subTest(status=download_status):
                result, _ = await self._run(download_status)
                self.assertIsInstance(result, ApiException)
                self.assertEqual(result.status, download_status)


if __name__ == "__main__":
    unittest.main()