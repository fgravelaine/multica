"use client";

// SPIKE (not upstream): the mission index.
//
// The view was unreachable — one small link on an issue, which is not a way in.
// This is the way in, and it is deliberately thin: a mission is not an entity
// in this product, it is a top-level issue that has children. So there is
// nothing here to create, filter, sort or save. It is a derivation, and making
// it look like a place to put things would be inventing a second board.
//
// What it shows is the one thing that decides which mission you open: how much
// of it is done, and whether anything in it is waiting on a person.

import { useQuery } from "@tanstack/react-query";
import { Hand, Waypoints } from "lucide-react";
import { api } from "@multica/core/api";
import type { MissionSummary } from "@multica/core/types";
import { useWorkspacePaths } from "@multica/core/paths";
import { cn } from "@multica/ui/lib/utils";

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
  const paths = useWorkspacePaths();
  const { data, isLoading, error } = useQuery({
    queryKey: ["missions"],
    queryFn: () => api.listMissions(),
  });

  return (
    <div className="h-full min-h-0 overflow-y-auto overscroll-contain">
      <div className="mx-auto w-full max-w-4xl px-6 py-6">
        <header className="mb-4">
          <h1 className="flex items-center gap-2 text-lg font-semibold">
            <Waypoints className="size-4 text-muted-foreground" />
            Missions
          </h1>
          <p className="mt-1 text-caption text-muted-foreground">
            Every top-level issue that has a tree under it. Open one to see where
            it is and what is waiting on you.
          </p>
        </header>

        {isLoading ? (
          <p className="text-caption text-muted-foreground">Loading…</p>
        ) : error ? (
          <p className="text-caption text-muted-foreground">Could not load missions.</p>
        ) : !data || data.missions.length === 0 ? (
          // Not an error and not a blank page: the honest reason, and what
          // makes one appear.
          <p className="rounded-lg border border-dashed px-4 py-6 text-center text-caption text-muted-foreground">
            No missions yet. A mission is a top-level issue with sub-issues —
            add one to an issue and it shows up here.
          </p>
        ) : (
          <ul className="divide-y rounded-lg border">
            {data.missions.map((mission) => (
              <li key={mission.id}>
                <MissionRow mission={mission} href={paths.mission(mission.id)} />
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function MissionRow({ mission, href }: { mission: MissionSummary; href: string }) {
  const pct = mission.units === 0 ? 0 : Math.round((mission.done / mission.units) * 100);
  return (
    <a href={href} className="flex items-center gap-3 px-4 py-3 hover:bg-accent/40">
      <span className="w-16 shrink-0 font-mono text-caption text-muted-foreground">
        {mission.identifier}
      </span>

      <span className="min-w-0 flex-1 truncate text-sm">{mission.title}</span>

      {/* The one thing worth interrupting for, so it sits before the progress
          bar rather than after it. */}
      {mission.open_hands > 0 ? (
        <span className="flex shrink-0 items-center gap-1 rounded-sm border border-amber-500/40 bg-amber-500/10 px-1.5 py-0.5 text-[10px] font-semibold uppercase text-amber-700 dark:text-amber-400">
          <Hand className="size-3" />
          {mission.open_hands}
        </span>
      ) : null}

      <span className="flex w-40 shrink-0 items-center gap-2">
        <span className="h-1.5 flex-1 overflow-hidden rounded-full bg-muted">
          <span
            className={cn("block h-full rounded-full", pct === 100 ? "bg-emerald-500" : "bg-primary")}
            style={{ width: `${pct}%` }}
          />
        </span>
        <span className="w-12 shrink-0 text-right font-mono text-[10px] text-muted-foreground tabular-nums">
          {mission.done}/{mission.units}
        </span>
      </span>

      <span className="w-20 shrink-0 text-right text-[10px] text-muted-foreground">
        {ago(mission.last_activity_at ?? mission.updated_at)}
      </span>
    </a>
  );
}
