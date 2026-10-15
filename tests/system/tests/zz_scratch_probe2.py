from __future__ import annotations
import json
import pytest
from hindsight_system_tests.payloads import consolidation, extracted, fact

pytestmark = pytest.mark.asyncio
QUERY = "Where does Alice live?"

FACTS = [
    ("Alice moved to Berlin in 2021", ["Alice", "Berlin"]),
    ("Alice plays the cello professionally", ["Alice", "cello"]),
    ("Alice lives in Berlin", ["Alice", "Berlin"]),
    ("Bob moved to Paris", ["Bob", "Paris"]),
    ("Alice", []),
    ("Alice is a cellist who lives in Berlin and loves music", ["Alice", "Berlin", "cello"]),
]


async def test_probe_matrix(client, llm, bank_id, settled):
    llm.on_step("extract_facts").returns(
        extracted(*[fact(t, who="Alice", entities=e) for t, e in FACTS])
    )
    llm.on_step("consolidate").returns(consolidation())
    await client.aretain(bank_id=bank_id, content="Alice moved to Berlin in 2021 and works as a cellist.")
    await settled(bank_id)
    response = await client.arecall(bank_id=bank_id, query=QUERY, trace=True)
    out = []
    for r in response.results:
        out.append({"text": r.text, "semantic": r.scores.semantic, "reranker": r.scores.reranker})
    open("/tmp/ref_matrix.json", "w").write(json.dumps(out))
    print("DUMPED", len(out))
