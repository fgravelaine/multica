"use client";

// SPIKE (not upstream): "is this ticket standing at a gate", for the board.
//
// Split out of the card so the gate list is ONE cached query for the whole
// board rather than a fetch per card. React Query dedupes by key, so every card
// mounting this hook shares a single request and a single cache entry — the
// same call the ticket panel already makes.

import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import type { LevelGate } from "@multica/core/types";

export function useGateForCard(
  level: string | null | undefined,
  status: string,
): LevelGate | undefined {
  const wsId = useWorkspaceId();
  const { data } = useQuery({
    queryKey: ["level-gates", wsId],
    queryFn: () => api.listLevelGates(),
    // Gates change when somebody reconfigures a rung, which is rare and
    // deliberate. Five minutes keeps a full board from re-asking on every
    // navigation without making a change invisible for a session.
    staleTime: 5 * 60 * 1000,
  });
  if (!level) {
    // An undeclared rung takes its level from depth server-side. The board does
    // not re-derive that walk — showing a gate the server might not apply would
    // be worse than showing none, because the lock would be a lie.
    return undefined;
  }
  return data?.gates.find((g) => g.level === level && g.status_key === status);
}

// gateReason is the card's tooltip: short, and it names what is in the way
// rather than just asserting that something is.
export function gateReason(gate: LevelGate): string {
  switch (gate.ratifier_type) {
    case "agent":
      return gate.requires_verdicts
        ? "Needs every criterion ruled, then one named agent releases it"
        : "One named agent releases this";
    case "check":
      return `Waiting on ${gate.required_checks?.join(", ") ?? "checks"}`;
    default:
      return gate.requires_verdicts
        ? "Needs every criterion ruled, then a person releases it"
        : "A person releases this";
  }
}
