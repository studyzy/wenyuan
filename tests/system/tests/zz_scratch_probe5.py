from __future__ import annotations
import json
import pytest
from hindsight_system_tests.payloads import consolidation, extracted, fact

pytestmark = pytest.mark.asyncio
QUERY = "Where does Alice live."


@pytest.mark.parametrize("ents,content", [
    (["Alice"], "Alice moved to Berlin in 2021."),
    (["Alice", "Berlin"], "Alice moved to Berlin in 2021."),
    (["Alice"], "Alice moved to Berlin in 2021 and works as a cellist."),
    (["Alice", "Berlin"], "Alice moved to Berlin in 2021 and works as a cellist."),
])
async def test_ent(client, llm, bank_id, settled, ents, content):
    llm.on_step("extract_facts").returns(
        extracted(fact("Alice moved to Berlin in 2021", who="Alice", entities=ents))
    )
    llm.on_step("consolidate").returns(consolidation())
    await client.aretain(bank_id=bank_id, content=content)
    await settled(bank_id)
    response = await client.arecall(bank_id=bank_id, query=QUERY, trace=True)
    print("ENT", ents, repr(content), json.dumps({r.text: {"semantic": r.scores.semantic, "reranker": r.scores.reranker} for r in response.results}))
