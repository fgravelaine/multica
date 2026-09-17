#!/usr/bin/env python3
"""Seed a Multica workspace with the Galactic team, so Veezeet can run on it.

SPIKE (not upstream). This reads galactic-team and writes Multica. It never
writes back: the operating model is the source, the workspace is the projection.

What it seeds, and why each one is here rather than done by hand:

  statuses      `qa` and `needs discussion`. Galactics' flow has eight states;
                Multica ships seven and two of them are not the same seven.
                `todo` is NOT seeded — it is already Galactics' `ready` under a
                different label, renamed in the locale files rather than
                duplicated as a custom status.

  project       The product line. Veezeet is a PROJECT inside the team's
                workspace, not a workspace of its own — `agent`, `squad`,
                `skill`, `referential` and `issue_status` are all
                workspace-scoped, so one workspace per product would mean one
                copy of the whole team per product.

  agents        One per file in agents/. The persona body becomes the agent's
                instructions verbatim. Nothing is summarised: a persona that is
                paraphrased on the way in is a different agent.

  squads        Parsed from each persona's own `**Squad:**` line, so the roster
                cannot drift from the files that declare it.

  gates         `qa` ratified by AP-5, `in_review` by a named reviewer. This is
                the one part that encodes a rule rather than copying a fact:
                "qa is yours, and only yours" becomes something the product
                refuses rather than something an agent remembers.

Idempotent. Run it twice and the second run changes nothing.
"""

import argparse
import json
import os
import re
import sys
import urllib.error
import urllib.request

# Rungs that get gates. Only `objective` and below: a campaign is ratified by a
# human at the merge, and there is no mechanical check above the ticket — which
# is levels.md's own "there is no T4 above the ticket", not a shortcut here.
GATED_RUNGS = ["objective", "task"]

STATUSES = [
    # (name, category). `qa` sits after in_review and before done.
    ("QA", "started"),
    ("Needs discussion", "unstarted"),
]


class Api:
    def __init__(self, base, token, workspace):
        self.base, self.token, self.workspace = base.rstrip("/"), token, workspace

    @staticmethod
    def rows(payload, key):
        """Some endpoints wrap their list, some return a bare array.

        `/api/agents` and `/api/squads` return a JSON array; `/api/projects` and
        `/api/issue-statuses` wrap it under a key. Normalising here keeps the
        seeding functions from each carrying their own guess.
        """
        if isinstance(payload, list):
            return payload
        return payload.get(key, [])

    def call(self, method, path, body=None):
        sep = "&" if "?" in path else "?"
        url = f"{self.base}{path}{sep}workspace_id={self.workspace}"
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(url, data=data, method=method)
        req.add_header("Authorization", f"Bearer {self.token}")
        req.add_header("Content-Type", "application/json")
        # The local stack serves a self-signed cert; this script only ever talks
        # to it. It is not a client anyone should point at a real deployment.
        import ssl
        ctx = ssl.create_default_context()
        ctx.check_hostname = False
        ctx.verify_mode = ssl.CERT_NONE
        try:
            with urllib.request.urlopen(req, context=ctx) as r:
                raw = r.read()
                return json.loads(raw) if raw else {}
        except urllib.error.HTTPError as e:
            detail = e.read().decode()[:400]
            raise RuntimeError(f"{method} {path} -> {e.code}: {detail}") from None


def parse_persona(path):
    """Pull name, tagline and squad out of one agent file.

    The NAME is the part of the H1 before the em dash: "AP-5 — QA / Acceptance"
    is the agent AP-5 doing QA, not an agent called "AP-5 — QA / Acceptance".
    """
    text = path.read_text()
    tagline = ""
    m = re.search(r"^tagline:\s*(.+)$", text, re.M)
    if m:
        tagline = m.group(1).strip()

    m = re.search(r"^# (.+)$", text, re.M)
    if not m:
        return None
    heading = m.group(1).strip()
    name = re.split(r"\s+[—–-]\s+", heading, maxsplit=1)[0].strip()

    squad = None
    leads = False
    m = re.search(r"^\*\*Squad:\*\*\s*(.+)$", text, re.M)
    if m:
        line = m.group(1).strip()
        if not line.lower().startswith("none"):
            squad = re.split(r"\s+—\s+", line, maxsplit=1)[0].strip()
            leads = "you lead it" in line.lower()

    # The instructions are the file from the H1 down, frontmatter stripped. The
    # frontmatter is Galactics' own bookkeeping (id, born-of, accent_color) and
    # means nothing to a runtime.
    body = text.split("---", 2)[-1].strip() if text.startswith("---") else text
    return {
        "name": name,
        "heading": heading,
        "tagline": tagline,
        "squad": squad,
        "leads": leads,
        "instructions": body,
    }


def seed_statuses(api, log):
    existing = {s["key"]: s for s in Api.rows(api.call("GET", "/api/issue-statuses"), "statuses")}
    for name, category in STATUSES:
        key = name.lower().replace(" ", "_")
        if key in existing:
            log(f"  status {key:18} already there")
            continue
        created = api.call("POST", "/api/issue-statuses", {
            "name": name, "category": category, "color": "#7d8a91",
        })
        log(f"  status {created['key']:18} created")
        existing[created["key"]] = created
    return existing


def seed_project(api, name, log):
    for p in Api.rows(api.call("GET", "/api/projects"), "projects"):
        # Multica calls a project's name its `title`.
        if (p.get("title") or "").lower() == name.lower():
            log(f"  project {name} already there")
            return p
    created = api.call("POST", "/api/projects", {"title": name})
    log(f"  project {name} created")
    return created


def seed_agents(api, personas, runtime_id, log):
    existing = {a["name"]: a for a in Api.rows(api.call("GET", "/api/agents"), "agents")}
    out = {}
    for p in personas:
        if p["name"] in existing:
            agent = existing[p["name"]]
            # Update rather than skip: the persona file is the source, so a
            # changed file has to reach the workspace. Skipping would let the
            # two drift silently, which is the whole failure this script exists
            # to prevent.
            api.call("PUT", f"/api/agents/{agent['id']}", {
                "description": p["tagline"], "instructions": p["instructions"],
            })
            log(f"  agent  {p['name']:18} updated")
        else:
            agent = api.call("POST", "/api/agents", {
                "name": p["name"],
                "description": p["tagline"],
                "instructions": p["instructions"],
                "runtime_id": runtime_id,
            })
            log(f"  agent  {p['name']:18} created")
        out[p["name"]] = agent
    return out


def seed_squads(api, personas, agents, log):
    wanted = {}
    for p in personas:
        if not p["squad"]:
            continue
        squad = wanted.setdefault(p["squad"], {"leader": None, "members": []})
        if p["leads"]:
            squad["leader"] = p["name"]
        else:
            squad["members"].append(p["name"])

    existing = {s["name"]: s for s in Api.rows(api.call("GET", "/api/squads"), "squads")}
    for name, spec in wanted.items():
        if not spec["leader"]:
            # A squad with no declared leader is a parsing failure, not a valid
            # roster: every squad in the files names one. Refuse rather than
            # create a headless squad that routes nowhere.
            log(f"  squad  {name:18} SKIPPED — no leader declared in any persona")
            continue
        leader_id = agents[spec["leader"]]["id"]
        member_ids = [agents[m]["id"] for m in spec["members"] if m in agents]
        if name in existing:
            api.call("PUT", f"/api/squads/{existing[name]['id']}", {
                "leader_id": leader_id, "member_ids": member_ids,
            })
            log(f"  squad  {name:18} updated  ({spec['leader']} leads {len(member_ids)})")
        else:
            api.call("POST", "/api/squads", {
                "name": name, "leader_id": leader_id, "member_ids": member_ids,
            })
            log(f"  squad  {name:18} created  ({spec['leader']} leads {len(member_ids)})")


def seed_gates(api, statuses, agents, log):
    """qa is AP-5's, and in_review is a reviewer's.

    This is the only part that turns a written rule into a refusal. AP-5's file
    says "`qa` is yours, and only yours. No other agent moves an issue into or
    out of that state" — until now that was a sentence an agent had to remember.
    """
    ap5 = agents.get("AP-5")
    qa_key = next((k for k in statuses if k.startswith("qa")), None)
    if not qa_key or not ap5:
        log("  gates  SKIPPED — need both a qa status and AP-5")
        return
    for rung in GATED_RUNGS:
        api.call("PUT", f"/api/level-gates/{rung}", {
            "position": 1, "status_key": qa_key,
            "ratifier_type": "agent", "ratifier_id": ap5["id"],
        })
        api.call("PUT", f"/api/level-gates/{rung}", {
            "position": 2, "status_key": "in_review", "ratifier_type": "human",
        })
        log(f"  gates  {rung:18} qa→AP-5, in_review→human")


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--galactic", default=os.path.expanduser(
        "~/Projects/localenv/projects/galactic-team"), help="path to the galactic-team checkout")
    ap.add_argument("--api", default=os.environ.get("MULTICA_API_URL", "https://api.multica.localhost"))
    ap.add_argument("--token", default=os.environ.get("MULTICA_TOKEN", ""))
    ap.add_argument("--workspace", default=os.environ.get("MULTICA_WORKSPACE_ID", ""))
    ap.add_argument("--runtime-id", required=True, help="runtime the agents run on")
    ap.add_argument("--project", default="Veezeet")
    args = ap.parse_args()

    if not args.token or not args.workspace:
        ap.error("--token and --workspace are required (or MULTICA_TOKEN / MULTICA_WORKSPACE_ID)")

    from pathlib import Path
    agents_dir = Path(args.galactic) / "agents"
    if not agents_dir.is_dir():
        ap.error(f"no agents/ under {args.galactic}")

    personas = []
    for path in sorted(agents_dir.glob("*.md")):
        p = parse_persona(path)
        if p:
            personas.append(p)
        else:
            print(f"  skipped {path.name}: no H1 heading", file=sys.stderr)

    api = Api(args.api, args.token, args.workspace)
    log = lambda m: print(m, flush=True)

    log(f"Seeding {len(personas)} personas from {agents_dir}")
    statuses = seed_statuses(api, log)
    seed_project(api, args.project, log)
    agents = seed_agents(api, personas, args.runtime_id, log)
    seed_squads(api, personas, agents, log)
    seed_gates(api, statuses, agents, log)
    log("Done.")


if __name__ == "__main__":
    main()
