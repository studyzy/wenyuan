"""The recall-response conveniences installed by the wrapper package.

These are attached to the generated classes at import of ``wenyuan_client``, so
importing the package is what opts a caller into ``to_prompt_string``, iteration,
length, indexing and the short reprs.
"""

from __future__ import annotations

import json
import pathlib
import sys
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))

import wenyuan_client  # noqa: E402  (importing it installs the conveniences)
from wenyuan_client import RecallResponse, iter_results, result_at, result_count, to_prompt_string  # noqa: E402


def _response(payload: dict) -> RecallResponse:
    return RecallResponse.from_dict(payload)


class PromptStringTest(unittest.TestCase):
    def test_facts_only(self):
        response = _response(
            {
                "results": [
                    {
                        "id": "1",
                        "text": "Alice works at Google",
                        "type": "world",
                        "context": "work",
                        "occurred_start": "2024-01-15T10:00:00Z",
                        "occurred_end": "2024-06-15T10:00:00Z",
                        "mentioned_at": "2024-03-01T09:00:00Z",
                    },
                    {"id": "2", "text": "The sky is blue", "type": "world"},
                ]
            }
        )
        prompt = response.to_prompt_string()
        self.assertTrue(prompt.startswith("FACTS:\n"))
        facts = json.loads(prompt[len("FACTS:\n") :])
        self.assertEqual(
            facts[0],
            {
                "text": "Alice works at Google",
                "context": "work",
                "occurred_start": "2024-01-15T10:00:00Z",
                "occurred_end": "2024-06-15T10:00:00Z",
                "mentioned_at": "2024-03-01T09:00:00Z",
            },
        )
        self.assertEqual(facts[1], {"text": "The sky is blue"})

    def test_source_chunk_is_included(self):
        response = _response(
            {
                "results": [{"id": "1", "text": "Alice works at Google", "type": "world", "chunk_id": "chunk_1"}],
                "chunks": {
                    "chunk_1": {"id": "chunk_1", "text": "Alice works at Google on the AI team.", "chunk_index": 0}
                },
            }
        )
        facts = json.loads(response.to_prompt_string()[len("FACTS:\n") :])
        self.assertEqual(facts[0]["source_chunk"], "Alice works at Google on the AI team.")

    def test_unknown_chunk_id_is_ignored(self):
        response = _response(
            {"results": [{"id": "1", "text": "Some fact", "type": "world", "chunk_id": "missing"}]}
        )
        facts = json.loads(response.to_prompt_string()[len("FACTS:\n") :])
        self.assertNotIn("source_chunk", facts[0])

    def test_entities_section(self):
        response = _response(
            {
                "results": [{"id": "1", "text": "Alice works at Google", "type": "world"}],
                "entities": {
                    "Alice": {"entity_id": "e1", "canonical_name": "Alice", "observations": [{"text": "Alice is senior."}]}
                },
            }
        )
        prompt = response.to_prompt_string()
        self.assertIn("ENTITIES:", prompt)
        self.assertIn("## Alice\nAlice is senior.", prompt)

    def test_empty_results(self):
        self.assertEqual(_response({"results": []}).to_prompt_string(), "FACTS:\n[]")

    def test_module_level_helper_matches_the_method(self):
        response = _response({"results": [{"id": "1", "text": "hi", "type": "world"}]})
        self.assertEqual(to_prompt_string(response), response.to_prompt_string())


class IterationTest(unittest.TestCase):
    def _two(self) -> RecallResponse:
        return _response(
            {
                "results": [
                    {"id": "1", "text": "first", "type": "world"},
                    {"id": "2", "text": "second", "type": "world"},
                ]
            }
        )

    def test_iteration(self):
        self.assertEqual([result.text for result in self._two()], ["first", "second"])
        self.assertEqual([result.text for result in iter_results(self._two())], ["first", "second"])

    def test_length(self):
        self.assertEqual(len(self._two()), 2)
        self.assertEqual(len(_response({"results": []})), 0)
        self.assertEqual(result_count(self._two()), 2)

    def test_indexing(self):
        response = self._two()
        self.assertEqual(response[0].text, "first")
        self.assertEqual(response[1].text, "second")
        self.assertEqual(result_at(response, 1).text, "second")

    def test_reprs(self):
        response = self._two()
        self.assertEqual(repr(response), "RecallResponse(2 results)")
        self.assertIn("RecallResult(", repr(response[0]))
        self.assertIn("first", repr(response[0]))

    def test_response_repr_counts_extras(self):
        response = _response(
            {
                "results": [{"id": "1", "text": "hi", "type": "world", "chunk_id": "c1"}],
                "trace": {"steps": []},
                "chunks": {"c1": {"id": "c1", "text": "chunk", "chunk_index": 0}},
            }
        )
        self.assertEqual(repr(response), "RecallResponse(1 results, trace=True, chunks=1)")


if __name__ == "__main__":
    unittest.main()