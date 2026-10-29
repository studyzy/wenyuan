package mcp

// Code generated from the reference MCP tool definitions. DO NOT EDIT.

// memoryTools is the directive, memory and document tool roster in registration order.
func memoryTools() []tool {
	return []tool{
		{
			Name:        "list_directives",
			Description: "List directives for a memory bank.\n\nDirectives are instructions that guide how the memory engine processes and\nresponds to queries. They influence reflect behavior and memory organization.\n\nArgs:\n    tags: Optional tags to filter by\n    active_only: If True, only return active directives (default: True)\n    limit: Maximum number of results, 1-1000 (default: 100)\n    offset: Pagination offset (default: 0). Page until the returned items add up to 'total'.\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"active_only": map[string]any{
						"default": true,
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
					"limit": map[string]any{
						"default": 100,
						"maximum": 1000,
						"minimum": 1,
						"type":    "integer",
					},
					"offset": map[string]any{
						"default": 0,
						"minimum": 0,
						"type":    "integer",
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
				},
				"type": "object",
			},
		},
		{
			Name:        "create_directive",
			Description: "Create a new directive for a memory bank.\n\nDirectives guide how the memory engine processes queries and generates reflections.\n\nArgs:\n    name: Human-readable name for the directive\n    content: The directive content/instructions\n    priority: Priority level (higher = more important, default: 0)\n    is_active: Whether the directive is active (default: True)\n    tags: Optional tags for filtering\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
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
					"is_active": map[string]any{
						"default": true,
						"type":    "boolean",
					},
					"name": map[string]any{
						"type": "string",
					},
					"priority": map[string]any{
						"default": 0,
						"type":    "integer",
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
				},
				"required": []any{
					"name",
					"content",
				},
				"type": "object",
			},
		},
		{
			Name:        "delete_directive",
			Description: "Delete a directive.\n\nPermanently removes a directive from the memory bank.\n\nArgs:\n    directive_id: The ID of the directive to delete\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
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
					"directive_id": map[string]any{
						"type": "string",
					},
				},
				"required": []any{
					"directive_id",
				},
				"type": "object",
			},
		},
		{
			Name:        "list_memories",
			Description: "Browse stored memories with optional filtering.\n\nLists memory units (facts) stored in the bank. Unlike recall, this is a direct\nbrowse/search without relevance ranking.\n\nArgs:\n    type: Filter by fact type: 'world', 'experience', or 'observation'\n    q: Optional text search query to filter memories\n    limit: Maximum number of results (default: 100)\n    offset: Pagination offset (default: 0)\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.\n    tags: Optional list of tag names to filter by.\n    tags_match: How to combine tags: 'any' (OR, default) or 'all' (AND)\n        both also include untagged memories; 'any_strict'/'all_strict'\n        exclude untagged; 'exact' matches the tag set exactly.",
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
					"q": map[string]any{
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
					"type": map[string]any{
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
			Name:        "get_memory",
			Description: "Get a specific memory by ID.\n\nReturns the full memory unit including content, metadata, and timestamps.\n\nArgs:\n    memory_id: The ID of the memory to retrieve\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
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
					"memory_id": map[string]any{
						"type": "string",
					},
				},
				"required": []any{
					"memory_id",
				},
				"type": "object",
			},
		},
		{
			Name:        "update_memory",
			Description: "\n            Edit a memory unit to correct what was extracted.\n\n            Pass any of text / context / occurred_start / occurred_end / mentioned_at /\n            fact_type / entities. For context and the dates, \"\" clears the field and omitting it\n            leaves it unchanged; entities replaces the fact's entity set ([] detaches\n            all). The memory is re-embedded and its derived observations, links, and\n            graph are recomputed automatically.\n\n            resolve_entities controls how the names in entities are matched. The\n            default True behaves like retain and may resolve a name onto a similar\n            entity that already exists, which silently discards a correction when the\n            bank holds a near-duplicate name. Pass False whenever you are correcting a\n            fact deliberately: an existing entity is then reused only on a\n            case-insensitive name match, and any other name becomes its own entity.\n\n            Only raw world/experience facts can be edited; observations are derived.\n            To retire or restore a fact, use invalidate_memory instead.\n    ",
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
					"entities": map[string]any{
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
					"fact_type": map[string]any{
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
					"memory_id": map[string]any{
						"type": "string",
					},
					"mentioned_at": map[string]any{
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
					"occurred_end": map[string]any{
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
					"occurred_start": map[string]any{
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
					"resolve_entities": map[string]any{
						"default": true,
						"type":    "boolean",
					},
					"text": map[string]any{
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
					"memory_id",
				},
				"type": "object",
			},
		},
		{
			Name:        "invalidate_memory",
			Description: "\n            Soft-retire a memory unit (or restore a previously retired one).\n\n            Invalidating moves the fact out of the active set: it's excluded from\n            recall, consolidation, and the knowledge graph, its links are pruned, and\n            its derived observations are recomputed without it — but it's kept for\n            audit and is fully reversible. Pass restore=True to bring it back.\n\n            Only raw world/experience facts can be invalidated; observations are derived.\n    ",
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
					"memory_id": map[string]any{
						"type": "string",
					},
					"reason": map[string]any{
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
					"restore": map[string]any{
						"default": false,
						"type":    "boolean",
					},
				},
				"required": []any{
					"memory_id",
				},
				"type": "object",
			},
		},
		{
			Name:        "list_documents",
			Description: "List documents in a memory bank.\n\nDocuments are containers for related memories (e.g., a conversation transcript,\na meeting notes file). Memories created with a document_id are grouped under that document.\n\nArgs:\n    q: Optional search query to filter documents\n    limit: Maximum number of results (default: 100)\n    offset: Pagination offset (default: 0). Page until the returned items add up to 'total'.\n        A 'q' search is capped: when 'total_is_estimate' is true there are more\n        matches than 'total' — narrow 'q' to reach them.\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
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
					"q": map[string]any{
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
			Name:        "get_document",
			Description: "Get a specific document by ID.\n\nReturns document metadata and associated memory information.\n\nArgs:\n    document_id: The ID of the document to retrieve\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
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
					"document_id": map[string]any{
						"type": "string",
					},
				},
				"required": []any{
					"document_id",
				},
				"type": "object",
			},
		},
		{
			Name:        "delete_document",
			Description: "Delete a document and its associated memories.\n\nPermanently removes a document and all memories linked to it.\n\nArgs:\n    document_id: The ID of the document to delete\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
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
					"document_id": map[string]any{
						"type": "string",
					},
				},
				"required": []any{
					"document_id",
				},
				"type": "object",
			},
		},
	}
}
