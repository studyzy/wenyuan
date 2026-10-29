package mcp

// Code generated from the reference MCP tool definitions. DO NOT EDIT.

// adminTools is the operation and bank tool roster in registration order.
func adminTools() []tool {
	return []tool{
		{
			Name:        "list_operations",
			Description: "List async operations for a memory bank.\n\nOperations track background tasks like retain processing, mental model refresh, etc.\n\nArgs:\n    status: Filter by status: 'pending', 'running', 'completed', 'failed', 'cancelled'\n    type: Filter by operation type: 'retain', 'consolidation', 'refresh_mental_model',\n        'file_convert_retain', 'webhook_delivery'\n    limit: Maximum number of results, 1-100 (default: 20)\n    offset: Number of operations to skip (default: 0). Page until you reach 'total'.\n    exclude_parents: Exclude parent batch operations (default: False)\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
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
					"exclude_parents": map[string]any{
						"default": false,
						"type":    "boolean",
					},
					"limit": map[string]any{
						"default": 20,
						"maximum": 100,
						"minimum": 1,
						"type":    "integer",
					},
					"offset": map[string]any{
						"default": 0,
						"minimum": 0,
						"type":    "integer",
					},
					"status": map[string]any{
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
			Name:        "get_operation",
			Description: "Get the status of an async operation.\n\nCheck progress of background tasks like retain processing or mental model refresh.\n\nArgs:\n    operation_id: The ID of the operation to check\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
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
					"operation_id": map[string]any{
						"type": "string",
					},
				},
				"required": []any{
					"operation_id",
				},
				"type": "object",
			},
		},
		{
			Name:        "cancel_operation",
			Description: "Cancel a pending or running async operation.\n\nArgs:\n    operation_id: The ID of the operation to cancel\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
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
					"operation_id": map[string]any{
						"type": "string",
					},
				},
				"required": []any{
					"operation_id",
				},
				"type": "object",
			},
		},
		{
			Name:        "list_tags",
			Description: "List tags used in a memory bank.\n\nTags are used to organize and filter memories, directives, and mental models.\n\nArgs:\n    q: Optional pattern to filter tags (e.g., 'project:*')\n    limit: Maximum number of results (default: 100)\n    offset: Pagination offset (default: 0). Page until the returned items add up to 'total'.\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
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
			Name:        "get_bank",
			Description: "Get the profile of a memory bank.\n\nReturns bank metadata including name, disposition, and mission.\n\nArgs:\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
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
				},
				"type": "object",
			},
		},
		{
			Name:        "get_bank_stats",
			Description: "Get statistics for a memory bank.\n\nReturns counts of nodes, links, and other metrics.\n\nArgs:\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
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
				},
				"type": "object",
			},
		},
		{
			Name:        "update_bank",
			Description: "Update a memory bank's configuration.\n\nUpdates the bank's name and/or any bank-level configuration fields.\nOnly provided fields will be updated; omitted fields remain unchanged.\n\nArgs:\n    name: Human-friendly display name for the bank.\n    mission: Deprecated alias for config_updates.reflect_mission.\n    config_updates: Dictionary of configuration fields to update. Supports all\n        bank-configurable fields including:\n        - reflect_mission: Mission/context for Reflect operations.\n        - retain_mission: Steers what gets extracted during retain().\n        - retain_extraction_mode: 'concise' (default), 'verbose', or 'custom'.\n        - retain_custom_instructions: Custom extraction prompt (active when mode is 'custom').\n        - retain_chunk_size: Target maximum characters for each content chunk.\n        - retain_structured_chunk_size: Maximum characters for a single JSONL line or conversation turn to keep whole.\n        - retain_chunk_batch_size: Number of chunks to process in parallel.\n        - enable_observations: Toggle observation consolidation after retain().\n        - observations_mission: Controls observation synthesis rules.\n        - disposition_skepticism: Critical evaluation level (1-5).\n        - disposition_literalism: Literal vs. abstract interpretation (1-5).\n        - disposition_empathy: Emotional context consideration (1-5).\n        - entity_labels: Controlled vocabulary for entity classification.\n        - entities_allow_free_form: Allow labels outside entity_labels.\n        - recall_include_chunks: Include raw chunks in recall results.\n        - recall_prefer_observations: Default prefer_observations for recall requests that omit it.\n        - recall_max_tokens: Max tokens for recall results.\n        - mcp_enabled_tools: Tool allowlist for this bank.\n        Any configurable field name is accepted (use Python field names).\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
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
					"config_updates": map[string]any{
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
				"type": "object",
			},
		},
		{
			Name:        "delete_bank",
			Description: "Delete a memory bank and all its data.\n\nWARNING: This permanently deletes the bank and all its memories, documents,\nmental models, directives, and other data. This action cannot be undone.\n\nArgs:\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
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
				},
				"type": "object",
			},
		},
		{
			Name:        "clear_memories",
			Description: "Clear all memories from a bank without deleting the bank itself.\n\nOptionally filter by fact type to only clear specific kinds of memories.\n\nArgs:\n    type: Optional fact type filter: 'world', 'experience', or 'observation'. If not specified, clears all.\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
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
	}
}
