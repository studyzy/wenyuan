# 第三轮路由清单（100 条，按 API 编号）

> 生成：参考 `api/http.py` 装饰器抽取 + 本服务 `internal/httpx/openapi.json` 核对。
> 编号沿用 `docs/API-AUDIT-MANIFEST.md`；#100 是 `include_in_schema=False` 的 `GET /v1/version`。

## 单元 `monitor`（#1–#5）

| # | API | operationId | 参考装饰器行 |
|---|---|---|---|
| 1 | `GET /health` | `-` | `api/http.py:5650` |
| 2 | `GET /health/ready` | `get_readiness` | `api/http.py:5667` |
| 3 | `GET /health/live` | `get_liveness` | `api/http.py:5684` |
| 4 | `GET /version` | `get_version` | `api/http.py:5703` |
| 5 | `GET /metrics` | `-` | `api/http.py:5748` |

## 单元 `bank-lifecycle`（#6–#13）

| # | API | operationId | 参考装饰器行 |
|---|---|---|---|
| 6 | `GET /v1/default/banks` | `list_banks` | `api/http.py:6592` |
| 7 | `GET /v1/default/banks/{bank_id}/aliases` | `list_bank_aliases` | `api/http.py:8476` |
| 8 | `POST /v1/default/banks/{bank_id}/aliases` | `create_bank_alias` | `api/http.py:8499` |
| 9 | `PATCH /v1/default/banks/{bank_id}/aliases/{alias}` | `set_bank_alias_primary` | `api/http.py:8530` |
| 10 | `DELETE /v1/default/banks/{bank_id}/aliases/{alias}` | `delete_bank_alias` | `api/http.py:8565` |
| 11 | `PUT /v1/default/banks/{bank_id}` | `create_or_update_bank` | `api/http.py:8593` |
| 12 | `PATCH /v1/default/banks/{bank_id}` | `update_bank` | `api/http.py:8638` |
| 13 | `DELETE /v1/default/banks/{bank_id}` | `delete_bank` | `api/http.py:8684` |

## 单元 `bank-config`（#14–#18）

| # | API | operationId | 参考装饰器行 |
|---|---|---|---|
| 14 | `GET /v1/default/banks/{bank_id}/profile` | `get_bank_profile` | `api/http.py:8411` |
| 15 | `PUT /v1/default/banks/{bank_id}/profile` | `update_bank_disposition` | `api/http.py:8424` |
| 16 | `POST /v1/default/banks/{bank_id}/background` | `add_bank_background` | `api/http.py:8439` |
| 17 | `GET /v1/bank-template-schema` | `get_bank_template_schema` | `api/http.py:9450` |
| 18 | `GET /v1/default/banks/{bank_id}/config` | `get_bank_config` | `api/http.py:9612` |

## 单元 `retain`（#19–#23）

| # | API | operationId | 参考装饰器行 |
|---|---|---|---|
| 19 | `PATCH /v1/default/banks/{bank_id}/config` | `update_bank_config` | `api/http.py:9641` |
| 20 | `DELETE /v1/default/banks/{bank_id}/config` | `reset_bank_config` | `api/http.py:9678` |
| 21 | `POST /v1/default/banks/{bank_id}/memories/dry-run-extract` | `dry_run_extract_memories` | `api/http.py:5922` |
| 22 | `POST /v1/default/banks/{bank_id}/prompts/preview` | `preview_prompt` | `api/http.py:5981` |
| 23 | `POST /v1/default/banks/{bank_id}/memories` | `retain_memories` | `api/http.py:10052` |

## 单元 `recall-reflect`（#24–#25）

| # | API | operationId | 参考装饰器行 |
|---|---|---|---|
| 24 | `POST /v1/default/banks/{bank_id}/files/retain` | `file_retain` | `api/http.py:10359` |
| 25 | `DELETE /v1/default/banks/{bank_id}/memories` | `clear_bank_memories` | `api/http.py:10528` |

## 单元 `memory-crud`（#26–#29）

| # | API | operationId | 参考装饰器行 |
|---|---|---|---|
| 26 | `POST /v1/default/banks/{bank_id}/memories/recall` | `recall_memories` | `api/http.py:6165` |
| 27 | `POST /v1/default/banks/{bank_id}/reflect` | `reflect` | `api/http.py:6410` |
| 28 | `GET /v1/default/banks/{bank_id}/memories/list` | `list_memories` | `api/http.py:5815` |
| 29 | `GET /v1/default/banks/{bank_id}/memories/{memory_id}` | `get_memory` | `api/http.py:6046` |

## 单元 `observations`（#30–#35）

| # | API | operationId | 参考装饰器行 |
|---|---|---|---|
| 30 | `PATCH /v1/default/banks/{bank_id}/memories/{memory_id}` | `update_memory` | `api/http.py:6078` |
| 31 | `GET /v1/default/banks/{bank_id}/memories/{memory_id}/history` | `get_observation_history` | `api/http.py:6134` |
| 32 | `DELETE /v1/default/banks/{bank_id}/observations` | `clear_observations` | `api/http.py:9462` |
| 33 | `GET /v1/default/banks/{bank_id}/observations/scopes` | `list_observation_scopes` | `api/http.py:9487` |
| 34 | `POST /v1/default/banks/{bank_id}/consolidation-strategies/preview` | `preview_consolidation_strategies` | `api/http.py:9521` |
| 35 | `POST /v1/default/banks/{bank_id}/consolidation/recover` | `recover_consolidation` | `api/http.py:9556` |

## 单元 `mental-models`（#36–#44）

| # | API | operationId | 参考装饰器行 |
|---|---|---|---|
| 36 | `DELETE /v1/default/banks/{bank_id}/memories/{memory_id}/observations` | `clear_memory_observations` | `api/http.py:9581` |
| 37 | `POST /v1/default/banks/{bank_id}/consolidate` | `trigger_consolidation` | `api/http.py:9705` |
| 38 | `GET /v1/default/banks/{bank_id}/mental-models` | `list_mental_models` | `api/http.py:6920` |
| 39 | `GET /v1/default/banks/{bank_id}/mental-models/{mental_model_id}` | `get_mental_model` | `api/http.py:6983` |
| 40 | `GET /v1/default/banks/{bank_id}/mental-models/{mental_model_id}/history` | `get_mental_model_history` | `api/http.py:7021` |
| 41 | `POST /v1/default/banks/{bank_id}/mental-models` | `create_mental_model` | `api/http.py:7052` |
| 42 | `POST /v1/default/banks/{bank_id}/mental-models/{mental_model_id}/refresh` | `refresh_mental_model` | `api/http.py:7099` |
| 43 | `POST /v1/default/banks/{bank_id}/mental-models/{mental_model_id}/dry-run-refresh` | `dry_run_refresh_mental_model` | `api/http.py:7133` |
| 44 | `POST /v1/default/banks/{bank_id}/mental-models/{mental_model_id}/clear` | `clear_mental_model` | `api/http.py:7183` |

## 单元 `knowledge`（#45–#52）

| # | API | operationId | 参考装饰器行 |
|---|---|---|---|
| 45 | `PATCH /v1/default/banks/{bank_id}/mental-models/{mental_model_id}` | `update_mental_model` | `api/http.py:7219` |
| 46 | `DELETE /v1/default/banks/{bank_id}/mental-models/{mental_model_id}` | `delete_mental_model` | `api/http.py:7262` |
| 47 | `GET /v1/default/banks/{bank_id}/knowledge-base/tree` | `get_knowledge_base_tree` | `api/http.py:7302` |
| 48 | `POST /v1/default/banks/{bank_id}/knowledge-base/folders` | `create_knowledge_folder` | `api/http.py:7329` |
| 49 | `POST /v1/default/banks/{bank_id}/knowledge-base/pages` | `create_knowledge_page` | `api/http.py:7361` |
| 50 | `GET /v1/default/banks/{bank_id}/knowledge-base/export` | `export_knowledge_base` | `api/http.py:7414` |
| 51 | `GET /v1/default/banks/{bank_id}/knowledge-base/search` | `search_knowledge_base` | `api/http.py:7457` |
| 52 | `GET /v1/default/banks/{bank_id}/knowledge-base/pages/{page_id}` | `get_knowledge_page` | `api/http.py:7492` |

## 单元 `directives`（#53–#57）

| # | API | operationId | 参考装饰器行 |
|---|---|---|---|
| 53 | `PATCH /v1/default/banks/{bank_id}/knowledge-base/nodes/{node_id}` | `update_knowledge_node` | `api/http.py:7520` |
| 54 | `DELETE /v1/default/banks/{bank_id}/knowledge-base/nodes/{node_id}` | `delete_knowledge_node` | `api/http.py:7589` |
| 55 | `GET /v1/default/banks/{bank_id}/directives` | `list_directives` | `api/http.py:7620` |
| 56 | `GET /v1/default/banks/{bank_id}/directives/{directive_id}` | `get_directive` | `api/http.py:7669` |
| 57 | `POST /v1/default/banks/{bank_id}/directives` | `create_directive` | `api/http.py:7699` |

## 单元 `read-surface`（#58–#70）

| # | API | operationId | 参考装饰器行 |
|---|---|---|---|
| 58 | `PATCH /v1/default/banks/{bank_id}/directives/{directive_id}` | `update_directive` | `api/http.py:7734` |
| 59 | `DELETE /v1/default/banks/{bank_id}/directives/{directive_id}` | `delete_directive` | `api/http.py:7771` |
| 60 | `GET /v1/default/banks/{bank_id}/graph` | `get_graph` | `api/http.py:5774` |
| 61 | `GET /v1/default/banks/{bank_id}/entities` | `list_entities` | `api/http.py:6748` |
| 62 | `GET /v1/default/banks/{bank_id}/entities/graph` | `get_entity_graph` | `api/http.py:6796` |
| 63 | `GET /v1/default/banks/{bank_id}/entities/{entity_id}` | `get_entity` | `api/http.py:6838` |
| 64 | `POST /v1/default/banks/{bank_id}/entities/{entity_id}/regenerate` | `regenerate_entity_observations` | `api/http.py:6895` |
| 65 | `GET /v1/default/banks/{bank_id}/documents` | `list_documents` | `api/http.py:7801` |
| 66 | `GET /v1/default/banks/{bank_id}/documents/{document_id}/chunks` | `-` | `api/http.py:None` |
| 67 | `POST /v1/default/banks/{bank_id}/documents/{document_id}/reprocess` | `-` | `api/http.py:None` |
| 68 | `GET /v1/default/banks/{bank_id}/documents/{document_id}` | `-` | `api/http.py:None` |
| 69 | `GET /v1/default/banks/{bank_id}/tags` | `list_tags` | `api/http.py:8009` |
| 70 | `GET /v1/default/chunks/{chunk_id}` | `-` | `api/http.py:None` |

## 单元 `operations`（#71–#75）

| # | API | operationId | 参考装饰器行 |
|---|---|---|---|
| 71 | `PATCH /v1/default/banks/{bank_id}/documents/{document_id}` | `-` | `api/http.py:None` |
| 72 | `DELETE /v1/default/banks/{bank_id}/documents/{document_id}` | `-` | `api/http.py:None` |
| 73 | `GET /v1/default/banks/{bank_id}/operations` | `list_operations` | `api/http.py:8210` |
| 74 | `GET /v1/default/banks/{bank_id}/operations/{operation_id}` | `get_operation_status` | `api/http.py:8258` |
| 75 | `DELETE /v1/default/banks/{bank_id}/operations/{operation_id}` | `cancel_operation` | `api/http.py:8296` |

## 单元 `transfer`（#76–#85）

| # | API | operationId | 参考装饰器行 |
|---|---|---|---|
| 76 | `POST /v1/default/banks/{bank_id}/operations/{operation_id}/retry` | `retry_operation` | `api/http.py:8333` |
| 77 | `DELETE /v1/default/banks/{bank_id}/operations/{operation_id}/delete` | `delete_operation` | `api/http.py:8363` |
| 78 | `POST /v1/default/banks/{bank_id}/import` | `import_bank_template` | `api/http.py:8716` |
| 79 | `GET /v1/default/banks/{bank_id}/export` | `export_bank_template` | `api/http.py:8803` |
| 80 | `GET /v1/default/banks/{bank_id}/document-transfer` | `export_documents_sync_removed` | `api/http.py:8894` |
| 81 | `POST /v1/default/banks/{bank_id}/document-transfer/export` | `export_documents` | `api/http.py:8924` |
| 82 | `POST /v1/default/banks/{bank_id}/document-transfer` | `import_documents` | `api/http.py:8988` |
| 83 | `POST /v1/default/banks/{bank_id}/transfer/export` | `export_bank_transfer` | `api/http.py:9062` |
| 84 | `POST /v1/default/banks/{bank_id}/transfer/import` | `import_bank_transfer` | `api/http.py:9146` |
| 85 | `POST /v1/default/banks/{bank_id}/clone` | `clone_bank` | `api/http.py:9252` |

## 单元 `webhooks`（#86–#90）

| # | API | operationId | 参考装饰器行 |
|---|---|---|---|
| 86 | `GET /v1/default/banks/{bank_id}/attachments/{attachment_id}` | `get_bank_attachment` | `api/http.py:9335` |
| 87 | `GET /v1/default/files/download/{key}` | `-` | `api/http.py:None` |
| 88 | `POST /v1/default/banks/{bank_id}/webhooks` | `create_webhook` | `api/http.py:9768` |
| 89 | `GET /v1/default/banks/{bank_id}/webhooks` | `list_webhooks` | `api/http.py:9829` |
| 90 | `DELETE /v1/default/banks/{bank_id}/webhooks/{webhook_id}` | `delete_webhook` | `api/http.py:9892` |

## 单元 `reporting`（#91–#100）

| # | API | operationId | 参考装饰器行 |
|---|---|---|---|
| 91 | `PATCH /v1/default/banks/{bank_id}/webhooks/{webhook_id}` | `update_webhook` | `api/http.py:9923` |
| 92 | `GET /v1/default/banks/{bank_id}/webhooks/{webhook_id}/deliveries` | `list_webhook_deliveries` | `api/http.py:10006` |
| 93 | `GET /v1/default/banks/{bank_id}/stats` | `get_agent_stats` | `api/http.py:6620` |
| 94 | `POST /v1/default/banks/{bank_id}/health/llm` | `test_bank_llm` | `api/http.py:6678` |
| 95 | `GET /v1/default/banks/{bank_id}/stats/memories-timeseries` | `get_memories_timeseries` | `api/http.py:6713` |
| 96 | `GET /v1/default/banks/{bank_id}/audit-logs` | `list_audit_logs` | `api/http.py:10574` |
| 97 | `GET /v1/default/banks/{bank_id}/audit-logs/stats` | `audit_log_stats` | `api/http.py:10615` |
| 98 | `GET /v1/default/banks/{bank_id}/llm-requests` | `list_llm_requests` | `api/http.py:10648` |
| 99 | `GET /v1/default/banks/{bank_id}/llm-requests/stats` | `llm_request_stats` | `api/http.py:10706` |
| 100 | `GET /v1/version` | `-` | `api/http.py:5716` |
