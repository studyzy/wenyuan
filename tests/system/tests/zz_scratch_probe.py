from __future__ import annotations

import pytest

from hindsight_system_tests.payloads import consolidation, extracted, fact

pytestmark = pytest.mark.asyncio

QUERY = "Where does Alice live?"


async def test_probe_scores(client, llm, bank_id, settled):
    llm.on_step("extract_facts").returns(
        extracted(
            fact("Alice moved to Berlin in 2021", who="Alice", entities=["Alice", "Berlin"]),
            fact("Alice plays the cello professionally", who="Alice", entities=["Alice", "cello"]),
        )
    )
    llm.on_step("consolidate").returns(consolidation())
    await client.aretain(bank_id=bank_id, content="Alice moved to Berlin in 2021 and works as a cellist.")
    await settled(bank_id)

    response = await client.arecall(bank_id=bank_id, query=QUERY, trace=True)
    for r in response.results:
        print("RESULT", repr(r.text), "semantic=", r.scores.semantic, "keyword=", r.scores.keyword, "reranker=", r.scores.reranker, "final=", r.scores.final)
    import json
    print("TRACE", json.dumps(response.trace, default=str)[:4000])

import json as _json


async def test_probe_dump(client, llm, bank_id, settled):
    llm.on_step("extract_facts").returns(
        extracted(
            fact("Alice moved to Berlin in 2021", who="Alice", entities=["Alice", "Berlin"]),
            fact("Alice plays the cello professionally", who="Alice", entities=["Alice", "cello"]),
        )
    )
    llm.on_step("consolidate").returns(consolidation())
    await client.aretain(bank_id=bank_id, content="Alice moved to Berlin in 2021 and works as a cellist.")
    await settled(bank_id)
    response = await client.arecall(bank_id=bank_id, query=QUERY, trace=True)
    open("/tmp/ref_trace.json", "w").write(_json.dumps(response.trace, default=str))
    print("BANK", bank_id)
