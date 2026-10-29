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
						"anyOf": []any{
							map[string]any{
								"type": "string",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"content": map[string]any{
						"type": "string",
					},
					"context": map[string]any{
						"default": "general",
						"type":    "string",
					},
					"document_id": map[string]any{
						"anyOf": []any{
							map[string]any{
								"type": "string",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"metadata": map[string]any{
						"anyOf": []any{
							map[string]any{
								"additionalProperties": map[string]any{
									"type": "string",
								},
								"type": "object",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"strategy": map[string]any{
						"anyOf": []any{
							map[string]any{
								"type": "string",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"tags": map[string]any{
						"anyOf": []any{
							map[string]any{
								"items": map[string]any{
									"type": "string",
								},
								"type": "array",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"timestamp": map[string]any{
						"anyOf": []any{
							map[string]any{
								"type": "string",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"update_mode": map[string]any{
						"anyOf": []any{
							map[string]any{
								"type": "string",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
				},
				"required": []any{
					"content",
				},
				"type": "object",
			},
		},
		{
			Name:        "sync_retain",
			Description: "Store information to long-term memory and wait for completion.\n\nUnlike retain (which is asynchronous), this tool blocks until the memory\nis fully stored and immediately available for recall.\n\nArgs:\n    content: The fact/memory to store (be specific and include relevant details)\n    context: Category for the memory (e.g., 'preferences', 'work', 'hobbies', 'family'). Default: 'general'\n    timestamp: When this event/fact occurred (ISO format, e.g., '2024-01-15T10:30:00Z'). Useful for timeline tracking.\n    tags: Optional tags for scoped visibility filtering (e.g., ['project:alpha', 'user:123'])\n    metadata: Optional key-value metadata to attach (e.g., {'source': 'slack', 'channel': 'general'})\n    document_id: Optional document ID to associate this memory with\n    bank_id: Optional bank to store in (defaults to session bank). Use for cross-bank operations.\n    strategy: Optional named retain strategy (e.g., 'exact' for verbatim storage). Strategies are defined in the bank config.",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"anyOf": []any{
							map[string]any{
								"type": "string",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"content": map[string]any{
						"type": "string",
					},
					"context": map[string]any{
						"default": "general",
						"type":    "string",
					},
					"document_id": map[string]any{
						"anyOf": []any{
							map[string]any{
								"type": "string",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"metadata": map[string]any{
						"anyOf": []any{
							map[string]any{
								"additionalProperties": map[string]any{
									"type": "string",
								},
								"type": "object",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"strategy": map[string]any{
						"anyOf": []any{
							map[string]any{
								"type": "string",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"tags": map[string]any{
						"anyOf": []any{
							map[string]any{
								"items": map[string]any{
									"type": "string",
								},
								"type": "array",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"timestamp": map[string]any{
						"anyOf": []any{
							map[string]any{
								"type": "string",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
				},
				"required": []any{
					"content",
				},
				"type": "object",
			},
		},
		{
			Name:        "recall",
			Description: "Search memories to provide personalized, context-aware responses.\n\nUse this tool PROACTIVELY to:\n- Check user's preferences before making suggestions\n- Recall user's history to provide continuity\n- Remember user's goals and context\n- Personalize responses based on past interactions",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"anyOf": []any{
							map[string]any{
								"type": "string",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"budget": map[string]any{
						"default": "high",
						"type":    "string",
					},
					"include_chunks": map[string]any{
						"default": false,
						"type":    "boolean",
					},
					"include_entities": map[string]any{
						"default": false,
						"type":    "boolean",
					},
					"include_source_facts": map[string]any{
						"default": false,
						"type":    "boolean",
					},
					"max_chunk_tokens": map[string]any{
						"default": 8192,
						"type":    "integer",
					},
					"max_entity_tokens": map[string]any{
						"default": 500,
						"type":    "integer",
					},
					"max_source_facts_tokens": map[string]any{
						"default": 4096,
						"type":    "integer",
					},
					"max_tokens": map[string]any{
						"default": 4096,
						"type":    "integer",
					},
					"min_scores": map[string]any{
						"anyOf": []any{
							map[string]any{
								"additionalProperties": true,
								"type":                 "object",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"prefer_observations": map[string]any{
						"anyOf": []any{
							map[string]any{"type": "boolean"},
							map[string]any{"type": "null"},
						},
						"default": nil,
					},
					"query": map[string]any{
						"type": "string",
					},
					"query_timestamp": map[string]any{
						"anyOf": []any{
							map[string]any{
								"type": "string",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"tag_groups": map[string]any{
						"anyOf": []any{
							map[string]any{
								"items": map[string]any{
									"additionalProperties": true,
									"type":                 "object",
								},
								"type": "array",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"tags": map[string]any{
						"anyOf": []any{
							map[string]any{
								"items": map[string]any{
									"type": "string",
								},
								"type": "array",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"tags_match": map[string]any{
						"default": "any",
						"enum": []any{
							"any",
							"all",
							"any_strict",
							"all_strict",
							"exact",
						},
						"type": "string",
					},
					"temporal_window": map[string]any{
						"anyOf": []any{
							map[string]any{
								"additionalProperties": true,
								"type":                 "object",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"types": map[string]any{
						"anyOf": []any{
							map[string]any{
								"items": map[string]any{
									"type": "string",
								},
								"type": "array",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
				},
				"required": []any{
					"query",
				},
				"type": "object",
			},
		},
		{
			Name:        "reflect",
			Description: "Generate thoughtful analysis by synthesizing stored memories with the bank's personality.\n\nWHEN TO USE THIS TOOL:\nUse reflect when you need reasoned analysis, not just fact retrieval. This tool\nthinks through the question using everything the bank knows and its personality traits.\n\nEXAMPLES OF GOOD QUERIES:\n- \"What patterns have emerged in how I approach debugging?\"\n- \"Based on my past decisions, what architectural style do I prefer?\"\n- \"What might be the best approach for this problem given what you know about me?\"\n- \"How should I prioritize these tasks based on my goals?\"\n\nHOW IT DIFFERS FROM RECALL:\n- recall: Returns raw facts matching your search (fast lookup)\n- reflect: Reasons across memories to form a synthesized answer (deeper analysis)\n\nUse recall for \"what did I say about X?\" and reflect for \"what should I do about X?\"\n\nArgs:\n    query: The question or topic to reflect on\n    context: Optional context about why this reflection is needed\n    budget: Search budget - 'low', 'mid', or 'high' (default: 'low')\n    max_tokens: Maximum tokens for the response (default: 4096)\n    response_schema: Optional JSON schema for structured output. When provided, the response includes a 'structured_output' field.\n    tags: Optional tags to filter memories by (e.g., ['project:alpha'])\n    tags_match: How to match tags - 'any' (any tag, untagged included), 'all' (all tags, untagged included),\n        'any_strict' / 'all_strict' (same, untagged excluded) or 'exact' (exactly these tags). Default: 'any'\n    apply_all_directives: Apply every active directive regardless of tags. By default directives are scoped like memories (untagged always apply; tagged apply only when tags match). Set true to apply all directives, ignoring tag scope.\n    include_based_on: Include source facts used for synthesis. Defaults to false because broad reflections can exceed MCP client result limits.\n    include_trace: Include the reflection's internal trace fields (tool_trace/llm_trace and directives_applied). Defaults to false because the trace can be tens of KB and overflow MCP client context; enable only for debugging.\n    bank_id: Optional bank to reflect in (defaults to session bank). Use for cross-bank operations.",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"apply_all_directives": map[string]any{
						"default": false,
						"type":    "boolean",
					},
					"bank_id": map[string]any{
						"anyOf": []any{
							map[string]any{
								"type": "string",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"budget": map[string]any{
						"default": "low",
						"type":    "string",
					},
					"context": map[string]any{
						"anyOf": []any{
							map[string]any{
								"type": "string",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"include_based_on": map[string]any{
						"default": false,
						"type":    "boolean",
					},
					"include_trace": map[string]any{
						"default": false,
						"type":    "boolean",
					},
					"max_tokens": map[string]any{
						"default": 4096,
						"type":    "integer",
					},
					"query": map[string]any{
						"type": "string",
					},
					"response_schema": map[string]any{
						"anyOf": []any{
							map[string]any{
								"additionalProperties": true,
								"type":                 "object",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"tags": map[string]any{
						"anyOf": []any{
							map[string]any{
								"items": map[string]any{
									"type": "string",
								},
								"type": "array",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"tags_match": map[string]any{
						"default": "any",
						"enum": []any{
							"any",
							"all",
							"any_strict",
							"all_strict",
							"exact",
						},
						"type": "string",
					},
				},
				"required": []any{
					"query",
				},
				"type": "object",
			},
		},
		{
			Name:        "list_banks",
			Description: "List available memory banks, most recently written first.\n\nUse this tool to discover what memory banks exist in the system.\nEach bank is an isolated memory store (like a separate \"brain\").\n\nArgs:\n    query: Optional case-insensitive substring to match against bank ID and name.\n    limit: Maximum number of banks to return (default 100).\n    offset: Number of banks to skip, for paging through `total`.\n\nReturns:\n    JSON with the page of banks (IDs, names, dispositions, missions) plus\n    the total number of matching banks and the limit/offset used.",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"limit": map[string]any{
						"default": 100,
						"minimum": 0,
						"type":    "integer",
					},
					"offset": map[string]any{
						"default": 0,
						"minimum": 0,
						"type":    "integer",
					},
					"query": map[string]any{
						"anyOf": []any{
							map[string]any{
								"type": "string",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
				},
				"type": "object",
			},
		},
		{
			Name:        "create_bank",
			Description: "Create a new memory bank or get an existing one.\n\nMemory banks are isolated stores - each one is like a separate \"brain\" for a user/agent.\nBanks are auto-created with default settings if they don't exist.\n\nArgs:\n    bank_id: Unique identifier for the bank (e.g., 'user-123', 'agent-alpha')\n    name: Optional human-friendly name for the bank\n    mission: Optional mission describing who the agent is and what they're trying to accomplish",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"type": "string",
					},
					"mission": map[string]any{
						"anyOf": []any{
							map[string]any{
								"type": "string",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"name": map[string]any{
						"anyOf": []any{
							map[string]any{
								"type": "string",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
				},
				"required": []any{
					"bank_id",
				},
				"type": "object",
			},
		},
	}
}
