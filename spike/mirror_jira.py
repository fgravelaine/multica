#!/usr/bin/env python3
"""Mirror a Jira project into a Multica workspace. One way, Jira wins.

SPIKE (not upstream). The ticket system stays the source of truth; this
workspace is a projection of it. Nothing here ever writes to Jira, and a
divergence is resolved by re-running, never by editing the mirror.

Three choices, and each one is here because the obvious alternative was worse:

  identity      A Jira key is not a UUID, and `origin_type` on the issue API
                accepts one value — `quick_create`. Using the provenance field
                would mean widening a product column and its allowlist so a
                spike could hold a foreign key in it. The mapping is the
                MIRROR's bookkeeping, so it lives in the mirror: a state file
                beside this script. The product gains no Jira-shaped field.

  no level      A mirrored issue declares NO level, so no gate fires on it.
                This is not a bypass. A gate asks who may release a rung; an
                issue whose ratification already happened in another system
                does not sit on a rung here. The model gives this for free —
                a rung that is not declared has no gates — so the mirror needs
                no exception written for it.

  runs off      Every write passes `suppress_run`. A mirrored status change
                replays a decision; it must not start an agent. Without this,
                importing sixty issues in `in progress` would enqueue sixty
                runs against a backlog nobody asked to execute.

Credentials are read from the environment and never written down, the way
`install-plugin.py` resolves its token rather than committing one:

    export JIRA_EMAIL=you@example.com
    export JIRA_API_TOKEN=...      # id.atlassian.com -> API tokens

Idempotent. Run it twice and the second run changes nothing.
"""

import argparse
import base64
import json
import os
import ssl
import sys
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

# Jira status name (lowercased) -> Multica status key. The eight states in
# `vocabulary/ticket-vocabulary.md`, and nothing else: an unmapped status is
# reported rather than guessed, because guessing puts an issue in a state its
# own project never gave it.
STATUS_MAP = {
    "backlog": "backlog",
    "needs discussion": "needs_discussion",
    "ready": "todo",
    "in progress": "in_progress",
    "in review": "in_review",
    "qa": "qa",
    "done": "done",
    "blocked": "blocked",
}

PRIORITY_MAP = {
    "highest": "urgent",
    "high": "high",
    "medium": "medium",
    "low": "low",
    "lowest": "low",
}


def insecure_ctx():
    """The local stack serves a self-signed cert; this only talks to it."""
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    return ctx


class Jira:
    """Read-only. Every method here is a GET, and that is the contract."""

    def __init__(self, site, email, token):
        self.site = site.rstrip("/")
        raw = f"{email}:{token}".encode()
        self.auth = base64.b64encode(raw).decode()

    def get(self, path, params=None):
        url = f"{self.site}{path}"
        if params:
            url = f"{url}?{urllib.parse.urlencode(params)}"
        req = urllib.request.Request(url, method="GET")
        req.add_header("Authorization", f"Basic {self.auth}")
        req.add_header("Accept", "application/json")
        try:
            with urllib.request.urlopen(req) as r:
                return json.loads(r.read())
        except urllib.error.HTTPError as e:
            body = e.read().decode()[:300]
            raise RuntimeError(f"GET {path} -> {e.code}: {body}") from None

    def issues(self, project):
        """Every issue in the project, oldest first, paged.

        `/search/jql` is the endpoint that survived the 2025 deprecation of
        `/search`; it pages by token rather than by offset.
        """
        fields = "summary,description,status,priority,parent,issuetype"
        token, out = None, []
        while True:
            params = {
                "jql": f"project = {project} ORDER BY created ASC",
                "fields": fields,
                "maxResults": 100,
            }
            if token:
                params["nextPageToken"] = token
            page = self.get("/rest/api/3/search/jql", params)
            out.extend(page.get("issues", []))
            token = page.get("nextPageToken")
            if not token or page.get("isLast"):
                break
        return out


def adf_to_markdown(node, depth=0):
    """Flatten Atlassian Document Format into the Markdown the fork parses.

    This exists for one reason: `parseCriteriaFromDescription` reads an
    `## Acceptance Criteria` section and keeps `- [ ]` lines. A description
    that arrives as a JSON tree carries the same criteria and the parser
    cannot see them, so the gate that reads verdicts would have nothing to
    rule on. Only the node types Jira emits from typed text are handled;
    anything else falls through to its text content rather than being dropped,
    because a silently missing paragraph is worse than an unstyled one.
    """
    if node is None:
        return ""
    kind = node.get("type")
    children = node.get("content") or []

    def inner(sep=""):
        return sep.join(adf_to_markdown(c, depth) for c in children)

    if kind == "text":
        return node.get("text", "")
    if kind == "hardBreak":
        return "\n"
    if kind == "paragraph":
        return inner() + "\n"
    if kind == "heading":
        level = node.get("attrs", {}).get("level", 1)
        return "#" * level + " " + inner() + "\n"
    if kind in ("bulletList", "orderedList", "taskList"):
        return "".join(adf_to_markdown(c, depth) for c in children)
    if kind in ("listItem", "taskItem"):
        body = "".join(adf_to_markdown(c, depth + 1) for c in children).strip()
        mark = ""
        if kind == "taskItem":
            mark = "[x] " if node.get("attrs", {}).get("state") == "DONE" else "[ ] "
        pad = "  " * depth
        return f"{pad}- {mark}{body}\n"
    if kind == "codeBlock":
        return "```\n" + inner() + "\n```\n"
    if kind == "blockquote":
        return "> " + inner().strip() + "\n"
    if kind == "rule":
        return "---\n"
    if kind == "doc":
        return inner()
    return inner()


class Multica:
    def __init__(self, base, token, workspace):
        self.base, self.token, self.workspace = base.rstrip("/"), token, workspace

    def call(self, method, path, body=None):
        sep = "&" if "?" in path else "?"
        url = f"{self.base}{path}{sep}workspace_id={self.workspace}"
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(url, data=data, method=method)
        req.add_header("Authorization", f"Bearer {self.token}")
        req.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(req, context=insecure_ctx()) as r:
                raw = r.read()
                return json.loads(raw) if raw else {}
        except urllib.error.HTTPError as e:
            body = e.read().decode()[:400]
            raise RuntimeError(f"{method} {path} -> {e.code}: {body}") from None

    @staticmethod
    def rows(payload, key):
        return payload if isinstance(payload, list) else payload.get(key, [])

    def project(self, name, log):
        for p in self.rows(self.call("GET", "/api/projects"), "projects"):
            if (p.get("title") or "").lower() == name.lower():
                return p
        created = self.call("POST", "/api/projects", {"title": name})
        log(f"  project {name} created")
        return created


class State:
    """Jira key -> Multica issue id, and nothing else.

    Losing this file does not lose the mirror: the next run re-creates the
    issues, which is why `--check` exists and why the file is reported rather
    than assumed. It is bookkeeping, not truth.
    """

    def __init__(self, path):
        self.path = Path(path)
        self.map = {}
        if self.path.exists():
            self.map = json.loads(self.path.read_text()).get("issues", {})

    def save(self):
        self.path.write_text(json.dumps({"issues": self.map}, indent=2, sort_keys=True) + "\n")


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--site", default=os.environ.get("JIRA_SITE", "https://frankoa.atlassian.net"))
    ap.add_argument("--jira-project", default="GALACTEAM")
    ap.add_argument("--api", default=os.environ.get("MULTICA_API_URL", "https://api.multica.localhost"))
    ap.add_argument("--token", default=os.environ.get("MULTICA_TOKEN", ""))
    ap.add_argument("--workspace", default=os.environ.get("MULTICA_WORKSPACE_ID", ""))
    ap.add_argument("--project", default="Galactic Team", help="Multica project to mirror into")
    ap.add_argument("--state", default=str(Path(__file__).with_name("jira_mirror_state.json")))
    ap.add_argument("--check", action="store_true", help="report what would change, write nothing")
    args = ap.parse_args()

    email = os.environ.get("JIRA_EMAIL", "")
    api_token = os.environ.get("JIRA_API_TOKEN", "")
    if not email or not api_token:
        ap.error("JIRA_EMAIL and JIRA_API_TOKEN must be set — this script never stores them")
    if not args.token or not args.workspace:
        ap.error("--token and --workspace are required (or MULTICA_TOKEN / MULTICA_WORKSPACE_ID)")

    log = lambda m: print(m, flush=True)
    jira = Jira(args.site, email, api_token)
    mc = Multica(args.api, args.token, args.workspace)
    state = State(args.state)

    issues = jira.issues(args.jira_project)
    log(f"Jira {args.jira_project}: {len(issues)} issues")

    project = mc.project(args.project, log) if not args.check else {"id": None}
    created = updated = skipped = 0
    unmapped = []

    # Pass 1 — the issues themselves. Parents are linked in pass 2, because a
    # child can be older than its parent and a forward reference would fail.
    for iss in issues:
        key = iss["key"]
        f = iss["fields"]
        jira_status = (f.get("status") or {}).get("name", "").strip().lower()
        status = STATUS_MAP.get(jira_status)
        if not status:
            unmapped.append(f"{key}: {jira_status!r}")
            skipped += 1
            continue

        title = f.get("summary", "").strip()
        description = adf_to_markdown(f.get("description")).strip()
        # The key is written into the body, not the title: the title is the
        # issue's own sentence and stays readable, while the body keeps the
        # pointer back to the system that owns the row.
        body = f"*Mirrored from {key} — Jira is the source of truth.*\n\n{description}"
        priority = PRIORITY_MAP.get((f.get("priority") or {}).get("name", "").lower(), "medium")

        existing = state.map.get(key)
        if args.check:
            log(f"  {key:16} {'update' if existing else 'create':6} {status:16} {title[:50]}")
            continue

        if existing:
            mc.call("PUT", f"/api/issues/{existing}", {
                "title": title, "description": body,
                "status": status, "priority": priority,
                "suppress_run": True,
            })
            updated += 1
        else:
            # Created in `backlog` and moved after, so the create path can
            # never enqueue a run for an issue that arrives mid-flight.
            new = mc.call("POST", "/api/issues", {
                "title": title, "description": body,
                "status": "backlog", "priority": priority,
                "project_id": project["id"],
            })
            state.map[key] = new["id"]
            state.save()
            if status != "backlog":
                mc.call("PUT", f"/api/issues/{new['id']}", {
                    "status": status, "suppress_run": True,
                })
            created += 1

    # Pass 2 — parent links, now that every key that will exist does.
    linked = 0
    if not args.check:
        for iss in issues:
            parent = (iss["fields"].get("parent") or {}).get("key")
            child_id = state.map.get(iss["key"])
            parent_id = state.map.get(parent) if parent else None
            if child_id and parent_id:
                mc.call("PUT", f"/api/issues/{child_id}", {
                    "parent_issue_id": parent_id, "suppress_run": True,
                })
                linked += 1
        state.save()

    log(f"created {created}, updated {updated}, linked {linked}, skipped {skipped}")
    if unmapped:
        log("Unmapped Jira statuses — add them to STATUS_MAP rather than guessing:")
        for u in unmapped:
            log(f"  {u}")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
