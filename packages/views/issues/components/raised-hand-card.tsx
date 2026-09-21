"use client";

// SPIKE (not upstream): the raised hand, in the issue.
//
// The whole point of the object is that a decision can be APPLIED to it, so
// this component is a row of buttons, not a rendering of a question. Clicking
// one writes the decision, delivers it to the agent, and returns the unit to
// the board — the agent resumes on its existing session.
//
// What it shows, and why each part is there:
//   - the question,
//   - every option with the COST OF BEING WRONG on that side. This is the field
//     that lets you decide in thirty seconds without knowing the domain, so it
//     gets equal weight to the label rather than being hidden behind a tooltip,
//   - the raiser's recommendation, marked. A raiser that stops without one has
//     handed over its judgement along with the decision.

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Hand, Check, MessagesSquare } from "lucide-react";
import { api } from "@multica/core/api";
import type { Agent, RaisedHand } from "@multica/core/types";
import { useWorkspacePaths } from "@multica/core/paths";
import { cn } from "@multica/ui/lib/utils";
import { useNavigation } from "../../navigation";

export function RaisedHandCard({ issueId }: { issueId: string }) {
  const queryClient = useQueryClient();
  const [pendingKey, setPendingKey] = useState<string | null>(null);

  const { data } = useQuery({
    queryKey: ["raised-hands", issueId],
    queryFn: () => api.listIssueRaisedHands(issueId),
    enabled: !!issueId,
  });

  const answer = useMutation({
    mutationFn: ({ handId, optionKey }: { handId: string; optionKey: string }) =>
      api.answerIssueRaisedHand(issueId, optionKey, undefined, handId),
    onSettled: () => {
      setPendingKey(null);
      void queryClient.invalidateQueries({ queryKey: ["raised-hands", issueId] });
      void queryClient.invalidateQueries({ queryKey: ["issue", issueId] });
      void queryClient.invalidateQueries({ queryKey: ["comments", issueId] });
    },
  });

  const hands = data?.hands ?? [];
  // EVERY open hand, not the first. A unit may be parked on several since
  // migration 491, and it resumes when the last is answered — so rendering one
  // would hide the reason the board is not moving after you answered it.
  const open = hands.filter((hand: RaisedHand) => hand.status === "open");

  // Only OPEN hands are rendered. An answered one is history, and its decision
  // is already in the comment feed where the agent read it.
  if (open.length === 0) return null;

  return (
    <div className="mb-4 rounded-lg border border-amber-500/40 bg-amber-500/5 p-3">
      <div className="mb-2 flex items-center gap-2">
        <Hand className="size-4 shrink-0 text-amber-600 dark:text-amber-400" />
        <span className="text-caption font-semibold uppercase tracking-wide text-amber-700 dark:text-amber-400">
          {open.length > 1 ? `${open.length} hands raised` : "Hand raised"}
        </span>
      </div>
      {open.length > 1 ? (
        <p className="mb-3 text-caption text-muted-foreground">
          The unit stays parked until every one is answered.
        </p>
      ) : null}
      <DiscussWithAgent issueId={issueId} />

      {open.map((hand: RaisedHand) => (
        <HandBlock
          key={hand.id}
          hand={hand}
          pendingKey={pendingKey}
          isPending={answer.isPending}
          onPick={(optionKey) => {
            setPendingKey(`${hand.id}:${optionKey}`);
            answer.mutate({ handId: hand.id, optionKey });
          }}
        />
      ))}
    </div>
  );
}

// PICKING AN OPTION IS NOT THE ONLY THING YOU MAY WANT TO DO WITH A QUESTION.
//
// The alternative was the comment thread, and a comment mention dispatches a
// WORK run: the agent claims a task against this issue and arrives with the
// issue brief, ready to do the ticket. What you want here is to argue.
//
// A chat bound to this issue gets a brief carrying the unit and every open
// hand with its costs, and the instruction to contest rather than comply. The
// conversation is the lead's job in `raised-hand.md` — "contest first, the
// answer is often already in a reference".
function DiscussWithAgent({ issueId }: { issueId: string }) {
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const [agentId, setAgentId] = useState("");

  const { data: agents } = useQuery({
    queryKey: ["agents", "for-discussion"],
    queryFn: () => api.listAgents(),
  });

  const start = useMutation({
    mutationFn: (id: string) =>
      api.createChatSession({ agent_id: id, issue_id: issueId }),
    onSuccess: (session) => navigation.push(paths.chatSession(session.id)),
  });

  const roster = (agents ?? []).filter((a: Agent) => !a.archived_at);
  if (roster.length === 0) return null;

  return (
    <div className="mb-3 flex items-center gap-2">
      <MessagesSquare className="size-3.5 shrink-0 text-muted-foreground" />
      <select
        value={agentId}
        disabled={start.isPending}
        onChange={(e) => {
          const id = e.target.value;
          setAgentId(id);
          if (id) start.mutate(id);
        }}
        aria-label="Discuss this with an agent"
        className="rounded-md border bg-transparent px-2 py-0.5 text-caption text-muted-foreground transition-colors hover:border-foreground/20 disabled:opacity-60"
      >
        <option value="">Talk it through with…</option>
        {roster.map((a: Agent) => (
          <option key={a.id} value={a.id}>
            {a.name}
          </option>
        ))}
      </select>
      {start.isPending ? (
        <span className="text-caption text-muted-foreground">opening…</span>
      ) : null}
    </div>
  );
}

function HandBlock({
  hand,
  pendingKey,
  isPending,
  onPick,
}: {
  hand: RaisedHand;
  pendingKey: string | null;
  isPending: boolean;
  onPick: (optionKey: string) => void;
}) {
  const open = hand;
  return (
    <div className="mb-3 last:mb-0">

      <p className="mb-3 text-sm font-medium leading-snug">{open.question}</p>

      {open.material ? (
        <p className="mb-3 whitespace-pre-wrap text-caption text-muted-foreground">{open.material}</p>
      ) : null}

      <div className="flex flex-col gap-2">
        {open.options.map((option) => {
          const recommended = open.recommendation === option.key;
          const busy = isPending && pendingKey === `${hand.id}:${option.key}`;
          return (
            <button
              key={option.key}
              type="button"
              disabled={isPending}
              onClick={() => onPick(option.key)}
              className={cn(
                "group rounded-md border px-3 py-2 text-left transition-colors",
                "hover:border-amber-500 hover:bg-amber-500/10",
                "disabled:cursor-not-allowed disabled:opacity-60",
                recommended ? "border-amber-500/60 bg-amber-500/5" : "border-border",
              )}
            >
              <span className="flex items-center gap-2">
                <span className="text-sm font-medium">{option.label}</span>
                {recommended ? (
                  <span className="inline-flex items-center gap-1 rounded-sm bg-amber-500/15 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-amber-700 dark:text-amber-400">
                    <Check className="size-3" />
                    recommended
                  </span>
                ) : null}
                {busy ? <span className="text-caption text-muted-foreground">…</span> : null}
              </span>
              {/* The cost of being WRONG on this side — the field that makes the
                  decision cheap for someone outside the domain. */}
              <span className="mt-0.5 block text-caption leading-snug text-muted-foreground">
                If wrong: {option.cost}
              </span>
            </button>
          );
        })}
      </div>

      <p className="mt-2 text-caption text-muted-foreground">
        Picking an option records the decision. The issue goes back on the board once every open
        hand is answered, and the agent continues from where it stopped.
      </p>
    </div>
  );
}
