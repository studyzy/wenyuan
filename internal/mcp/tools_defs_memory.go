package mcp

// Code generated from the reference MCP tool definitions. DO NOT EDIT.

// memoryTools is the directive, memory and document tool roster in registration order.
func memoryTools() []tool {
	return []tool{
		{
			Name:        "list_directives",
			Description: "List directives for a memory bank.\n\nDirectives are instructions that guide how the memory engine processes and\nresponds to queries. They influence reflect behavior and memory organization.",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"active_only": map[string]any{
						"default":     true,
						"description": "If True, only return active directives (default: True)",
						"type":        "boolean",
					},
					"bank_id": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional bank (defaults to session bank). Use for cross-bank operations.",
					},
					"limit": map[string]any{
						"default":     100,
						"description": "Maximum number of results, 1-1000 (default: 100)",
						"maximum":     1000,
						"minimum":     1,
						"type":        "integer",
					},
					"offset": map[string]any{
						"default":     0,
						"description": "Pagination offset (default: 0). Page until the returned items add up to 'total'.",
						"minimum":     0,
						"type":        "integer",
					},
					"tags": map[string]any{
						"anyOf": []any{
							map[string]any{"items": map[string]any{"type": "string"}, "type": "array"},
							map[string]any{"type": "null"},
						},
						"default":     nil,
						"description": "Optional tags to filter by",
					},
				},
				"type": "object",
			},
		},
		{
			Name:        "create_directive",
			Description: "Create a new directive for a memory bank.\n\nDirectives guide how the memory engine processes queries and generates reflections.",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional bank (defaults to session bank). Use for cross-bank operations.",
					},
					"content": map[string]any{"description": "The directive content/instructions", "type": "string"},
					"is_active": map[string]any{
						"default":     true,
						"description": "Whether the directive is active (default: True)",
						"type":        "boolean",
					},
					"name": map[string]any{"description": "Human-readable name for the directive", "type": "string"},
					"priority": map[string]any{
						"default":     0,
						"description": "Priority level (higher = more important, default: 0)",
						"type":        "integer",
					},
					"tags": map[string]any{
						"anyOf": []any{
							map[string]any{"items": map[string]any{"type": "string"}, "type": "array"},
							map[string]any{"type": "null"},
						},
						"default":     nil,
						"description": "Optional tags for filtering",
					},
				},
				"required": []any{"name", "content"},
				"type":     "object",
			},
		},
		{
			Name:        "delete_directive",
			Description: "Delete a directive.\n\nPermanently removes a directive from the memory bank.",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional bank (defaults to session bank). Use for cross-bank operations.",
					},
					"directive_id": map[string]any{"description": "The ID of the directive to delete", "type": "string"},
				},
				"required": []any{"directive_id"},
				"type":     "object",
			},
		},
		{
			Name:        "list_memories",
			Description: "Browse stored memories with optional filtering.\n\nLists memory units (facts) stored in the bank. Unlike recall, this is a direct\nbrowse/search without relevance ranking.",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional bank (defaults to session bank). Use for cross-bank operations.",
					},
					"limit": map[string]any{
						"default":     100,
						"description": "Maximum number of results (default: 100)",
						"minimum":     0,
						"type":        "integer",
					},
					"offset": map[string]any{
						"default":     0,
						"description": "Pagination offset (default: 0)",
						"minimum":     0,
						"type":        "integer",
					},
					"q": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional text search query to filter memories",
					},
					"tags": map[string]any{
						"anyOf": []any{
							map[string]any{"items": map[string]any{"type": "string"}, "type": "array"},
							map[string]any{"type": "null"},
						},
						"default":     nil,
						"description": "Optional list of tag names to filter by.",
					},
					"tags_match": map[string]any{
						"default":     "any",
						"description": "How to combine tags: 'any' (OR, default) or 'all' (AND)\nboth also include untagged memories; 'any_strict'/'all_strict'\nexclude untagged; 'exact' matches the tag set exactly.",
						"enum":        []any{"any", "all", "any_strict", "all_strict", "exact"},
						"type":        "string",
					},
					"type": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Filter by fact type: 'world', 'experience', or 'observation'",
					},
				},
				"type": "object",
			},
		},
		{
			Name:        "get_memory",
			Description: "Get a specific memory by ID.\n\nReturns the full memory unit including content, metadata, and timestamps.",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional bank (defaults to session bank). Use for cross-bank operations.",
					},
					"memory_id": map[string]any{"description": "The ID of the memory to retrieve", "type": "string"},
				},
				"required": []any{"memory_id"},
				"type":     "object",
			},
		},
		{
			Name:        "update_memory",
			Description: "\n            Edit a memory unit to correct what was extracted.\n\n            Pass any of text / context / occurred_start / occurred_end / fact_type /\n            entities. For context and the dates, \"\" clears the field and omitting it\n            leaves it unchanged; entities replaces the fact's entity set ([] detaches\n            all). The memory is re-embedded and its derived observations, links, and\n            graph are recomputed automatically.\n\n            resolve_entities controls how the names in entities are matched. The\n            default True behaves like retain and may resolve a name onto a similar\n            entity that already exists, which silently discards a correction when the\n            bank holds a near-duplicate name. Pass False whenever you are correcting a\n            fact deliberately: an existing entity is then reused only on a\n            case-insensitive name match, and any other name becomes its own entity.\n\n            Only raw world/experience facts can be edited; observations are derived.\n            To retire or restore a fact, use invalidate_memory instead.\n    ",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"anyOf":   []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default": nil,
					},
					"context": map[string]any{
						"anyOf":   []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default": nil,
					},
					"entities": map[string]any{
						"anyOf": []any{
							map[string]any{"items": map[string]any{"type": "string"}, "type": "array"},
							map[string]any{"type": "null"},
						},
						"default": nil,
					},
					"fact_type": map[string]any{
						"anyOf":   []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default": nil,
					},
					"memory_id": map[string]any{"type": "string"},
					"occurred_end": map[string]any{
						"anyOf":   []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default": nil,
					},
					"occurred_start": map[string]any{
						"anyOf":   []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default": nil,
					},
					"resolve_entities": map[string]any{"default": true, "type": "boolean"},
					"text": map[string]any{
						"anyOf":   []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default": nil,
					},
				},
				"required": []any{"memory_id"},
				"type":     "object",
			},
		},
		{
			Name:        "invalidate_memory",
			Description: "\n            Soft-retire a memory unit (or restore a previously retired one).\n\n            Invalidating moves the fact out of the active set: it's excluded from\n            recall, consolidation, and the knowledge graph, its links are pruned, and\n            its derived observations are recomputed without it \u2014 but it's kept for\n            audit and is fully reversible. Pass restore=True to bring it back.\n\n            Only raw world/experience facts can be invalidated; observations are derived.\n    ",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"anyOf":   []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default": nil,
					},
					"memory_id": map[string]any{"type": "string"},
					"reason": map[string]any{
						"anyOf":   []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default": nil,
					},
					"restore": map[string]any{"default": false, "type": "boolean"},
				},
				"required": []any{"memory_id"},
				"type":     "object",
			},
		},
		{
			Name:        "list_documents",
			Description: "List documents in a memory bank.\n\nDocuments are containers for related memories (e.g., a conversation transcript,\na meeting notes file). Memories created with a document_id are grouped under that document.",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional bank (defaults to session bank). Use for cross-bank operations.",
					},
					"limit": map[string]any{
						"default":     100,
						"description": "Maximum number of results (default: 100)",
						"minimum":     0,
						"type":        "integer",
					},
					"offset": map[string]any{
						"default":     0,
						"description": "Pagination offset (default: 0). Page until the returned items add up to 'total'.",
						"minimum":     0,
						"type":        "integer",
					},
					"q": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional search query to filter documents",
					},
				},
				"type": "object",
			},
		},
		{
			Name:        "get_document",
			Description: "Get a specific document by ID.\n\nReturns document metadata and associated memory information.",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional bank (defaults to session bank). Use for cross-bank operations.",
					},
					"document_id": map[string]any{"description": "The ID of the document to retrieve", "type": "string"},
				},
				"required": []any{"document_id"},
				"type":     "object",
			},
		},
		{
			Name:        "delete_document",
			Description: "Delete a document and its associated memories.\n\nPermanently removes a document and all memories linked to it.",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional bank (defaults to session bank). Use for cross-bank operations.",
					},
					"document_id": map[string]any{"description": "The ID of the document to delete", "type": "string"},
				},
				"required": []any{"document_id"},
				"type":     "object",
			},
		},
	}
}
