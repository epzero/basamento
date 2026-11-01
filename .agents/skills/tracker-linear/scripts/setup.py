#!/usr/bin/env python3
"""Make a Linear team match docs/engineering/tracker.json.

Adds the workflow states, the `ask` label group and the other labels that are
missing from one team. It never renames, changes or deletes anything, so it
is safe to run again and on a team that is in use.

    LINEAR_API_KEY=lin_api_… python3 setup.py --team BAS            # print the plan
    LINEAR_API_KEY=lin_api_… python3 setup.py --team BAS --apply    # carry it out
    LINEAR_API_KEY=lin_api_… python3 setup.py --team BAS --check    # exit 1 if anything is missing

The key is a personal API key of a workspace admin (Linear: Settings →
Security & access → Personal API keys). Standard library only.
"""

import argparse
import json
import os
import sys
import urllib.error
import urllib.request
from pathlib import Path

API_URL = os.environ.get("LINEAR_API_URL", "https://api.linear.app/graphql")
DEFINITION = Path(__file__).resolve().parents[4] / "docs" / "engineering" / "tracker.json"

# Linear wants a color for every state; the definition only says what the
# state is for.
STATE_COLORS = {
    "backlog": "#bec2c8",
    "unstarted": "#e2e2e2",
    "started": "#f2c94c",
    "completed": "#5e6ad2",
    "canceled": "#95a2b3",
}

TEAM_QUERY = """
query($key: String!) {
  teams(filter: {key: {eq: $key}}) {
    nodes { id key name states(first: 100) { nodes { id name type } } }
  }
}"""

# Team labels and workspace labels: a workspace label with the same name
# already serves every team, and Linear refuses a second one.
LABELS_QUERY = """
query($team: ID!, $after: String) {
  issueLabels(first: 100, after: $after,
              filter: {or: [{team: {id: {eq: $team}}}, {team: {null: true}}]}) {
    nodes { id name isGroup parent { id } }
    pageInfo { hasNextPage endCursor }
  }
}"""

STATE_CREATE = """
mutation($input: WorkflowStateCreateInput!) {
  workflowStateCreate(input: $input) { success workflowState { id } }
}"""

LABEL_CREATE = """
mutation($input: IssueLabelCreateInput!) {
  issueLabelCreate(input: $input) { success issueLabel { id } }
}"""


def graphql(key, query, variables):
    body = json.dumps({"query": query, "variables": variables}).encode()
    request = urllib.request.Request(
        API_URL, data=body,
        headers={"Content-Type": "application/json", "Authorization": key})
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            payload = json.load(response)
    except urllib.error.HTTPError as err:
        sys.exit(f"Linear answered {err.code}: {err.read().decode(errors='replace')[:500]}")
    except urllib.error.URLError as err:
        sys.exit(f"cannot reach {API_URL}: {err.reason}")
    if payload.get("errors"):
        sys.exit("Linear refused: " + "; ".join(e.get("message", "?") for e in payload["errors"]))
    return payload["data"]


def fetch(key, team_key):
    teams = graphql(key, TEAM_QUERY, {"key": team_key})["teams"]["nodes"]
    if not teams:
        sys.exit(f"no team with key {team_key!r} is visible to this API key")
    team = teams[0]
    labels, after = [], None
    while True:
        page = graphql(key, LABELS_QUERY, {"team": team["id"], "after": after})["issueLabels"]
        labels += page["nodes"]
        if not page["pageInfo"]["hasNextPage"]:
            return team, labels
        after = page["pageInfo"]["endCursor"]


def plan(definition, team, labels):
    """What is missing, as a list of steps, and what exists but differs."""
    steps, notes = [], []

    states = {s["name"].lower(): s for s in team["states"]["nodes"]}
    for want in definition["states"]:
        have = states.get(want["name"].lower())
        if have is None:
            steps.append({"do": "state", "name": want["name"], "type": want["type"],
                          "description": want["means"]})
        elif have["type"] != want["type"]:
            notes.append(f"state {want['name']!r} is of type {have['type']}, "
                         f"expected {want['type']}: change it in Linear's team settings")

    top = {l["name"].lower(): l for l in labels if l["parent"] is None}
    for want in definition["labels"]:
        if want["name"].lower() not in top:
            steps.append({"do": "label", "name": want["name"], "color": want["color"],
                          "description": want["description"], "group": None})

    for group in definition["label_groups"]:
        have = top.get(group["name"].lower())
        if have is None:
            steps.append({"do": "group", "name": group["name"], "color": group["color"],
                          "description": group["description"]})
            children = set()
        else:
            if not have["isGroup"]:
                notes.append(f"label {group['name']!r} exists but is not a group: "
                             "turn it into a group in Linear, or rename it, then run again")
                continue
            children = {l["name"].lower() for l in labels
                        if l["parent"] and l["parent"]["id"] == have["id"]}
        for want in group["labels"]:
            if want["name"].lower() not in children:
                steps.append({"do": "label", "name": want["name"], "color": group["color"],
                              "description": want["description"], "group": group["name"]})
    return steps, notes


def describe(step):
    if step["do"] == "state":
        return f"state        {step['name']} ({step['type']})"
    if step["do"] == "group":
        return f"label group  {step['name']}"
    where = f"{step['group']} → " if step["group"] else ""
    return f"label        {where}{step['name']}"


def apply(key, team, labels, steps):
    groups = {l["name"].lower(): l["id"] for l in labels if l["parent"] is None}
    for step in steps:
        if step["do"] == "state":
            graphql(key, STATE_CREATE, {"input": {
                "teamId": team["id"], "name": step["name"], "type": step["type"],
                "color": STATE_COLORS[step["type"]], "description": step["description"]}})
        else:
            fields = {"teamId": team["id"], "name": step["name"], "color": step["color"],
                      "description": step["description"]}
            if step["do"] == "group":
                fields["isGroup"] = True
            elif step["group"]:
                fields["parentId"] = groups[step["group"].lower()]
            created = graphql(key, LABEL_CREATE, {"input": fields})["issueLabelCreate"]
            if step["do"] == "group":
                groups[step["name"].lower()] = created["issueLabel"]["id"]
        print("created  " + describe(step))


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--team", required=True, help="the team's key, such as BAS")
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--apply", action="store_true", help="create what is missing")
    mode.add_argument("--check", action="store_true", help="exit 1 if anything is missing or differs")
    args = parser.parse_args()

    key = os.environ.get("LINEAR_API_KEY")
    if not key:
        sys.exit("set LINEAR_API_KEY to a personal API key of a workspace admin")

    definition = json.loads(DEFINITION.read_text())
    team, labels = fetch(key, args.team)
    steps, notes = plan(definition, team, labels)

    print(f"team {team['key']} ({team['name']}): {len(steps)} to create, {len(notes)} to fix by hand")
    for note in notes:
        print("by hand  " + note)
    if args.apply:
        apply(key, team, labels, steps)
    else:
        for step in steps:
            print("missing  " + describe(step))
        if steps and not args.check:
            print("run again with --apply to create them")
    if notes or (args.check and steps):
        sys.exit(1)


if __name__ == "__main__":
    main()
