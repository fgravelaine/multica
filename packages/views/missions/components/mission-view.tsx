"use client";

// SPIKE (not upstream): the mission view.
//
// Two surfaces, side by side, because the mission asks two questions that do
// not have the same shape:
//
//   WHERE IS IT — the canvas. Pan and zoom, the tree laid out left to right.
//   A shape you read at a glance: how wide the mission got, which branch
//   stopped, how far the frontier is from the root.
//
//   WHAT IS WAITING ON ME — the panel. An ordered list, longest wait first,
//   deliberately not by priority, with the referential diagnostic under it.
//
// The canvas is the surface and the panel is the answer; neither replaces the
// other, and a single scrolling document was the wrong shape for both. The
// panel's sections are unchanged from the first version — only where they sit.
//
// Everything here is read-only. There is not one mutation in this file, and
// every number comes from the one aggregation request.

import { useCallback, useMemo, useState } from "react";
import { useDefaultLayout } from "react-resizable-panels";
import { useQuery } from "@tanstack/react-query";
import {
  AlertTriangle,
  Layers,
  PauseCircle,
  Scale,
  Users,
  ShieldQuestion,
} from "lucide-react";
import { api } from "@multica/core/api";
import type {
  MissionResponse,
  MissionStage,
  MissionWaitingUnit,
} from "@multica/core/types";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  ResizablePanelGroup,
  ResizablePanel,
  ResizableHandle,
} from "@multica/ui/components/ui/resizable";
import { cn } from "@multica/ui/lib/utils";
import { MissionCanvas } from "./mission-canvas";
import { MissionUnitDrawer } from "./mission-unit-drawer";

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
  stage_not_promoted: "never promoted",
};

export function MissionView({ issueId }: { issueId: string }) {
  const paths = useWorkspacePaths();
  const { defaultLayout, onLayoutChanged } = useDefaultLayout({
    id: "multica_mission_layout",
  });
  // Which unit's context is open. Stable callbacks, or every node's data object
  // is new on each render and React Flow re-renders the whole canvas on a pan.
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const select = useCallback((id: string) => setSelectedId(id), []);
  const closeDrawer = useCallback(() => setSelectedId(null), []);
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

  // SidebarInset is `h-svh overflow-hidden`, so the page owns its own height
  // and its own scroll. The canvas needs the height (it measures its container
  // to fit the view); the panel is the only part that scrolls.
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="shrink-0 border-b px-6 py-3">
        <MissionHeader data={data} issueHref={paths.issueDetail(data.root.id)} />
      </div>

      <ResizablePanelGroup
        orientation="horizontal"
        className="min-h-0 flex-1"
        defaultLayout={defaultLayout}
        onLayoutChanged={onLayoutChanged}
      >
        <ResizablePanel id="canvas" minSize="35%">
          <MissionCanvas data={data} selectedId={selectedId} onSelect={select} />
        </ResizablePanel>
        <ResizableHandle />
        <ResizablePanel id="panel" defaultSize="34%" minSize="22%" maxSize="55%">
          <div className="h-full min-h-0 overflow-y-auto overscroll-contain border-l px-5 py-4">
            <WaitingSection data={data} paths={paths} />
            <WithLeadSection data={data} paths={paths} />
            <StalledSection data={data} paths={paths} />
            <ReferentialSection data={data} paths={paths} />
            <LeadContestSection data={data} />
            <StageSection data={data} />
          </div>
        </ResizablePanel>
      </ResizablePanelGroup>

      <MissionUnitDrawer data={data} issueId={selectedId} onClose={closeDrawer} />
    </div>
  );
}

function MissionHeader({ data, issueHref }: { data: MissionResponse; issueHref: string }) {
  const paths = useWorkspacePaths();
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

  const missions = data.nodes.filter((n) => n.level === "mission").length;
  const objectives = data.nodes.filter((n) => n.level === "objective").length;
  const tasks = data.nodes.filter((n) => n.level === "task").length;
  // Steps are deliberately not counted here. They are below the reporting
  // line — the number of them is the unit's business, not the mission's, and
  // putting it in the header would undo the distinction the rung makes.
  const steps = data.nodes.filter((n) => n.level === "step").length;

  return (
    <header className="mb-6 border-b pb-4">
      {/* The product line, NOT a rung. Multica's project is a flat per-issue
          tag that is not inherited, so it groups across the tree rather than
          sitting above it — right for a product, wrong for a ladder. The rung
          above a mission is a campaign, and that is parentage. */}
      <p className="flex items-center gap-1.5 text-caption uppercase tracking-wide text-muted-foreground">
        {data.product ? (
          <>
            {data.product.icon ? <span aria-hidden>{data.product.icon}</span> : null}
            <a
              href={paths.projectDetail(data.product.id)}
              className="hover:text-foreground hover:underline"
            >
              {data.product.title}
            </a>
            <span aria-hidden className="text-muted-foreground/40">/</span>
          </>
        ) : (
          <>
            <span className="text-muted-foreground/60">No product</span>
            <span aria-hidden className="text-muted-foreground/40">/</span>
          </>
        )}
        <span>{data.root.level}</span>
      </p>
      <h1 className="mt-1 text-xl font-semibold leading-tight">{data.root.title}</h1>
      <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-caption text-muted-foreground">
        {/* The issue page owns the goal, the description, the comments and the
            execution log. This links to it rather than redrawing any of it. */}
        <a href={issueHref} className="font-medium text-foreground hover:underline">
          {data.root.identifier}
        </a>
        {/* Counted by rung, not lumped as "sub-issues". Objectives are what
            must be taken; tasks are what units are ordered to do — and knowing
            three objectives carry forty tasks is a different fact from
            "43 sub-issues". */}
        {/* Only the rungs that actually appear below whatever you opened —
            a mission has no missions under it, and printing "0 missions"
            would be noise rather than a fact. */}
        <span>
          {missions > 0 ? (
            <>
              {missions} {missions === 1 ? "mission" : "missions"} ·{" "}
            </>
          ) : null}
          {objectives} {objectives === 1 ? "objective" : "objectives"} · {tasks}{" "}
          {tasks === 1 ? "task" : "tasks"}
          {steps > 0 ? (
            <span className="text-muted-foreground/60"> · {steps} folded</span>
          ) : null}
        </span>
        {totals.metered > 0 ? (
          <span>
            {formatTokens(totals.tokens)} tokens · ${(totals.ticks / COST_USD_TICKS_PER_USD).toFixed(2)}
          </span>
        ) : null}
        {data.truncated ? <span>tree shown to depth {data.max_depth}</span> : null}
        {/* The number the recipient exists to produce. Raw hands raised is not
            it: a team whose hands all get settled by a lead has not stopped
            asking, its referentials and leads can answer. */}
        {data.autonomy.total > 0 ? (
          <span>
            {data.autonomy.total} hands raised ·{" "}
            <span className="font-medium text-foreground">
              {data.autonomy.reached_human} reached you
            </span>
            {data.autonomy.settled_by_lead > 0
              ? ` · ${data.autonomy.settled_by_lead} settled by a lead`
              : null}
          </span>
        ) : null}
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
                  {/* A hand a lead already tried and could not settle says
                      something the raw count does not: the referential was too
                      thin for the lead too. */}
                  {unit.hand?.escalated ? (
                    <span className="rounded-sm border border-border bg-muted px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
                      escalated
                    </span>
                  ) : null}
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

// ── 1b. Stalled at an un-promoted barrier ───────────────────────────────────
//
// Its own section, below the waiting list and visibly quieter, because the two
// are different in kind. A waiting row is something that ASKED. A stalled row
// asked for nothing — the stage below it finished, nobody promoted this one,
// and every unit in it still looks perfectly healthy. It is the only way a
// mission stops without anything appearing wrong, which is exactly why it has
// to be on the screen you open first.

function StalledSection({
  data,
  paths,
}: {
  data: MissionResponse;
  paths: ReturnType<typeof useWorkspacePaths>;
}) {
  if (data.stalled.length === 0) return null;

  return (
    <section className="mb-6">
      <h2 className="mb-2 flex items-center gap-2 text-caption font-semibold uppercase tracking-wide text-muted-foreground">
        <PauseCircle className="size-3.5" />
        Stalled at a barrier ({data.stalled.length})
      </h2>
      <p className="mb-2 text-caption text-muted-foreground">
        The stage below closed and nothing promoted these. Nobody is blocked and nothing failed —
        the mission has simply stopped.
      </p>
      <ul className="divide-y rounded-lg border border-dashed">
        {data.stalled.map((unit) => (
          <li key={unit.issue_id}>
            <a
              href={paths.issueDetail(unit.issue_id)}
              className="flex items-start gap-3 px-3 py-2.5 transition-colors hover:bg-accent/50"
            >
              {/* The clock is the BARRIER'S — how long this stage has been ready
                  to start — not the child's own updated_at, which would report
                  the day it was created. */}
              <span
                className="w-12 shrink-0 pt-0.5 text-right font-mono text-caption tabular-nums text-muted-foreground"
                title={
                  unit.since_exact
                    ? `the stage below closed ${new Date(unit.since).toLocaleString()}`
                    : `approximate — the predecessor stage has no complete transition record (${new Date(unit.since).toLocaleString()})`
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
        : reason === "stage_not_promoted"
          ? "border-sky-500/40 bg-sky-500/10 text-sky-700 dark:text-sky-400"
          : "border-border bg-muted text-muted-foreground";
  return (
    <span className={cn("rounded-sm border px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide", tone)}>
      {REASON_LABEL[reason]}
    </span>
  );
}

// ── 1c. With a lead ─────────────────────────────────────────────────────────
//
// Below the two lists that are on you, because these are not. A hand addressed
// to a squad leader is out of the primary list by construction — that is what
// having a recipient buys, and the section exists so the work is still visible
// without being counted as an interruption you owe an answer to.

function WithLeadSection({
  data,
  paths,
}: {
  data: MissionResponse;
  paths: ReturnType<typeof useWorkspacePaths>;
}) {
  if (data.with_lead.length === 0) return null;

  return (
    <section className="mb-6">
      <h2 className="mb-2 flex items-center gap-2 text-caption font-semibold uppercase tracking-wide text-muted-foreground">
        <Users className="size-3.5" />
        With a lead ({data.with_lead.length})
      </h2>
      <p className="mb-2 text-caption text-muted-foreground">
        Raised, but not on you. The lead answers it, or escalates what it cannot settle.
      </p>
      <ul className="divide-y rounded-lg border border-dashed">
        {data.with_lead.map((unit) => (
          <li key={unit.issue_id}>
            <a
              href={paths.issueDetail(unit.issue_id)}
              className="flex items-start gap-3 px-3 py-2.5 transition-colors hover:bg-accent/50"
            >
              <span className="w-12 shrink-0 pt-0.5 text-right font-mono text-caption tabular-nums text-muted-foreground">
                {unit.since_exact ? "" : "~"}
                {formatWaited(unit.waited_seconds)}
              </span>
              <span className="min-w-0 flex-1">
                <span className="flex flex-wrap items-baseline gap-2">
                  <span className="font-mono text-caption text-muted-foreground">{unit.identifier}</span>
                  <span className="text-sm font-medium">{unit.title}</span>
                  {unit.hand?.lead_name ? (
                    <span className="rounded-sm border border-border bg-muted px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
                      {unit.hand.lead_name}
                    </span>
                  ) : null}
                </span>
                {unit.detail ? (
                  <span className="mt-0.5 block truncate text-caption text-muted-foreground">
                    {unit.detail}
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
      {/* The banner stays in the code, not because it renders today but because
          the honesty rule it enforces outlives this particular field: a
          grouping by proxy must say so, and a server that ever falls back to
          one will set the flag again. */}
      {data.referential_stand_in ? (
        <p className="mb-2 rounded-md border border-dashed bg-muted/30 px-3 py-2 text-caption text-muted-foreground">
          <span className="font-medium text-foreground">Stand-in.</span> These are grouped by{" "}
          <code className="font-mono">{data.referential_field}</code> — a proxy for the body of
          knowledge the question interrogates, not the thing itself.
        </p>
      ) : (
        <p className="mb-2 text-caption text-muted-foreground">
          Each raised hand names the body of knowledge that failed to answer it. A tall bar is a
          referential too thin to answer on its own.
        </p>
      )}
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

// ── 2b. Who contests, who relays ────────────────────────────────────────────
//
// A lead's job before escalating is to check whether the answer already exists
// in a referential it can reach. Nothing enforces that and nothing could — a
// lead that relays a question unchanged looks identical to one that checked and
// found nothing. What IS measurable is the outcome.
//
// Settled counts only hands the lead answered itself. Escalated counts the ones
// it passed up. A lead at 0/8 is relaying; a lead at 7/8 is answering from its
// referentials. Open hands are excluded from the ratio and shown separately,
// because a hand nobody has touched yet is not evidence either way.

function LeadContestSection({ data }: { data: MissionResponse }) {
  if (data.leads.length === 0) return null;

  return (
    <section className="mb-6">
      <h2 className="mb-2 flex items-center gap-2 text-caption font-semibold uppercase tracking-wide text-muted-foreground">
        <Scale className="size-3.5" />
        Who contests, who relays
      </h2>
      <p className="mb-2 text-caption text-muted-foreground">
        Hands a lead settled itself, against the ones it passed up. A low share means the
        referentials are not answering the lead either — or the lead is not asking them.
      </p>
      <ul className="divide-y rounded-lg border">
        {data.leads.map((lead) => {
          const decided = lead.settled + lead.escalated;
          const share = decided > 0 ? lead.settled / decided : 0;
          return (
            <li key={lead.lead_id} className="flex items-center gap-3 px-3 py-2.5">
              <span className="w-32 shrink-0 truncate text-sm font-medium">
                {lead.lead_name || lead.lead_id}
              </span>
              <span className="h-4 flex-1 overflow-hidden rounded-sm bg-muted" title="share settled by the lead">
                <span
                  className="block h-full bg-emerald-500/60"
                  style={{ width: `${Math.round(share * 100)}%` }}
                />
              </span>
              <span className="w-32 shrink-0 text-right font-mono text-caption tabular-nums text-muted-foreground">
                {decided > 0 ? (
                  <>
                    {lead.settled}/{decided} settled
                  </>
                ) : (
                  <>nothing decided</>
                )}
                {lead.still_open > 0 ? ` · ${lead.still_open} open` : null}
              </span>
            </li>
          );
        })}
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

