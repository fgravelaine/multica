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

## Status

- [x] fork, clone, upstream remote
- [x] running locally (Docker, on the localenv stack)
- [x] baseline measured: queued work already waits
- [ ] routing spike
- [ ] raised hand spike
