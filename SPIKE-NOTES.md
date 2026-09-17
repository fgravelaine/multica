# Spike notes — task-level routing, the raised hand, and the mission view

Throwaway fork. Not a contribution. Nothing here is written for a maintainer.

Upstream at fork point: `cf52ba33c` (2026-09-14).

---

## Where a task's runtime is decided

**`ClaimAgentTask`, `server/pkg/db/queries/agent.sql:721`.** One SQL statement.
Everything else is plumbing around it.

The decision is made in four places that all say the same thing — *the agent
owns the runtime, the task only carries a copy*:

| # | Place | What it does |
|---|---|---|
| 1 | `service/task.go:1291` (and 1458, 1640, 2056) | enqueue stamps `RuntimeID: agent.RuntimeID` onto the task row |
| 2 | `service/task.go:3488` | claim with no runtime given resolves `agent.RuntimeID` |
| 3 | `service/task.go:3499` | claim with a runtime given returns `runtime_mismatch` unless it equals `agent.RuntimeID` |
| 4 | `agent.sql:747` | `AND a.runtime_id = atq.runtime_id` — *"A task's persisted runtime is not authority after an agent rebind."* |

`agent_task_queue.runtime_id` already exists as a column. It is not a routing
decision; it is a denormalised copy of the agent's binding, and #4 exists
precisely to stop anyone reading it as one. Writing a different runtime onto a
task makes that task permanently unclaimable — no daemon's claim can satisfy #4.

### The batch path
`ClaimTasksForRuntimes` (`service/task.go:3831`) → `ClaimTasksByRuntime`
(`handler/daemon.go:1705`). A daemon = one machine, posting every `runtime_id`
it hosts plus its free slot count. The server claims across the set and hands
back tasks already stamped with a runtime so the daemon routes locally. Six
steps; step 6 calls the same `claimTask` per distinct agent, so all four fences
above still apply.

### cloudruntime is not a second router
`internal/cloudruntime/client.go` is a plain HTTP client to a remote fleet API,
reached only through `handler/cloud_runtime.go`, which is a reverse proxy
(`/api/v1/nodes` → EC2). A cloud node boots a daemon and registers as an
ordinary `agent_runtime` row with `daemon_id` NULL. So there is exactly ONE
dispatch mechanism in the server, and it is agent-keyed. Nothing to generalise
from.

---

## Waiting vs failing

The premise "a task whose runtime is offline fails rather than waits" is not
what the code says, and the distinction is sharper than expected.

`runtimeVerdict` (`service/agent_ready.go:155`) returns three verdicts:

- **`AgentAvailable`** — runtime online.
- **`AgentWaitable`** — offline. Queued work waits; the wait ends by itself.
- **`AgentBlocked`** — refuse. Three causes: no runtime bound at all
  (`agent_runtime_required`), CLI not executable (`runtime_unusable`), runtime
  profile missing (`runtime_profile_missing`). The comment on each says why:
  *waiting changes nothing, the machine is already on.*

So the server already draws the line the spike wanted to draw — **but it draws
it on the agent, not on the task.** The property "is waiting a plan here" is
derived from `agent.runtime_id` and that runtime's row. A task has no say.

Two other wait shapes already exist on the task row:
- `status='deferred'` + `fire_at` — a timed wait, swept into `queued`.
- `status='waiting_local_directory'` + `wait_reason` — a *daemon-side* wait.
  The task was already claimed; the daemon holds it while another task owns the
  path. Not a return to the pool.

Eight statuses total (`migrations/128`): queued, dispatched, running, completed,
failed, cancelled, waiting_local_directory, deferred.

---

## The two UI behaviours, in code

**Backlog parks, Todo runs.** `shouldEnqueueAgentTask`,
`handler/issue.go:3874`. Only the fixed `backlog` key parks — a *custom*
unstarted status does not. Confirmed.

**Session resume.** `GetLastTaskSession`, `agent.sql:993`. Keyed on
(agent_id, issue_id), accepts completed / failed / cancelled, `DISTINCT ON
(session_id)` then filters resume-unsafe terminal states.

On the escape hatch: the *mechanism* exists — `retired_session_id`,
`force_fresh_session`, a poison-reason filter list, manual rerun via
`rerun_of_task_id`. What is missing is only the **surface**: nothing a user can
type. #1296 asked for the surface, not the mechanism.

---

---

## Baseline, measured — a queued task already waits

Not read off the code; run. Daemon stopped so both runtimes went offline,
then an issue assigned to an agent on the offline Claude runtime:

```
task     | status | runtime  | failure_reason
01a0a190 | queued | 6389f249 |
```

Queued. No failure. Fifteen minutes later the daemon came back and the same
row went `completed` without anyone touching it.

**So "a task whose runtime is offline fails rather than waits" is not the gap.**
Waiting is already the behaviour, and `ClaimAgentTask`'s `r.status = 'online'`
predicate is what produces it: an offline runtime simply does not match, and
the row stays `queued`. Nothing expires it.

The real gap is narrower and harder: **a task cannot name a machine**, so it
can only ever wait for the one machine its agent happens to be bound to.
`runtime_offline` as a *failure* reason belongs to a different moment — a task
already dispatched or running when its runtime drops (`daemon.go:5714`).

---

## Running it

Docker, on the localenv stack. `docker-compose.localenv.yml` layers over
`docker-compose.selfhost.yml` + `.build.yml`.

| What | Where |
|---|---|
| Web app + same-origin API | `https://app.multica.localhost` |
| API for token callers (CLI, daemon, curl) | `https://api.multica.localhost` |
| Backend direct (daemon) | `localhost:8080` |
| Postgres | `localhost:5433` (5432 is veezeet-api) |

Two things fought back, both cookie-shaped:

1. **Caddy's `local-defaults` snippet breaks the API.** Its cors rule sets
   `Access-Control-Allow-Origin: *` with `>`, clobbering the origin the backend
   echoes. A wildcard origin with `Access-Control-Allow-Credentials: true` is
   rejected by every browser. Use `caddy.tls: internal` instead.
2. **The web app cannot use a second hostname.** The auth cookie is
   `SameSite=Strict` and the CSRF cookie is read by JS on the app origin, so
   `api.multica.localhost` failed every write with "CSRF validation failed".
   `COOKIE_DOMAIN=.multica.localhost` does not rescue it. The app proxies
   `/api`, `/ws`, `/uploads` itself (`apps/web/next.config.ts`), so
   `NEXT_PUBLIC_API_URL` stays empty and the browser talks to one origin.

The daemon runs on the host under its own profile — `multica --profile spike` —
so it cannot collide with the Multica desktop app's daemon, which serves the
real hosted workspaces from `~/.multica/profiles/desktop-api.multica.ai/`.

Two runtimes registered on one machine, which is what makes the routing spike
testable at all: `Claude (spike-mac)` and `Opencode (spike-mac)`.

**`multica agent create --runtime-id` is required.** The ontology, stated in
the CLI: an agent cannot exist without a machine.

---

---

## Spike 1 — task-level routing. It works, and it cost five fences.

An issue carries `routing_policy`; every task it enqueues inherits it; the claim
resolves it against whichever machine is asking for work. Two forms only —
`runtime:<uuid>` and `provider:<name>` — because two is the minimum that proves
dispatch-time resolution is a different thing from an enqueue-time copy.

**Proof, run three times:**

| agent | bound to | policy | ran on | result |
|---|---|---|---|---|
| Scout | claude | `provider:opencode` | opencode | reached opencode, opencode CLI exited 1 (not configured here) |
| Drifter | opencode | `provider:claude` | claude | **completed**, replied `routed` |

The second row is the whole spike in one line: the agent is bound to one
machine and the run happened on another, on purpose, and the product did not
notice anything was unusual.

### The four places were five

The code read found four. A fifth only appeared by running it, and it is the
one that matters most:

| # | Place | What it asserts |
|---|---|---|
| 1 | `service/task.go` enqueue | task runtime := `agent.runtime_id` |
| 2 | `service/task.go:3488` | claim with no runtime resolves the agent's |
| 3 | `service/task.go:3499` | claim with a runtime → `runtime_mismatch` unless it is the agent's |
| 4 | `agent.sql` `ClaimAgentTask` | `a.runtime_id = atq.runtime_id` |
| 5 | **`handler/daemon.go:2267`** | **after dispatch, before delivery: `agent.RuntimeID != task.RuntimeID` → fail the task** |

Fence 5 is not a claim fence. The first routed task claimed correctly, was
dispatched to the right machine, and then died with
`invalid_task_identity`: *"The agent moved to another runtime before this task
could start."* Which is a perfectly true sentence in a model where a task's
runtime can only ever be a stale copy of its agent's. Nothing is wrong with
that check — it is right about the world it was written for.

**This is the finding a code read could not produce.** Four of the five say
"the agent owns the machine" as an authorization rule. The fifth says it as a
*definition of task identity*: a task whose runtime differs from its agent's is
not a routed task, it is a corrupted one.

### Where it was genuinely forced, not just tedious

- **Fence 3 costs an extra query.** It runs before any task row is read, so
  there is no policy to consult at that point. `HasRoutedTaskForAgentAndRuntime`
  exists only to let it stand down. That is ordering, not feature cost.
- **Fence 5 was skipped, and skipping lost something real.** That check also
  catches an owner rebind mid-flight; a routed task no longer gets that. The
  honest fix is not a condition — it is for the rebind guard to compare against
  what the task *asked for* rather than against the agent, which changes what
  "task identity" means.
- **Nothing was hardcoded past the model.** Migration 251's CHECK is untouched.

### The wall, stated exactly

```sql
CHECK (runtime_id IS NOT NULL OR completed_at IS NOT NULL)   -- migration 251
```

Migration 251 says, in its own words, that "an ACTIVE task must always have a
runtime, so claim / dispatch / delivery-CAS paths can never observe
runtime_id IS NULL". NULL is confined to history, deliberately.

So a policy task must still name a machine at INSERT. Two consequences:

1. **"Resolved at dispatch" can only ever be a RE-resolution.** The stamped
   runtime is provisional and the claim overwrites it, but a first resolution
   with no machine named is not expressible.
2. **A task waiting for a machine that has never registered is not
   representable.** `ResolveRoutingPolicyRuntime` returns no row and the enqueue
   is refused. This is the one place the spike deliberately stops rather than
   tunnelling: removing that CHECK is not a change to routing, it is a change to
   what an active task is.

### And the waiting fell out for free

Nothing was written for it. An offline runtime does not satisfy
`r.status = 'online'`, the row stays `queued`, and it runs when the machine
returns — the behaviour already measured in the baseline, now also true across
a policy. The only thing that does not fall out is waiting for a machine that
does not exist yet, which is the CHECK again.

---

---

## Spike 2 — the raised hand. It works, and it resisted almost nothing.

`raised_hand` is a table; `multica hand raise | list | answer` is the surface.
An agent raised one from inside a real run, the unit parked, a human picked an
option, the unit came back, and the agent continued **in the same session**.

```
task     | status    | session
01a0a1ad | completed | 31749f1c-936     <- raised the hand, stopped
01a0a1b4 | completed | 31749f1c-936     <- resumed after the answer
```

Same session id. That continuity was not written: the re-triggered task is the
same `(agent_id, issue_id)` pair, so `GetLastTaskSession` resumed the agent's
conversation on its own. The agent's own words on resume: *"The hand came back
as option b — build a PlaceCard component in the design system — and that is
the recorded decision."*

### What makes it an object rather than a comment

The schema refuses a hand that is not answerable:

- **two to six options**, checked in SQL and in the handler — "a raised hand
  with one option is not a question";
- **every option needs a `cost`** — what it costs to be *wrong on that side*,
  not what the option does. This is the field that lets someone decide in
  thirty seconds without knowing the domain, and the API rejects a hand without
  it;
- **a recommendation must be one of the option keys** — a raiser that stops
  without a pick has handed over its judgement along with the decision;
- **one open hand per issue**, a partial unique index. A second is 409;
- **answering cannot pick outside the set**. If none of the options is right,
  the question was wrong, and the repair is a new hand.

All five verified against the running instance.

### Two things it did NOT need to add, and that is the result

- **No new status.** `backlog` already parks. Answering moves the issue to
  `todo` and the product's own `WillEnqueueRun` re-triggers the agent.
- **No resume mechanism.** Session resume already existed.

So the raised hand is roughly 90% existing machinery with an object in front of
it. **The gap was never mechanism — it was that nothing could be *applied*.**
A comment holds the question, the answer and the record all at once, so nothing
can act on it; somebody reads it, decides, and then separately remembers to
un-park the work. Splitting the decision (the object) from its delivery (a
comment the object writes) is the entire change.

### Where it did resist

**One parking status, and it is hardwired to one transition.** `blocked` is the
semantically correct place to park, and it cannot be used:
`service/issue_trigger.go` only re-enqueues on a status change whose PREVIOUS
status was `backlog`. Park in `blocked` and the answer strands the unit — it
moves to `todo` and nothing picks it up.

So "park and resume" is not a capability in this model. It is one hardcoded
edge, `backlog → anything`, and anything that wants to park has to borrow it.
The cost is conflating "never started" with "stopped mid-flight for a
decision"; only the raised-hand object tells those apart.

**The answer comment is best-effort, and that is a real hole.** The decision is
on the hand, but the agent reads comments. If the comment write fails, the
promotion still fires and a run resumes with no answer in its feed. Correct
would be one transaction over answer + comment + promotion. Left as is, and
written down rather than papered over.

### The gap that matters most: there is no recipient

Every hand here goes to the human. The design note is emphatic that this is
backwards — *"L'humain est le destinataire le plus rare — la plupart des mains
levées se referment contre un référentiel ou chez le lead"* — and that the two
need separate counters, because the second is what measures autonomy.

That is not a missing column. A recipient needs somewhere for the hand to go
that is not a person: a lead who can contest it first, and a referential the
answer might already be in. Multica has squads with a leader, so the lead half
has somewhere to land. The referential half has nothing to land on — there is
no object in the product that a question can be checked against before a human
sees it. **That is the deeper divergence of the two, and it is not about
routing at all.**

---

---

# What the spike answered

The question was not whether these could be built. It was whether two models of
the world fit. Here is the answer, in the order the evidence arrived.

## 1. The premise about waiting was wrong, and usefully so

A queued task whose runtime is offline **waits**, and resumes by itself. Measured
before anything was changed. `runtime_offline` as a *failure* is a different
moment — a task already dispatched when its machine drops.

So the gap was never "it fails instead of waiting". It is that **a task cannot
name a machine**, so it can only ever wait for the one its agent is bound to.
Narrower than expected, and harder.

## 2. Routing fit. The divergence is reducible — to a column and five edits.

An agent bound to Opencode ran its task on Claude and replied. Nothing else in
the product noticed.

What it cost: one column on `agent_task_queue`, one on `issue`, and five places
that each assert the agent owns the machine. Four were findable by reading.
**The fifth was not**, and it is the one that says something:
`handler/daemon.go:2267` runs *after* dispatch and fails the task with *"The
agent moved to another runtime before this task could start."* That is not an
authorization fence. It is a definition: a task whose runtime differs from its
agent's is not a routed task, it is a corrupted one.

That is the shape of the whole divergence. Multica is not *preventing*
task-level routing. It has no concept of it, so a task whose runtime differs
from its agent's can only be read as damage.

## 3. The one thing that did not fit, exactly located

```sql
CHECK (runtime_id IS NOT NULL OR completed_at IS NOT NULL)   -- migration 251
```

An active task must name a machine. So:

- dispatch-time resolution is always a RE-resolution, never a first one;
- **a task waiting for a machine that has never registered cannot be written at
  all.**

The second is where the two models genuinely part. In the cycle, a unit carries
its own delegation boundary and can be written before anyone knows who executes
it. Here, work cannot exist without an executor already registered. That is not
one more field. Removing that CHECK is not a change to routing — it changes what
an active task *is*, and three subsystems (claim, dispatch, delivery-CAS) are
written against it holding.

**Verdict on the reducibility question: reducible for routing, structural for
unassigned work.** And the structural half is not where the spike expected to
find it.

## 4. The raised hand fit almost completely — which is its own answer

Roughly 90% existing machinery. `backlog` already parks. `WillEnqueueRun`
already re-triggers. `GetLastTaskSession` already resumes — four runs on one
issue, all on session `31749f1c`, across a CLI answer and a UI answer.

So: **does having it as an object change how it feels to be interrupted, or is a
comment convention enough?**

A comment convention is not enough, and the reason is narrower than "structure
is nice". A comment is the question, the answer and the record at the same time,
so **nothing can be applied to it**. Someone reads it, decides, and then
separately remembers to un-park the work. Splitting the decision (the object)
from its delivery (a comment the object writes) is the entire change, and it is
what makes one click do three things.

The part that actually changes the feel is one field: **the cost of being wrong
on each side**, required per option. It is what lets the decision be made in
thirty seconds by someone who does not know the domain. Without it the object is
a poll and you are back to needing the raiser's knowledge.

## 5. Where the models part on the raised hand, and it is not routing

**There is no recipient.** Every hand goes to the human. The design note says
this is exactly backwards — most hands should close against a referential or at
the lead, and only the residue reaches a person, which is the number that
measures autonomy.

Adding a recipient is not a column. It needs two things to send a hand *to*:

- a **lead** who contests it first. Multica has squads with a leader, so this
  half has somewhere to land.
- a **referential** the question might already be answered by. **Multica has no
  such object at all.** Nothing in the product can be checked against before a
  human is disturbed.

That is the deeper divergence of the two, and it has nothing to do with
machines. The cycle's referential — the thing T1 grills against and T5 writes
back into — has no counterpart here. Which also means Multica cannot express T5:
there is nowhere for what a run learned to go except the ticket it learned it
on.

## 6. The product pushed back correctly, once

`TestWorkspaceDeletionManifestCoversPublicSchema` failed on the first full test
run: `unclassified=[raised_hand]`. Multica requires every new table to carry an
explicit workspace-teardown decision — delete, detach, keep, settle — before CI
passes. Adding a table without saying what happens to it when a workspace is
deleted is not possible here.

Worth recording because it is the opposite of the five fences. Those were the
model refusing something it had no concept of. This is a gate doing exactly its
job, catching a spike's new table on the first run and asking one question.

**6738 tests pass**, that classification being the only change any of them
needed.

## 7. One smaller finding worth keeping

**Multica has exactly one parking status and it is hardwired to one transition.**
`blocked` exists and is semantically right, but only a change whose *previous*
status was `backlog` re-enqueues a run. So anything that wants to park and
resume must borrow `backlog`, conflating "never started" with "stopped
mid-flight for a decision". Park-and-resume is an edge, not a capability.

# Part two — the mission view, and what it dragged in

Everything above was answered by 2026-09-14. What follows was a second brief: a
read-only view answering the one question the board cannot — where is a mission,
and what is waiting on me. It resisted more than the first two, and most of what
it taught came from being looked at rather than reasoned about.

## 8. The view was built twice, because the first shape was wrong

A scrolling document with the waiting list first. Defensible on paper and wrong
in the hand: "where is this" is a shape, and a list answers it one row at a
time. Rebuilt as a canvas (`@xyflow/react`, the React port of what n8n uses),
pan and zoom, one column per depth. The layout is four lines, not a dependency —
it is a tree, not a graph, so `dagre` would have bought nothing.

Three defects only looking found. React Flow needs a node's height before it
renders, so a node sized to its own title pushes its text out of its box. The
minimap reads MEASURED sizes, so a custom node draws an empty grey square until
the sizes are declared. And the page did not scroll at all — `SidebarInset` is
`h-svh overflow-hidden`, so a page carrying no scroll container of its own is
cut at the fold. None of these were reachable by reading the code.

## 9. The vocabulary, and what it is actually backed by

Campaign → Mission → Objective → Task → Step, stored on `issue.level`.

The words are borrowed from military usage, where each is precise about what a
thing obliges. **The ordering is not doctrinal** — doctrine does not rank them;
a squad has a mission, an objective is assigned at any echelon, and its
hierarchy is units, not work items. Claiming otherwise was overreach, corrected
in a6805ec1a.

Checked boundary by boundary against the server:

| boundary | backed by |
|---|---|
| project / campaign | the product's own entity, a flat per-issue tag |
| campaign / mission | shape only — a campaign has no parent to wake |
| mission / objective | **nothing.** No code keys on depth |
| objective / task | **nothing**, likewise |
| task / step | real — a step is folded, below the reporting line |

So `objective` is a WRITING DISCIPLINE, not a rule the server keeps: a title at
depth 2 must answer "what does this promise, and can you tell whether you hold
it?". It earns its place by catching a real defect — "Empty state" sitting where
an objective belongs — not by being enforced.

The same argument cuts both ways and that is why it is worth writing down: the
reason a `subtask` rung was rejected ("nothing changes below depth 2") rejects
`objective` exactly as hard. `step` survives only because the view itself folds
it.

## 10. A derived rung cannot express an orphan — the reason `level` is stored

An objective triggered on its own has no parent, so depth says 0, so it IS a
campaign. The thing you are looking for and the thing it is mistaken for are
byte-identical, and no filter separates them.

Storing the rung makes the orphan a DISAGREEMENT: declared against parentage.
NULL means undeclared and falls back to depth, so nothing existing changes
meaning, and a unit only becomes capable of being an orphan once somebody states
what it was meant to be. Reparenting deliberately does not rewrite a declared
level — healing it silently is how the signal is lost.

An orphan is **not an error**. It is work that started before the thing above it
existed, which is most of how work actually starts. Nothing in the UI paints it
red.

## 11. The second wall: a wait cannot cross a parent

The stage barrier orders SIBLINGS under one parent. It cannot say "this task
waits on a task in another mission, owned by another squad" — the ordinary case
the moment two teams share a release.

`issue_dependency` is the product's own table for exactly that, present since
migration 034 with a blocks/blocked_by/related type, and **dead**: no query, no
API, no client type, no UI, no rows. The only code touching it was the
workspace-deletion manifest.

Unlike migration 251's CHECK, this was not a wall — it was an empty room. It now
has a reader, a writer and a CLI. The two kinds of wait are kept apart
everywhere because they do not clear the same way: a barrier clears when the
stage below closes, a dependency clears when one named thing is finished,
possibly by people who have never heard of this mission.

## 12. Per-rung run settings cost one field, and no daemon change

An agent carries one model, one thinking level and one service tier. "The same
agent, cheap here and expensive there" therefore means COPYING the agent — and a
copied persona splits its raised hands, its contest ratio and its autonomy
numbers across the copies.

Traced the dispatch path expecting a wall and did not find one. The payload has
carried `Model`, `ThinkingLevel` and `ServiceTier` on every claim all along;
the daemon already qualifies the model per provider, validates the tier against
that model's catalog, and degrades rather than failing. The only thing that was
not per-task was where the value came from.

So `level_policy` is a table and three lines at the claim's issue load. No
protocol change, no daemon change, no client upgrade. This is the clearest
"reducible" answer in the whole spike, and it is the opposite of finding 3.

**And it may be the wrong idea.** Measured on ~22 runs: tokens per run did not
vary by rung — 126k, 122k, 117k across campaign, mission and objective. The rung
is a claim about scope and blast radius, not a measurement of difficulty. It was
built anyway, as a deliberate bet, because there is no real data yet. The first
thing to check once there is: whether it held. The alternative needing no
prediction is reactive — run cheap, escalate a notch on failure, raise a hand on
the second.

## 13. The house gates are good, and caught six things

Not a courtesy. In order: the workspace-deletion manifest caught `raised_hand`,
then `referential`, then `level_policy`. The route registry made a nav page
without an icon a compile error. The palette's keyword `Record` made a page
without search terms one. Locale parity made an English-only label one.
`IssueToMap` and the CLI's `validIssueFields` both caught `level` the moment it
reached the wire. Every one was right and every one was fixed rather than
excluded.

Two conventions the repo warned about in comments I was editing next to, and I
got wrong anyway on the first pass: Base UI items take `onClick`, not
`onSelect` — an `onSelect` typechecks and lands silently on the DOM node. And
`invalidateQueries` needs `issueKeys.all(wsId)`; a hand-written `["issue"]`
matches nothing, which is how a write reached the database while the panel kept
reading "Undeclared".

## What was NOT done

- **No UI for `level_policy`.** CLI and API only. The rung settings are the one
  part of the ladder a human cannot set from the product.
- **No cycle check on dependencies**, deliberately: two units each waiting on
  the other is a real thing a team does to itself, and it is better seen than
  refused.
- **Silent tier degradation.** The daemon drops an unsupported tier and logs it.
  Per-agent that is fine; per-rung, "I asked for high and got standard" happens
  inside a run and nothing surfaces it. Surfacing it IS a protocol change.
- **Routing and model untested together.** Route a task to `provider:opencode`
  while naming a Claude model and `qualifyTaskModel` has to reconcile them. The
  spike has never sent both.
- **`TestCommentSourceContextLifecycle` fails**, on this branch and on a clean
  `origin/main` against a clean database built from main's own migrations. The
  test harness passes a nil `storage.Storage` and the source-context capture
  copies attachments through it; the test has a skip guard for "no database" and
  none for "no storage". Not a Multica defect — an environment dependency this
  stack does not provide.
- No recipient, so no lead-first routing and no two counters.
- The answer comment is best-effort. If it fails the promotion still fires and a
  run resumes with the decision missing from its feed. Correct is one
  transaction over answer + comment + promotion.
- `material` is free text; the design note hangs an image off each option.
- Routing has no UI — API and CLI only.
- No `provider:` fallback ordering, no cost/latency-aware policy. Deliberate:
  two forms were the minimum to prove dispatch-time resolution differs from an
  enqueue-time copy.

## Status

- [x] fork, clone, upstream remote
- [x] running locally (Docker, on the localenv stack)
- [x] baseline measured: queued work already waits
- [x] routing spike — works; five fences; one wall left standing
- [x] raised hand spike — works end to end from CLI and UI
- [x] 6738 tests passing
- [x] report
- [x] mission view — canvas, panel, per-unit drawer
- [x] the referential, made real (catalog, not a stand-in)
- [x] the lead as a recipient; contest ratio per lead, and on exposed metrics
- [x] the leveled board, and orphans that can be found
- [x] a wait that crosses a tree — issue_dependency brought to life
- [x] what a rung runs on — level_policy, no daemon change

Whether any of this is worth proposing upstream is a decision for later and was
not part of this session.
