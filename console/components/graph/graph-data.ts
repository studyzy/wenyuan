/**
 * Shared graph data model and conversion.
 *
 * Framework-agnostic on purpose: the renderer consumes these types, and the
 * converter turns the upstream API response into them, so a second renderer
 * would not have to re-derive either.
 */

export interface GraphNode {
  id: string;
  label?: string;
  color?: string;
  size?: number;
  group?: string;
  metadata?: Record<string, unknown>;
}

export interface GraphLink {
  source: string;
  target: string;
  color?: string;
  width?: number;
  type?: string;
  entity?: string;
  weight?: number;
  metadata?: Record<string, unknown>;
}

export interface GraphData {
  nodes: GraphNode[];
  links: GraphLink[];
}

/** The shape the upstream graph endpoint returns. */
export interface UpstreamGraphPayload {
  nodes?: Array<{ data: { id: string; label?: string; color?: string } }>;
  edges?: Array<{
    data: {
      source: string;
      target: string;
      color?: string;
      lineStyle?: string;
      linkType?: string;
      entityName?: string;
      weight?: number;
      similarity?: number;
    };
  }>;
  table_rows?: Array<{ id: string; text: string; entities?: string; context?: string }>;
}

/** Longest label taken from a memory's text before it is elided. */
const LABEL_MAX = 40;

/**
 * Convert the upstream graph response into the renderer's model.
 *
 * The upstream separates the structural graph (nodes and edges) from the rows
 * that describe them, so the two are joined here: a node's label prefers its own
 * label, falls back to its memory text, and finally to a short id prefix so
 * every node is labelled with something.
 *
 * An edge with no explicit link type is classified from its line style, which is
 * how the upstream distinguishes a temporal link from a semantic one.
 */
export function convertHindsightGraphData(upstream: UpstreamGraphPayload): GraphData {
  const nodes: GraphNode[] = (upstream.nodes ?? []).map((entry) => {
    const row = upstream.table_rows?.find((candidate) => candidate.id === entry.data.id);
    let label = entry.data.label;
    if (!label && row?.text) {
      label = row.text.length > LABEL_MAX ? `${row.text.substring(0, LABEL_MAX)}...` : row.text;
    }
    if (!label) label = entry.data.id.substring(0, 8);
    return {
      id: entry.data.id,
      label,
      color: entry.data.color,
      metadata: row as Record<string, unknown> | undefined,
    };
  });

  const links: GraphLink[] = (upstream.edges ?? []).map((entry) => ({
    source: entry.data.source,
    target: entry.data.target,
    color: entry.data.color,
    type: entry.data.linkType ?? (entry.data.lineStyle === "dashed" ? "temporal" : "semantic"),
    entity: entry.data.entityName,
    weight: entry.data.weight ?? entry.data.similarity,
  }));

  return { nodes, links };
}