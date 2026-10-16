// Shared graph data model + conversion used by the memory visualizations
// (Constellation, entities view). The Cytoscape-based "Graph" view that used to
// live here was removed; only the framework-agnostic types and the API-response
// converter remain, since the constellation and entity views build on them.

// ============================================================================
// Types & Interfaces
// ============================================================================

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

// ============================================================================
// Utility Functions
// ============================================================================

export function convertGraphPayload(payload: UpstreamGraphPayload): GraphData {
  const nodes: GraphNode[] = (payload.nodes || []).map((n) => {
    const tableRow = payload.table_rows?.find((r) => r.id === n.data.id);
    // Use memory text as label, truncated to ~40 chars
    let label = n.data.label;
    if (!label && tableRow?.text) {
      label = tableRow.text.length > 40 ? tableRow.text.substring(0, 40) + "..." : tableRow.text;
    }
    if (!label) {
      label = n.data.id.substring(0, 8);
    }
    return {
      id: n.data.id,
      label,
      color: n.data.color,
      metadata: tableRow,
    };
  });

  const links: GraphLink[] = (payload.edges || []).map((e) => ({
    source: e.data.source,
    target: e.data.target,
    color: e.data.color,
    // Use linkType directly from API, fallback to lineStyle check, default to semantic
    type: e.data.linkType || (e.data.lineStyle === "dashed" ? "temporal" : "semantic"),
    entity: e.data.entityName, // API returns entityName
    weight: e.data.weight ?? e.data.similarity,
  }));

  return { nodes, links };
}
