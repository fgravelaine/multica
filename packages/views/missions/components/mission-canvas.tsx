"use client";

// SPIKE (not upstream): the mission canvas.
//
// The tree as a canvas you pan and zoom, rather than an indented list. A list
// answers "what is under this" one row at a time; the mission question is
// "where is this", and that is a shape — how wide the mission got, which branch
// stopped, how far the frontier is from the root. You read a shape at a glance
// and a list never.
//
// READ ONLY, and enforced rather than implied: nodes cannot be dragged,
// connected, or deleted, and there is no handler in this file that writes
// anything. The only interaction that leaves the canvas is a link to the issue.
//
// Layout is computed here rather than by a layout library. It is a tree, not a
// graph — every node has exactly one parent — so the tidy-tree rule is four
// lines (a leaf takes the next row; a parent centres on its children) and a
// second dependency would buy nothing.

import { useCallback, useMemo } from "react";
import {
  Background,
  BackgroundVariant,
  Controls,
  Handle,
  MiniMap,
  Position,
  ReactFlow,
  ReactFlowProvider,
  type Edge,
  type Node,
  type NodeProps,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Hand, Pause } from "lucide-react";
import type { MissionNode, MissionResponse } from "@multica/core/types";
import { useWorkspacePaths } from "@multica/core/paths";
import { useTheme } from "@multica/ui/components/common/theme-provider";
import { cn } from "@multica/ui/lib/utils";

const NODE_W = 248;
/**
 * Fixed, and the node's own box is fixed to match: React Flow needs the height
 * before it renders to lay anything out, so a node that sizes itself to its
 * title would push its own text out of its box. Two lines of title is the
 * budget; longer titles clamp.
 */
const NODE_H = 84;
/** Column pitch and row pitch. Generous enough that edges read at 50% zoom. */
const GAP_X = 96;
const GAP_Y = 16;

/** How a node's stage stands, so the chip can carry the barrier's own words. */
type StageState = "closed" | "frontier" | "ahead" | "none";

interface MissionNodeData extends Record<string, unknown> {
  node: MissionNode;
  href: string;
  isRoot: boolean;
  /** This unit is itself in the waiting list — not merely above one. */
  waiting: boolean;
  handRaised: boolean;
  stageState: StageState;
}

/**
 * Tidy left-to-right layout.
 *
 * x is depth, so the column a node sits in IS its distance from the root. y is
 * assigned by walking the tree in the order the server returned it (stage, then
 * position), giving each leaf the next free row and centring each parent on its
 * children. Siblings therefore stay in stage order top to bottom, which is what
 * makes a barrier visible as a horizontal line rather than a fact you look up.
 */
function layout(data: MissionResponse): Map<string, { x: number; y: number }> {
  const childrenOf = new Map<string, MissionNode[]>();
  for (const node of data.nodes) {
    if (!node.parent_id) continue;
    const list = childrenOf.get(node.parent_id) ?? [];
    list.push(node);
    childrenOf.set(node.parent_id, list);
  }

  const position = new Map<string, { x: number; y: number }>();
  let nextRow = 0;

  // Iterative, not recursive: a mission is depth-bounded by the server but the
  // bound is a payload guard, not a promise about the stack.
  const place = (root: MissionNode) => {
    type Frame = { node: MissionNode; visited: boolean };
    const stack: Frame[] = [{ node: root, visited: false }];
    while (stack.length > 0) {
      const frame = stack[stack.length - 1]!;
      const kids = childrenOf.get(frame.node.id) ?? [];

      if (!frame.visited) {
        frame.visited = true;
        // Pushed in reverse so they pop in the server's order.
        for (let i = kids.length - 1; i >= 0; i -= 1) {
          stack.push({ node: kids[i]!, visited: false });
        }
        continue;
      }

      stack.pop();
      const x = frame.node.depth * (NODE_W + GAP_X);
      if (kids.length === 0) {
        position.set(frame.node.id, { x, y: nextRow * (NODE_H + GAP_Y) });
        nextRow += 1;
        continue;
      }
      const first = position.get(kids[0]!.id)!;
      const last = position.get(kids[kids.length - 1]!.id)!;
      position.set(frame.node.id, { x, y: (first.y + last.y) / 2 });
    }
  };

  place(data.root);
  return position;
}

function stageStateOf(node: MissionNode, data: MissionResponse): StageState {
  if (node.stage === null) return "none";
  const stage = data.stages.find((s) => s.stage === node.stage);
  if (!stage) return "none";
  if (stage.closed) return "closed";
  if (stage.frontier) return "frontier";
  return "ahead";
}

function MissionIssueNode({ data }: NodeProps<Node<MissionNodeData>>) {
  const { node, href, isRoot, waiting, handRaised, stageState } = data;
  return (
    <div
      className={cn(
        "flex h-[84px] w-[248px] flex-col gap-1 overflow-hidden rounded-lg border bg-card px-3 py-2 shadow-sm transition-colors",
        isRoot && "border-primary/60 bg-primary/5",
        waiting && "border-amber-500/70",
        node.terminal && "opacity-55",
      )}
    >
      {/* Both handles on every node: React Flow needs a source on the parent
          and a target on the child, and a node is usually both. */}
      <Handle type="target" position={Position.Left} isConnectable={false} className="!opacity-0" />
      <Handle type="source" position={Position.Right} isConnectable={false} className="!opacity-0" />

      <div className="flex items-center gap-1.5">
        <span className="font-mono text-[10px] text-muted-foreground">{node.identifier}</span>
        {node.stage !== null ? (
          <span
            className={cn(
              "rounded-sm px-1 py-px font-mono text-[9px] uppercase",
              stageState === "closed" && "bg-muted text-muted-foreground",
              stageState === "frontier" && "bg-amber-500/15 text-amber-600 dark:text-amber-400",
              stageState === "ahead" && "bg-muted/60 text-muted-foreground/70",
              stageState === "none" && "bg-muted text-muted-foreground",
            )}
          >
            s{node.stage}
          </span>
        ) : null}
        <span className="ml-auto flex items-center gap-1">
          {handRaised ? <Hand className="size-3 text-amber-600 dark:text-amber-400" /> : null}
          {!handRaised && waiting ? <Pause className="size-3 text-amber-600 dark:text-amber-400" /> : null}
        </span>
      </div>

      <a
        href={href}
        className={cn(
          "line-clamp-2 flex-1 text-[12px] leading-snug hover:underline",
          node.terminal && "line-through",
        )}
      >
        {node.title}
      </a>

      <span className="shrink-0 text-[10px] text-muted-foreground">{node.status}</span>
    </div>
  );
}

const nodeTypes = { missionIssue: MissionIssueNode };

function Canvas({ data }: { data: MissionResponse }) {
  const paths = useWorkspacePaths();
  const { resolvedTheme } = useTheme();

  const waitingIds = useMemo(() => {
    const ids = new Set<string>();
    for (const unit of [...data.waiting, ...data.with_lead, ...data.stalled]) {
      ids.add(unit.issue_id);
    }
    return ids;
  }, [data.waiting, data.with_lead, data.stalled]);

  const handIds = useMemo(() => {
    const ids = new Set<string>();
    for (const unit of [...data.waiting, ...data.with_lead]) {
      if (unit.hand) ids.add(unit.issue_id);
    }
    return ids;
  }, [data.waiting, data.with_lead]);

  const { nodes, edges } = useMemo(() => {
    const position = layout(data);
    const flowNodes: Node<MissionNodeData>[] = data.nodes.map((node) => ({
      id: node.id,
      type: "missionIssue",
      position: position.get(node.id) ?? { x: 0, y: 0 },
      // Declared rather than measured. The minimap and fitView both read a
      // node's size from the store, and a custom node has none until after its
      // first paint — which is why the minimap came up empty. These are the
      // same constants the layout used, so nothing can drift.
      width: NODE_W,
      height: NODE_H,
      data: {
        node,
        href: paths.issueDetail(node.id),
        isRoot: node.id === data.root.id,
        waiting: waitingIds.has(node.id),
        handRaised: handIds.has(node.id),
        stageState: stageStateOf(node, data),
      },
      draggable: false,
      connectable: false,
      deletable: false,
    }));

    const flowEdges: Edge[] = data.nodes
      .filter((node) => node.parent_id)
      .map((node) => ({
        id: `${node.parent_id}->${node.id}`,
        source: node.parent_id!,
        target: node.id,
        type: "smoothstep",
        deletable: false,
        focusable: false,
        // A branch with something waiting under it is drawn live; the rest is
        // scenery. This is the one thing the canvas must say at any zoom.
        animated: node.waiting_below,
        style: node.waiting_below
          ? { stroke: "var(--color-amber-500)", strokeWidth: 1.5 }
          : { strokeWidth: 1.25 },
      }));

    return { nodes: flowNodes, edges: flowEdges };
  }, [data, paths, waitingIds, handIds]);

  // The minimap is the only place a node's colour has to survive being three
  // pixels wide, so it says one thing: is anything waiting under here.
  const minimapColor = useCallback(
    (node: Node) => ((node.data as MissionNodeData).node.waiting_below ? "#f59e0b" : "#71717a"),
    [],
  );

  return (
    <ReactFlow
      nodes={nodes}
      edges={edges}
      nodeTypes={nodeTypes}
      colorMode={resolvedTheme === "dark" ? "dark" : "light"}
      fitView
      fitViewOptions={{ padding: 0.15, maxZoom: 1 }}
      minZoom={0.1}
      maxZoom={1.75}
      proOptions={{ hideAttribution: false }}
      // Read only, stated six ways so no future edit makes it writable by
      // accident.
      nodesDraggable={false}
      nodesConnectable={false}
      elementsSelectable={false}
      edgesFocusable={false}
      deleteKeyCode={null}
      onNodesChange={undefined}
      panOnScroll
      selectionOnDrag={false}
      zoomOnDoubleClick={false}
    >
      <Background variant={BackgroundVariant.Dots} gap={18} size={1} />
      <Controls showInteractive={false} position="bottom-left" />
      {/* Sized explicitly. The default minimap is a fraction of the container,
          which on a short pane overflows the canvas rather than shrinking. */}
      <MiniMap
        pannable
        zoomable
        nodeColor={minimapColor}
        nodeStrokeWidth={0}
        nodeBorderRadius={2}
        maskColor="rgb(0 0 0 / 0.35)"
        style={{ width: 150, height: 104 }}
        className="!bg-card !border rounded-md"
      />
    </ReactFlow>
  );
}

export function MissionCanvas({ data }: { data: MissionResponse }) {
  return (
    <ReactFlowProvider>
      <Canvas data={data} />
    </ReactFlowProvider>
  );
}
