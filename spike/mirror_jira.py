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

Jira is read through `acli`, the Atlassian CLI, which is already authenticated
on this machine over OAuth. That is the whole reason it is used rather than the
REST API: there is no token to mint, none to store, and none for this script to
hold. `acli jira auth status` says whether it will work.

`acli` decides what this can do, and it cannot write a work item without being
told to, so the one-way property is the tool's rather than a promise in a
comment.

Idempotent. Run it twice and the second run changes nothing.
"""

import argparse
import json
import os
import ssl
import subprocess
import sys
import time
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
    """Read-only, through `acli`. Two searches, plus one view per child issue.

    The shape is dictated by two limits of the CLI, not chosen:

      `search --fields` REJECTS `parent`, so a parent link cannot come from the
      bulk read. `view --fields '*all'` returns it, so the children get a call
      each — 36 of 183 here, rather than 183 of 183.

      A `view` per issue is what broke the first full run: 183 sequential calls
      and one came back "failed to fetch work item details" at number 109, which
      reads fine on its own. That was the API under load, not a bad issue, and
      the cost of finding out was the whole run. Hence both the smaller call
      count and the retry below.
    """

    def __init__(self, project):
        self.project = project

    @staticmethod
    def _run(args, attempts=2):
        last = ""
        for n in range(attempts):
            out = subprocess.run(
                ["acli", "jira", "workitem", *args],
                capture_output=True, text=True,
            )
            if out.returncode == 0:
                return json.loads(out.stdout)
            last = out.stderr.strip()[:200]
            if n + 1 < attempts:
                time.sleep(2)
        raise RuntimeError(f"acli {' '.join(args)}: {last}")

    def bulk(self):
        """Every issue, with everything the bulk endpoint will give."""
        return self._run([
            "search", "--jql", f"project = {self.project} ORDER BY created ASC",
            "--paginate", "--json",
            "--fields", "key,summary,status,priority,description",
        ])

    def parent_links(self, log):
        """child key -> parent key, for the issues that have one.

        A read that fails here costs one link, not the run: the issue still
        mirrors, it just arrives without its parent, and the failure is named
        so the next run can pick it up.
        """
        children = self._run([
            "search", "--jql", f"project = {self.project} AND parent is not EMPTY",
            "--paginate", "--json",
        ])
        links, failed = {}, []
        for row in children:
            key = row["key"]
            try:
                fields = self._run(["view", key, "--fields", "parent", "--json"])["fields"]
            except RuntimeError as e:
                failed.append(f"{key}: {e}")
                continue
            parent = (fields.get("parent") or {}).get("key")
            if parent:
                links[key] = parent
        for f in failed:
            log(f"  parent unread {f}")
        return links


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
    # A smart link carries no text node — only a URL in its attributes. Left
    # unhandled it renders as nothing, and a sentence naming two issues comes
    # out naming neither. Measured on GALACTEAM-184, whose first paragraph lost
    # both keys it cited.
    if kind in ("inlineCard", "blockCard", "embedCard"):
        url = node.get("attrs", {}).get("url", "")
        tail = url.rstrip("/").rsplit("/", 1)[-1]
        return tail or url
    if kind == "mention":
        return node.get("attrs", {}).get("text", "")
    if kind == "emoji":
        return node.get("attrs", {}).get("text", "")
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
    ap.add_argument("--jira-project", default="GALACTEAM")
    ap.add_argument("--api", default=os.environ.get("MULTICA_API_URL", "https://api.multica.localhost"))
    ap.add_argument("--token", default=os.environ.get("MULTICA_TOKEN", ""))
    ap.add_argument("--workspace", default=os.environ.get("MULTICA_WORKSPACE_ID", ""))
    ap.add_argument("--project", default="Galactic Team", help="Multica project to mirror into")
    ap.add_argument("--state", default=str(Path(__file__).with_name("jira_mirror_state.json")))
    ap.add_argument("--check", action="store_true", help="report what would change, write nothing")
    args = ap.parse_args()

    if not args.token or not args.workspace:
        ap.error("--token and --workspace are required (or MULTICA_TOKEN / MULTICA_WORKSPACE_ID)")

    log = lambda m: print(m, flush=True)
    jira = Jira(args.jira_project)
    mc = Multica(args.api, args.token, args.workspace)
    state = State(args.state)

    issues = jira.bulk()
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

    # Pass 2 — parent links, now that every key that will exist does. The links
    # are fetched here rather than with the bulk read because `search --fields`
    # refuses `parent`; see Jira.parent_links.
    linked = 0
    if not args.check:
        log("Reading parent links")
        for child, parent in jira.parent_links(log).items():
            child_id, parent_id = state.map.get(child), state.map.get(parent)
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
