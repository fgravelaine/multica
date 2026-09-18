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
import type { MissionLevel, LevelGate } from "@multica/core/types";
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

// SPIKE (not upstream): the verify beat, on the ticket.
//
// Everything below this point existed only as API and CLI. A ticket that
// refuses to move showed a 409 toast and nothing else — the person looking at
// it could see that it was stuck and not why, which is the worst of both: a
// hard block with a soft explanation.
//
// It writes, so it lives here rather than in the mission view, same rule as the
// two controls above.
export function VerifySection({
  issueId,
  level,
  status,
}: {
  issueId: string;
  level?: string | null;
  status: string;
}) {
  const queryClient = useQueryClient();
  const wsId = useWorkspaceId();
  const [ruling, setRuling] = useState<number | null>(null);
  const [evidence, setEvidence] = useState("");
  const [verdict, setVerdict] = useState<boolean | null>(null);

  const criteria = useQuery({
    queryKey: ["issue-criteria", issueId],
    queryFn: () => api.listIssueCriteria(issueId),
  });

  // One request for the whole workspace's gates rather than one per issue: the
  // list is tiny and identical for every ticket, so a per-issue fetch would be
  // a fan-out that buys nothing. Cached across every ticket the user opens.
  const gates = useQuery({
    queryKey: ["level-gates", wsId],
    queryFn: () => api.listLevelGates(),
    staleTime: 5 * 60 * 1000,
  });

  const rule = useMutation({
    mutationFn: ({ ordinal, passed }: { ordinal: number; passed: boolean }) =>
      api.recordIssueVerdict(issueId, ordinal, passed, evidence),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["issue-criteria", issueId] });
      // The gate reads these, so the issue's own state can change with them.
      // issueKeys.all is the repo's prefix — a hand-written ["issue"] misses it.
      queryClient.invalidateQueries({ queryKey: issueKeys.all(wsId) });
      setRuling(null);
      setEvidence("");
      setVerdict(null);
    },
  });

  const importFromDescription = useMutation({
    mutationFn: () => api.setIssueCriteria(issueId, { from_description: true }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["issue-criteria", issueId] });
    },
  });

  const list = criteria.data?.criteria ?? [];

  // The gate this ticket is standing at, if any. Undeclared rungs fall back to
  // depth server-side; the panel does not re-derive that, so a ticket whose
  // rung is undeclared simply shows no gate rather than a guessed one.
  const gateHere = (gates.data?.gates ?? []).find(
    (g) => g.level === level && g.status_key === status,
  );

  if (list.length === 0) {
    return (
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <span className="text-xs font-medium text-muted-foreground">Acceptance criteria</span>
        </div>
        <p className="text-xs text-muted-foreground">
          None. This unit cannot be verified — and an issue with no criteria goes back to
          whoever scoped it rather than through.
        </p>
        <Button
          variant="outline"
          size="sm"
          className="h-7 text-xs"
          disabled={importFromDescription.isPending}
          onClick={() => importFromDescription.mutate()}
        >
          <Plus className="mr-1 h-3 w-3" />
          Read them from the description
        </Button>
        {importFromDescription.isError ? (
          <p className="text-xs text-destructive">
            No `## Acceptance Criteria` section with bullets in this issue.
          </p>
        ) : null}
      </div>
    );
  }

  const unruled = list.filter((c) => !c.ruled).length;
  const failed = list.filter((c) => c.ruled && !c.passed).length;

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-muted-foreground">Acceptance criteria</span>
        {gateHere?.requires_verdicts ? (
          <span
            className={cn(
              "inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px]",
              failed > 0 || unruled > 0
                ? "bg-destructive/10 text-destructive"
                : "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
            )}
          >
            <Lock className="h-3 w-3" />
            {failed > 0
              ? `${failed} failing`
              : unruled > 0
                ? `${unruled} unruled`
                : "verdicts in"}
          </span>
        ) : null}
      </div>

      <ol className="space-y-1.5">
        {list.map((c) => (
          <li key={c.id} className="rounded border border-border/60 p-2">
            <div className="flex items-start gap-2">
              <span
                className={cn(
                  "mt-0.5 shrink-0 rounded px-1 text-[10px] font-medium uppercase",
                  // An unruled criterion is NEITHER colour. `passed` is
                  // coalesced server-side, so painting it red would tell the
                  // reader that everything nobody has checked has failed.
                  !c.ruled
                    ? "bg-muted text-muted-foreground"
                    : c.passed
                      ? "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400"
                      : "bg-destructive/10 text-destructive",
                )}
              >
                {!c.ruled ? "—" : c.passed ? "pass" : "fail"}
              </span>
              <div className="min-w-0 flex-1">
                <p className="text-xs leading-snug">{c.statement}</p>
                {c.ruled && c.evidence ? (
                  <p className="mt-1 text-[11px] leading-snug text-muted-foreground">
                    {c.evidence}
                  </p>
                ) : null}
              </div>
              {ruling === c.ordinal ? null : (
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-6 shrink-0 px-1.5 text-[11px]"
                  onClick={() => {
                    setRuling(c.ordinal);
                    setEvidence("");
                    setVerdict(null);
                  }}
                >
                  {c.ruled ? "Re-check" : "Rule"}
                </Button>
              )}
            </div>

            {ruling === c.ordinal ? (
              <div className="mt-2 space-y-2 border-t border-border/60 pt-2">
                <div className="flex gap-1">
                  <Button
                    variant={verdict === true ? "default" : "outline"}
                    size="sm"
                    className="h-6 px-2 text-[11px]"
                    onClick={() => setVerdict(true)}
                  >
                    <Check className="mr-1 h-3 w-3" />
                    Pass
                  </Button>
                  <Button
                    variant={verdict === false ? "destructive" : "outline"}
                    size="sm"
                    className="h-6 px-2 text-[11px]"
                    onClick={() => setVerdict(false)}
                  >
                    <X className="mr-1 h-3 w-3" />
                    Fail
                  </Button>
                </div>
                <Input
                  value={evidence}
                  onChange={(e) => setEvidence(e.target.value)}
                  placeholder="Steps to reproduce, expected, observed"
                  className="h-7 text-xs"
                />
                <div className="flex items-center gap-2">
                  <Button
                    size="sm"
                    className="h-6 px-2 text-[11px]"
                    // Evidence is required on a PASS as much as a fail. A pass
                    // nobody can reproduce is the aggregate "works fine" the
                    // per-criterion rule exists to refuse.
                    disabled={verdict === null || evidence.trim() === "" || rule.isPending}
                    onClick={() => rule.mutate({ ordinal: c.ordinal, passed: verdict === true })}
                  >
                    Record
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-6 px-2 text-[11px]"
                    onClick={() => setRuling(null)}
                  >
                    Cancel
                  </Button>
                  {verdict !== null && evidence.trim() === "" ? (
                    <span className="text-[11px] text-muted-foreground">
                      Evidence is required, on a pass too.
                    </span>
                  ) : null}
                </div>
              </div>
            ) : null}
          </li>
        ))}
      </ol>

      {gateHere ? <GateNotice gate={gateHere} unruled={unruled} failed={failed} /> : null}
    </div>
  );
}

// GateNotice says why the ticket will not move, in the place where the person
// is trying to move it.
//
// Without this the block is a 409 toast that vanishes. The refusal is real and
// deliberate; leaving it unexplained is what makes a hard block feel like a bug.
// joinClauses turns a list of clauses into one sentence: "a, b and c".
function joinClauses(clauses: string[]): string {
  if (clauses.length <= 1) return clauses[0] ?? "";
  return `${clauses.slice(0, -1).join(", ")} and ${clauses[clauses.length - 1]}`;
}

function GateNotice({
  gate,
  unruled,
  failed,
}: {
  gate: LevelGate;
  unruled: number;
  failed: number;
}) {
  // Each reason is a full clause, so they read as a sentence when joined. An
  // earlier version assembled noun phrases — "will not move on until 3
  // criterion without a verdict" — which is both ungrammatical and does not say
  // what has to happen.
  const reasons: string[] = [];
  if (gate.requires_verdicts) {
    if (failed > 0) {
      reasons.push(
        failed === 1 ? "1 criterion is failing" : `${failed} criteria are failing`,
      );
    } else if (unruled > 0) {
      reasons.push(
        unruled === 1
          ? "1 criterion still needs a verdict"
          : `${unruled} criteria still need a verdict`,
      );
    }
  }
  if (gate.ratifier_type === "agent") reasons.push("one named agent must release it");
  if (gate.ratifier_type === "human") reasons.push("a person must release it");
  if (gate.ratifier_type === "check" && gate.required_checks?.length) {
    reasons.push(`${gate.required_checks.join(", ")} must pass`);
  }

  if (reasons.length === 0) return null;

  return (
    <p className="flex items-start gap-1.5 rounded bg-muted/50 p-2 text-[11px] leading-snug text-muted-foreground">
      <Lock className="mt-0.5 h-3 w-3 shrink-0" />
      <span>
        This ticket is at a gate. {joinClauses(reasons)}.
      </span>
    </p>
  );
}
