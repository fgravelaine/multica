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
import { CircleSlash, Eye, Hand, TriangleAlert, LogIn } from "lucide-react";
import type {
  MissionNode,
  MissionResponse,
  MissionWaitingReason,
} from "@multica/core/types";
import { useTheme } from "@multica/ui/components/common/theme-provider";
import { cn } from "@multica/ui/lib/utils";
import { ActorAvatar } from "../../common/actor-avatar";

const NODE_W = 248;
/**
 * Fixed, and the node's own box is fixed to match: React Flow needs the height
 * before it renders to lay anything out, so a node that sizes itself to its
 * title would push its own text out of its box. Two lines of title is the
 * budget; longer titles clamp.
 */
const NODE_H = 92;
/** Column pitch and row pitch. Generous enough that edges read at 50% zoom. */
const GAP_X = 96;
const GAP_Y = 16;

/** How a node's stage stands, so the chip can carry the barrier's own words. */
type StageState = "closed" | "frontier" | "ahead" | "none";

interface MissionNodeData extends Record<string, unknown> {
  node: MissionNode;
  /** Opens this unit's context. Stable, or every node re-renders on any pan. */
  onSelect: (issueId: string) => void;
  selected: boolean;
  isRoot: boolean;
  /**
   * Why this unit is parked, or null when it is not. Not a boolean: "waiting"
   * is five different facts — a question nobody answered, a dependency, a
   * review queue, a crashed run, a barrier nobody promoted — and they call for
   * five different things from the reader. A single pause icon made them look
   * like one fact.
   */
  waitingReason: MissionWaitingReason | null;
  stageState: StageState;
}

/**
 * One icon per reason, and the tone matches the panel's chip for the same
 * reason so the canvas and the list cannot disagree about what a colour means.
 */
const REASON_ICON: Record<
  MissionWaitingReason,
  { Icon: typeof Hand; tone: string; label: string }
> = {
  hand_raised: {
    Icon: Hand,
    tone: "text-amber-600 dark:text-amber-400",
    label: "hand raised — a question nobody has answered",
  },
  blocked: {
    Icon: CircleSlash,
    tone: "text-rose-600 dark:text-rose-400",
    label: "blocked",
  },
  in_review: {
    Icon: Eye,
    tone: "text-muted-foreground",
    label: "in review — waiting on a reviewer",
  },
  run_failed: {
    Icon: TriangleAlert,
    tone: "text-rose-600 dark:text-rose-400",
    label: "the last run failed",
  },
  stage_not_promoted: {
    Icon: LogIn,
    tone: "text-sky-600 dark:text-sky-400",
    label: "the stage below closed and nobody promoted this one",
  },
};

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
  const { node, onSelect, selected, isRoot, waitingReason, stageState } = data;
  const reason = waitingReason ? REASON_ICON[waitingReason] : null;
  return (
    // A button, not a link. Clicking a unit on the canvas opens its context in
    // the drawer; leaving the canvas for the issue is a deliberate second act,
    // from a link inside that drawer. A canvas you fall out of by misclicking
    // is not one you explore.
    <button
      type="button"
      onClick={() => onSelect(node.id)}
      className={cn(
        "flex h-[92px] w-[248px] cursor-pointer flex-col gap-1 overflow-hidden rounded-lg border bg-card px-3 py-2 text-left shadow-sm transition-colors hover:border-foreground/30",
        isRoot && "border-primary/60 bg-primary/5",
        waitingReason && "border-amber-500/70",
        selected && "ring-2 ring-primary ring-offset-1 ring-offset-background",
        node.terminal && "opacity-55",
      )}
    >
      {/* Both handles on every node: React Flow needs a source on the parent
          and a target on the child, and a node is usually both. */}
      <Handle type="target" position={Position.Left} isConnectable={false} className="!opacity-0" />
      <Handle type="source" position={Position.Right} isConnectable={false} className="!opacity-0" />
      {/* Separate anchors for the barrier, top and bottom: siblings sit in one
          column, so a blocker edge drawn left-to-right would run backwards
          through the nodes between them. */}
      <Handle
        id="blocks-source"
        type="source"
        position={Position.Bottom}
        isConnectable={false}
        className="!opacity-0"
      />
      <Handle
        id="blocks-target"
        type="target"
        position={Position.Top}
        isConnectable={false}
        className="!opacity-0"
      />

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
          {reason ? (
            <reason.Icon className={cn("size-3.5", reason.tone)} aria-label={reason.label} />
          ) : null}
        </span>
      </div>

      <span
        className={cn(
          "line-clamp-2 flex-1 text-[12px] leading-snug",
          node.terminal && "line-through",
        )}
      >
        {node.title}
      </span>

      <div className="flex shrink-0 items-center gap-1.5">
        {/* Who is on it. The canvas without this says where the work is and
            not who has it, which is half the question on a team whose units
            are agents. Resolved from the workspace catalogs the client already
            holds — three cached list queries, never one per node. */}
        {node.assignee_type && node.assignee_id ? (
          <ActorAvatar
            actorType={node.assignee_type}
            actorId={node.assignee_id}
            size="xs"
            showStatusDot
          />
        ) : (
          <span className="size-4 rounded-full border border-dashed border-muted-foreground/40" />
        )}
        <span className="truncate text-[10px] text-muted-foreground">{node.status}</span>
      </div>
    </button>
  );
}

const nodeTypes = { missionIssue: MissionIssueNode };

function Canvas({
  data,
  selectedId,
  onSelect,
}: {
  data: MissionResponse;
  selectedId: string | null;
  onSelect: (issueId: string) => void;
}) {
  const { resolvedTheme } = useTheme();

  // One map, not two sets. The reason comes from the unit the server already
  // classified — the canvas does not re-derive it, so it cannot drift from what
  // the panel says about the same unit.
  const reasonByIssue = useMemo(() => {
    const map = new Map<string, MissionWaitingReason>();
    for (const unit of [...data.waiting, ...data.with_lead, ...data.stalled]) {
      map.set(unit.issue_id, unit.reason);
    }
    return map;
  }, [data.waiting, data.with_lead, data.stalled]);

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
        onSelect,
        selected: node.id === selectedId,
        isRoot: node.id === data.root.id,
        waitingReason: reasonByIssue.get(node.id) ?? null,
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

    // The barrier, drawn. A parent→child edge says "this is part of that"; a
    // blocker edge says "this cannot start until that finishes", and they are
    // different claims that must not look alike — so these are rose, dashed,
    // and arrive at the top of the blocked unit rather than its left side.
    //
    // Bounded by the server: only the frontier stage blocks, so this is
    // (open units in the frontier) × (units above it), never the transitive
    // closure of the whole tree.
    const known = new Set(data.nodes.map((n) => n.id));
    for (const node of data.nodes) {
      for (const blocker of node.blocked_by ?? []) {
        if (!known.has(blocker.issue_id)) continue;
        flowEdges.push({
          id: `blocks:${blocker.issue_id}->${node.id}`,
          source: blocker.issue_id,
          target: node.id,
          sourceHandle: "blocks-source",
          targetHandle: "blocks-target",
          type: "smoothstep",
          deletable: false,
          focusable: false,
          label: "blocks",
          labelStyle: { fontSize: 9, fill: "var(--color-rose-500)" },
          labelBgStyle: { fill: "var(--color-background)" },
          style: { stroke: "var(--color-rose-500)", strokeWidth: 1.25, strokeDasharray: "3 3" },
        });
      }
    }

    return { nodes: flowNodes, edges: flowEdges };
  }, [data, selectedId, onSelect, reasonByIssue]);

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
      // Read only: nothing here changes a row. What is off is everything that
      // mutates the graph — drag, connect, delete.
      nodesDraggable={false}
      nodesConnectable={false}
      deleteKeyCode={null}
      // Selectable stays ON, and not as a concession: React Flow gives a node
      // `pointer-events: none` unless it is selectable, draggable, or has a
      // flow-level click handler, so with this off the node's own button never
      // receives a click and the context drawer cannot open. Selection is view
      // state — which unit you are looking at — not a write.
      elementsSelectable
      selectNodesOnDrag={false}
      edgesFocusable={false}
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

export function MissionCanvas({
  data,
  selectedId,
  onSelect,
}: {
  data: MissionResponse;
  selectedId: string | null;
  onSelect: (issueId: string) => void;
}) {
  return (
    <ReactFlowProvider>
      <Canvas data={data} selectedId={selectedId} onSelect={onSelect} />
    </ReactFlowProvider>
  );
}
