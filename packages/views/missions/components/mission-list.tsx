"use client";

// SPIKE (not upstream): the leveled board.
//
// One rung at a time, with how much sits on every other rung, because the
// question "how much is happening" has a different answer at each — and the
// answer at one rung tells you nothing about the next.
//
// The thing this exists for is the ORPHAN: a unit whose declared rung disagrees
// with its parentage — an objective triggered on its own, with no mission over
// it. Not expressible when the rung is derived from depth, because a parentless
// objective IS a campaign and the two are identical.
//
// An orphan is NOT an error, and nothing here paints it as one. It is an
// ordinary shape — work that started before the thing above it existed, which
// is most of how work actually starts. It is marked so you can find it, and
// counted so you can see how much of it there is; it is not red, it carries no
// warning triangle, and the board does not ask you to fix it.
//
// Read only, like the rest of this view.

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Hand, Unlink, Waypoints, X } from "lucide-react";
import { api } from "@multica/core/api";
import type { MissionBoardRow, MissionLevel } from "@multica/core/types";
import { useWorkspacePaths } from "@multica/core/paths";
import { useWorkspaceId } from "@multica/core/hooks";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { cn } from "@multica/ui/lib/utils";

const RUNGS: { level: MissionLevel; label: string }[] = [
  { level: "campaign", label: "Campaigns" },
  { level: "mission", label: "Missions" },
  { level: "objective", label: "Objectives" },
  { level: "task", label: "Tasks" },
  { level: "step", label: "Steps" },
];

function ago(iso: string | undefined): string {
  if (!iso) return "—";
  const seconds = Math.max(Math.floor((Date.now() - new Date(iso).getTime()) / 1000), 0);
  if (seconds < 60) return "just now";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

export function MissionList() {
  const [level, setLevel] = useState<MissionLevel>("campaign");
  const [orphansOnly, setOrphansOnly] = useState(false);
  const [campaign, setCampaign] = useState<{ id: string; title: string } | null>(null);

  const { data, isLoading, error } = useQuery({
    queryKey: ["missions", level, orphansOnly, campaign?.id ?? null],
    queryFn: () =>
      api.listMissions({
        level,
        orphans: orphansOnly,
        campaign: campaign?.id,
      }),
  });

  const orphansHere = data?.levels.find((l) => l.level === level)?.orphans ?? 0;

  return (
    <div className="h-full min-h-0 overflow-y-auto overscroll-contain">
      <div className="mx-auto w-full max-w-5xl px-6 py-6">
        <header className="mb-4">
          <h1 className="flex items-center gap-2 text-lg font-semibold">
            <Waypoints className="size-4 text-muted-foreground" />
            Work
          </h1>
          <p className="mt-1 text-caption text-muted-foreground">
            Campaign → Mission → Objective → Task → Step. Pick a rung to see
            everything on it.
          </p>
        </header>

        {/* Every rung, always, including the empty ones — tabs that appear and
            disappear as work moves are tabs you cannot learn. */}
        <div className="mb-3 flex flex-wrap gap-1">
          {RUNGS.map((rung) => {
            const counts = data?.levels.find((l) => l.level === rung.level);
            const active = rung.level === level;
            return (
              <button
                key={rung.level}
                type="button"
                onClick={() => setLevel(rung.level)}
                className={cn(
                  "flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-caption transition-colors",
                  active
                    ? "border-foreground/30 bg-accent text-foreground"
                    : "text-muted-foreground hover:border-foreground/20 hover:text-foreground",
                )}
              >
                {rung.label}
                <span className="font-mono text-[10px] tabular-nums opacity-60">
                  {counts?.total ?? 0}
                </span>
                {counts && counts.orphans > 0 ? (
                  <span
                    title={`${counts.orphans} standing on their own`}
                    className="flex items-center gap-0.5 rounded-sm bg-muted px-1 font-mono text-[10px] text-muted-foreground"
                  >
                    <Unlink className="size-2.5" />
                    {counts.orphans}
                  </span>
                ) : null}
              </button>
            );
          })}
        </div>

        <div className="mb-3 flex flex-wrap items-center gap-2 text-caption">
          <button
            type="button"
            onClick={() => setOrphansOnly((v) => !v)}
            disabled={orphansHere === 0 && !orphansOnly}
            className={cn(
              "flex items-center gap-1.5 rounded-md border px-2 py-0.5 transition-colors disabled:opacity-40",
              orphansOnly
                ? "border-foreground/30 bg-accent text-foreground"
                : "text-muted-foreground hover:border-foreground/20",
            )}
          >
            <Unlink className="size-3" />
            On their own
          </button>

          {campaign ? (
            <button
              type="button"
              onClick={() => setCampaign(null)}
              className="flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-muted-foreground hover:border-foreground/20"
            >
              in {campaign.title}
              <X className="size-3" />
            </button>
          ) : null}
        </div>

        {isLoading ? (
          <p className="text-caption text-muted-foreground">Loading…</p>
        ) : error ? (
          <p className="text-caption text-muted-foreground">Could not load the board.</p>
        ) : !data || data.rows.length === 0 ? (
          <p className="rounded-lg border border-dashed px-4 py-6 text-center text-caption text-muted-foreground">
            {orphansOnly
              ? "Nothing standing on its own here — every unit hangs where it says it does."
              : "Nothing on this rung."}
          </p>
        ) : (
          <ul className="divide-y rounded-lg border">
            {data.rows.map((row) => (
              <li key={row.id}>
                <Row row={row} onFilterCampaign={setCampaign} showCampaign={!campaign} />
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function Row({
  row,
  onFilterCampaign,
  showCampaign,
}: {
  row: MissionBoardRow;
  onFilterCampaign: (c: { id: string; title: string }) => void;
  showCampaign: boolean;
}) {
  const paths = useWorkspacePaths();
  // The workspace's own name for the status, not the key it is stored under.
  // One cached catalog query for the page, never one per row.
  const { labelOf } = useIssueStatuses(useWorkspaceId());
  const pct = row.units === 0 ? 0 : Math.round((row.done / row.units) * 100);

  return (
    <div className="flex items-center gap-3 px-4 py-2.5 hover:bg-accent/40">
      <a href={paths.tree(row.id)} className="flex min-w-0 flex-1 items-center gap-3">
        <span className="w-16 shrink-0 font-mono text-caption text-muted-foreground">
          {row.identifier}
        </span>
        <span className="min-w-0 flex-1 truncate text-sm">{row.title}</span>
        <span className="shrink-0 text-[10px] text-muted-foreground">
          {labelOf(row.status)}
        </span>
      </a>

      {/* Stated, not alarmed about. Saying what it claims to be and what the
          tree says instead is what makes it findable; a red badge would be
          calling an ordinary shape a fault. */}
      {row.orphan ? (
        <span
          title={
            row.parent
              ? `Declared ${row.level}, but its parent makes it something else`
              : `Declared ${row.level}, with nothing above it`
          }
          className="flex shrink-0 items-center gap-1 rounded-sm border px-1.5 py-0.5 text-[10px] text-muted-foreground"
        >
          <Unlink className="size-3" />
          on its own
        </span>
      ) : null}

      {row.open_hands > 0 ? (
        <span className="flex shrink-0 items-center gap-1 rounded-sm border border-amber-500/40 bg-amber-500/10 px-1.5 py-0.5 text-[10px] font-semibold uppercase text-amber-700 dark:text-amber-400">
          <Hand className="size-3" />
          {row.open_hands}
        </span>
      ) : null}

      {row.parent ? (
        <span className="hidden w-40 shrink-0 truncate text-[10px] text-muted-foreground md:block">
          under {row.parent.title}
        </span>
      ) : null}

      {/* Only when it says something: on the campaign rung every row IS its own
          campaign, and repeating it would be noise. */}
      {showCampaign && row.level !== "campaign" && !row.orphan ? (
        <button
          type="button"
          onClick={() => onFilterCampaign({ id: row.campaign.id, title: row.campaign.title })}
          className="hidden w-32 shrink-0 truncate text-left text-[10px] text-muted-foreground hover:text-foreground hover:underline lg:block"
        >
          {row.campaign.title}
        </button>
      ) : null}

      <span className="flex w-32 shrink-0 items-center gap-2">
        <span className="h-1.5 flex-1 overflow-hidden rounded-full bg-muted">
          <span
            className={cn(
              "block h-full rounded-full",
              pct === 100 ? "bg-emerald-500" : "bg-primary",
            )}
            style={{ width: `${pct}%` }}
          />
        </span>
        <span className="w-10 shrink-0 text-right font-mono text-[10px] tabular-nums text-muted-foreground">
          {row.done}/{row.units}
        </span>
      </span>

      <span className="w-20 shrink-0 text-right text-[10px] text-muted-foreground">
        {ago(row.last_activity_at ?? row.updated_at)}
      </span>
    </div>
  );
}
