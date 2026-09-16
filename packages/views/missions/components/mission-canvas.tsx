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

import { useCallback, useMemo, useState } from "react";
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
import { CircleSlash, Eye, Hand, TriangleAlert, LogIn, Lock } from "lucide-react";
import type {
  MissionNode,
  MissionResponse,
  MissionWaitingReason,
} from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
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
const NODE_H = 104;
/** Column pitch and row pitch. Generous enough that edges read at 50% zoom. */
const GAP_X = 96;
const GAP_Y = 16;

/**
 * The ladder, in order, one name per column.
 *
 * Which name a column gets depends on where the tree you opened sits in the
 * workspace, so the server sends each node its own rung and these are only the
 * headers for depth 0..4 FROM A CAMPAIGN. Open the view lower down and the
 * server's levels still say the truth on each node.
 */
const LEVEL_COLUMNS = ["Campaign", "Missions", "Objectives", "Tasks", "Steps"] as const;

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
  /** Tasks folded inside this one. 0 when there are none, or it is open. */
  foldedCount: number;
  onUnfold: (issueId: string) => void;
  /** Workspace label for the status key, e.g. "In progress" not "in_progress". */
  statusLabel: string;
  /** Terminal and total below this unit. Both 0 when it has no children. */
  done: number;
  units: number;
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
function layout(
  data: MissionResponse,
  visible: MissionNode[],
): Map<string, { x: number; y: number }> {
  const childrenOf = new Map<string, MissionNode[]>();
  for (const node of visible) {
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
  const {
    node,
    onSelect,
    selected,
    isRoot,
    waitingReason,
    stageState,
    foldedCount,
    onUnfold,
    statusLabel,
    done,
    units,
  } = data;
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
        "flex h-[104px] w-[248px] cursor-pointer flex-col gap-1 overflow-hidden rounded-lg border bg-card px-3 py-2 text-left shadow-sm transition-colors hover:border-foreground/30",
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

      {/* Completion, on the card rather than only in the drawer. A leaf gets
          nothing: 0/0 is not 0% done, it is a unit with nothing underneath,
          and a full-width empty bar would read as "none of it is finished". */}
      {units > 0 ? (
        <span className="flex shrink-0 items-center gap-1.5">
          <span className="h-1 flex-1 overflow-hidden rounded-full bg-muted">
            <span
              className={cn(
                "block h-full rounded-full",
                done === units ? "bg-emerald-500" : "bg-primary",
              )}
              style={{ width: `${Math.round((done / units) * 100)}%` }}
            />
          </span>
          <span className="shrink-0 font-mono text-[9px] tabular-nums text-muted-foreground">
            {done}/{units}
          </span>
        </span>
      ) : null}

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
        <span className="truncate text-[10px] text-muted-foreground">{statusLabel}</span>

        {/* What is standing in the way, on the card. It was drawn as an edge
            and only as an edge, which means you had to trace a line to learn
            the one thing that decides whether this can start at all. */}
        {node.blocked_by?.length ? (
          <span
            title={`Waiting on ${node.blocked_by.map((b) => b.identifier).join(", ")}`}
            className="flex shrink-0 items-center gap-0.5 rounded-sm border border-rose-500/40 px-1 text-[9px] text-rose-600 dark:text-rose-400"
          >
            <Lock className="size-2.5" />
            {node.blocked_by.length}
          </span>
        ) : null}
        {foldedCount > 0 ? (
          // A nested span rather than a nested button: a button inside a button
          // is invalid markup and React will say so. The stopPropagation is
          // what keeps unfolding from also opening the drawer.
          <span
            role="button"
            tabIndex={0}
            onClick={(event) => {
              event.stopPropagation();
              onUnfold(node.id);
            }}
            onKeyDown={(event) => {
              if (event.key !== "Enter" && event.key !== " ") return;
              event.stopPropagation();
              event.preventDefault();
              onUnfold(node.id);
            }}
            title={`${foldedCount} tasks inside this one`}
            className="ml-auto shrink-0 cursor-pointer rounded-sm border px-1 text-[9px] text-muted-foreground hover:border-foreground/40 hover:text-foreground"
          >
            +{foldedCount}
          </span>
        ) : null}
      </div>
    </button>
  );
}

function LevelHeaderNode({ data }: NodeProps<Node<{ label: string }>>) {
  return (
    <div
      style={{ width: NODE_W }}
      className="select-none pl-1 text-[11px] font-semibold uppercase tracking-widest text-muted-foreground/50"
    >
      {data.label}
    </div>
  );
}

const nodeTypes = { missionIssue: MissionIssueNode, levelHeader: LevelHeaderNode };

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
  // One cached catalog for the canvas, never one lookup per node's render.
  const { labelOf } = useIssueStatuses(useWorkspaceId());
  // Which tasks have been opened to show the tasks inside them.
  //
  // This is the whole of "subtask". A task that breaks down is still a task —
  // nothing in the product changes below depth 2 — so the difference is not a
  // rank, it is what the mission tracks. "Write it" is what a unit is ordered
  // to do and reports on; its steps are the unit's own business, and the
  // canvas keeps them folded until you ask.
  const [unfolded, setUnfolded] = useState<Set<string>>(() => new Set());
  const unfold = useCallback((id: string) => {
    setUnfolded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

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

  // Terminal and total below every unit, from the tree that is already here.
  // No request: the whole subtree is in the payload, so a rollup is a walk over
  // memory rather than a count the server has to be asked for.
  const progress = useMemo(() => {
    const childrenOf = new Map<string, MissionNode[]>();
    for (const node of data.nodes) {
      if (!node.parent_id) continue;
      const list = childrenOf.get(node.parent_id) ?? [];
      list.push(node);
      childrenOf.set(node.parent_id, list);
    }
    const out = new Map<string, { done: number; units: number }>();
    // Deepest first, so a parent's totals are ready when it is reached. The
    // server returns breadth-first, so reversing is enough.
    for (let i = data.nodes.length - 1; i >= 0; i -= 1) {
      const node = data.nodes[i]!;
      let done = 0;
      let units = 0;
      for (const child of childrenOf.get(node.id) ?? []) {
        const below = out.get(child.id) ?? { done: 0, units: 0 };
        units += 1 + below.units;
        done += (child.terminal ? 1 : 0) + below.done;
      }
      out.set(node.id, { done, units });
    }
    return out;
  }, [data.nodes]);

  const { nodes, edges } = useMemo(() => {
    // Everything except the inside of a folded task. Computed parent-first,
    // which the server's breadth-first ordering guarantees, so folding a task
    // hides everything beneath it and not just its first row.
    const hidden = new Set<string>();
    const folded = new Map<string, number>();
    for (const node of data.nodes) {
      if (!node.parent_id) continue;
      if (hidden.has(node.parent_id)) {
        hidden.add(node.id);
        continue;
      }
      const parent = data.nodes.find((n) => n.id === node.parent_id);
      // A task's children are steps, and a step is folded by definition —
      // that fold is the whole content of the task/step boundary.
      if (parent?.level === "task" && !unfolded.has(parent.id)) {
        hidden.add(node.id);
        folded.set(parent.id, (folded.get(parent.id) ?? 0) + 1);
      }
    }
    const visible = data.nodes.filter((n) => !hidden.has(n.id));

    const position = layout(data, visible);
    const flowNodes: Node<MissionNodeData>[] = visible.map((node) => ({
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
        foldedCount: folded.get(node.id) ?? 0,
        onUnfold: unfold,
        statusLabel: labelOf(node.status),
        done: progress.get(node.id)?.done ?? 0,
        units: progress.get(node.id)?.units ?? 0,
      },
      draggable: false,
      connectable: false,
      deletable: false,
    }));

    const flowEdges: Edge[] = visible
      .filter((node) => node.parent_id && !hidden.has(node.parent_id))
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

    // The ladder, written above the columns the layout already produces. Depth
    // is the x axis, so a column IS a rung. Named up to the deepest column that
    // exists — a mission with no tasks does not advertise an empty Tasks
    // column — and the last name covers every column beyond it, because depth
    // 3 and 4 are tasks too.
    const maxDepth = visible.reduce((m, n) => Math.max(m, n.depth), 0);
    let headerY = Infinity;
    for (const p of position.values()) headerY = Math.min(headerY, p.y);
    if (!Number.isFinite(headerY)) headerY = 0;
    for (let depth = 0; depth <= maxDepth; depth += 1) {
      const label = LEVEL_COLUMNS[Math.min(depth, LEVEL_COLUMNS.length - 1)]!;
      // One header per column: past the last name, the column is more tasks
      // and repeating the word would be noise.
      if (depth >= LEVEL_COLUMNS.length) break;
      flowNodes.push({
        id: `level:${depth}`,
        type: "levelHeader",
        position: { x: depth * (NODE_W + GAP_X), y: headerY - 34 },
        data: { label } as never,
        draggable: false,
        selectable: false,
        connectable: false,
        deletable: false,
        width: NODE_W,
        height: 18,
      } as never);
    }

    // The barrier, drawn. A parent→child edge says "this is part of that"; a
    // blocker edge says "this cannot start until that finishes", and they are
    // different claims that must not look alike — so these are rose, dashed,
    // and arrive at the top of the blocked unit rather than its left side.
    //
    // Bounded by the server: only the frontier stage blocks, so this is
    // (open units in the frontier) × (units above it), never the transitive
    // closure of the whole tree.
    const known = new Set(visible.map((n) => n.id));
    for (const node of visible) {
      for (const blocker of node.blocked_by ?? []) {
        // An outside blocker has no node to draw to. The card's count carries
        // it, and the drawer names it — drawing an edge to nowhere would be
        // worse than not drawing one.
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
          label: blocker.relation === "dependency" ? "waits on" : "blocks",
          labelStyle: { fontSize: 9, fill: "var(--color-rose-500)" },
          labelBgStyle: { fill: "var(--color-background)" },
          style: { stroke: "var(--color-rose-500)", strokeWidth: 1.25, strokeDasharray: "3 3" },
        });
      }
    }

    return { nodes: flowNodes, edges: flowEdges };
  }, [data, selectedId, onSelect, reasonByIssue, unfolded, unfold, labelOf, progress]);

  // The minimap is the only place a node's colour has to survive being three
  // pixels wide, so it says one thing: is anything waiting under here.
  const minimapColor = useCallback((node: Node) => {
    // Not every node is a unit: the column headers are nodes too, so that they
    // pan and zoom with the columns they label. They have no unit to colour.
    const unit = (node.data as Partial<MissionNodeData>).node;
    if (!unit) return "transparent";
    return unit.waiting_below ? "#f59e0b" : "#71717a";
  }, []);

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
