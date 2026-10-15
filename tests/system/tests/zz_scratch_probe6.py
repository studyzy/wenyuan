from __future__ import annotations
import json
import pytest
from hindsight_system_tests.payloads import consolidation, extracted, fact

pytestmark = pytest.mark.asyncio
QUERY = "Where does Alice live?"

CASES = [
    ("Alice lives in Berlin", "Alice", ["Alice"]),
    ("Alice lives in Berlin", "Alice", ["Alice", "Berlin"]),
    ("Alice lives in Berlin", "Alice", ["Alice", "Berlin", "cello"]),
    ("Alice plays the cello professionally", "Alice", ["Alice"]),
    ("Alice plays the cello professionally", "Alice", ["Alice", "cello"]),
    ("Alice moved to Berlin in 2021", "Alice", []),
    ("Alice moved to Berlin in 2021", "Alice", ["Alice"]),
    ("Alice moved to Berlin in 2021", "Alice", ["Alice", "Berlin"]),
]


@pytest.mark.parametrize("what,who,ents", CASES)
async def test_case(client, llm, bank_id, settled, what, who, ents):
    llm.on_step("extract_facts").returns(extracted(fact(what, who=who, entities=ents)))
    llm.on_step("consolidate").returns(consolidation())
    await client.aretain(bank_id=bank_id, content=what + ".")
    await settled(bank_id)
    response = await client.arecall(bank_id=bank_id, query=QUERY, trace=True)
    rec = {"what": what, "ents": ents, "results": {r.text: {"semantic": r.scores.semantic, "reranker": r.scores.reranker} for r in response.results}}
    open("/tmp/ref_ents.jsonl", "a").write(json.dumps(rec) + "\n")
