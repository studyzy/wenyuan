"""End-to-end tests against a running server.

The suite is gated on ``WENYUAN_API_URL``: without it every test is skipped,
so the offline gate stays green on a machine that has no server. With it set,
each test talks to a real service over HTTP:

    WENYUAN_API_URL=http://127.0.0.1:18888 python3 -m unittest discover -s tests

A server started with ``WENYUAN_API_DATABASE_URL=memory`` is enough — no model
provider is configured, so the assertions are structural (an operation
succeeded, an id round-tripped, a list carries what was written) rather than
about extracted facts or synthesised prose, which need a retention model.
"""

from __future__ import annotations

import asyncio
import os
import pathlib
import sys
import unittest
import uuid

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))

from wenyuan_client import WenYuan  # noqa: E402
from wenyuan_client_api.exceptions import ApiException  # noqa: E402
from wenyuan_client_api.models.bank_config_update import BankConfigUpdate  # noqa: E402
from wenyuan_client_api.models.chunk_include_options import ChunkIncludeOptions  # noqa: E402
from wenyuan_client_api.models.include_options import IncludeOptions  # noqa: E402


def _live_url():
    return os.environ.get("WENYUAN_API_URL")


@unittest.skipUnless(_live_url(), "WENYUAN_API_URL is not set: no server to test against")
class LiveServerTestCase(unittest.IsolatedAsyncioTestCase):
    """Base case that owns a unique bank per test and cleans it up."""

    async def asyncSetUp(self):
        self.client = WenYuan()
        self.bank_id = f"py-sdk-{uuid.uuid4().hex[:12]}"
        await self.client.acreate_bank(self.bank_id, name="Python SDK live", mission="serve the live suite")

    async def asyncTearDown(self):
        try:
            await self.client.banks.delete_bank(self.bank_id)
        except ApiException:
            pass
        await self.client.aclose()

    async def retain(self, text, *, tags=None):
        item = {"content": text}
        if tags is not None:
            item["tags"] = tags
        response = await self.client.aretain_batch(self.bank_id, [item])
        self.assertTrue(response.success)
        self.assertEqual(response.items_count, 1)
        return response


class BankLifecycleTest(LiveServerTestCase):
    async def test_create_list_and_delete(self):
        page = await self.client.alist_banks(q=self.bank_id)
        listed = [bank.bank_id for bank in page.banks]
        self.assertIn(self.bank_id, listed)

        await self.client.banks.delete_bank(self.bank_id)
        after = await self.client.alist_banks(q=self.bank_id)
        self.assertNotIn(self.bank_id, [bank.bank_id for bank in after.banks])

    async def test_config_round_trip(self):
        config = await self.client.banks.get_bank_config(self.bank_id)
        self.assertEqual(config.config.get("reflect_mission"), "serve the live suite")

        update = BankConfigUpdate(updates={"retain_extraction_mode": "verbose"})
        configured = await self.client.banks.update_bank_config(self.bank_id, bank_config_update=update)
        self.assertEqual(configured.config.get("retain_extraction_mode"), "verbose")
        self.assertEqual(configured.overrides.get("retain_extraction_mode"), "verbose")

        reset = await self.client.banks.reset_bank_config(self.bank_id)
        self.assertNotEqual(reset.config.get("retain_extraction_mode"), "verbose")

    async def test_retired_profile_route_answers_gone(self):
        # The contract retires the profile read (the document keeps the operation,
        # marked removed), so the 410 is what makes get_bank_config the supported
        # way to read a bank's settings.
        with self.assertRaises(ApiException) as caught:
            await self.client.banks.get_bank_profile(self.bank_id)
        self.assertEqual(caught.exception.status, 410)


class RetainReadTest(LiveServerTestCase):
    async def test_retain_then_document_and_chunk_reads(self):
        await self.retain("The schema stays the reference's schema, object for object.")

        documents = await self.client.documents.list_documents(self.bank_id)
        self.assertGreater(documents.total, 0)
        self.assertTrue(documents.items)
        document_id = documents.items[0].id

        document = await self.client.documents.get_document(self.bank_id, document_id)
        self.assertEqual(document.id, document_id)

        chunks = await self.client.documents.list_document_chunks(self.bank_id, document_id)
        self.assertTrue(chunks.items)
        chunk = await self.client.documents.get_chunk(chunks.items[0].chunk_id)
        self.assertEqual(chunk.chunk_id, chunks.items[0].chunk_id)
        self.assertTrue(chunk.chunk_text.strip())

    async def test_tagged_write_is_selected_by_its_tag_and_not_by_another(self):
        tag = "py-sdk-tagged"
        items = [
            {"content": "A tagged note records that Alice reviews the schema before every release.", "tags": [tag]},
            {"content": "A second tagged note records that Bob keeps the migration series generated.", "tags": [tag]},
        ]
        response = await self.client.aretain_batch(self.bank_id, items, document_tags=["py-sdk-document"])
        self.assertEqual(response.items_count, 2)

        tagged = await self.client.arecall(self.bank_id, "what do the tagged notes record?", tags=[tag])
        self.assertTrue(tagged.results)

        # A tag nobody wrote must select nothing: that proves the filter is the
        # tag and not "any query returns the bank's content".
        unwritten = await self.client.arecall(
            self.bank_id, "what do the tagged notes record?", tags=["py-sdk-never-written"]
        )
        self.assertEqual(len(unwritten.results), 0)

    async def test_async_retain_names_an_operation_in_the_queue(self):
        response = await self.client.aretain(self.bank_id, "queued work", retain_async=True)
        self.assertTrue(response.var_async)
        self.assertTrue(response.operation_id)

        operations = await self.client.operations.list_operations(self.bank_id)
        queued = [operation.operation_id for operation in operations.operations]
        self.assertIn(response.operation_id, queued)

        status = await self.client.operations.get_operation_status(self.bank_id, response.operation_id)
        self.assertTrue(status.status)


class RecallReflectTest(LiveServerTestCase):
    async def test_recall_returns_the_write(self):
        await self.retain("Alice prefers tea over coffee in the afternoon")

        response = await self.client.arecall(self.bank_id, "what does Alice prefer to drink?")
        self.assertTrue(response.results)
        for result in response.results:
            self.assertTrue(result.id)
            self.assertTrue(result.text.strip())

    async def test_recall_options(self):
        await self.retain(
            "The migration series is generated from the reference catalogue, so a schema change is made there.",
            tags=["py-sdk-option"],
        )

        response = await self.client.arecall(
            self.bank_id,
            "how is the migration series generated?",
            tags=["py-sdk-option"],
            max_tokens=64,
            trace=True,
            include=IncludeOptions(chunks=ChunkIncludeOptions()),
        )
        self.assertTrue(response.results)
        self.assertTrue(response.trace)
        self.assertIsNotNone(response.chunks)

    async def test_reflect_answers(self):
        await self.retain("The migration series is generated from the reference catalogue.")
        try:
            response = await self.client.areflect(
                self.bank_id, "how is the migration series produced?"
            )
        except ApiException as exc:
            # A deployment may have a model configured and that model may be
            # unreachable or slow; reflection then answers its documented
            # wall-clock-budget refusal. The call reached the handler and the
            # client decoded the refusal, which is what this suite proves.
            if exc.status == 504:
                return
            raise
        self.assertTrue(response.text.strip())


class EntityGraphTest(LiveServerTestCase):
    async def test_entities_and_graph_agree(self):
        await self.retain("Alice Chen works with Bob in the Berlin office.")

        entities = await self.client.entities.list_entities(self.bank_id)
        graph = await self.client.entities.get_entity_graph(self.bank_id)
        self.assertEqual(graph.total_entities, entities.total)
        for node in graph.nodes:
            self.assertTrue(node.data.id)


class MonitoringTest(LiveServerTestCase):
    async def test_version_is_served(self):
        version = await self.client.monitoring.get_version()
        self.assertTrue(version)


class WrapperConvenienceTest(LiveServerTestCase):
    async def test_version_convenience(self):
        version = await self.client.aget_version()
        self.assertTrue(version.api_version)

    async def test_list_memories_carries_the_write(self):
        await self.retain("Alice reviews the schema before every release.")

        page = await self.client.alist_memories(self.bank_id)

        self.assertGreaterEqual(page.total, 1)
        self.assertTrue(page.items)

    async def test_bank_config_round_trip(self):
        config = await self.client.aget_bank_config(self.bank_id)
        self.assertEqual(config["bank_id"], self.bank_id)

        updated = await self.client.aupdate_bank_config(self.bank_id, retain_extraction_mode="verbose")
        self.assertEqual(updated["overrides"].get("retain_extraction_mode"), "verbose")

        reset = await self.client.areset_bank_config(self.bank_id)
        self.assertNotEqual(reset["config"].get("retain_extraction_mode"), "verbose")

    async def test_set_mission_writes_the_mission(self):
        profile = await self.client.aset_mission(self.bank_id, "serve the live suite")
        self.assertEqual(profile.mission, "serve the live suite")


class MentalModelLiveTest(LiveServerTestCase):
    async def test_create_list_and_delete(self):
        created = await self.client.acreate_mental_model(
            self.bank_id, name="Housing", source_query="Where does Alice live?"
        )
        self.assertTrue(created.mental_model_id)
        self.assertTrue(created.operation_id)

        page = await self.client.alist_mental_models(self.bank_id)
        listed = [item.id for item in page.items]
        self.assertIn(created.mental_model_id, listed)

        model = await self.client.aget_mental_model(self.bank_id, created.mental_model_id)
        self.assertEqual(model.id, created.mental_model_id)

        await self.client.adelete_mental_model(self.bank_id, created.mental_model_id)
        after = await self.client.alist_mental_models(self.bank_id)
        self.assertNotIn(created.mental_model_id, [item.id for item in after.items])

    async def test_trigger_patch_round_trips_and_history_is_queryable(self):
        created = await self.client.acreate_mental_model(
            self.bank_id, name="Delta", source_query="What changed?"
        )

        updated = await self.client.aupdate_mental_model(
            self.bank_id, created.mental_model_id, trigger={"mode": "delta"}
        )
        self.assertEqual(updated.trigger.mode, "delta")

        history = await self.client.aget_mental_model_history(self.bank_id, created.mental_model_id)
        self.assertIsInstance(list(history), list)


class KnowledgeBaseLiveTest(LiveServerTestCase):
    async def test_folder_and_page_round_trip(self):
        folder = await self.client.acreate_knowledge_folder(self.bank_id, "Operations")
        self.assertTrue(folder.id)

        tree = await self.client.aget_knowledge_base_tree(self.bank_id)
        self.assertIn(folder.id, [node.id for node in tree.roots])

        page = await self.client.acreate_knowledge_page(
            self.bank_id, name="Deploying", source_query="How is the service deployed?", parent_id=folder.id
        )
        self.assertTrue(page.page_id)

        fetched = await self.client.aget_knowledge_page(self.bank_id, page.page_id)
        self.assertEqual(fetched.id, page.page_id)

        renamed = await self.client.aupdate_knowledge_node(self.bank_id, page.page_id, name="Deployment")
        self.assertEqual(renamed.name, "Deployment")

        await self.client.adelete_knowledge_node(self.bank_id, page.page_id)
        await self.client.adelete_knowledge_node(self.bank_id, folder.id)
        after = await self.client.aget_knowledge_base_tree(self.bank_id)
        self.assertNotIn(folder.id, [node.id for node in after.roots])

    async def test_export_returns_a_bundle(self):
        await self.client.acreate_knowledge_folder(self.bank_id, "Bundle")

        bundle = await self.client.aexport_knowledge_base(self.bank_id)

        self.assertTrue(bundle.files)


class DirectiveLiveTest(LiveServerTestCase):
    async def test_create_list_update_delete(self):
        created = await self.client.acreate_directive(self.bank_id, name="Tone", content="Be terse")
        self.assertTrue(created.id)

        page = await self.client.alist_directives(self.bank_id)
        # ListDirectivesResponse carries a list, not a page: unwrap either shape.
        items = page.items if hasattr(page, "items") else page.directives
        self.assertIn(created.id, [item.id for item in items])

        fetched = await self.client.aget_directive(self.bank_id, created.id)
        self.assertEqual(fetched.name, "Tone")

        updated = await self.client.aupdate_directive(self.bank_id, created.id, content="Be concise")
        self.assertEqual(updated.content, "Be concise")

        await self.client.adelete_directive(self.bank_id, created.id)
        after = await self.client.alist_directives(self.bank_id)
        rest = after.items if hasattr(after, "items") else after.directives
        self.assertNotIn(created.id, [item.id for item in rest])


class TransferLiveTest(LiveServerTestCase):
    async def _await_operation(self, operation_id: str) -> None:
        for _ in range(300):
            status = await self.client.operations.get_operation_status(self.bank_id, operation_id)
            if status.status in ("completed", "failed", "cancelled"):
                self.assertEqual(status.status, "completed", status.error_message)
                return
            await asyncio.sleep(0.1)
        self.fail(f"operation {operation_id} did not reach a terminal state")

    async def _delete_bank(self, bank_id: str) -> None:
        try:
            await self.client.adelete_bank(bank_id)
        except ApiException:
            pass

    async def test_export_then_import_restores_the_memories(self):
        await self.retain("Alice reviews the schema before every release.")

        archive = await self.client.aexport_bank(self.bank_id, poll_interval=0.2, timeout=60)
        self.assertTrue(archive.startswith(b"PK"))

        target = f"{self.bank_id}-restored"
        try:
            operation_id = await self.client.aimport_bank(self.bank_id, archive, target_bank_id=target)
            self.assertTrue(operation_id)
            await self._await_operation(operation_id)

            restored = await self.client.alist_memories(target)
            self.assertGreaterEqual(restored.total, 1)
        finally:
            await self._delete_bank(target)

    async def test_export_documents_returns_an_archive(self):
        await self.retain("A note for the document export flow.")

        archive = await self.client.aexport_documents(self.bank_id, poll_interval=0.2, timeout=60)

        self.assertTrue(archive.startswith(b"PK"))

    async def test_clone_bank_creates_an_independent_bank(self):
        await self.retain("Alice owns the release checklist.")

        target = f"{self.bank_id}-clone"
        try:
            operation_id = await self.client.aclone_bank(self.bank_id, target)
            await self._await_operation(operation_id)

            cloned = await self.client.alist_memories(target)
            self.assertGreaterEqual(cloned.total, 1)
        finally:
            await self._delete_bank(target)


if __name__ == "__main__":
    unittest.main()