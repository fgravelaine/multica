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

# Part three — the collision

The spike designed a vocabulary and a raised hand next to a model that was
already written. `operating-model/cycle/levels.md` and `.../raised-hand.md` are
`status: accepted` in Galactics and were not read until after all of the above
was built. This section is what reading them cost.

## 14. Galactics' worked example is the one this spike rebuilt

Its ladder, in its own words:

| | the work | unit produced |
|---|---|---|
| N0 · direction | make the planner the core of the product | missions |
| N1 · mission | **the trip planner** | sub-missions |
| N2 · sub-mission | **build the day-by-day itinerary** | features |
| N3 · feature | **compose a day from the chosen places** | tickets |
| N4 · ticket | **the daily timeline component** | split, NOT delegated |

Those are SPIK-7, SPIK-10 and SPIK-13. The spike reconstructed Galactics' own
example from a mission description and never noticed where it came from.

## 15. Two five-level models, and one word at two ranks

| the spike | Galactics |
|---|---|
| Campaign | N0 · direction |
| Mission | N1 · mission |
| Objective | N2 · sub-mission |
| Task | N3 · feature |
| Step | N4 · ticket |

`campaign` is the collision. In Galactics it is **N2 of the content lane** — a
sub-level under a sub-mission. The spike put it at the top. One word, two ranks,
inside one system.

**And the shapes differ, not just the labels.** A Galactics level declares five
parameters and "a project that declares four is not running the cycle": the
reference it argues against, the unit produced, the gate that closes verify, who
ratifies, and the delegation boundary. `issue.level` plus `level_policy`
declares one — what the rung runs on.

Where the tree runs out, checked field by field:

| Galactics parameter | Multica holds it? |
|---|---|
| the reference | **yes** — `referential`, arrived at independently |
| the unit produced | implicit: the child rung |
| the gate that closes verify | **nothing** |
| who ratifies | **nothing.** Assignee is who does it, not who accepts it |
| the delegation boundary | partly — `step` folds, which is N4's "split, not delegated" |

Two of five have nowhere to live. That is the answer to "can Multica carry
Galactics' levels": it can carry their ORDER and their names, and it cannot
carry what makes them levels.

## 16. The raised hand: three of four, and the missing one was recorded

Galactics names four things a tool must carry, "both the minimum and the
maximum":

1. **a state that remembers where it came from** — WAS MISSING. This spike
   parked to a constant (`backlog`) and resumed to a constant (`todo`), so a
   unit that stopped at `in_review` came back as `todo`: work waiting on a
   reviewer silently became work waiting to be picked up. Migration 486 records
   `status_before`; `in_review → backlog → in_review` now round-trips.
2. a recipient — built.
3. a field the payload fits in — built, with the costs.
4. a resume — built.

## 17. The third wall, retracted — and the smaller one behind it

**This section claimed a wall that does not exist. Retracted, with the reason,
because the wrong version was load-bearing for two later sections.**

What it said: Multica re-fires a run on exactly one transition, `backlog →
todo`, so restoring the previous state and resuming the run are the same write
and cannot both be had.

What `WillEnqueueRun` actually does (`service/issue_trigger.go:122`):

```go
case in.StatusChanged && prevStatus == "backlog" &&
     currentStatus != "backlog" &&
     currentStatus != "done" && currentStatus != "cancelled":
```

**`backlog → anything active`, not `backlog → todo`.** The comment two lines
above it even says so: *"Only the fixed backlog key parks work. Leaving it for
another unstarted or started status can enqueue a run."* I read the constant and
not the predicate.

So the resume works exactly as Galactics asks. Parked from `in_review`, the unit
comes back to `in_review`; parked from `in_progress`, back to `in_progress`.
Measured: hand raised on an issue sitting in `in_review` with an agent
assignee, answered, issue returned to `in_review` **and** the queue went 0 → 1.

### The smaller wall behind it

The retraction exposes a real one, and it is the opposite shape.

The resume **always** re-fires. There is no way to say "restore this state and
do NOT re-dispatch". A hand raised while the unit was at `in_review` was waiting
on a REVIEWER — and answering it re-runs the agent, which is not the beat it
stopped at. The old section had this case backwards: it claimed the product
fires no run there and called that correct. It fires one.

That is cheaper to fix than the wall I invented — one recorded bit on the hand,
`resume_dispatches`, set from whether the parked state was an agent's or a
reviewer's. Not built.

**Why the error is worth keeping on the page.** Both wrong claims came from
reading `const resumeStatus = issuestatus.Todo` and generalising from it,
instead of reading the predicate that consumes it. The constant is a FALLBACK
for hands raised before `status_before` existed. It was never the rule.

## 18. The trigger set is closed, and this spike has no trigger at all

Galactics names three, and says the set is closed: a `block` found at framing;
three failures in a row; a contradiction discovered in flight.

`raised_hand` has no trigger column. Any agent may raise one for any reason. And
Galactics states the exact consequence:

> an open trigger set makes the raised hand the default exit, and then the
> counter measures how tired an agent is rather than where the references are
> thin.

That is a direct hit on the referential diagnostic and the per-lead contest
ratio built here. Both count hands. Without a closed trigger set they count what
that sentence says they count.

**Closed in §21.** `raised_hand.trigger`, required, CHECK in the three.

## 19. `answered_by_level` is the wrong thing under the right name

Galactics says three things come back, in order: the answer; **its level** — a
rule, or a local choice; and the diff that follows. A rule *"goes up into the
reference and the unit carries a pointer to it"*.

This spike's `answered_by_level` records WHO answered — lead or human. Same
word, different concept, and the real one is absent.

**That absence is the largest finding in part three.** The referential
diagnostic measures which body of knowledge is too thin to answer on its own,
and there is no path from an answer back into that body. Hands get counted
forever and no referential ever gets thicker. Galactics' model closes that loop;
this implementation opens it and leaves it open.

**Closed in §21.** `answer_scope`, plus a `referential_entry` table that is where
a rule lands.

## 20. What did converge, without either side knowing

- `referential` is Galactics' **reference**, built independently from a one-line
  brief, down to `unclassified` being a countable key.
- "the unit parks alone, its neighbours carry on" — implemented, and it cost
  nothing because no such coupling existed.
- "it goes to the lead, not to the top", and the lead **contests first** — the
  recipient chain and the contest ratio are that sentence, measured.
- the payload is never an open question; bounded options with the cost of being
  wrong on each side, plus a recommendation — enforced by the CLI, which refuses
  an option without a cost.

Four of Galactics' rules were rebuilt from a product brief by someone who had
not read them. That is worth as much as the collisions: it says the model is
reachable from the problem, not only from the document.

## 21. Both ends of the loop, closed

§18 and §19 are the same finding from two sides. One says the count is polluted
at the input; the other says the count has no output. Each one alone leaves a
number that means nothing, so both were built.

**The trigger.** `raised_hand.trigger`, required, CHECK constrained to `block`,
`three_failures`, `contradiction`. Refused at the CLI before the request leaves,
and again at the handler, because the CLI is not the only client. A refused
trigger writes no row at all — a hand that parks its unit and then fails
validation would be worse than accepting a fourth trigger.

Adding a fourth is now a migration. That is the point: Galactics says the set is
closed and a fourth "is a change to this file, not a judgement call in a
session". A CHECK constraint is what that sentence looks like in a database.

**The scope.** `raised_hand.answer_scope`, `rule` or `local`, defaulting to
`local`. Defaulting the other way would put every off-hand decision into the
body of knowledge, which is how a reference becomes something nobody trusts.

A `rule` needs a statement, and an empty one is a 400. A rule with nothing
written down is a local choice wearing a label, and it would raise the one
number this exists to expose.

**Where a rule lands.** New table `referential_entry` — workspace, referential
key, statement, and the hand that produced it. Three decisions in it:

- `referential_key` is TEXT, not an FK. Same reason `raised_hand` does not key
  it either: an entry must outlive the catalog row it names.
- `source_hand_id` is `ON DELETE SET NULL`, not CASCADE. A rule outlives the
  question that produced it. A reference that empties itself when old hands are
  cleaned up never accumulates anything.
- the write is **best-effort**, logged at `slog.Error` on failure and never
  rolled back onto the answer. A bookkeeping write must not strand a unit that
  is trying to resume. This is a deliberate correctness trade and it is the
  weakest line in §21 — the right shape is one transaction over answer,
  comment, entry and promotion, which §"What was NOT done" already names for the
  comment.

**The readout.** `GET /api/referentials/loop`, and `multica referential loop`:

```
REFERENTIAL        HANDS  RULES  LOCAL  OPEN
design_system      8      1      0      0
architecture       2      0      0      0
product_direction  2      0      0      0
api_contract       2      0      1      0
```

RULES is the column that was missing. Hands alone cannot distinguish a reference
that is steadily learning from one being asked the same kind of question over
and over — both render as a large number. `design_system` at eight hands and one
rule is the first thing in this spike that says which one it is.

Six of those eight hands predate the column and are neither `rule` nor `local`.
They are left NULL rather than backfilled to a default. Guessing the scope of an
answer nobody declared would put a made-up number in the one column built to
stop made-up numbers.

**Measured end to end, twice.** A hand with no trigger is refused; `tired` is
refused; each of the three is accepted and recorded. An answer with `--rule`
writes the statement and the count moves 8/0 → 8/1. An answer without it moves
LOCAL and writes nothing up. Both units came back to `todo` — the state they
were in, which is §"migration 486" doing its job.

## 22. The gate, and the near miss that was already there

I said nothing in Multica had a gate's shape. That was too strong, and the
correction is the finding.

**Two things already had most of it.** `in_review` is a pause the whole product
respects — agents park finished work there, the notification listeners treat it
as the dominant "this needs you now", the runtime sweeper deliberately refuses to
reset it. And `raised_hand` is a working gate already: it parks a unit, names a
recipient, and the unit cannot resume until someone answers.

What was actually missing is narrower:

| | `in_review` | `raised_hand` | `level_gate` |
|---|---|---|---|
| parks the unit | yes | yes | yes |
| names who decides | no | yes | yes |
| declared per rung | no | no | yes |
| runs a check | no | no | **no** |

**Nothing validates a transition anywhere in Multica.** `resolveIssueStatusKey`
checks that a status key is in the workspace catalog and is not archived, and
that is the whole of it. `in_review` → `done` is unguarded, by anyone. So
`in_review` is a place that holds a unit, not a gate that can refuse to release
one.

### Ordered, and why not parallel

Two reviewers on one unit either queue or both hold it. Galactics already runs
the queued form — its own state flow is `in review → qa → done`, where "in review
puts the change proposal in front of a reviewer, qa puts the acceptance criteria
in front of AP-5". Parallel k-of-n ratification has no worked example on either
side, and a tally nobody has exercised is a guess about how a team reviews.

**And levels.md is already plural without saying so.** It gives a level ONE gate
cell and ONE ratifier cell, but its own table breaks that: N3's gate reads "AP-5
against the criteria, `gate-pr`" — two checks — and N4's ratifier reads "the
gate, then the merge" — a sequence. Multiple gates are not a deviation from
Galactics. They are its undeclared practice.

### The four rules

1. **No skipping forward.** Gate k is reachable only from gate k-1, and a `done`
   status only from the last gate. Without this the gates are decoration.
2. **Only the ratifier releases a gate forward.** This is "who accepts the
   return", and it is what makes rule 1 mean anything — an ordered path anyone
   may walk is a longer path, not a gate.
3. **Backwards is always open.** The rule most likely to be argued with.
   Galactics has AP-5 move a failing issue out to `in_progress`, which reads as
   "rejection is the ratifier's too". Not enforced, because the cost is
   asymmetric: a privileged rejection means a unit sits until exactly one actor
   appears, with no escape and no way for the author to withdraw. A redo is
   cheap; being unable to move at all is not.
4. **A closed status is always reachable.** Cancelling is never gated. `done` is
   NOT exempt — done is the thing being ratified.

`human` tests the KIND of actor, `agent` tests IDENTITY. Any-agent would let the
agent that did the work accept its own return.

### `lead` is deliberately absent

`raised_hand` resolves a lead relative to the agent that raised the hand. A gate
has no raiser. Making `lead` mean "the assignee's lead" is a different
definition, and inventing one to fill a column is how a vocabulary drifts. One
CHECK change away when somebody says what it should mean.

### A defect this found in what was already built

The mission view keyed `waitingReview` on the literal `in_review`. **A custom
status does not inherit review behaviour** — `customBehavior` maps only the
terminal categories and returns the key itself for the rest, so
`Effective("qa")` is `"qa"`, which matches neither `Blocked` nor `InReview`. A
unit parked at a custom `qa` fell through to the default and rendered as
**waiting on nobody**. Galactics' own flow is `in review → qa → done`, so that
is not a hypothetical workspace. The board now reads the declared gate statuses.

That inheritance rule is deliberate upstream, not an oversight: *"Custom statuses
inherit only terminal lifecycle semantics, not parked, review, blocked or
active-agent recovery behavior."* A custom status may not silently acquire
`in_review`'s meaning. The consequence is that **a review gate has to be declared
to be seen** — which is what `level_gate` now is.

### Measured end to end

Seven transitions against the live stack, on an `objective` with `qa` at gate 1
and `in_review` at gate 2:

```
todo       -> done        REFUSED  has not passed qa
todo       -> in_review   REFUSED  gates qa before in_review
todo       -> qa          ok       entering gate 1
qa         -> done        REFUSED  has not passed in_review
qa         -> in_progress ok       backwards is open
qa         -> in_review   ok
in_review  -> done        ok       last gate passed
```

Plus: an agent-ratified gate refusing a member, an ungated rung completely
inert, and cancelling out of a gate mid-walk.

**One thing to be honest about.** My first attempt at the agent-ratifier check
looked like a bug — a member released an agent-ratified gate. It was a bad test:
the issue I used had a parent and no declared rung, so its depth put it on a
different rung with no gates at all. The code was right. Worth recording because
the failure mode is the one this whole table invites — a gate that looks declared
and is silently inert on the unit in front of you.

**The sub-case that did not run.** "A different agent cannot release the gate" is
written and skips: the test workspace has one agent. The member-vs-agent and
agent-vs-human-gate cases both ran.

## 23. The scripted gate, and a wall that was not one

I wrote in §22's proposal that a scripted gate *"needs a runner. Does not
exist"*, and filed it beside the protocol-change wall. **That was wrong, and it
was the cheapest thing in the spike.**

Multica needs no runner. GitHub is the runner, and Multica already ingests the
results: it handles `check_run`, `check_suite` and `status` webhooks and writes
one row per check into `github_pull_request_check_run` with its name, status and
conclusion, plus a rollup on the PR. `github.go:268` renders that rollup to
clients today. **Every one of Galactics' nine scripts already lands in this
database. Nothing was allowed to refuse on it.**

### What it covers

| gate-pr mechanism | count | expressible before | now |
|---|---|---|---|
| `script` | 9 | no | yes |
| `CI` | 2 | no | yes |
| `agent review` | 2 | yes | yes |

2 of 13 → 13 of 13.

### The head_sha join is the load-bearing line

Check runs accumulate per commit. Joining them to the PR without matching
`head_sha` would let a green run from three commits ago release a gate on a head
nothing has checked — the exact failure a scripted gate exists to prevent, and
silent. Measured: two green checks, `head_sha` moved, the gate refuses with *"has
not reported on PR #412's current head"*; CI re-runs on the new head and it
opens.

### Vacuous truth is the trap

*"Every required check succeeded"* is **true over an empty set**. An issue with
no change proposal attached would pass a scripted gate on a naive reading, while
the gate still reads as configured. That is a distinct refusal, and it is the
first test in the file.

### Fail closed, against this file's own habit

Everywhere else in `level_gate.go` a failed lookup widens what is allowed —
refusing because a COUNT failed would look like a rejected review. The scripted
gate does the opposite: an unreadable check table refuses. The whole value of a
gate nobody is asked is that it cannot be talked past, and "the database was
briefly unavailable" is not evidence that nine scripts passed.

Same reason check names are free text and not validated against a catalog: a
check is whatever a workflow calls its job and can be renamed upstream without
warning, so a gate naming a check that never reports must fail closed. A typo is
then a gate that never opens, which is loud. Validating against checks seen so
far would refuse a correct name the first time it is used.

### Measured

```
qa gated on composition-guard + frontmatter-lint, in_review behind it

no PR attached          REFUSED  "no change proposal is attached"
one check in_progress   REFUSED  "frontmatter-lint ... still running on PR #412"
that check fails        REFUSED  "frontmatter-lint ... concluded failure"
both green              ok
new commit pushed       REFUSED  "has not reported on PR #412's current head"
CI green on new head    ok
```

**One honest note on a failure I caused.** A webhook dedupe test failed in the
full package run while my hand-made PR fixture was still sitting in the shared
database. I had inserted it for the CLI walk-through and not removed it.
Deleting it made the package green. My leftover row, not the gate.

## 24. T4 exists

§22 built what CLOSES the verify beat and not the beat itself. `qa` was a place
a unit waited rather than work anyone did: the gate could answer *"AP-5 said
yes"* and could not answer *"what did AP-5 check, and what did it find"*.

**Nothing here is a new process.** Every rule below is already written in
`agents/ap5.md`. What changed is that the product holds them instead of an agent
remembering them.

| AP-5's sentence | was | is now |
|---|---|---|
| "Each acceptance criterion gets its own line and its own verdict" | `- [ ]` in a description | `acceptance_criterion` rows |
| "Evidence or it did not happen" | prose in a comment | `criterion_verdict.evidence`, NOT NULL |
| "No criteria, no verdict ... you do not pass it" | AP-5 reads and decides | the gate refuses |
| "`qa` is yours, and only yours" | convention | §22's ratifier |

### It reads the template you already write

`## Acceptance Criteria` with checkboxes is Galactics' own issue template, and
the checkbox was always a per-criterion object in embryo — nothing read it.
`multica criteria <issue> --from-description` does. It stops at the next
heading, so `## Agent Notes` is not mistaken for criteria, and a ticked `- [x]`
still counts: dropping it would make the imported list shorter than the one in
front of the person importing it.

### Two conditions, asked separately

`requires_verdicts` is its own column rather than a fourth ratifier kind,
because *"did the right party say yes"* and *"does the evidence exist"* are
different questions and a real gate asks both. AP-5 ratifying `qa` with no
verdicts behind it is the exact situation this table exists to end.

### The traps

**Vacuous truth, again.** "Every criterion passed" is TRUE over an empty list,
so an issue nobody wrote criteria for would sail through. It is its own
refusal — and it is also AP-5's own first finding, so the message says the issue
goes back to whoever scoped it.

**"Nobody looked" must never render as "it failed".** sqlc infers non-null from
the LATERAL join and generates a plain `bool`, which both loses that distinction
and fails the scan outright on an unruled criterion. A `CASE` degrades it to
`interface{}`. The fix is to COALESCE and move the two-state answer onto
`ruled_at`, which IS typed nullable — with a comment on both sides saying that
`passed` means nothing without it.

**Evidence is required on a PASS.** A pass nobody can reproduce is the aggregate
"works fine" AP-5's file refuses by name, wearing a per-criterion costume.

**Verdicts accumulate.** A re-check after a fix is a new row. Overwriting would
make "criterion 2 failed twice before passing" unaskable — and that sequence is
the input to raised-hand.md's staffing question, *do the past answers predict
the next ones*.

**Rewriting the criteria drops their verdicts.** A verdict is evidence about one
exact statement; carrying it onto a rewritten one produces exactly the
unreproducible pass AP-5 calls an incomplete verdict.

### Measured, on a Veezeet objective with the real template

```
criteria --from-description     3 criteria, stops at ## Agent Notes
qa -> in_review                 REFUSED  "criterion 1, 2, 3 has no verdict yet"
verdict 1 pass (no --evidence)  REFUSED  at the CLI
1 pass, 2 FAIL, 3 pass          recorded
qa -> in_review                 REFUSED  "criterion 2 failed"
2 pass after the fix            recorded — two rows on criterion 2, not one
qa -> in_review                 REFUSED  "ratified by one named agent"  <- the
                                         verdicts passed; the ratifier is next
an issue with no criteria       REFUSED  "no acceptance criteria"
```

The last-but-one line is the point: both conditions hold independently, and the
gate says which one is in the way.

## 25. The agent is handed its mandate

§24 made the criteria objects and taught the gate to read their verdicts.
Nothing gave them to the agent that has to SATISFY them: it received a
description and was left to find the list inside it. AP-5's file calls the
criteria *"your entire mandate"* — a mandate you have to go looking for is not
one.

Four hops, following the path the status catalog already takes: loaded on claim,
onto the claim payload, across the wire, into the brief. Degrades to no section
rather than failing the claim, because an issue with no criteria is a finding
for whoever verifies it, not a reason to refuse to start the work.

```
## Acceptance Criteria

What this unit is verified against, one verdict each. These are the
mandate; nothing else is.

1. [pass] Returns used, limit and a percentage
   evidence: curl returned all three
2. [FAIL] Free plan gets limit 1000
   evidence: got 500
3. [—]    Unauthenticated gets 401
```

**`—` and not `fail`, and that is the test worth keeping.** `passed` is
coalesced server-side and means nothing without `ruled`; rendering it alone
would mark every criterion nobody has looked at yet as FAILED. That is the most
alarming possible way to say "not checked", and it would send an agent chasing a
bug that is not there.

### Naming the verb where the agent is looking

The verify commands render next to the criteria, and only when there are any:

```
- `multica verdict <id> <criterion> <pass|fail> --evidence "..."` — rule on ONE
  acceptance criterion. One verdict per criterion, never an aggregate; evidence
  is required on a pass as much as on a fail. A comment is not a verdict: a gate
  that reads verdicts cannot read prose.
```

That last sentence is load-bearing. AP-5's own file says *"record the verdict on
the issue"*, and an agent reading that reaches for `issue comment add` — the
verb it already knows. §23's group made `multica verdict` findable; this makes
it the obvious one at the moment it is needed.

**Gated on the criteria existing**, like squad maintenance is gated on leading a
squad. A run with nothing to verify does not carry the surface.

## 26. The ticket, and a column that was never there

The gate refused, the board showed a 409 toast, and that was all anyone could
see. A hard block with no explanation reads as a bug, so the criteria, their
verdicts and the reason a ticket will not move now render on the ticket itself,
and a gated card carries a lock on the board.

Ruling is a form on the criterion: pass or fail, evidence, record. `Record`
stays disabled until BOTH are set — evidence on a pass as much as on a fail,
which is the same rule the API enforces and the same one AP-5's file states.

An unruled criterion renders `—`, never red. `passed` is coalesced server-side,
so painting it from that value alone would mark everything nobody has checked as
failed.

### The bug underneath, which was the real find

The board would never have shown a gate, because **`level` was null in every
list payload** — present as a key, null as a value, on an issue whose row says
`objective`.

Multica has FOUR hand-built issue SELECTs, assembled with `fmt.Sprintf` rather
than generated by sqlc:

| where | used by |
|---|---|
| `issue.go` — ListIssues | the default list |
| `issue.go` — the grouped CTE, twice over (inner and outer) | grouped views |
| `issue.go` — SearchIssues | search |
| `issue_table_rows.go` | **the board** |

Migration 484 added `issue.level`. The sqlc queries picked it up; none of these
four did, because nothing regenerates them. So `level` arrived correctly through
`/api/issues/{id}` and the `open_only` list, and silently as null everywhere
else — including the one path the board actually uses, which turned out not to
be `/api/issues` at all but `POST /api/issues/table/rows`.

**The feature was inert with no error anywhere.** Every existing house gate
passed: `TestIssueToMap_KeysMatchIssueResponse` and the CLI's
`validIssueFields` check that the field EXISTS and is spelled consistently.
Neither checks that a given path populates it.

Two guards now. One drives the real handlers and asserts the value comes back;
one reads the source and refuses a hand-built issue SELECT that does not name
`i.level`, with a count so that a moved query fails loudly instead of quietly
guarding nothing. Both were verified to fail before the fix.

Fixing it broke three tests that mirror these queries — the grouped CTE's outer
projection re-lists its columns, and the search parity test carries its own copy
of the scan. That is the same defect in a third form: a column list written by
hand in four places, and a test that duplicates two of them.

## 27. A real agent, and it refused a human's evidence

Everything up to here was driven from the CLI by a human. This is the first run
by an agent, and it changed two of my conclusions.

### "Not logged in" was not a login problem

`claude auth status` reported `loggedIn: true` the whole time. The daemon had
been running since 15 September with `--no-auto-reload` and held a stale
environment. `multica daemon restart` fixed it. I had told the Grand Master to
go and log in; that was wrong, and it cost a round trip.

Then a second one, mine: the daemon BINARY was from 17 Sep 23:01 and §25's brief
change from 18 Sep 11:06. I had been rebuilding `bin/multica` while the running
process kept the old one. A daemon started with `--no-auto-reload` does not pick
up a rebuild, and nothing says so.

### The brief was the whole difference

Same issue, same agent, same persona. Only the brief changed:

```
without the criteria in the brief
  -> multica issue comment add
  -> "**[AP-5]** Verdict: FAIL (0/3 criteria verified)"
  -> zero rows written

with the criteria in the brief
  -> multica criteria <id> --output json
  -> three verdicts, each with its evidence, author_type=agent
```

The first run is exactly what §25 predicted: AP-5's own file says "record the
verdict on the issue", and an agent reading that reaches for `issue comment add`
because that is the verb it already knows. Making `multica verdict` findable
(§23) was not enough. It had to be named next to the criteria, at the moment
they are in front of the agent.

### It reverted my verdict

While testing the UI I typed a fabricated `curl` output into an evidence field
and passed criterion 1. AP-5 found it:

> No repository, deployment, or PR is attached to the Veezeet project — there is
> no reachable /api/billing/quota to call. The pasted curl output was submitted
> in a comment as a claim, not produced by this run against a real system.
> **Reverting the prior PASS: an unverifiable claim is not a passing verdict.**

An agent refused a human's evidence as unverifiable, and wrote a second verdict
over it. That only works because verdicts ACCUMULATE (§24) rather than
overwrite — an update-in-place would have let my claim stand or silently erased
the disagreement. The design decision was made for a different reason
(keeping "failed twice before passing" answerable) and paid off here.

The gate now reads `qa: criterion 1, 2, 3 failed`.

### What it also showed about rule 3

On the FIRST run — before the brief carried the criteria — AP-5 moved the issue
from `qa` back to `in_progress` with no verdicts recorded, and the gate allowed
it. That is §22's rule 3 working as designed: backwards is always open, because
a privileged rejection strands a unit until exactly one actor appears.

The cost is now visible: **a unit can leave a gate with nothing recorded.** The
rule still looks right to me, and the trade is no longer theoretical.

## What was NOT done

- **No parallel ratification.** Gates queue; they cannot both hold a unit. Two
  people who must each sign off the same piece of content is a real case and it
  has no worked example on either side, so the tally was not guessed at. §22.
- **Nothing reads a gate back into the product's own UI.** The board renders a
  unit parked at a gate (§22 fixed that), but nothing shows WHICH gate, who is
  expected, or how long it has stood there. CLI and API only, like level_policy.
- **The gate refuses on two paths, not every path.** `UpdateIssue` and the batch
  update — the two a human or an agent drives. The system transitions
  (`UpdateIssueStatus`: a hand parking, a PR sync, a task completing, the
  runtime sweeper) are deliberately not gated: those are the machine moving a
  unit, not somebody ratifying it, and gating a raised hand's park would be a
  defect. Whether that split is the right one is untested.
- **Nothing reads the referential back.** A rule lands in `referential_entry` and
  is counted; no agent is handed it when it starts work. The loop is closed for
  measurement, not for use. That is the honest limit of §21.
- **Only ONE agent run, on one issue.** §27 is the whole of it. Nothing here has
  been exercised by a squad, by a leader dispatching to members, by two agents on
  one tree, or by anything with a repository attached.
- **A provider auth failure is diagnosable on one runtime and invisible on
  another.** The same cause — a dead credential — came back from Claude as a
  typed `agent_error.provider_auth_or_access`, a `failed` task and a comment on
  the issue. From Codex it was a task sitting in `running` for twelve minutes
  with zero `task_message` rows, no error, and the queue blocked behind it.
  Confirmed at the source: `codex login status` reports "Logged in using
  ChatGPT" while `codex exec` gets `401 Unauthorized` and cannot refresh its
  token. A provider that cannot authenticate should produce a FAILED task, not a
  suspended one. Not a spike defect, and not fixed here.
- **A rebind strands pending work.** A queued task carries the runtime it was
  enqueued against. Rebinding an agent between runtimes leaves its queued task
  unclaimable with no error and no sweep — the daemon logs "task claim: no tasks
  available" forever. Found by doing something unusual; nothing recovers from it.
  Not a spike defect, and not fixed here.
- **Referentials are read-only.** Six built-ins, seeded lazily, no create
  endpoint. Veezeet's own references — its domain model, its API contract —
  cannot be added. `brand_register` and `product_direction` happen to be there.
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
- [x] read Galactics' own cycle, and recorded where the two models collide
- [x] the trigger set, closed — and the answer scope that gives the count an output
- [x] the gate and its ratifier, per rung — the first refusal in this spike
- [x] the cycle verbs made findable, and `restate` under its own name
- [x] the Galactic team seeded into a workspace, Veezeet beside it
- [x] scripted gates — 13 of gate-pr's 13 checks now expressible
- [x] the verify beat — criteria and verdicts as objects, read by the gate
- [x] the criteria reach the agent that has to satisfy them
- [x] criteria, verdicts and the gate, on the ticket and on the board
- [x] a real agent run — AP-5 wrote three verdicts, and revoked one of mine

Whether any of this is worth proposing upstream is a decision for later and was
not part of this session.
