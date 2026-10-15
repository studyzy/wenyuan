from __future__ import annotations
import json
import pytest
from hindsight_system_tests.payloads import consolidation, extracted, fact

pytestmark = pytest.mark.asyncio
QUERY = "Where does Alice live?"

FACTS = {
    "A": ("Alice lives in Berlin", "Alice"),
    "B": ("Alice moved to Berlin in 2021", "Alice"),
    "C": ("Alice plays the cello professionally", "Alice"),
    "D": ("Bob moved to Paris", "Bob"),
}

SETS = ["A", "B", "C", "D", "AB", "AC", "BC", "ABC", "ABCD"]


@pytest.mark.parametrize("keys", SETS)
async def test_set(client, llm, bank_id, settled, keys):
    extras = [FACTS[k] for k in keys]
    llm.on_step("extract_facts").returns(extracted(*[fact(w, who=who, entities=[who]) for w, who in extras]))
    llm.on_step("consolidate").returns(consolidation())
    await client.aretain(bank_id=bank_id, content=" ".join(w for w, _ in extras))
    await settled(bank_id)
    response = await client.arecall(bank_id=bank_id, query=QUERY, trace=True)
    out = {r.text: {"semantic": r.scores.semantic, "reranker": r.scores.reranker} for r in response.results}
    open("/tmp/ref_sets.jsonl", "a").write(json.dumps({"keys": keys, "out": out}) + "\n")
    print("SET", keys, json.dumps(out))
