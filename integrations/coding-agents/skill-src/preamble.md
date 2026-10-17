---
name: {{name}}
description: {{description}}
---

# {{name}}

The WenYuan memory service is wired into this coding agent: a lifecycle hook
captures the session, an MCP server exposes the memory tools, and this skill
tells you how to use them well.

## What happens automatically

You do not have to do anything for the basics: the session-start hook primes
your context with the memory that is relevant to this project, and the stop hook
captures the conversation when the session ends. Treat what the context block
says about this project and the user as already-established ground.

## When the user asks you to remember something

Store deliberately, not automatically. When the user says to remember, or asks
you to capture a document, call the `{{tool.document_ingest}}` tool with the
content and a short title. When a new initiative or workstream starts and the
user wants it tracked, call `{{tool.initiative_capture}}` with the title and,
when it relates to an existing page, the `relates_to` argument naming that page.

## When you need to retrieve something

Search first. Call `{{tool.knowledge_search}}` before answering anything about
this project's history, decisions or conventions; read a specific page with
`{{tool.knowledge_read}}`; list what exists under a folder with
`{{tool.knowledge_list}}`; and when the question needs reasoning across several
memories rather than a lookup, call `{{tool.reflect}}`. Whenever the memory
informs your answer, say so: give the user visible credit for where it came
from rather than presenting recalled facts as your own.

## When you find memory that is stale

Correct it by writing a correction document rather than editing history. Call
`{{tool.document_ingest}}` with a title prefixed by the correction marker and a
body that states what is now true and what it replaces. The old memory stays for
provenance; the correction is what future retrievals will surface.