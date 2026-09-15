"use client";

// SPIKE (not upstream): the mission view.
//
// Reading order is the design, and it is deliberately not the tree:
//
//   1. what is waiting on a human, longest wait first;
//   2. which referential the open questions are interrogating;
//   3. the stage frontier;
//   4. the tree, collapsed below the first level.
//
// A tree first would make this a prettier board. The list first makes it a
// thing you act on. Everything here is read-only — there is not one mutation
// in this file, and every number comes from the one aggregation request.

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  AlertTriangle,
  ChevronRight,
  CircleDot,
  Hand,
  Layers,
  ShieldQuestion,
} from "lucide-react";
import { api } from "@multica/core/api";
import type {
  MissionNode,
  MissionResponse,
  MissionStage,
  MissionWaitingUnit,
} from "@multica/core/types";
import { useWorkspacePaths } from "@multica/core/paths";
import { cn } from "@multica/ui/lib/utils";

/** 1e-10 USD per tick — server/pkg/agent.CostUSDTicksPerUSD. */
const COST_USD_TICKS_PER_USD = 10_000_000_000;

function formatWaited(seconds: number): string {
  if (seconds < 60) return `${Math.max(seconds, 0)}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h`;
  return `${Math.floor(hours / 24)}d`;
}

function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`;
  return String(n);
}

const REASON_LABEL: Record<MissionWaitingUnit["reason"], string> = {
  hand_raised: "hand raised",
  blocked: "blocked",
  in_review: "in review",
  run_failed: "run failed",
};

export function MissionView({ issueId }: { issueId: string }) {
  const paths = useWorkspacePaths();
  const { data, isLoading, error } = useQuery({
    queryKey: ["mission", issueId],
    queryFn: () => api.getMission(issueId),
    enabled: !!issueId,
  });

  if (isLoading) {
    return <p className="p-6 text-caption text-muted-foreground">Loading mission…</p>;
  }
  if (error || !data) {
    return <p className="p-6 text-caption text-muted-foreground">This issue has no mission to show.</p>;
  }

  return (
    <div className="mx-auto w-full max-w-5xl px-6 py-6">
      <MissionHeader data={data} issueHref={paths.issueDetail(data.root.id)} />
      <WaitingSection data={data} paths={paths} />
      <ReferentialSection data={data} paths={paths} />
      <StageSection data={data} />
      <TreeSection data={data} paths={paths} />
    </div>
  );
}

function MissionHeader({ data, issueHref }: { data: MissionResponse; issueHref: string }) {
  const totals = useMemo(() => {
    let tokens = 0;
    let ticks = 0;
    let metered = 0;
    for (const node of data.nodes) {
      if (!node.usage) continue;
      metered += 1;
      tokens +=
        node.usage.total_input_tokens +
        node.usage.total_output_tokens +
        node.usage.total_cache_read_tokens +
        node.usage.total_cache_write_tokens;
      ticks += node.usage.total_cost_usd_ticks;
    }
    return { tokens, ticks, metered };
  }, [data.nodes]);

  return (
    <header className="mb-6 border-b pb-4">
      <p className="text-caption uppercase tracking-wide text-muted-foreground">Mission</p>
      <h1 className="mt-1 text-xl font-semibold leading-tight">{data.root.title}</h1>
      <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-caption text-muted-foreground">
        {/* The issue page owns the goal, the description, the comments and the
            execution log. This links to it rather than redrawing any of it. */}
        <a href={issueHref} className="font-medium text-foreground hover:underline">
          {data.root.identifier}
        </a>
        <span>{data.nodes.length - 1} sub-issues</span>
        {totals.metered > 0 ? (
          <span>
            {formatTokens(totals.tokens)} tokens · ${(totals.ticks / COST_USD_TICKS_PER_USD).toFixed(2)}
          </span>
        ) : null}
        {data.truncated ? <span>tree shown to depth {data.max_depth}</span> : null}
      </div>
    </header>
  );
}

// ── 1. What is waiting on a human ───────────────────────────────────────────

function WaitingSection({
  data,
  paths,
}: {
  data: MissionResponse;
  paths: ReturnType<typeof useWorkspacePaths>;
}) {
  if (data.waiting.length === 0) {
    return (
      <section className="mb-6 rounded-lg border bg-muted/30 p-4">
        <p className="text-sm font-medium">Nothing is waiting on you.</p>
        <p className="mt-1 text-caption text-muted-foreground">
          No raised hand, nothing blocked, nothing in review, no failed run.
        </p>
      </section>
    );
  }

  return (
    <section className="mb-6">
      <h2 className="mb-2 flex items-center gap-2 text-caption font-semibold uppercase tracking-wide text-muted-foreground">
        <AlertTriangle className="size-3.5" />
        Waiting on you ({data.waiting.length})
      </h2>
      <ul className="divide-y rounded-lg border">
        {data.waiting.map((unit) => (
          <li key={unit.issue_id}>
            <a
              href={paths.issueDetail(unit.issue_id)}
              className="flex items-start gap-3 px-3 py-2.5 transition-colors hover:bg-accent/50"
            >
              {/* Longest wait first, and the duration leads the row because it
                  is what the ordering is by. `~` marks a duration derived from
                  a fallback timestamp rather than the transition itself. */}
              <span
                className="w-12 shrink-0 pt-0.5 text-right font-mono text-caption tabular-nums text-muted-foreground"
                title={
                  unit.since_exact
                    ? `since ${new Date(unit.since).toLocaleString()}`
                    : `approximate — no status-change record; measured from the issue's last update (${new Date(unit.since).toLocaleString()})`
                }
              >
                {unit.since_exact ? "" : "~"}
                {formatWaited(unit.waited_seconds)}
              </span>
              <span className="min-w-0 flex-1">
                <span className="flex flex-wrap items-baseline gap-2">
                  <span className="font-mono text-caption text-muted-foreground">{unit.identifier}</span>
                  <span className="text-sm font-medium">{unit.title}</span>
                  <ReasonChip reason={unit.reason} />
                </span>
                {unit.detail ? (
                  <span className="mt-0.5 block truncate text-caption text-muted-foreground">
                    {unit.detail}
                  </span>
                ) : null}
                {/* The options travel with the hand, so the cost of being wrong
                    on each side is readable from the list without opening the
                    issue. Answering still happens on the issue: this view does
                    not write. */}
                {unit.hand ? (
                  <span className="mt-1 block space-y-0.5">
                    {unit.hand.options.map((option) => (
                      <span key={option.key} className="block text-caption text-muted-foreground">
                        <span className="font-medium text-foreground">{option.label}</span>
                        {unit.hand?.recommendation === option.key ? (
                          <span className="ml-1 text-amber-600 dark:text-amber-400">· recommended</span>
                        ) : null}
                        <span> — if wrong: {option.cost}</span>
                      </span>
                    ))}
                  </span>
                ) : null}
              </span>
            </a>
          </li>
        ))}
      </ul>
    </section>
  );
}

function ReasonChip({ reason }: { reason: MissionWaitingUnit["reason"] }) {
  const tone =
    reason === "hand_raised"
      ? "border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-400"
      : reason === "run_failed"
        ? "border-rose-500/40 bg-rose-500/10 text-rose-700 dark:text-rose-400"
        : "border-border bg-muted text-muted-foreground";
  return (
    <span className={cn("rounded-sm border px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide", tone)}>
      {REASON_LABEL[reason]}
    </span>
  );
}

// ── 2. The referential diagnostic ───────────────────────────────────────────

function ReferentialSection({
  data,
  paths,
}: {
  data: MissionResponse;
  paths: ReturnType<typeof useWorkspacePaths>;
}) {
  if (data.referentials.length === 0) return null;
  const max = Math.max(...data.referentials.map((r) => r.count));

  return (
    <section className="mb-6">
      <h2 className="mb-2 flex items-center gap-2 text-caption font-semibold uppercase tracking-wide text-muted-foreground">
        <ShieldQuestion className="size-3.5" />
        Which referential cannot answer
      </h2>
      {/* Said plainly and never hidden. A diagnostic that looks authoritative
          while grouping by a proxy is worse than no diagnostic at all. */}
      {data.referential_stand_in ? (
        <p className="mb-2 rounded-md border border-dashed bg-muted/30 px-3 py-2 text-caption text-muted-foreground">
          <span className="font-medium text-foreground">Stand-in.</span> A raised hand carries no
          referential yet, so these are grouped by the agent that raised them (
          <code className="font-mono">{data.referential_field}</code>) — a proxy for the body of
          knowledge the question interrogates, not the thing itself.
        </p>
      ) : null}
      <ul className="space-y-1.5">
        {data.referentials.map((group) => (
          <li key={group.key} className="flex items-center gap-3">
            <span className="w-32 shrink-0 truncate text-sm">{group.label}</span>
            <span className="h-4 flex-1 overflow-hidden rounded-sm bg-muted">
              <span
                className="block h-full bg-amber-500/60"
                style={{ width: `${Math.round((group.count / max) * 100)}%` }}
              />
            </span>
            <span className="w-6 shrink-0 text-right font-mono text-caption tabular-nums">
              {group.count}
            </span>
          </li>
        ))}
      </ul>
      <ul className="mt-2 space-y-0.5">
        {data.referentials.flatMap((group) =>
          group.hands.map((hand) => (
            <li key={hand.id} className="truncate text-caption text-muted-foreground">
              <a href={paths.issueDetail(hand.issue_id)} className="hover:underline">
                {group.label}: {hand.question}
              </a>
            </li>
          )),
        )}
      </ul>
    </section>
  );
}

// ── 3. The stage frontier ───────────────────────────────────────────────────

function StageSection({ data }: { data: MissionResponse }) {
  if (data.stages.length === 0) return null;
  const ignored = data.unstaged_ignored.length;

  return (
    <section className="mb-6">
      <h2 className="mb-2 flex items-center gap-2 text-caption font-semibold uppercase tracking-wide text-muted-foreground">
        <Layers className="size-3.5" />
        Stages
      </h2>
      <div className="flex flex-wrap gap-2">
        {data.stages.map((stage) => (
          <StageChip key={stage.stage ?? "implicit"} stage={stage} />
        ))}
      </div>
      {/* Invisible in the product today, and the reason a mission can look
          healthy while work under it gates nothing. */}
      {ignored > 0 ? (
        <p className="mt-2 text-caption text-muted-foreground">
          {ignored} {ignored === 1 ? "child carries" : "children carry"} no stage in a staged set. The
          barrier ignores {ignored === 1 ? "it" : "them"} entirely, so {ignored === 1 ? "it" : "they"}{" "}
          can never close a stage or wake the parent.
        </p>
      ) : null}
    </section>
  );
}

function StageChip({ stage }: { stage: MissionStage }) {
  return (
    <span
      className={cn(
        "rounded-md border px-2.5 py-1.5 text-caption",
        stage.frontier
          ? "border-amber-500/50 bg-amber-500/10"
          : stage.closed
            ? "border-border bg-muted/40 text-muted-foreground"
            : "border-border",
      )}
    >
      <span className="font-medium">
        {stage.stage === null ? "All sub-issues" : `Stage ${stage.stage}`}
      </span>
      <span className="ml-2 font-mono tabular-nums">
        {stage.terminal}/{stage.total}
      </span>
      {stage.frontier ? (
        <span className="ml-2 font-semibold text-amber-700 dark:text-amber-400">frontier</span>
      ) : null}
      {stage.closed ? <span className="ml-2">closed</span> : null}
    </span>
  );
}

// ── 4. The tree ─────────────────────────────────────────────────────────────

function TreeSection({
  data,
  paths,
}: {
  data: MissionResponse;
  paths: ReturnType<typeof useWorkspacePaths>;
}) {
  const childrenOf = useMemo(() => {
    const map = new Map<string, MissionNode[]>();
    for (const node of data.nodes) {
      if (!node.parent_id) continue;
      const list = map.get(node.parent_id) ?? [];
      list.push(node);
      map.set(node.parent_id, list);
    }
    return map;
  }, [data.nodes]);

  // The first level is open; everything below is on demand, which is what keeps
  // a forty-ticket mission readable at a glance.
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set([data.root.id]));
  const toggle = (id: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  return (
    <section>
      <h2 className="mb-2 text-caption font-semibold uppercase tracking-wide text-muted-foreground">
        Tree
      </h2>
      <div className="rounded-lg border">
        <TreeRows
          nodes={childrenOf.get(data.root.id) ?? []}
          childrenOf={childrenOf}
          expanded={expanded}
          toggle={toggle}
          paths={paths}
          depth={0}
        />
      </div>
    </section>
  );
}

function TreeRows({
  nodes,
  childrenOf,
  expanded,
  toggle,
  paths,
  depth,
}: {
  nodes: MissionNode[];
  childrenOf: Map<string, MissionNode[]>;
  expanded: Set<string>;
  toggle: (id: string) => void;
  paths: ReturnType<typeof useWorkspacePaths>;
  depth: number;
}) {
  return (
    <>
      {nodes.map((node) => {
        const kids = childrenOf.get(node.id) ?? [];
        const isOpen = expanded.has(node.id);
        return (
          <div key={node.id}>
            <div
              className="flex items-center gap-2 border-b px-3 py-2 last:border-b-0"
              style={{ paddingLeft: `${12 + depth * 16}px` }}
            >
              {kids.length > 0 ? (
                <button
                  type="button"
                  onClick={() => toggle(node.id)}
                  aria-label={isOpen ? "Collapse" : "Expand"}
                  className="shrink-0 rounded-sm p-0.5 hover:bg-accent"
                >
                  <ChevronRight
                    className={cn("size-3.5 text-muted-foreground transition-transform", isOpen && "rotate-90")}
                  />
                </button>
              ) : (
                <span className="w-[18px] shrink-0" />
              )}

              <CircleDot
                className={cn(
                  "size-3.5 shrink-0",
                  node.terminal ? "text-muted-foreground/50" : "text-muted-foreground",
                )}
              />

              <a
                href={paths.issueDetail(node.id)}
                className="min-w-0 flex-1 truncate hover:underline"
              >
                <span className="font-mono text-caption text-muted-foreground">{node.identifier}</span>
                <span className={cn("ml-2 text-sm", node.terminal && "text-muted-foreground line-through")}>
                  {node.title}
                </span>
              </a>

              {node.stage !== null ? (
                <span className="shrink-0 rounded-sm bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
                  s{node.stage}
                </span>
              ) : null}

              <span className="shrink-0 text-caption text-muted-foreground">{node.status}</span>

              {/* The only thing a collapsed branch must still be able to say. */}
              {node.waiting_below ? (
                <Hand className="size-3.5 shrink-0 text-amber-600 dark:text-amber-400" />
              ) : (
                <span className="w-3.5 shrink-0" />
              )}
            </div>
            {isOpen && kids.length > 0 ? (
              <TreeRows
                nodes={kids}
                childrenOf={childrenOf}
                expanded={expanded}
                toggle={toggle}
                paths={paths}
                depth={depth + 1}
              />
            ) : null}
          </div>
        );
      })}
    </>
  );
}
