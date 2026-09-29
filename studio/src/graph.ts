// Canvas projection of the semantic graph. The canvas is a *view*:
// every semantic field that React Flow does not understand lives
// inside node.data.node verbatim, so fromCanvas can rebuild the
// artifact without drift. Layout (positions/viewport) is written
// back into presentation.layout only — a layout-only edit never
// touches definition bytes, and an untouched canvas produces
// byte-identical presentation.

import type { Edge as RFEdge, Node as RFNode, Viewport } from "@xyflow/react";
import type { Artifact, Edge, Node } from "./schema";

export interface CanvasNodeData extends Record<string, unknown> {
  node: Node;
  unresolved: boolean;
  isExit: boolean;
  // True when the node had an authored position; otherwise the
  // position is a grid fallback and only persists if the user moves it.
  positionAuthored: boolean;
  fallbackPos: { x: number; y: number };
}

export interface Canvas {
  nodes: RFNode<CanvasNodeData>[];
  edges: RFEdge[];
  viewport?: Viewport;
}

const GRID = 160;

export function toCanvas(artifact: Artifact, catalog?: string[]): Canvas {
  const g = artifact.definition.graph;
  const positions = artifact.presentation?.layout?.positions ?? {};
  const exits = new Set(g.exits);
  const nodes: RFNode<CanvasNodeData>[] = g.nodes.map((n, i) => {
    const authored = positions[n.id];
    const fallbackPos = { x: (i % 4) * GRID, y: Math.floor(i / 4) * GRID };
    return {
      id: n.id,
      type: "inofyNode",
      position: authored ?? fallbackPos,
      data: {
        node: n,
        unresolved:
          n.kind === "call" && catalog != null && !catalog.includes(n.type ?? ""),
        isExit: exits.has(n.id),
        positionAuthored: authored != null,
        fallbackPos,
      },
    };
  });
  const edges: RFEdge[] = g.edges.map((e, i) => ({
    id: `e${i}-${e.from}-${e.port ?? ""}-${e.to}`,
    source: e.from,
    target: e.to,
    ...(e.port ? { sourceHandle: e.port, label: e.port } : {}),
    data: { semantic: e },
  }));
  const canvas: Canvas = { nodes, edges };
  const vp = artifact.presentation?.layout?.viewport;
  if (vp) canvas.viewport = { x: vp.x, y: vp.y, zoom: vp.zoom };
  return canvas;
}

// fromCanvas merges canvas membership/layout back onto the prior
// artifact: node/edge identity comes from the canvas, every other
// semantic and presentation field is carried verbatim from `base`.
export function fromCanvas(canvas: Canvas, base: Artifact): Artifact {
  const ids = new Set(canvas.nodes.map((n) => n.id));
  const semanticNodes: Node[] = canvas.nodes.map((n) => n.data.node);
  const semanticEdges: Edge[] = canvas.edges
    .map((e) => {
      const sem = e.data?.semantic as Edge | undefined;
      return (
        sem ?? {
          from: e.source,
          to: e.target,
          ...(e.sourceHandle ? { port: e.sourceHandle } : {}),
        }
      );
    })
    .filter((e) => ids.has(e.from) && ids.has(e.to));
  // Exit membership survives only while its node survives.
  const exits = base.definition.graph.exits.filter((x) => ids.has(x));

  // Layout write-back: persist a position only when it was authored,
  // when the node is new, or when the user moved it off the fallback.
  const basePositions = base.presentation?.layout?.positions ?? {};
  const baseIds = new Set(base.definition.graph.nodes.map((n) => n.id));
  const positions: Record<string, { x: number; y: number }> = {};
  let positionsChanged = false;
  for (const n of canvas.nodes) {
    const prior = basePositions[n.id];
    const d = n.data;
    const moved =
      d.fallbackPos == null ||
      n.position.x !== d.fallbackPos.x ||
      n.position.y !== d.fallbackPos.y;
    if (prior != null || !baseIds.has(n.id) || (d.positionAuthored === false && moved)) {
      positions[n.id] = { x: n.position.x, y: n.position.y };
      if (!prior || prior.x !== n.position.x || prior.y !== n.position.y) {
        positionsChanged = true;
      }
    }
  }
  const baseVP = base.presentation?.layout?.viewport;
  const vpChanged =
    canvas.viewport != null &&
    (baseVP == null ||
      canvas.viewport.x !== baseVP.x ||
      canvas.viewport.y !== baseVP.y ||
      canvas.viewport.zoom !== baseVP.zoom);

  let presentation = base.presentation;
  if (positionsChanged || vpChanged) {
    presentation = {
      ...base.presentation,
      layout: {
        ...base.presentation?.layout,
        ...(positionsChanged ? { positions } : {}),
        ...(vpChanged && canvas.viewport
          ? {
              viewport: {
                x: canvas.viewport.x,
                y: canvas.viewport.y,
                zoom: canvas.viewport.zoom,
              },
            }
          : {}),
      },
    };
  }

  return {
    ...base,
    definition: {
      ...base.definition,
      graph: {
        ...base.definition.graph,
        nodes: semanticNodes,
        edges: semanticEdges,
        exits,
      },
    },
    ...(presentation === undefined ? {} : { presentation }),
  };
}
