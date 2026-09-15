// SPIKE (not upstream): the mission view.
//
// Mirrors server/internal/handler/mission.go. Read-only by construction: there
// is no mutation type in this file and no client method that writes.

import type { RaisedHandOption } from "./issue";

/** Why a unit is parked. Every value is a state the product already records. */
export type MissionWaitingReason =
  | "hand_raised"
  | "blocked"
  | "in_review"
  | "run_failed"
  /** The stage below closed and nobody promoted this one. Reported separately. */
  | "stage_not_promoted";

export interface MissionNodeUsage {
  total_input_tokens: number;
  total_output_tokens: number;
  total_cache_read_tokens: number;
  total_cache_write_tokens: number;
  /** 1e-10 USD, the same unit every other usage endpoint emits. */
  total_cost_usd_ticks: number;
  task_count: number;
}

export interface MissionNode {
  id: string;
  parent_id: string | null;
  identifier: string;
  number: number;
  title: string;
  status: string;
  status_category?: string;
  priority: string;
  assignee_type: string | null;
  assignee_id: string | null;
  /** null for a child that carries no stage. */
  stage: number | null;
  depth: number;
  terminal: boolean;
  /** True when this node, or anything beneath it, is in the waiting list. */
  waiting_below: boolean;
  /** Absent when the issue has never had a metered run. */
  usage?: MissionNodeUsage;
}

export interface MissionStage {
  /** null for the single implicit stage of an unstaged sibling set. */
  stage: number | null;
  total: number;
  terminal: number;
  /**
   * Cumulative, like the product's barrier: a stage is closed only when it and
   * every stage below it is terminal.
   */
  closed: boolean;
  /** The lowest stage that is not closed. At most one. */
  frontier: boolean;
  issue_ids: string[];
}

export interface MissionHand {
  id: string;
  issue_id: string;
  question: string;
  options: RaisedHandOption[];
  recommendation?: string;
  agent_id?: string;
  agent_name?: string;
  created_at: string;
}

export interface MissionWaitingUnit {
  issue_id: string;
  identifier: string;
  title: string;
  status: string;
  depth: number;
  reason: MissionWaitingReason;
  /** The reason in the system's own words, when it has any. */
  detail?: string;
  since: string;
  /** False when `since` is a fallback rather than the real transition time. */
  since_exact: boolean;
  waited_seconds: number;
  hand?: MissionHand;
}

export interface MissionReferential {
  key: string;
  label: string;
  count: number;
  hands: MissionHand[];
}

export interface MissionResponse {
  root: MissionNode;
  nodes: MissionNode[];
  stages: MissionStage[];
  /** Longest wait first. Deliberately not sorted by priority. */
  waiting: MissionWaitingUnit[];
  /**
   * Stages whose predecessor closed and which nobody promoted. Every unit in
   * one looks fine and the mission has stopped — the failure the waiting list
   * structurally cannot catch.
   */
  stalled: MissionWaitingUnit[];
  referentials: MissionReferential[];
  /**
   * True while hands carry no referential of their own and the grouping falls
   * back to the raising agent. The UI must say so.
   */
  referential_stand_in: boolean;
  referential_field: string;
  /** Children the stage barrier ignores entirely, so they gate nothing. */
  unstaged_ignored: string[];
  truncated: boolean;
  max_depth: number;
}
