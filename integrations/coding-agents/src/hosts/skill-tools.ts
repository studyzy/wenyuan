/**
 * The memory tool identifiers the companion skill names.
 *
 * These are the identifiers the installed MCP server exposes, so the skill
 * text and the server surface cannot drift: the generated skill body is filled
 * from this declaration, and a test asserts the file names exactly these.
 */

export const SKILL_NAME = "wenyuan-memory";

export const SKILL_DESCRIPTION =
  "Remember and recall what this project's coding sessions have already established";

/**
 * The six memory tools the skill instructs the agent to call, by their fixed
 * server-exposed identifiers. The keys are the roles the skill body names; the
 * values are the identifiers the installed server advertises.
 */
export const SKILL_TOOL_IDENTIFIERS: Readonly<Record<string, string>> = {
  document_ingest: "retain",
  initiative_capture: "create_knowledge_page",
  knowledge_search: "search_knowledge_base",
  knowledge_read: "get_knowledge_page",
  knowledge_list: "get_knowledge_base_tree",
  reflect: "reflect",
};