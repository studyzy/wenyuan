package mcp

// Code generated from the reference MCP tool definitions. DO NOT EDIT.

// knowledgeTools is the knowledge tool roster in registration order.
func knowledgeTools() []tool {
	return []tool{
		{
			Name:        "get_knowledge_base_tree",
			Description: "Browse the knowledge base as a nested tree of folders and pages.\n\nStart here to discover what the bank documents: each page is a living\nmarkdown document synthesized from the bank's memories, and folders\ngroup them. Use get_knowledge_page to read a page's content, or\nsearch_knowledge_base when you know what you are looking for.\n\nPages report `is_stale`: false means the page is provably up to date;\ntrue means something was written since its last refresh, so it MAY be\nout of date.\n\nArgs:\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
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
			Name:        "search_knowledge_base",
			Description: "Find knowledge pages by relevance (hybrid keyword + semantic search).\n\nSearches page names and content, returning ranked pages with a short\nsnippet each. Read a hit in full with get_knowledge_page. This searches\nthe curated knowledge base only — use recall to search raw memories.\n\nArgs:\n    query: What to search for\n    limit: Maximum pages to return (1-50, default: 10)\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
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
						"default": 10,
						"type":    "integer",
					},
					"query": map[string]any{
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
			Name:        "get_knowledge_page",
			Description: "Read a knowledge page as a markdown document.\n\nReturns the page's YAML frontmatter (id, type, title, description,\ntags, timestamp) followed by its synthesized markdown body. Discover\npage ids with get_knowledge_base_tree or search_knowledge_base.\n\nArgs:\n    page_id: The ID of the page to read (a `kp-...` node id)\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
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
					"page_id": map[string]any{
						"type": "string",
					},
				},
				"required": []any{
					"page_id",
				},
				"type": "object",
			},
		},
		{
			Name:        "create_knowledge_folder",
			Description: "Create a folder in the knowledge base.\n\nFolders group pages; they hold no content of their own.\n\nArgs:\n    name: Folder name\n    parent_id: Optional parent folder id (a `kf-...` node id). Omit to create at the top level.\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
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
					"name": map[string]any{
						"type": "string",
					},
					"parent_id": map[string]any{
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
					"name",
				},
				"type": "object",
			},
		},
		{
			Name:        "create_knowledge_page",
			Description: "Create a knowledge page — a living document answering a question.\n\nThe page's content is synthesized from the bank's memories by running\nsource_query, asynchronously: use the returned operation_id to track\ncompletion, then read it with get_knowledge_page. By default the page\nkeeps itself current, rebuilding after each consolidation.\n\nEXAMPLES:\n- name=\"Deployment Runbook\", source_query=\"How is this service deployed and rolled back?\"\n- name=\"Team Preferences\", source_query=\"What tools and conventions does the team prefer?\"\n\nArgs:\n    name: Page name (must be unique within its folder)\n    source_query: The question this page answers and rebuilds itself from\n    parent_id: Optional parent folder id (a `kf-...` node id). Omit to create at the top level.\n    tags: Optional tags scoping which memories the page is built from\n    max_tokens: Maximum tokens for the generated content (default: 4096)\n    trigger: Refresh policy for this page — when it rebuilds itself (mode,\n        refresh_after_consolidation, refresh_cron) and what it rebuilds from\n        (fact_types, tags_match, tag_groups, exclude_mental_models,\n        recall_max_tokens, ...). Omitted fields keep the knowledge-page\n        defaults: incremental (delta) rebuilds from consolidated observations\n        after each consolidation, ignoring sibling pages. Set refresh_cron\n        instead to move the page onto a fixed UTC schedule.\n    refresh_after_consolidation: Legacy shorthand for trigger.refresh_after_consolidation.\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
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
					"max_tokens": map[string]any{
						"anyOf": []any{
							map[string]any{
								"type": "integer",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"name": map[string]any{
						"type": "string",
					},
					"parent_id": map[string]any{
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
					"refresh_after_consolidation": map[string]any{
						"anyOf": []any{
							map[string]any{
								"type": "boolean",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"source_query": map[string]any{
						"type": "string",
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
					"trigger": map[string]any{
						"anyOf": []any{
							map[string]any{
								"additionalProperties": false,
								"description":          "The refresh policy of a mental model or knowledge page, as an MCP tool input.\n\nMirrors the HTTP ``MentalModelTrigger`` field for field — an agent that read\nthe API docs must not have a call rejected for naming a setting that exists.\n``tests/test_mcp_tools.py::test_trigger_input_covers_every_http_trigger_field``\nfails if the two drift apart.\n\nThe one deliberate difference is that every field is optional with no default:\nthe HTTP model fills unset fields with its own defaults, which makes a partial\ntrigger silently reset the rest, while these tools send only what the caller\nactually set (``model_dump(exclude_unset=True)``) and the engine merges that\nover the stored trigger. Passing an explicit ``null`` still clears a setting.",
								"properties": map[string]any{
									"budget": map[string]any{
										"anyOf": []any{
											map[string]any{
												"description": "Budget levels for recall/reflect operations.",
												"enum": []any{
													"low",
													"mid",
													"high",
												},
												"type": "string",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "How many agent steps a refresh may spend, as a multiple of the server's reflect iteration limit: 'low' halves it, 'mid' keeps it, 'high' doubles it. null = mid.",
									},
									"exclude_mental_model_ids": map[string]any{
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
										"default":     nil,
										"description": "Exclude specific mental models from the refresh's reflect loop, by ID.",
									},
									"exclude_mental_models": map[string]any{
										"anyOf": []any{
											map[string]any{
												"type": "boolean",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "Exclude ALL mental models from the refresh's reflect loop, so a model never reflects on its siblings.",
									},
									"fact_types": map[string]any{
										"anyOf": []any{
											map[string]any{
												"items": map[string]any{
													"enum": []any{
														"world",
														"experience",
														"observation",
													},
													"type": "string",
												},
												"type": "array",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "Fact types to retrieve during refresh; null includes all of world, experience and observation.",
									},
									"full_refresh_cron": map[string]any{
										"anyOf": []any{
											map[string]any{
												"type": "string",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "UTC five-field cron schedule for a scheduled FULL refresh, e.g. '0 3 * * 0' for Sundays at 03:00 UTC: the content is regenerated from scratch on this schedule whatever 'mode' says, to clear the drift delta edits accumulate. Independent of refresh_cron and refresh_after_consolidation, and it does not change 'mode'. Skipped when no refresh ran since the last full one. null = no schedule.",
									},
									"include_chunks": map[string]any{
										"anyOf": []any{
											map[string]any{
												"type": "boolean",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "Override whether the refresh's internal recall returns raw chunk text. null = bank/global default.",
									},
									"keep_trace": map[string]any{
										"anyOf": []any{
											map[string]any{
												"type": "boolean",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "Record how each refresh reached its result under reflect_response.trace (mode and why, resolved scope and window, facts retrieved vs used, tool and LLM calls, delta operations). Only the latest refresh's trace is kept. This is the only way to diagnose a cron- or consolidation-driven refresh, since nobody watches those run.",
									},
									"min_refresh_interval_seconds": map[string]any{
										"anyOf": []any{
											map[string]any{
												"minimum": 0,
												"type":    "integer",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "Minimum seconds between two AUTOMATIC refreshes. A trigger that arrives sooner is queued and parked until the window expires, and further triggers fold into that one queued refresh, so a burst of retains costs one refresh. Explicit refreshes ignore it. 0 disables the floor; null falls back to the bank/global setting.",
									},
									"mode": map[string]any{
										"anyOf": []any{
											map[string]any{
												"enum": []any{
													"full",
													"delta",
												},
												"type": "string",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "Refresh mode. 'full' regenerates the content from scratch on each refresh; 'delta' makes surgical edits to the existing content, preserving unchanged sections byte-for-byte. Delta falls back to a full regeneration when there is no existing content or the source_query changed.",
									},
									"recall_chunks_max_tokens": map[string]any{
										"anyOf": []any{
											map[string]any{
												"type": "integer",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "Override the token budget for raw chunks from the refresh's internal recall. null = bank/global default.",
									},
									"recall_max_tokens": map[string]any{
										"anyOf": []any{
											map[string]any{
												"type": "integer",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "Override the token budget for facts from the refresh's internal recall. null = bank/global default.",
									},
									"reflect_search_observations_include_entities": map[string]any{
										"anyOf": []any{
											map[string]any{
												"type": "boolean",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "Override whether search_observations attaches resolved entity names, which can be over half the tool payload. null = enabled.",
									},
									"reflect_search_observations_max_tokens": map[string]any{
										"anyOf": []any{
											map[string]any{
												"minimum": 1,
												"type":    "integer",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "Override the token budget for the refresh's search_observations calls. Lowering it drops the lowest-ranked observations and shrinks the reflect context. null = the shipped 5000.",
									},
									"refresh_after_consolidation": map[string]any{
										"anyOf": []any{
											map[string]any{
												"type": "boolean",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "Refresh automatically after observations are consolidated. Mutually exclusive with refresh_cron.",
									},
									"refresh_cron": map[string]any{
										"anyOf": []any{
											map[string]any{
												"type": "string",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "UTC five-field cron schedule, e.g. '0 3 * * *' for daily at 03:00 UTC. A scheduled refresh runs only when the model is stale, so an unchanged scope costs no LLM call. Mutually exclusive with refresh_after_consolidation. null = no schedule.",
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
										"default":     nil,
										"description": "JSON Schema for structured output. Each refresh then also stores a parsed result under reflect_response.structured_output, alongside the markdown content.",
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
										"default":     nil,
										"description": "Compound boolean tag expressions used during refresh INSTEAD of the model's flat tags. When set, the model's own tags are not used for filtering. Each group is a leaf {'tags': [...], 'match': 'any'|'all'|'any_strict'|'all_strict'|'exact'} or a compound {'and': [groups]}, {'or': [groups]}, {'not': group}, nested to any depth.",
									},
									"tags_match": map[string]any{
										"anyOf": []any{
											map[string]any{
												"enum": []any{
													"any",
													"all",
													"any_strict",
													"all_strict",
													"exact",
												},
												"type": "string",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "How this model's tags select memories during refresh: any, all, any_strict, all_strict, or exact. Unset means 'all_strict' for a tagged model and 'any' for an untagged one.",
									},
								},
								"type": "object",
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
					"source_query",
				},
				"type": "object",
			},
		},
		{
			Name:        "update_knowledge_node",
			Description: "Rename or move a folder/page, and/or update a page's options.\n\nOnly the arguments you pass are changed; everything else keeps its\ncurrent value. Changing source_query schedules an async refresh so the\npage rebuilds against the new question.\n\nArgs:\n    node_id: The ID of the folder (`kf-...`) or page (`kp-...`) to update\n    name: New name for the node\n    parent_id: Folder id to move the node into, or \"root\" to move it to the top level\n    source_query: Pages only — the new question the page answers\n    tags: Pages only — replacement tag list (pass [] to clear)\n    max_tokens: Pages only — new maximum tokens for the generated content\n    trigger: Pages only — refresh policy fields to change: mode,\n        refresh_after_consolidation, refresh_cron, fact_types, tags_match,\n        tag_groups, exclude_mental_models, recall_max_tokens, and so on. This\n        is a PATCH: fields you omit keep their current values, so putting a\n        page on a cron schedule does not reset its delta mode or its\n        observation-only scope.\n    refresh_after_consolidation: Pages only — legacy shorthand for\n        trigger.refresh_after_consolidation\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
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
					"max_tokens": map[string]any{
						"anyOf": []any{
							map[string]any{
								"type": "integer",
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
					"node_id": map[string]any{
						"type": "string",
					},
					"parent_id": map[string]any{
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
					"refresh_after_consolidation": map[string]any{
						"anyOf": []any{
							map[string]any{
								"type": "boolean",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
					"source_query": map[string]any{
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
					"trigger": map[string]any{
						"anyOf": []any{
							map[string]any{
								"additionalProperties": false,
								"description":          "The refresh policy of a mental model or knowledge page, as an MCP tool input.\n\nMirrors the HTTP ``MentalModelTrigger`` field for field — an agent that read\nthe API docs must not have a call rejected for naming a setting that exists.\n``tests/test_mcp_tools.py::test_trigger_input_covers_every_http_trigger_field``\nfails if the two drift apart.\n\nThe one deliberate difference is that every field is optional with no default:\nthe HTTP model fills unset fields with its own defaults, which makes a partial\ntrigger silently reset the rest, while these tools send only what the caller\nactually set (``model_dump(exclude_unset=True)``) and the engine merges that\nover the stored trigger. Passing an explicit ``null`` still clears a setting.",
								"properties": map[string]any{
									"budget": map[string]any{
										"anyOf": []any{
											map[string]any{
												"description": "Budget levels for recall/reflect operations.",
												"enum": []any{
													"low",
													"mid",
													"high",
												},
												"type": "string",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "How many agent steps a refresh may spend, as a multiple of the server's reflect iteration limit: 'low' halves it, 'mid' keeps it, 'high' doubles it. null = mid.",
									},
									"exclude_mental_model_ids": map[string]any{
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
										"default":     nil,
										"description": "Exclude specific mental models from the refresh's reflect loop, by ID.",
									},
									"exclude_mental_models": map[string]any{
										"anyOf": []any{
											map[string]any{
												"type": "boolean",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "Exclude ALL mental models from the refresh's reflect loop, so a model never reflects on its siblings.",
									},
									"fact_types": map[string]any{
										"anyOf": []any{
											map[string]any{
												"items": map[string]any{
													"enum": []any{
														"world",
														"experience",
														"observation",
													},
													"type": "string",
												},
												"type": "array",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "Fact types to retrieve during refresh; null includes all of world, experience and observation.",
									},
									"full_refresh_cron": map[string]any{
										"anyOf": []any{
											map[string]any{
												"type": "string",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "UTC five-field cron schedule for a scheduled FULL refresh, e.g. '0 3 * * 0' for Sundays at 03:00 UTC: the content is regenerated from scratch on this schedule whatever 'mode' says, to clear the drift delta edits accumulate. Independent of refresh_cron and refresh_after_consolidation, and it does not change 'mode'. Skipped when no refresh ran since the last full one. null = no schedule.",
									},
									"include_chunks": map[string]any{
										"anyOf": []any{
											map[string]any{
												"type": "boolean",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "Override whether the refresh's internal recall returns raw chunk text. null = bank/global default.",
									},
									"keep_trace": map[string]any{
										"anyOf": []any{
											map[string]any{
												"type": "boolean",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "Record how each refresh reached its result under reflect_response.trace (mode and why, resolved scope and window, facts retrieved vs used, tool and LLM calls, delta operations). Only the latest refresh's trace is kept. This is the only way to diagnose a cron- or consolidation-driven refresh, since nobody watches those run.",
									},
									"min_refresh_interval_seconds": map[string]any{
										"anyOf": []any{
											map[string]any{
												"minimum": 0,
												"type":    "integer",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "Minimum seconds between two AUTOMATIC refreshes. A trigger that arrives sooner is queued and parked until the window expires, and further triggers fold into that one queued refresh, so a burst of retains costs one refresh. Explicit refreshes ignore it. 0 disables the floor; null falls back to the bank/global setting.",
									},
									"mode": map[string]any{
										"anyOf": []any{
											map[string]any{
												"enum": []any{
													"full",
													"delta",
												},
												"type": "string",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "Refresh mode. 'full' regenerates the content from scratch on each refresh; 'delta' makes surgical edits to the existing content, preserving unchanged sections byte-for-byte. Delta falls back to a full regeneration when there is no existing content or the source_query changed.",
									},
									"recall_chunks_max_tokens": map[string]any{
										"anyOf": []any{
											map[string]any{
												"type": "integer",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "Override the token budget for raw chunks from the refresh's internal recall. null = bank/global default.",
									},
									"recall_max_tokens": map[string]any{
										"anyOf": []any{
											map[string]any{
												"type": "integer",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "Override the token budget for facts from the refresh's internal recall. null = bank/global default.",
									},
									"reflect_search_observations_include_entities": map[string]any{
										"anyOf": []any{
											map[string]any{
												"type": "boolean",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "Override whether search_observations attaches resolved entity names, which can be over half the tool payload. null = enabled.",
									},
									"reflect_search_observations_max_tokens": map[string]any{
										"anyOf": []any{
											map[string]any{
												"minimum": 1,
												"type":    "integer",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "Override the token budget for the refresh's search_observations calls. Lowering it drops the lowest-ranked observations and shrinks the reflect context. null = the shipped 5000.",
									},
									"refresh_after_consolidation": map[string]any{
										"anyOf": []any{
											map[string]any{
												"type": "boolean",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "Refresh automatically after observations are consolidated. Mutually exclusive with refresh_cron.",
									},
									"refresh_cron": map[string]any{
										"anyOf": []any{
											map[string]any{
												"type": "string",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "UTC five-field cron schedule, e.g. '0 3 * * *' for daily at 03:00 UTC. A scheduled refresh runs only when the model is stale, so an unchanged scope costs no LLM call. Mutually exclusive with refresh_after_consolidation. null = no schedule.",
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
										"default":     nil,
										"description": "JSON Schema for structured output. Each refresh then also stores a parsed result under reflect_response.structured_output, alongside the markdown content.",
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
										"default":     nil,
										"description": "Compound boolean tag expressions used during refresh INSTEAD of the model's flat tags. When set, the model's own tags are not used for filtering. Each group is a leaf {'tags': [...], 'match': 'any'|'all'|'any_strict'|'all_strict'|'exact'} or a compound {'and': [groups]}, {'or': [groups]}, {'not': group}, nested to any depth.",
									},
									"tags_match": map[string]any{
										"anyOf": []any{
											map[string]any{
												"enum": []any{
													"any",
													"all",
													"any_strict",
													"all_strict",
													"exact",
												},
												"type": "string",
											},
											map[string]any{
												"type": "null",
											},
										},
										"default":     nil,
										"description": "How this model's tags select memories during refresh: any, all, any_strict, all_strict, or exact. Unset means 'all_strict' for a tagged model and 'any' for an untagged one.",
									},
								},
								"type": "object",
							},
							map[string]any{
								"type": "null",
							},
						},
						"default": nil,
					},
				},
				"required": []any{
					"node_id",
				},
				"type": "object",
			},
		},
		{
			Name:        "delete_knowledge_node",
			Description: "Delete a knowledge-base folder or page and everything under it.\n\nDeleting a folder also deletes its whole subtree, and each deleted page\ntakes its backing mental model with it. This cannot be undone.\n\nArgs:\n    node_id: The ID of the folder (`kf-...`) or page (`kp-...`) to delete\n    bank_id: Optional bank (defaults to session bank). Use for cross-bank operations.",
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
					"node_id": map[string]any{
						"type": "string",
					},
				},
				"required": []any{
					"node_id",
				},
				"type": "object",
			},
		},
	}
}
