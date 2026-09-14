# Spike notes — task-level routing and the raised hand

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

## Status

- [x] fork, clone, upstream remote
- [x] running locally (Docker, on the localenv stack)
- [x] baseline measured: queued work already waits
- [x] routing spike — works; five fences; one wall left standing
- [x] raised hand spike — works; borrowed one hardcoded edge; no recipient
