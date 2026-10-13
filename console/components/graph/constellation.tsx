"use client";

import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { layoutWithLines, prepare, prepareWithSegments } from "@chenglou/pretext";

import { useTranslations } from "../../i18n";
import type { GraphData, GraphLink, GraphNode } from "./graph-data";

/** Remembers the ambient-motion preference for every graph in the app. */
export const MOTION_STORAGE_KEY = "graph-motion";

export interface ConstellationProps {
  data: GraphData;
  height?: number;
  onNodeClick?: (node: GraphNode) => void;
  nodeColorFn?: (node: GraphNode) => string;
  linkColorFn?: (link: GraphLink) => string;
  /** Overrides the on-screen dot radius. Default derives it from the link count. */
  nodeSizeFn?: (node: GraphNode) => number;
  /** Packs labels densely with no zoom threshold, for short labels like names. */
  compactLabels?: boolean;
  /** Overrides the heat gradient (0..1). Default is a normalised link count. */
  nodeHeatFn?: (node: GraphNode) => number;
  heatLegendLabel?: string;
  heatLegendEndpoints?: [string, string];
  /** Draws a small-to-big dot legend when size encodes a dimension. */
  sizeLegendLabel?: string;
  /** Nodes sharing a key are laid out around a common centroid and blobbed. */
  clusterKeyFn?: (node: GraphNode) => string | null;
  clusterColorFn?: (key: string) => string;
  clusterLabelFn?: (key: string) => string;
  /** Returns the groups a node belongs to; each group is drawn as its own hull. */
  nodeGroupsFn?: (node: GraphNode) => string[];
  groupColorFn?: (key: string) => string;
  groupLabelFn?: (key: string) => string;
}

type PreparedNode = {
  node: GraphNode;
  wx: number;
  wy: number;
  prepared: ReturnType<typeof prepareWithSegments>;
  preparedHeight: ReturnType<typeof prepare>;
  color: string;
  heatColor: string;
  linkCount: number;
  phase: number;
};

const FONT_SMALL = '11px Inter, -apple-system, "Segoe UI", sans-serif';
const FONT_BOLD = '600 10px Inter, -apple-system, "Segoe UI", sans-serif';
const MONO = '11px "SF Mono", "Fira Code", Consolas, monospace';

const DEFAULT_NODE_COLOR = "#0074d9";

const LINK_TYPE_COLORS: Record<string, string> = {
  semantic: "#2C82F5",
  temporal: "#F59E0B",
  entity: "#EC4899",
};

function lerp(a: number, b: number, t: number): number {
  return a + (b - a) * t;
}

function hexToRgba(hex: string, alpha: number): string {
  const normalized = hex.replace("#", "");
  const full =
    normalized.length === 3
      ? normalized
          .split("")
          .map((char) => char + char)
          .join("")
      : normalized;
  const value = Number.parseInt(full, 16);
  const r = (value >> 16) & 255;
  const g = (value >> 8) & 255;
  const b = value & 255;
  return `rgba(${r}, ${g}, ${b}, ${alpha})`;
}

/** Convex hull by monotone chain, used for the cluster and group blobs. */
function convexHull(points: Array<[number, number]>): Array<[number, number]> {
  if (points.length < 3) return points;
  const sorted = [...points].sort((a, b) => a[0] - b[0] || a[1] - b[1]);
  const cross = (o: [number, number], a: [number, number], b: [number, number]) =>
    (a[0] - o[0]) * (b[1] - o[1]) - (a[1] - o[1]) * (b[0] - o[0]);
  const lower: Array<[number, number]> = [];
  for (const point of sorted) {
    while (lower.length >= 2 && cross(lower[lower.length - 2], lower[lower.length - 1], point) <= 0) {
      lower.pop();
    }
    lower.push(point);
  }
  const upper: Array<[number, number]> = [];
  for (const point of [...sorted].reverse()) {
    while (upper.length >= 2 && cross(upper[upper.length - 2], upper[upper.length - 1], point) <= 0) {
      upper.pop();
    }
    upper.push(point);
  }
  upper.pop();
  lower.pop();
  return lower.concat(upper);
}

/** A cool-to-warm ramp for the heat gradient. */
function heatColor(t: number): string {
  const clamped = Math.max(0, Math.min(1, t));
  const r = Math.round(lerp(44, 236, clamped));
  const g = Math.round(lerp(130, 72, clamped));
  const b = Math.round(lerp(245, 153, clamped));
  return `rgb(${r}, ${g}, ${b})`;
}

/** Stable hash so a node keeps its position and phase across renders. */
function hashStr(value: string): number {
  let hash = 0;
  for (let i = 0; i < value.length; i += 1) {
    hash = (hash << 5) - hash + value.charCodeAt(i);
    hash |= 0;
  }
  return hash;
}

/** Read the stored motion preference, defaulting to on. */
function readStoredMotion(): boolean {
  if (typeof localStorage === "undefined") return true;
  return localStorage.getItem(MOTION_STORAGE_KEY) !== "off";
}

/** Track the theme by watching the class the theme toggle writes. */
function useIsDarkMode(): boolean {
  const [isDark, setIsDark] = useState(false);
  useEffect(() => {
    const update = () => setIsDark(document.documentElement.classList.contains("dark"));
    update();
    const observer = new MutationObserver(update);
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ["class"] });
    return () => observer.disconnect();
  }, []);
  return isDark;
}

function toolbarBtnStyle(isDark: boolean): CSSProperties {
  return {
    background: isDark ? "rgba(15,23,36,0.85)" : "rgba(255,255,255,0.9)",
    border: `1px solid ${isDark ? "rgba(100,160,255,0.18)" : "rgba(0,0,0,0.10)"}`,
    color: isDark ? "#DCE8FF" : "#0D1117",
    borderRadius: 8,
    padding: "4px 10px",
    fontSize: 12,
    cursor: "pointer",
  };
}

/**
 * A force-free constellation renderer.
 *
 * Layout is deterministic: a node's position comes from a hash of its id, so the
 * same graph always draws the same way and a re-render never reshuffles it. Three
 * layouts are available and mutually exclusive — flat ring, cluster (nodes share
 * a centroid and are wrapped in a blob), and group (nodes sit at the mean of
 * their groups' centroids so a shared node lands in the overlap, drawing an Euler
 * diagram).
 *
 * Ambient drift is on by default and remembered globally rather than per graph,
 * so the preference follows the reader. Pan, zoom, hover and click all work
 * without re-rendering React: the interaction state lives in a ref and the canvas
 * paints from `requestAnimationFrame`.
 */
export function Constellation({
  data,
  height = 700,
  onNodeClick,
  nodeColorFn,
  linkColorFn,
  nodeSizeFn,
  nodeHeatFn,
  heatLegendLabel,
  heatLegendEndpoints,
  compactLabels,
  sizeLegendLabel,
  clusterKeyFn,
  clusterColorFn,
  clusterLabelFn,
  nodeGroupsFn,
  groupColorFn,
  groupLabelFn,
}: ConstellationProps) {
  const t = useTranslations("constellation");
  const wrapperRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const isDark = useIsDarkMode();

  // The stored preference is read during the first render rather than in an
  // effect, so the initial paint already honours it and there is no flash of
  // motion for a reader who turned it off.
  const [motionEnabled, setMotionEnabled] = useState(readStoredMotion);
  const motionRef = useRef(motionEnabled);
  // Elapsed animation seconds, accumulated only while motion is on, so pausing
  // freezes the field and resuming continues without a jump.
  const clockRef = useRef({ seconds: 0, lastFrameMs: 0 });
  const animRef = useRef(0);

  const toggleMotion = useCallback(() => {
    setMotionEnabled((previous) => {
      const next = !previous;
      motionRef.current = next;
      localStorage.setItem(MOTION_STORAGE_KEY, next ? "on" : "off");
      return next;
    });
  }, []);

  // Interaction state lives in a ref: it changes every frame and would otherwise
  // re-render the tree sixty times a second.
  const stateRef = useRef({
    panX: 0,
    panY: 0,
    zoom: 1,
    dragging: false,
    lastX: 0,
    lastY: 0,
    hoverIndex: -1,
  });

  const prepared = useMemo(() => {
    const count = Math.max(data.nodes.length, 1);
    const linkCounts = new Map<string, number>();
    for (const link of data.links) {
      linkCounts.set(link.source, (linkCounts.get(link.source) ?? 0) + 1);
      linkCounts.set(link.target, (linkCounts.get(link.target) ?? 0) + 1);
    }
    const maxLinkCount = Math.max(1, ...linkCounts.values());

    // Groups (Euler mode) take precedence over clusters when either is supplied.
    const nodeGroups = nodeGroupsFn ? data.nodes.map((n) => nodeGroupsFn(n) || []) : null;
    const groupsActive = !!nodeGroups && nodeGroups.some((g) => g.length > 0);
    const groupCentroid = new Map<string, { cx: number; cy: number }>();
    if (groupsActive) {
      const allGroups = [...new Set(nodeGroups!.flat())];
      // Two groups sit opposite each other; three or more spread around a wide
      // ring so each set reads as its own circle with a clear overlap lens.
      const ringR = Math.max(300, allGroups.length * 170);
      allGroups.forEach((group, index) => {
        const angle = (index / Math.max(allGroups.length, 1)) * Math.PI * 2 - Math.PI / 2;
        groupCentroid.set(group, { cx: Math.cos(angle) * ringR, cy: Math.sin(angle) * ringR });
      });
    }

    const clusterKeys = clusterKeyFn ? data.nodes.map((n) => clusterKeyFn(n)) : null;
    const clusterMembers = new Map<string, number[]>();
    if (clusterKeys && !groupsActive) {
      clusterKeys.forEach((key, index) => {
        if (key == null) return;
        const list = clusterMembers.get(key) ?? [];
        list.push(index);
        clusterMembers.set(key, list);
      });
    }
    const clusterCentroid = new Map<string, { cx: number; cy: number }>();
    if (clusterKeys && !groupsActive) {
      const keys = [...clusterMembers.keys()];
      const ringR = Math.max(260, keys.length * 150);
      keys.forEach((key, index) => {
        const angle = (index / Math.max(keys.length, 1)) * Math.PI * 2 - Math.PI / 2;
        clusterCentroid.set(key, { cx: Math.cos(angle) * ringR, cy: Math.sin(angle) * ringR });
      });
    }

    const nodes: PreparedNode[] = data.nodes.map((node, index) => {
      const text = node.label || node.id.substring(0, 12);
      const clusterKey = clusterKeys ? clusterKeys[index] : null;
      const nodeGroupList = nodeGroups ? nodeGroups[index] : [];
      const color = groupsActive
        ? nodeGroupList.length > 1
          ? "#94a3b8"
          : groupColorFn?.(nodeGroupList[0]) || DEFAULT_NODE_COLOR
        : (clusterKey != null && clusterColorFn?.(clusterKey)) ||
          nodeColorFn?.(node) ||
          node.color ||
          DEFAULT_NODE_COLOR;

      const linkCount = linkCounts.get(node.id) ?? 0;
      // Square root keeps the ramp from saturating: with a linear mapping almost
      // every node reads as hottest.
      const heat = nodeHeatFn
        ? heatColor(Math.max(0, Math.min(1, nodeHeatFn(node))))
        : heatColor(Math.sqrt(linkCount / maxLinkCount));

      const seed = hashStr(node.id);
      let wx: number;
      let wy: number;
      const centroid = clusterKey != null ? clusterCentroid.get(clusterKey) : undefined;

      if (groupsActive) {
        // Mean of the node's groups: a single-group node lands near its group's
        // centre, a shared node lands between groups, in the overlap.
        let gx = 0;
        let gy = 0;
        let gn = 0;
        for (const group of nodeGroupList) {
          const groupCenter = groupCentroid.get(group);
          if (groupCenter) {
            gx += groupCenter.cx;
            gy += groupCenter.cy;
            gn += 1;
          }
        }
        // Fan the members across a disk around that mean so same-combo nodes
        // spread out instead of stacking on one point.
        const angle = ((Math.abs(seed) % 997) / 997) * Math.PI * 2;
        const radius = 40 + Math.sqrt((Math.abs(seed >> 7) % 1000) / 1000) * 190;
        wx = (gn ? gx / gn : 0) + Math.cos(angle) * radius;
        wy = (gn ? gy / gn : 0) + Math.sin(angle) * radius;
      } else if (centroid) {
        const members = clusterMembers.get(clusterKey!)!;
        const position = members.indexOf(index);
        const angle =
          (position / Math.max(members.length, 1)) * Math.PI * 2 + ((seed % 100) / 100) * 0.6;
        const radius = 120 * (0.25 + 0.75 * ((Math.abs(seed) % 1000) / 1000));
        wx = centroid.cx + Math.cos(angle) * radius;
        wy = centroid.cy + Math.sin(angle) * radius;
      } else {
        const angle = (index / count) * Math.PI * 2 + ((seed % 100) / 100) * 0.5;
        const baseRadius = Math.sqrt(count) * 30;
        const radius = baseRadius * 0.3 + ((Math.abs(seed) % 1000) / 1000) * baseRadius * 0.7;
        wx = Math.cos(angle) * radius + ((seed % 200) - 100) * 0.5;
        wy = Math.sin(angle) * radius + (((seed >> 8) % 200) - 100) * 0.5;
      }

      return {
        node,
        wx,
        wy,
        prepared: prepareWithSegments(text, FONT_SMALL),
        preparedHeight: prepare(text, FONT_SMALL),
        color,
        heatColor: centroid || groupsActive ? color : heat,
        linkCount,
        phase: ((Math.abs(seed) % 1000) / 1000) * Math.PI * 2,
      };
    });

    const preparedById = new Map(nodes.map((entry) => [entry.node.id, entry]));
    const bounds = {
      maxLinkCount,
      extent: Math.max(
        200,
        ...nodes.map((entry) => Math.max(Math.abs(entry.wx), Math.abs(entry.wy))),
      ),
    };

    return {
      nodes,
      preparedById,
      linkCounts,
      bounds,
      groupsActive,
      groupCentroid,
      nodeGroups,
      clusterKeys,
      clusterMembers,
      clusterCentroid,
    };
  }, [data, nodeColorFn, nodeHeatFn, nodeGroupsFn, groupColorFn, clusterKeyFn, clusterColorFn]);

  // Paint loop. Everything it reads comes from refs or the prepared model, so a
  // frame never triggers a React render.
  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const context = canvas.getContext("2d");
    if (!context) return;

    const { nodes, preparedById, bounds, groupsActive, groupCentroid, nodeGroups, clusterKeys } =
      prepared;

    const resize = () => {
      const rect = canvas.getBoundingClientRect();
      const dpr = window.devicePixelRatio || 1;
      canvas.width = rect.width * dpr;
      canvas.height = rect.height * dpr;
      context.setTransform(dpr, 0, 0, dpr, 0, 0);
    };
    resize();
    const observer = new ResizeObserver(resize);
    observer.observe(canvas);

    const drawFrame = (nowMs: number) => {
      const state = stateRef.current;
      const clock = clockRef.current;
      const dt = clock.lastFrameMs ? (nowMs - clock.lastFrameMs) / 1000 : 0;
      clock.lastFrameMs = nowMs;
      if (motionRef.current) clock.seconds += Math.min(dt, 0.05);
      const time = clock.seconds;

      const rect = canvas.getBoundingClientRect();
      const width = rect.width;
      const heightPx = rect.height;
      const baseScale = Math.min(width, heightPx) / (bounds.extent * 2.6);
      const scale = baseScale * state.zoom;

      context.clearRect(0, 0, width, heightPx);
      context.save();
      context.translate(width / 2 + state.panX, heightPx / 2 + state.panY);
      context.scale(scale, scale);

      // Group hulls first, so nodes and links sit on top of the blobs.
      if (groupsActive && nodeGroups) {
        const byGroup = new Map<string, Array<[number, number]>>();
        nodes.forEach((entry, index) => {
          for (const group of nodeGroups[index]) {
            const list = byGroup.get(group) ?? [];
            // Blob around the group's centre, not its members, so the overlap
            // reads as a genuine intersection rather than two hugging outlines.
            const centre = groupCentroid.get(group);
            if (centre) list.push([centre.cx * 0.55 + entry.wx * 0.45, centre.cy * 0.55 + entry.wy * 0.45]);
            byGroup.set(group, list);
          }
        });
        for (const [group, points] of byGroup) {
          const hull = convexHull(points);
          if (hull.length < 3) continue;
          const color = groupColorFn?.(group) ?? DEFAULT_NODE_COLOR;
          const centerX = hull.reduce((sum, p) => sum + p[0], 0) / hull.length;
          const centerY = hull.reduce((sum, p) => sum + p[1], 0) / hull.length;
          const spread = Math.max(
            60,
            ...hull.map((p) => Math.hypot(p[0] - centerX, p[1] - centerY)),
          );
          context.beginPath();
          context.arc(centerX, centerY, spread * 1.15, 0, Math.PI * 2);
          context.fillStyle = hexToRgba(color, isDark ? 0.10 : 0.08);
          context.fill();
          context.strokeStyle = hexToRgba(color, isDark ? 0.35 : 0.28);
          context.lineWidth = 1 / scale;
          context.stroke();
          context.fillStyle = hexToRgba(color, 0.9);
          context.font = FONT_BOLD;
          context.textAlign = "center";
          context.fillText(groupLabelFn?.(group) ?? group, centerX, centerY - spread * 1.3);
        }
      }

      // Links next, so they sit under the dots they connect.
      for (const link of data.links) {
        const from = preparedById.get(link.source);
        const to = preparedById.get(link.target);
        if (!from || !to) continue;
        const color =
          linkColorFn?.(link) ??
          link.color ??
          LINK_TYPE_COLORS[link.type ?? "semantic"] ??
          LINK_TYPE_COLORS.semantic;
        context.beginPath();
        context.moveTo(from.wx, from.wy);
        context.lineTo(to.wx, to.wy);
        context.strokeStyle = hexToRgba(color, isDark ? 0.28 : 0.22);
        context.lineWidth = (link.width ?? (1 + (link.weight ?? 0) * 0.5)) / scale;
        if (link.type === "temporal") context.setLineDash([6 / scale, 4 / scale]);
        context.stroke();
        context.setLineDash([]);
      }

      // Nodes and labels.
      const labelThreshold = compactLabels ? 0 : 0.55;
      nodes.forEach((entry, index) => {
        const drift = motionRef.current ? Math.sin(time * 0.6 + entry.phase) * 1.5 : 0;
        const radius =
          nodeSizeFn?.(entry.node) ?? 4 + Math.sqrt(entry.linkCount / bounds.maxLinkCount) * 10;
        const x = entry.wx + drift;
        const y = entry.wy + drift * 0.6;
        const hovered = state.hoverIndex === index;

        context.beginPath();
        context.arc(x, y, hovered ? radius * 1.3 : radius, 0, Math.PI * 2);
        context.fillStyle = entry.heatColor;
        context.fill();
        if (hovered) {
          context.strokeStyle = isDark ? "#DCE8FF" : "#0D1117";
          context.lineWidth = 1.5 / scale;
          context.stroke();
        }

        const labelVisible = compactLabels || state.zoom > labelThreshold || hovered;
        if (labelVisible) {
          const lines = layoutWithLines(entry.prepared, 160, 16);
          context.fillStyle = isDark ? "#94A8C9" : "#525866";
          context.font = FONT_SMALL;
          context.textAlign = "center";
          lines.lines.slice(0, 2).forEach((line, lineIndex) => {
            context.fillText(line.text, x, y + radius + 12 + lineIndex * 12);
          });
        }
      });

      context.restore();
      animRef.current = requestAnimationFrame(drawFrame);
    };

    animRef.current = requestAnimationFrame(drawFrame);
    return () => {
      cancelAnimationFrame(animRef.current);
      observer.disconnect();
    };
  }, [data, prepared, isDark, nodeSizeFn, linkColorFn, compactLabels, groupColorFn, groupLabelFn]);

  // Pointer interaction: pan by drag, zoom by wheel, hover for the cursor, click
  // to open a node. All of it mutates the ref and lets the painter catch up.
  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const { nodes, bounds } = prepared;

    const pointerToIndex = (clientX: number, clientY: number): number => {
      const rect = canvas.getBoundingClientRect();
      const state = stateRef.current;
      const scale = (Math.min(rect.width, rect.height) / (bounds.extent * 2.6)) * state.zoom;
      const worldX = (clientX - rect.left - rect.width / 2 - state.panX) / scale;
      const worldY = (clientY - rect.top - rect.height / 2 - state.panY) / scale;
      let best = -1;
      let bestDistance = Number.POSITIVE_INFINITY;
      nodes.forEach((entry, index) => {
        const distance = Math.hypot(entry.wx - worldX, entry.wy - worldY);
        const hitRadius = Math.max(8, 4 + Math.sqrt(entry.linkCount / bounds.maxLinkCount) * 10) + 4;
        if (distance < hitRadius && distance < bestDistance) {
          bestDistance = distance;
          best = index;
        }
      });
      return best;
    };

    const onPointerDown = (event: PointerEvent) => {
      const state = stateRef.current;
      state.dragging = true;
      state.lastX = event.clientX;
      state.lastY = event.clientY;
      canvas.setPointerCapture(event.pointerId);
    };
    const onPointerMove = (event: PointerEvent) => {
      const state = stateRef.current;
      if (state.dragging) {
        state.panX += event.clientX - state.lastX;
        state.panY += event.clientY - state.lastY;
        state.lastX = event.clientX;
        state.lastY = event.clientY;
        return;
      }
      const index = pointerToIndex(event.clientX, event.clientY);
      state.hoverIndex = index;
      canvas.style.cursor = index >= 0 ? "pointer" : "grab";
    };
    const onPointerUp = (event: PointerEvent) => {
      const state = stateRef.current;
      const moved =
        Math.hypot(event.clientX - state.lastX, event.clientY - state.lastY) > 4 || !state.dragging;
      state.dragging = false;
      canvas.style.cursor = "grab";
      if (moved && onNodeClick) {
        const index = pointerToIndex(event.clientX, event.clientY);
        if (index >= 0) onNodeClick(nodes[index].node);
      }
    };
    const onPointerLeave = () => {
      stateRef.current.dragging = false;
      stateRef.current.hoverIndex = -1;
      canvas.style.cursor = "grab";
    };
    const onWheel = (event: WheelEvent) => {
      event.preventDefault();
      const state = stateRef.current;
      state.zoom = Math.max(0.2, Math.min(6, state.zoom * (event.deltaY > 0 ? 0.9 : 1.1)));
    };

    canvas.addEventListener("pointerdown", onPointerDown);
    canvas.addEventListener("pointermove", onPointerMove);
    canvas.addEventListener("pointerup", onPointerUp);
    canvas.addEventListener("pointerleave", onPointerLeave);
    canvas.addEventListener("wheel", onWheel, { passive: false });
    return () => {
      canvas.removeEventListener("pointerdown", onPointerDown);
      canvas.removeEventListener("pointermove", onPointerMove);
      canvas.removeEventListener("pointerup", onPointerUp);
      canvas.removeEventListener("pointerleave", onPointerLeave);
      canvas.removeEventListener("wheel", onWheel);
    };
  }, [prepared, onNodeClick]);

  const resetView = useCallback(() => {
    stateRef.current.panX = 0;
    stateRef.current.panY = 0;
    stateRef.current.zoom = 1;
  }, []);

  return (
    <div
      ref={wrapperRef}
      className="relative w-full overflow-hidden rounded-[16px] border border-border bg-card"
      style={{ height: isFullscreen ? "100vh" : height }}
      data-fullscreen={isFullscreen}
    >
      <canvas ref={canvasRef} className="h-full w-full" />

      <div className="absolute right-3 top-3 flex gap-2">
        <button type="button" style={toolbarBtnStyle(isDark)} onClick={resetView}>
          {t("reset")}
        </button>
        <button type="button" style={toolbarBtnStyle(isDark)} onClick={toggleMotion}>
          {motionEnabled ? t("pauseMotion") : t("resumeMotion")}
        </button>
        <button
          type="button"
          style={toolbarBtnStyle(isDark)}
          onClick={() => setIsFullscreen((value) => !value)}
        >
          {isFullscreen ? t("exitFullscreen") : t("fullscreen")}
        </button>
      </div>

      <div className="pointer-events-none absolute bottom-3 left-3 flex flex-col gap-1">
        <Legend label={heatLegendLabel ?? t("links")} endpoints={heatLegendEndpoints ?? [t("few"), t("many")]} mono={false} />
        {sizeLegendLabel && <Legend label={sizeLegendLabel} endpoints={[t("small"), t("large")]} mono />}
        {prepared.clusterKeys?.some((key) => key != null) && clusterLabelFn && (
          <span className="text-[10px] text-muted-foreground">{t("clustered")}</span>
        )}
      </div>
    </div>
  );
}

function Legend({
  label,
  endpoints,
  mono,
}: {
  label: string;
  endpoints: [string, string];
  mono: boolean;
}) {
  return (
    <div className="flex items-center gap-2 text-[10px] uppercase tracking-wider text-muted-foreground">
      <span>{label}</span>
      <span>{endpoints[0]}</span>
      <span
        className="inline-block h-[6px] w-[36px] rounded-full"
        style={{
          background: mono
            ? "linear-gradient(90deg, #2C82F5 40%, #2C82F5 100%)"
            : `linear-gradient(90deg, ${heatColor(0)}, ${heatColor(1)})`,
        }}
      />
      <span>{endpoints[1]}</span>
    </div>
  );
}
