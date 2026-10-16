"""Recall-response conveniences, installed from the wrapper package.

A recall response already carries everything a caller needs to build a prompt —
the matched facts, the chunks they came from and the entity summaries — but it
arrives as a plain generated model, so reaching into it means walking
``response.results`` and re-assembling the same projection by hand every time.

This module supplies that projection. The functions are ordinary module-level
helpers, and :func:`install` attaches them to the generated ``RecallResponse``
and ``RecallResult`` classes when :mod:`wenyuan_client` is imported, so the
observable surface is::

    response.to_prompt_string()   # facts (+ source chunks) and entity sections
    for result in response: ...   # iterate the results
    len(response)                 # number of results
    response[0]                   # index into the results
    repr(response), repr(result)  # short, readable previews

The generated package itself is never edited: the attribute assignment happens
here, at import time, which is why a caller that only imports
``wenyuan_client_api`` sees the bare models. Importing :mod:`wenyuan_client` is
what opts into the conveniences.
"""

from __future__ import annotations

import json
from typing import Iterator

from wenyuan_client_api.models.recall_response import RecallResponse
from wenyuan_client_api.models.recall_result import RecallResult


def recall_result_repr(result: RecallResult) -> str:
    """A short, single-line preview of one recall result."""
    text = result.text or ""
    preview = text[:80] + "..." if len(text) > 80 else text
    identifier = result.id[:8] + "..." if result.id and len(result.id) > 8 else result.id
    return f"RecallResult(id='{identifier}', type='{result.type}', text='{preview}')"


def recall_response_repr(response: RecallResponse) -> str:
    """A short summary of a recall response and its optional extras."""
    count = len(response.results) if response.results else 0
    extras = []
    if response.trace:
        extras.append("trace=True")
    if response.entities:
        extras.append(f"entities={len(response.entities)}")
    if response.chunks:
        extras.append(f"chunks={len(response.chunks)}")
    extras_str = ", " + ", ".join(extras) if extras else ""
    return f"RecallResponse({count} results{extras_str})"


def iter_results(response: RecallResponse) -> Iterator[RecallResult]:
    """Iterate the response's results, treating a missing list as empty."""
    return iter(response.results or [])


def result_count(response: RecallResponse) -> int:
    """Return the number of results."""
    return len(response.results) if response.results else 0


def result_at(response: RecallResponse, index: int) -> RecallResult:
    """Access one result by index."""
    return response.results[index]


def to_prompt_string(response: RecallResponse) -> str:
    """Serialise a recall response to a string suitable for an LLM prompt.

    Builds two sections:

    * ``FACTS:`` — each result as a JSON object carrying ``text``, ``context``
      and the temporal fields (``occurred_start``, ``occurred_end``,
      ``mentioned_at``). When a result's ``chunk_id`` matches a chunk in the
      response, that chunk's text is included as ``source_chunk``.
    * ``ENTITIES:`` — one ``## name`` section per entity that carries an
      observation.
    """
    chunks_map = response.chunks or {}
    sections: list[str] = []

    formatted_facts: list[dict] = []
    for result in response.results or []:
        fact: dict = {"text": result.text}
        if result.context:
            fact["context"] = result.context
        for field in ("occurred_start", "occurred_end", "mentioned_at"):
            value = getattr(result, field, None)
            if value:
                fact[field] = value
        if result.chunk_id and result.chunk_id in chunks_map:
            fact["source_chunk"] = chunks_map[result.chunk_id].text
        formatted_facts.append(fact)
    sections.append("FACTS:\n" + json.dumps(formatted_facts, indent=2))

    if response.entities:
        entity_parts: list[str] = []
        for name, state in response.entities.items():
            observations = getattr(state, "observations", None)
            if observations:
                entity_parts.append(f"## {name}\n{observations[0].text}")
        if entity_parts:
            sections.append("ENTITIES:\n" + "\n\n".join(entity_parts))

    return "\n\n".join(sections)


def install() -> None:
    """Attach the conveniences to the generated recall classes.

    Idempotent, and safe to call whether or not the classes already carry the
    attributes (re-importing :mod:`wenyuan_client` does not re-wrap anything).
    """
    RecallResult.__repr__ = recall_result_repr
    RecallResponse.__repr__ = recall_response_repr
    RecallResponse.__iter__ = iter_results
    RecallResponse.__len__ = result_count
    RecallResponse.__getitem__ = result_at
    RecallResponse.to_prompt_string = to_prompt_string