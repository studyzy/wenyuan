from __future__ import annotations
import json
import pytest
from hindsight_system_tests.payloads import consolidation, extracted, fact

pytestmark = pytest.mark.asyncio
QUERY = "Where does Alice live?"


@pytest.mark.parametrize("occ,kind", [
    ("2019-03-15", "event"),
    (None, "event"),
    ("2019-03-15", "conversation"),
])
async def test_occ(client, llm, bank_id, settled, occ, kind):
    f = fact("Alice moved to Berlin in 2021", who="Alice", entities=["Alice", "Berlin"],
             occurred_start=occ, fact_kind=kind)
    llm.on_step("extract_facts").returns(extracted(f))
    llm.on_step("consolidate").returns(consolidation())
    await client.aretain(bank_id=bank_id, content="Alice moved to Berlin in 2021.")
    await settled(bank_id)
    response = await client.arecall(bank_id=bank_id, query=QUERY, trace=True)
    print("OCC", occ, kind, json.dumps({r.text: {"sem": r.scores.semantic, "occ_start": str(r.occurred_start)} for r in response.results}))
