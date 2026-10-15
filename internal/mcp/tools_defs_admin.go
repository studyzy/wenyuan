package mcp

// Code generated from the reference MCP tool definitions. DO NOT EDIT.

// adminTools is the operation and bank tool roster in registration order.
func adminTools() []tool {
	return []tool{
		{
			Name:        "list_operations",
			Description: "List async operations for a memory bank.\n\nOperations track background tasks like retain processing, mental model refresh, etc.",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional bank (defaults to session bank). Use for cross-bank operations.",
					},
					"exclude_parents": map[string]any{
						"default":     false,
						"description": "Exclude parent batch operations (default: False)",
						"type":        "boolean",
					},
					"limit": map[string]any{
						"default":     20,
						"description": "Maximum number of results, 1-100 (default: 20)",
						"maximum":     100,
						"minimum":     1,
						"type":        "integer",
					},
					"offset": map[string]any{
						"default":     0,
						"description": "Number of operations to skip (default: 0). Page until you reach 'total'.",
						"minimum":     0,
						"type":        "integer",
					},
					"status": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Filter by status: 'pending', 'running', 'completed', 'failed', 'cancelled'",
					},
					"type": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Filter by operation type: 'retain', 'consolidation', 'refresh_mental_model',\n'file_convert_retain', 'webhook_delivery'",
					},
				},
				"type": "object",
			},
		},
		{
			Name:        "get_operation",
			Description: "Get the status of an async operation.\n\nCheck progress of background tasks like retain processing or mental model refresh.",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional bank (defaults to session bank). Use for cross-bank operations.",
					},
					"operation_id": map[string]any{"description": "The ID of the operation to check", "type": "string"},
				},
				"required": []any{"operation_id"},
				"type":     "object",
			},
		},
		{
			Name:        "cancel_operation",
			Description: "Cancel a pending or running async operation.",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional bank (defaults to session bank). Use for cross-bank operations.",
					},
					"operation_id": map[string]any{"description": "The ID of the operation to cancel", "type": "string"},
				},
				"required": []any{"operation_id"},
				"type":     "object",
			},
		},
		{
			Name:        "list_tags",
			Description: "List tags used in a memory bank.\n\nTags are used to organize and filter memories, directives, and mental models.",
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
						"description": "Optional pattern to filter tags (e.g., 'project:*')",
					},
				},
				"type": "object",
			},
		},
		{
			Name:        "get_bank",
			Description: "Get the profile of a memory bank.\n\nReturns bank metadata including name, disposition, and mission.",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional bank (defaults to session bank). Use for cross-bank operations.",
					},
				},
				"type": "object",
			},
		},
		{
			Name:        "get_bank_stats",
			Description: "Get statistics for a memory bank.\n\nReturns counts of nodes, links, and other metrics.",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional bank (defaults to session bank). Use for cross-bank operations.",
					},
				},
				"type": "object",
			},
		},
		{
			Name:        "update_bank",
			Description: "Update a memory bank's configuration.\n\nUpdates the bank's name and/or any bank-level configuration fields.\nOnly provided fields will be updated; omitted fields remain unchanged.",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional bank (defaults to session bank). Use for cross-bank operations.",
					},
					"config_updates": map[string]any{
						"anyOf": []any{
							map[string]any{"additionalProperties": true, "type": "object"},
							map[string]any{"type": "null"},
						},
						"default":     nil,
						"description": "Dictionary of configuration fields to update. Supports all\nbank-configurable fields including:\n- reflect_mission: Mission/context for Reflect operations.\n- retain_mission: Steers what gets extracted during retain().\n- retain_extraction_mode: 'concise' (default), 'verbose', or 'custom'.\n- retain_custom_instructions: Custom extraction prompt (active when mode is 'custom').\n- retain_chunk_size: Target maximum characters for each content chunk.\n- retain_structured_chunk_size: Maximum characters for a single JSONL line or conversation turn to keep whole.\n- retain_chunk_batch_size: Number of chunks to process in parallel.\n- enable_observations: Toggle observation consolidation after retain().\n- observations_mission: Controls observation synthesis rules.\n- disposition_skepticism: Critical evaluation level (1-5).\n- disposition_literalism: Literal vs. abstract interpretation (1-5).\n- disposition_empathy: Emotional context consideration (1-5).\n- entity_labels: Controlled vocabulary for entity classification.\n- entities_allow_free_form: Allow labels outside entity_labels.\n- recall_include_chunks: Include raw chunks in recall results.\n- recall_max_tokens: Max tokens for recall results.\n- mcp_enabled_tools: Tool allowlist for this bank.\nAny configurable field name is accepted (use Python field names).",
					},
					"mission": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Deprecated alias for config_updates.reflect_mission.",
					},
					"name": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Human-friendly display name for the bank.",
					},
				},
				"type": "object",
			},
		},
		{
			Name:        "delete_bank",
			Description: "Delete a memory bank and all its data.\n\nWARNING: This permanently deletes the bank and all its memories, documents,\nmental models, directives, and other data. This action cannot be undone.",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional bank (defaults to session bank). Use for cross-bank operations.",
					},
				},
				"type": "object",
			},
		},
		{
			Name:        "clear_memories",
			Description: "Clear all memories from a bank without deleting the bank itself.\n\nOptionally filter by fact type to only clear specific kinds of memories.",
			InputSchema: map[string]any{
				"additionalProperties": false,
				"properties": map[string]any{
					"bank_id": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional bank (defaults to session bank). Use for cross-bank operations.",
					},
					"type": map[string]any{
						"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
						"default":     nil,
						"description": "Optional fact type filter: 'world', 'experience', or 'observation'. If not specified, clears all.",
					},
				},
				"type": "object",
			},
		},
	}
}
