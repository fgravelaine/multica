"use client";

// SPIKE (not upstream): one unit's context, without leaving the canvas.
//
// Everything here comes out of the mission payload that is already in memory.
// There is NO fetch in this file and there must never be one: a drawer that
// loads when you open it is a request per node, which is the single rule this
// view was given and the reason the endpoint aggregates at all. What the drawer
// needed that the payload did not carry — the latest run — was added to the
// aggregation instead, where it costs nothing because the query was already
// being made.
//
// Read only, like the rest. The one thing that leaves is a link to the issue.

import { useMemo } from "react";
import { ArrowUpRight, Ban, Hand, Layers, Play, Wallet } from "lucide-react";
import type {
  MissionHand,
  MissionLevel,
  MissionNode,
  MissionResponse,
  MissionWaitingUnit,
} from "@multica/core/types";
import { useWorkspacePaths } from "@multica/core/paths";
import { useActorName } from "@multica/core/workspace/hooks";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@multica/ui/components/ui/sheet";
import { cn } from "@multica/ui/lib/utils";
import { ActorAvatar } from "../../common/actor-avatar";

/**
 * What each rung obliges, in the mission-command sense the names come from.
 *
 * There is no `subtask` line because there is no subtask rung: a task inside a
 * task is still a task, and nothing in the product changes below depth 2.
 */
const LEVEL_MEANS: Record<MissionLevel, string> = {
  campaign: "a body of work with many missions in it.",
  mission: "carries the intent and the end state. Only a human writes one.",
  objective: "must be taken and held for the mission to succeed. You can tell whether you hold it.",
  task: "what one unit is ordered to do, and reports on.",
  step: "how a task gets done. Below the reporting line — folded until asked for.",
};

/** 1e-10 USD per tick — server/pkg/agent.CostUSDTicksPerUSD. */
const COST_USD_TICKS_PER_USD = 10_000_000_000;

function ago(iso: string | undefined): string {
  if (!iso) return "—";
  const seconds = Math.max(Math.floor((Date.now() - new Date(iso).getTime()) / 1000), 0);
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-start gap-3 py-1.5">
      <span className="w-24 shrink-0 text-caption text-muted-foreground">{label}</span>
      <span className="min-w-0 flex-1 text-caption">{children}</span>
    </div>
  );
}

function Section({
  icon,
  title,
  children,
}: {
  icon: React.ReactNode;
  title: string;
  children: React.ReactNode;
}) {
  return (
    <section className="mt-5 first:mt-0">
      <h3 className="mb-1.5 flex items-center gap-1.5 text-caption font-semibold uppercase tracking-wide text-muted-foreground">
        {icon}
        {title}
      </h3>
      {children}
    </section>
  );
}

export function MissionUnitDrawer({
  data,
  issueId,
  onClose,
}: {
  data: MissionResponse;
  issueId: string | null;
  onClose: () => void;
}) {
  const paths = useWorkspacePaths();
  const { getActorName } = useActorName();

  const node: MissionNode | undefined = useMemo(
    () => (issueId ? data.nodes.find((n) => n.id === issueId) : undefined),
    [data.nodes, issueId],
  );

  // The waiting entry is where the reason and the duration live; a unit that is
  // not parked simply has none, and the drawer says so rather than inventing a
  // reason for a unit that is fine.
  const parked: MissionWaitingUnit | undefined = useMemo(() => {
    if (!issueId) return undefined;
    return [...data.waiting, ...data.with_lead, ...data.stalled].find(
      (u) => u.issue_id === issueId,
    );
  }, [data.waiting, data.with_lead, data.stalled, issueId]);

  const hand: MissionHand | undefined = parked?.hand;

  const children = useMemo(
    () => (issueId ? data.nodes.filter((n) => n.parent_id === issueId) : []),
    [data.nodes, issueId],
  );

  const open = Boolean(issueId && node);

  return (
    <Sheet open={open} onOpenChange={(next) => !next && onClose()}>
      <SheetContent side="right" className="w-[440px] overflow-y-auto sm:max-w-[440px]">
        {node ? (
          <>
            <SheetHeader className="space-y-1">
              <span className="font-mono text-caption text-muted-foreground">
                {node.identifier}
              </span>
              <SheetTitle className="text-left text-base leading-snug">{node.title}</SheetTitle>
              {/* What it IS, and what that obliges. The rung is not decoration:
                  it says who may decide here, which is the whole reason the
                  vocabulary exists. */}
              <p className="text-caption text-muted-foreground">
                <span className="font-medium uppercase tracking-wide text-foreground">
                  {node.level}
                </span>
                {" — "}
                {LEVEL_MEANS[node.level]}
              </p>
              <SheetDescription className="sr-only">
                Read-only context for this unit of the mission.
              </SheetDescription>
              <a
                href={paths.issueDetail(node.id)}
                className="inline-flex w-fit items-center gap-1 text-caption text-primary hover:underline"
              >
                Open issue
                <ArrowUpRight className="size-3" />
              </a>
            </SheetHeader>

            <div className="px-4 pb-6">
              <Section icon={<Layers className="size-3.5" />} title="Where it is">
                <Row label="Status">
                  {node.status}
                  <span className="ml-2 text-muted-foreground">
                    {node.status_since_exact ? "since" : "last touched"}{" "}
                    {ago(node.status_since)}
                  </span>
                </Row>
                <Row label="Stage">
                  {node.stage === null ? (
                    <span className="text-muted-foreground">
                      none — the barrier ignores this unit
                    </span>
                  ) : (
                    `Stage ${node.stage}`
                  )}
                </Row>
                <Row label="Depth">
                  {node.depth === 0 ? "the mission root" : `${node.depth} below the root`}
                </Row>
                <Row label={node.level === "task" ? "Steps" : "Children"}>
                  {children.length === 0 ? (
                    <span className="text-muted-foreground">none</span>
                  ) : (
                    <>
                      {children.filter((c) => c.terminal).length}/{children.length} done
                      {/* The honest answer to "is this a subtask?". A task that
                          breaks down is still a task; the canvas keeps its
                          pieces folded because they are the unit's own
                          business, not the mission's. */}
                      {node.level === "task" ? (
                        <span className="ml-2 text-muted-foreground">
                          folded on the canvas
                        </span>
                      ) : null}
                    </>
                  )}
                </Row>
              </Section>

              {node.blocked_by?.length ? (
                <Section icon={<Ban className="size-3.5" />} title="Waiting on">
                  <p className="mb-1.5 text-[11px] text-muted-foreground">
                    This unit is in stage {node.stage}. It does not start until the
                    stage below closes — these are the units that have to finish.
                  </p>
                  <div className="space-y-1">
                    {node.blocked_by.map((blocker) => (
                      <a
                        key={blocker.issue_id}
                        href={paths.issueDetail(blocker.issue_id)}
                        className="flex items-center gap-2 rounded-md border px-2.5 py-1.5 hover:border-foreground/30"
                      >
                        <span className="font-mono text-[10px] text-muted-foreground">
                          {blocker.identifier}
                        </span>
                        <span className="min-w-0 flex-1 truncate text-caption">
                          {blocker.title}
                        </span>
                        <span className="shrink-0 text-[10px] text-muted-foreground">
                          {blocker.status}
                        </span>
                      </a>
                    ))}
                  </div>
                </Section>
              ) : null}

              <Section icon={<Hand className="size-3.5" />} title="Who has it">
                {node.assignee_type && node.assignee_id ? (
                  <div className="flex items-center gap-2 py-1.5">
                    <ActorAvatar
                      actorType={node.assignee_type}
                      actorId={node.assignee_id}
                      size="sm"
                      showStatusDot
                      enableHoverCard
                    />
                    <span className="text-caption">
                      {getActorName(node.assignee_type, node.assignee_id)}
                    </span>
                    <span className="text-caption text-muted-foreground">
                      {node.assignee_type}
                    </span>
                  </div>
                ) : (
                  <p className="py-1.5 text-caption text-muted-foreground">
                    Unassigned. Nothing will pick this up on its own.
                  </p>
                )}

                {parked ? (
                  <Row label="Waiting">
                    <span className="text-amber-600 dark:text-amber-400">
                      {parked.reason.replace(/_/g, " ")}
                    </span>
                    <span className="ml-2 text-muted-foreground">
                      {parked.since_exact ? "" : "about "}
                      {ago(parked.since)}
                    </span>
                    {parked.detail ? (
                      <span className="mt-1 block font-mono text-[11px] text-muted-foreground">
                        {parked.detail}
                      </span>
                    ) : null}
                  </Row>
                ) : null}
              </Section>

              {hand ? (
                <Section icon={<Hand className="size-3.5" />} title="The open question">
                  <p className="text-caption">{hand.question}</p>
                  <div className="mt-2 space-y-1.5">
                    {hand.options.map((option) => (
                      <div
                        key={option.key}
                        className={cn(
                          "rounded-md border px-2.5 py-1.5",
                          hand.recommendation === option.key && "border-primary/50 bg-primary/5",
                        )}
                      >
                        <span className="text-caption font-medium">{option.label}</span>
                        {hand.recommendation === option.key ? (
                          <span className="ml-1.5 text-[10px] uppercase text-primary">
                            recommended
                          </span>
                        ) : null}
                        <span className="mt-0.5 block text-[11px] text-muted-foreground">
                          if wrong: {option.cost}
                        </span>
                      </div>
                    ))}
                  </div>
                  <Row label="Referential">
                    {hand.referential ?? <span className="text-muted-foreground">unrecorded</span>}
                  </Row>
                  <Row label="With">
                    {hand.recipient_type === "lead"
                      ? (hand.lead_name ?? "a lead")
                      : "you"}
                    {hand.escalated ? (
                      <span className="ml-2 text-muted-foreground">
                        escalated — a lead could not settle it
                      </span>
                    ) : null}
                  </Row>
                  {hand.agent_name ? <Row label="Raised by">{hand.agent_name}</Row> : null}
                </Section>
              ) : null}

              <Section icon={<Play className="size-3.5" />} title="Last run">
                {node.last_run ? (
                  <>
                    <Row label="Outcome">
                      {node.last_run.status}
                      {node.last_run.failure_reason ? (
                        <span className="mt-1 block font-mono text-[11px] text-destructive">
                          {node.last_run.failure_reason}
                        </span>
                      ) : null}
                      {node.last_run.wait_reason ? (
                        <span className="mt-1 block font-mono text-[11px] text-muted-foreground">
                          waiting: {node.last_run.wait_reason}
                        </span>
                      ) : null}
                    </Row>
                    <Row label="Queued">{ago(node.last_run.created_at)}</Row>
                    {node.last_run.started_at ? (
                      <Row label="Started">{ago(node.last_run.started_at)}</Row>
                    ) : null}
                    {node.last_run.completed_at ? (
                      <Row label="Ended">{ago(node.last_run.completed_at)}</Row>
                    ) : null}
                  </>
                ) : (
                  <p className="py-1.5 text-caption text-muted-foreground">
                    Never run.
                  </p>
                )}
              </Section>

              <Section icon={<Wallet className="size-3.5" />} title="What it cost">
                {node.usage ? (
                  <>
                    <Row label="Tokens">
                      {(
                        node.usage.total_input_tokens +
                        node.usage.total_output_tokens +
                        node.usage.total_cache_read_tokens +
                        node.usage.total_cache_write_tokens
                      ).toLocaleString()}
                    </Row>
                    <Row label="Cost">
                      ${(node.usage.total_cost_usd_ticks / COST_USD_TICKS_PER_USD).toFixed(2)}
                    </Row>
                    <Row label="Runs">{node.usage.task_count}</Row>
                  </>
                ) : (
                  // Not the same as a run that cost nothing, and the two must
                  // not render alike.
                  <p className="py-1.5 text-caption text-muted-foreground">
                    No metered run.
                  </p>
                )}
              </Section>
            </div>
          </>
        ) : null}
      </SheetContent>
    </Sheet>
  );
}
