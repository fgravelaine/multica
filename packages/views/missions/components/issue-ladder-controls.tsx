"use client";

// SPIKE (not upstream): the write half of the ladder, on the issue page.
//
// It lives HERE and not in the mission view because the mission view writes
// nothing — not a status, not a rung, not a link. Both of these are writes, so
// they belong on the page that already owns the issue's fields.
//
// Two controls:
//   LevelPicker      declares what a unit is meant to be. Its own endpoint, not
//                    an UpdateIssue field, so it cannot share handleUpdateField
//                    with the other properties.
//   WaitsOnSection   declares a wait on a unit anywhere — the one stage
//                    ordering cannot express.

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Lock, Plus, X } from "lucide-react";
import { api } from "@multica/core/api";
import { issueKeys } from "@multica/core/issues/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import type { MissionLevel } from "@multica/core/types";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { cn } from "@multica/ui/lib/utils";

const RUNGS: { value: MissionLevel; label: string; means: string }[] = [
  { value: "campaign", label: "Campaign", means: "many missions in it" },
  { value: "mission", label: "Mission", means: "carries the intent and the end state" },
  { value: "objective", label: "Objective", means: "you can tell whether you hold it" },
  { value: "task", label: "Task", means: "what one unit is ordered to do" },
  { value: "step", label: "Step", means: "how a task gets done" },
];

export function LevelPicker({
  issueId,
  level,
  align = "start",
}: {
  issueId: string;
  level?: string | null;
  align?: "start" | "end";
}) {
  const queryClient = useQueryClient();
  const wsId = useWorkspaceId();
  const mutation = useMutation({
    mutationFn: (next: MissionLevel | null) => api.setIssueLevel(issueId, next),
    // The board's counts and the issue itself both move, and neither is worth
    // hand-patching: this is a rare, deliberate edit, not a keystroke.
    //
    // issueKeys.all is the repo's own prefix — a hand-written ["issue"] misses
    // it entirely, which is how the first version of this wrote to the database
    // and left the panel reading "Undeclared".
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: issueKeys.all(wsId) });
      void queryClient.invalidateQueries({ queryKey: ["missions"] });
      void queryClient.invalidateQueries({ queryKey: ["mission"] });
    },
  });

  const current = RUNGS.find((r) => r.value === level);

  return (
    <DropdownMenu>
      {/* No asChild, and onClick rather than onSelect: this repo wraps Base
          UI, where Menu.Item props extend the full div attribute set — an
          onSelect typechecks and silently lands on the DOM node. The file that
          renders this warns about exactly that. */}
      <DropdownMenuTrigger
        disabled={mutation.isPending}
        className={cn(
          "flex w-full items-center gap-1.5 rounded-md px-1.5 py-1 text-left text-caption hover:bg-accent",
          !current && "text-muted-foreground",
        )}
      >
        {current ? current.label : "Undeclared"}
      </DropdownMenuTrigger>
      <DropdownMenuContent align={align} className="w-64">
        {RUNGS.map((rung) => (
          <DropdownMenuItem key={rung.value} onClick={() => mutation.mutate(rung.value)}>
            <span className="flex w-full items-center gap-2">
              <Check
                className={cn("size-3.5 shrink-0", rung.value !== level && "opacity-0")}
              />
              <span className="min-w-0 flex-1">
                <span className="block text-caption">{rung.label}</span>
                <span className="block text-[10px] text-muted-foreground">{rung.means}</span>
              </span>
            </span>
          </DropdownMenuItem>
        ))}
        {/* Undeclaring is not the same as picking a rung, and it is not a
            "none" option among the five: it hands the unit back to depth, and
            an undeclared unit can never be reported as standing on its own. */}
        <DropdownMenuItem onClick={() => mutation.mutate(null)}>
          <span className="flex w-full items-center gap-2">
            <Check className={cn("size-3.5 shrink-0", level != null && "opacity-0")} />
            <span className="min-w-0 flex-1">
              <span className="block text-caption">Undeclared</span>
              <span className="block text-[10px] text-muted-foreground">
                take the rung from how deep it sits
              </span>
            </span>
          </span>
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

export function WaitsOnSection({ issueId }: { issueId: string }) {
  const paths = useWorkspacePaths();
  const queryClient = useQueryClient();
  const { labelOf } = useIssueStatuses(useWorkspaceId());
  const [adding, setAdding] = useState(false);
  const [draft, setDraft] = useState("");
  const [error, setError] = useState<string | null>(null);

  const { data } = useQuery({
    queryKey: ["issue-dependencies", issueId],
    queryFn: () => api.listIssueDependencies(issueId),
    enabled: !!issueId,
  });

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["issue-dependencies", issueId] });
    void queryClient.invalidateQueries({ queryKey: ["mission"] });
  };

  const add = useMutation({
    mutationFn: (identifier: string) => api.setIssueDependency(issueId, identifier),
    onSuccess: () => {
      setDraft("");
      setAdding(false);
      setError(null);
      refresh();
    },
    // The identifier is typed by hand, so the common failure is a typo. Say
    // which one failed rather than a generic toast somewhere else on screen.
    onError: (err: Error) => setError(err.message || "No issue with that identifier"),
  });

  const remove = useMutation({
    mutationFn: (blockerId: string) => api.removeIssueDependency(issueId, blockerId),
    onSuccess: refresh,
  });

  const blockers = data?.blocked_by ?? [];
  // Nothing to say and nothing to add: an empty section on every issue in the
  // workspace would be noise on the hundreds that wait on nothing.
  if (blockers.length === 0 && !adding) {
    return (
      <button
        type="button"
        onClick={() => setAdding(true)}
        className="flex items-center gap-1.5 text-micro text-muted-foreground hover:text-foreground"
      >
        <Plus className="size-3" />
        Waits on
      </button>
    );
  }

  return (
    <section className="space-y-1.5">
      <h3 className="flex items-center gap-1.5 text-body font-medium">
        <Lock className="size-3.5 text-muted-foreground" />
        Waits on
      </h3>
      {/* Said once, here, because the distinction is the whole reason this
          exists: stages order siblings, this reaches anywhere. */}
      <p className="text-[11px] text-muted-foreground">
        For a wait that stage ordering cannot express — a unit in another
        mission, another campaign, or another squad&apos;s tree.
      </p>

      {blockers.map((blocker) => (
        <div
          key={blocker.issue_id}
          className="flex items-center gap-2 rounded-md border px-2.5 py-1.5"
        >
          <a
            href={paths.issueDetail(blocker.issue_id)}
            className="flex min-w-0 flex-1 items-center gap-2 hover:underline"
          >
            <span className="font-mono text-[10px] text-muted-foreground">
              {blocker.identifier}
            </span>
            <span className="min-w-0 flex-1 truncate text-caption">{blocker.title}</span>
          </a>
          <span className="shrink-0 text-[10px] text-muted-foreground">
            {labelOf(blocker.status)}
          </span>
          <button
            type="button"
            onClick={() => remove.mutate(blocker.issue_id)}
            aria-label={`Stop waiting on ${blocker.identifier}`}
            className="shrink-0 rounded-sm p-0.5 text-muted-foreground hover:bg-accent hover:text-foreground"
          >
            <X className="size-3" />
          </button>
        </div>
      ))}

      {adding ? (
        <form
          onSubmit={(event) => {
            event.preventDefault();
            const value = draft.trim();
            if (value) add.mutate(value);
          }}
          className="flex items-center gap-1.5"
        >
          <Input
            autoFocus
            value={draft}
            onChange={(event) => {
              setDraft(event.target.value);
              setError(null);
            }}
            placeholder="Issue identifier, e.g. SPIK-25"
            className="h-7 text-caption"
          />
          <Button type="submit" size="sm" disabled={add.isPending || !draft.trim()}>
            Add
          </Button>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            onClick={() => {
              setAdding(false);
              setDraft("");
              setError(null);
            }}
          >
            Cancel
          </Button>
        </form>
      ) : (
        <button
          type="button"
          onClick={() => setAdding(true)}
          className="flex items-center gap-1.5 text-micro text-muted-foreground hover:text-foreground"
        >
          <Plus className="size-3" />
          Add
        </button>
      )}

      {error ? <p className="text-[11px] text-destructive">{error}</p> : null}
    </section>
  );
}
