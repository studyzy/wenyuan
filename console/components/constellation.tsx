"use client";

/**
 * Flat import path for the constellation renderer.
 *
 * The ported view components import `./constellation`; the console's own tree
 * keeps its `components/graph/` area. This module re-exports the single
 * implementation so both paths resolve to one source rather than two copies.
 */
export { Constellation, MOTION_STORAGE_KEY } from "./graph/constellation";
export type { ConstellationProps } from "./graph/constellation";