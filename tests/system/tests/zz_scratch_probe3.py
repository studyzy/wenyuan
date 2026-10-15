from __future__ import annotations
import json
import pytest
from hindsight_system_tests.payloads import consolidation, extracted, fact

pytestmark = pytest.mark.asyncio
QUERY = "Where does Alice live?"


@pytest.mark.parametrize("what,who", [
    ("Alice plays the cello professionally", "Alice"),
    ("Bob moved to Paris", "Bob"),
])
async def test_single(client, llm, bank_id, settled, what, who):
    llm.on_step("extract_facts").returns(extracted(fact(what, who=who, entities=[who])))
    llm.on_step("consolidate").returns(consolidation())
    await client.aretain(bank_id=bank_id, content=what + ".")
    await settled(bank_id)
    response = await client.arecall(bank_id=bank_id, query=QUERY, trace=True)
    for r in response.results:
        print("SINGLE", repr(r.text), "semantic=", r.scores.semantic, "reranker=", r.scores.reranker, "keyword=", r.scores.keyword)
