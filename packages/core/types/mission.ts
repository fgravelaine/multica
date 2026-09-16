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

/**
 * What a unit IS. The ladder is Campaign → Mission → Objective → Task, and only
 * the last three are depths in the issue tree — the campaign is Multica's
 * `project`, an entity that already exists.
 *
 * The words are borrowed from military usage; the ORDERING is local, not
 * doctrinal. Exactly one boundary is enforced by the product (campaign/mission,
 * which are different tables). `objective` vs `task` is a writing discipline —
 * no code keys on depth. The full accounting is on MissionNode.Level, server
 * side; read it before building behaviour on this field.
 *
 * `step` is the floor and earns its name on the FOLD, not on the server:
 * nothing below a task behaves differently, but a step is below the reporting
 * line and the canvas keeps it folded until asked. Depth 4 and below are steps
 * too — inside a folded step, so a reader never meets the name twice.
 */
export type MissionLevel = "mission" | "objective" | "task" | "step";

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
  /** Mission at depth 0, objective at 1, task below. See MissionLevel. */
  level: MissionLevel;
  terminal: boolean;
  /** True when this node, or anything beneath it, is in the waiting list. */
  waiting_below: boolean;
  /** Absent when the issue has never had a metered run. */
  usage?: MissionNodeUsage;
  /**
   * The units standing between this one and its turn. Absent when nothing is.
   *
   * Multica records no "blocked by" LINK — issue_dependency is a dead table
   * with no API and no writer. Its answer to "the email needs the design
   * validated" is the stage barrier, so these are the frontier stage's open
   * units: an ordering, named.
   */
  blocked_by?: MissionBlocker[];
  /** Absent when the issue has never run. */
  last_run?: MissionNodeRun;
  /** When the issue entered its current status. */
  status_since?: string;
  /** False when `status_since` is issue.updated_at rather than a transition. */
  status_since_exact: boolean;
}

/** One unit standing between another and its turn. */
export interface MissionBlocker {
  issue_id: string;
  identifier: string;
  title: string;
  status: string;
  stage?: number;
  /** Why it blocks. `stage_barrier` is the only relation the product records. */
  relation: string;
}

/** The latest run of one unit. Every field is a column the product writes. */
export interface MissionNodeRun {
  task_id: string;
  status: string;
  failure_reason?: string;
  wait_reason?: string;
  dispatched_at?: string;
  started_at?: string;
  completed_at?: string;
  created_at: string;
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
  /** Catalog key of the body of knowledge this question interrogates. */
  referential?: string;
  /** Where the hand is addressed: a squad leader, or the human. */
  recipient_type: "lead" | "human";
  lead_name?: string;
  /** True when a lead had it first and could not settle it. */
  escalated: boolean;
  created_at: string;
}

/**
 * Where raised hands ended up. `reached_human` is the number that measures
 * autonomy — a team whose hands all get settled by a lead is not a team that
 * stopped asking, it is one whose referentials and leads can answer.
 */
export interface MissionAutonomy {
  total: number;
  reached_human: number;
  escalated: number;
  settled_by_lead: number;
  settled_by_human: number;
  still_open: number;
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

/**
 * One lead's contest record. Settled against escalated is the closest the
 * system can get to "did this lead check the referential first" — nothing can
 * enforce the contest step, but the outcome is measurable.
 */
export interface MissionLeadContest {
  lead_id: string;
  lead_name?: string;
  total: number;
  settled: number;
  escalated: number;
  still_open: number;
}

/** The rung above the mission. Multica calls it a project. */
export interface MissionCampaign {
  id: string;
  title: string;
  icon?: string;
  status: string;
}

export interface MissionResponse {
  /** Absent when the mission belongs to no campaign — stated, not hidden. */
  campaign?: MissionCampaign;
  root: MissionNode;
  nodes: MissionNode[];
  stages: MissionStage[];
  /** Longest wait first. Deliberately not sorted by priority. */
  waiting: MissionWaitingUnit[];
  /** Hands addressed to a squad leader — out of the primary list by design. */
  with_lead: MissionWaitingUnit[];
  autonomy: MissionAutonomy;
  /** The same measurement per lead: who contests, who relays. */
  leads: MissionLeadContest[];
  /**
   * Stages whose predecessor closed and which nobody promoted. Every unit in
   * one looks fine and the mission has stopped — the failure the waiting list
   * structurally cannot catch.
   */
  stalled: MissionWaitingUnit[];
  referentials: MissionReferential[];
  /**
   * False since the raised hand carries a real referential_key. Kept on the
   * wire so a client can tell a genuine grouping from a proxy without knowing
   * which server version it is talking to.
   */
  referential_stand_in: boolean;
  referential_field: string;
  /** Children the stage barrier ignores entirely, so they gate nothing. */
  unstaged_ignored: string[];
  truncated: boolean;
  max_depth: number;
}

/**
 * One mission in the index.
 *
 * A mission is not an entity: it is a top-level issue that has children. The
 * index derives the list every time rather than storing one, which is why it
 * cannot be filtered, sorted or saved — there is nothing to save it on.
 */
export interface MissionSummary {
  id: string;
  identifier: string;
  number: number;
  title: string;
  status: string;
  /** Everything below the root, to the same depth bound the detail view uses. */
  units: number;
  done: number;
  open_hands: number;
  last_activity_at?: string;
  updated_at: string;
}

export interface MissionListResponse {
  missions: MissionSummary[];
  max_depth: number;
}
