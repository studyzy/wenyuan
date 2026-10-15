/**
 * Flat import path for the graph data model.
 *
 * The ported view components import `./graph-data`; the frontend's own tree keeps
 * its `components/graph/` area. This module re-exports the single implementation
 * so both paths resolve to one source rather than two copies.
 */
export * from "./graph/graph-data";