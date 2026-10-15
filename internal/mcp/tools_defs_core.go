package mcp

// Code generated from the reference MCP tool definitions. DO NOT EDIT.

// coreTools is the core tool roster in registration order.
func coreTools() []tool {
	return []tool{
		{
			Name:        "retain",
			Description: "Store important information to long-term memory.\n\nUse this tool PROACTIVELY whenever the user shares:\n- Personal facts, preferences, or interests\n- Important events or milestones\n- User history, experiences, or background\n- Decisions, opinions, or stated preferences\n- Goals, plans, or future intentions\n- Relationships or people mentioned\n- Work context, projects, or responsibilities",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"anyOf":   []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default": nil,
					},
					"content": map[string]any{"type": "string"},
					"context": map[string]any{"default": "general", "type": "string"},
					"document_id": map[string]any{
						"anyOf":   []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default": nil,
					},
					"metadata": map[string]any{
						"anyOf": []any{
							map[string]any{"additionalProperties": map[string]any{"type": "string"}, "type": "object"},
							map[string]any{"type": "null"},
						},
						"default": nil,
					},
					"strategy": map[string]any{
						"anyOf":   []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default": nil,
					},
					"tags": map[string]any{
						"anyOf": []any{
							map[string]any{"items": map[string]any{"type": "string"}, "type": "array"},
							map[string]any{"type": "null"},
						},
						"default": nil,
					},
					"timestamp": map[string]any{
						"anyOf":   []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default": nil,
					},
					"update_mode": map[string]any{
						"anyOf":   []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default": nil,
					},
				},
				"required": []any{"content"},
				"type":     "object",
			},
		},
		{
			Name:        "sync_retain",
			Description: "Store information to long-term memory and wait for completion.\n\nUnlike retain (which is asynchronous), this tool blocks until the memory\nis fully stored and immediately available for recall.",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional bank to store in (defaults to session bank). Use for cross-bank operations.",
					},
					"content": map[string]any{
						"description": "The fact/memory to store (be specific and include relevant details)",
						"type":        "string",
					},
					"context": map[string]any{
						"default":     "general",
						"description": "Category for the memory (e.g., 'preferences', 'work', 'hobbies', 'family'). Default: 'general'",
						"type":        "string",
					},
					"document_id": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional document ID to associate this memory with",
					},
					"metadata": map[string]any{
						"anyOf": []any{
							map[string]any{"additionalProperties": map[string]any{"type": "string"}, "type": "object"},
							map[string]any{"type": "null"},
						},
						"default":     nil,
						"description": "Optional key-value metadata to attach (e.g., {'source': 'slack', 'channel': 'general'})",
					},
					"strategy": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional named retain strategy (e.g., 'exact' for verbatim storage). Strategies are defined in the bank config.",
					},
					"tags": map[string]any{
						"anyOf": []any{
							map[string]any{"items": map[string]any{"type": "string"}, "type": "array"},
							map[string]any{"type": "null"},
						},
						"default":     nil,
						"description": "Optional tags for scoped visibility filtering (e.g., ['project:alpha', 'user:123'])",
					},
					"timestamp": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "When this event/fact occurred (ISO format, e.g., '2024-01-15T10:30:00Z'). Useful for timeline tracking.",
					},
				},
				"required": []any{"content"},
				"type":     "object",
			},
		},
		{
			Name:        "recall",
			Description: "Search memories to provide personalized, context-aware responses.\n\nUse this tool PROACTIVELY to:\n- Check user's preferences before making suggestions\n- Recall user's history to provide continuity\n- Remember user's goals and context\n- Personalize responses based on past interactions",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"anyOf":   []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default": nil,
					},
					"budget":     map[string]any{"default": "high", "type": "string"},
					"max_tokens": map[string]any{"default": 4096, "type": "integer"},
					"min_scores": map[string]any{
						"anyOf": []any{
							map[string]any{"additionalProperties": true, "type": "object"},
							map[string]any{"type": "null"},
						},
						"default": nil,
					},
					"prefer_observations": map[string]any{"default": false, "type": "boolean"},
					"query":               map[string]any{"type": "string"},
					"query_timestamp": map[string]any{
						"anyOf":   []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default": nil,
					},
					"tag_groups": map[string]any{
						"anyOf": []any{
							map[string]any{
								"items": map[string]any{"additionalProperties": true, "type": "object"},
								"type":  "array",
							},
							map[string]any{"type": "null"},
						},
						"default": nil,
					},
					"tags": map[string]any{
						"anyOf": []any{
							map[string]any{"items": map[string]any{"type": "string"}, "type": "array"},
							map[string]any{"type": "null"},
						},
						"default": nil,
					},
					"tags_match": map[string]any{"default": "any", "type": "string"},
					"temporal_window": map[string]any{
						"anyOf": []any{
							map[string]any{"additionalProperties": true, "type": "object"},
							map[string]any{"type": "null"},
						},
						"default": nil,
					},
					"types": map[string]any{
						"anyOf": []any{
							map[string]any{"items": map[string]any{"type": "string"}, "type": "array"},
							map[string]any{"type": "null"},
						},
						"default": nil,
					},
				},
				"required": []any{"query"},
				"type":     "object",
			},
		},
		{
			Name:        "reflect",
			Description: "Generate thoughtful analysis by synthesizing stored memories with the bank's personality.\n\nWHEN TO USE THIS TOOL:\nUse reflect when you need reasoned analysis, not just fact retrieval. This tool\nthinks through the question using everything the bank knows and its personality traits.\n\nEXAMPLES OF GOOD QUERIES:\n- \"What patterns have emerged in how I approach debugging?\"\n- \"Based on my past decisions, what architectural style do I prefer?\"\n- \"What might be the best approach for this problem given what you know about me?\"\n- \"How should I prioritize these tasks based on my goals?\"\n\nHOW IT DIFFERS FROM RECALL:\n- recall: Returns raw facts matching your search (fast lookup)\n- reflect: Reasons across memories to form a synthesized answer (deeper analysis)\n\nUse recall for \"what did I say about X?\" and reflect for \"what should I do about X?\"",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"apply_all_directives": map[string]any{
						"default":     false,
						"description": "Apply every active directive regardless of tags. By default directives are scoped like memories (untagged always apply; tagged apply only when tags match). Set true to apply all directives, ignoring tag scope.",
						"type":        "boolean",
					},
					"bank_id": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional bank to reflect in (defaults to session bank). Use for cross-bank operations.",
					},
					"budget": map[string]any{
						"default":     "low",
						"description": "Search budget - 'low', 'mid', or 'high' (default: 'low')",
						"type":        "string",
					},
					"context": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional context about why this reflection is needed",
					},
					"include_based_on": map[string]any{
						"default":     false,
						"description": "Include source facts used for synthesis. Defaults to false because broad reflections can exceed MCP client result limits.",
						"type":        "boolean",
					},
					"include_trace": map[string]any{
						"default":     false,
						"description": "Include the reflection's internal trace fields (tool_trace/llm_trace and directives_applied). Defaults to false because the trace can be tens of KB and overflow MCP client context; enable only for debugging.",
						"type":        "boolean",
					},
					"max_tokens": map[string]any{
						"default":     4096,
						"description": "Maximum tokens for the response (default: 4096)",
						"type":        "integer",
					},
					"query": map[string]any{"description": "The question or topic to reflect on", "type": "string"},
					"response_schema": map[string]any{
						"anyOf": []any{
							map[string]any{"additionalProperties": true, "type": "object"},
							map[string]any{"type": "null"},
						},
						"default":     nil,
						"description": "Optional JSON schema for structured output. When provided, the response includes a 'structured_output' field.",
					},
					"tags": map[string]any{
						"anyOf": []any{
							map[string]any{"items": map[string]any{"type": "string"}, "type": "array"},
							map[string]any{"type": "null"},
						},
						"default":     nil,
						"description": "Optional tags to filter memories by (e.g., ['project:alpha'])",
					},
					"tags_match": map[string]any{
						"default":     "any",
						"description": "How to match tags - 'any' (match any tag) or 'all' (match all tags). Default: 'any'",
						"type":        "string",
					},
				},
				"required": []any{"query"},
				"type":     "object",
			},
		},
		{
			Name:        "list_banks",
			Description: "List available memory banks, most recently written first.\n\nUse this tool to discover what memory banks exist in the system.\nEach bank is an isolated memory store (like a separate \"brain\").",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"limit": map[string]any{
						"default":     100,
						"description": "Maximum number of banks to return (default 100).",
						"minimum":     0,
						"type":        "integer",
					},
					"offset": map[string]any{
						"default":     0,
						"description": "Number of banks to skip, for paging through `total`.",
						"minimum":     0,
						"type":        "integer",
					},
					"query": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional case-insensitive substring to match against bank ID and name.",
					},
				},
				"type": "object",
			},
		},
		{
			Name:        "create_bank",
			Description: "Create a new memory bank or get an existing one.\n\nMemory banks are isolated stores - each one is like a separate \"brain\" for a user/agent.\nBanks are auto-created with default settings if they don't exist.",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"description": "Unique identifier for the bank (e.g., 'user-123', 'agent-alpha')",
						"type":        "string",
					},
					"mission": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional mission describing who the agent is and what they're trying to accomplish",
					},
					"name": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional human-friendly name for the bank",
					},
				},
				"required": []any{"bank_id"},
				"type":     "object",
			},
		},
	}
}
