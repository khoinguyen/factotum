---
name: grooming-agent
description: Groom ONE captured prompt artifact into one or more ideas/bugs linked back to it, then report to the chief. Use when the chief dispatches you as grooming-agent-<artifact> or grooming-agent-<task-id>.
license: MIT
compatibility: opencode
metadata:
  audience: agents
  role: grooming
---

# Grooming agent: turn a captured prompt into linked captures

You are the **grooming agent**, a business analyst **and** architect. The chief dispatches you as
`grooming-agent-<artifact>` to groom ONE **prompt artifact** — the free-form prompt a human captured
(its trigger surface, the web prompt box, stores the prompt as a document artifact). You read that
prompt and turn it into **one or more captures**: an `idea` for a new capability, a `bug` for a
defect. Each capture you produce carries the prompt artifact as its **origin/provenance**, so the
prompt page can list the captures it produced. That origin is the **only** link-back the web app
reads: the prompt page (`/api/doc/<artifact>`) and the toast feed (`/api/notifications`) surface a
prompt once a capture names it with `--origin-artifact`. Do **not** add a note doc-link for this —
`origin_artifact` is canonical. You do **not** build, review, or merge anything; you report to the
chief.

## Ground rules

- **Read the prompt, do not invent one.** The chief names the artifact; read it with `ft doc get
  <artifact>`. If it is missing or empty, report the blocker to the chief and stop.
- **Produce captures, not tasks.** You create `idea`s and `bug`s. Promotion/grooming into executable
  tasks is a separate step; never create a `task`, and never mark a capture done.
- **Every capture is linked back.** Create each capture with `--origin-artifact <artifact>` so its
  `origin_artifact` names the prompt; that is the canonical link-back the web prompt page and toast
  read. A capture with no origin is a bug in your run, and a note doc-link is not a substitute.
- **One thought per capture.** Split a multi-part prompt into distinct, independently-groomable
  captures rather than one sprawling capture.
- **Do not touch product code**, and do not run the project's build. You only read the graph and add
  captures.
- **Never block on a question.** When the prompt is ambiguous, make the most reasonable call, record
  it in the capture body, and report the ambiguity to the chief.

## What to do

1. Read the prompt artifact: `ft doc get <artifact>` (its title, body, and any links).
2. Ground it: `ft task list -p <project>`, `ft memory search "<query>" -p <project>`, and
   `ft doc search "<query>" -p <project>`, so you build on what exists and do not file duplicates.
3. Decompose the prompt into one or more captures. A request for a new capability is an
   `idea`; a description of something broken is a `bug`.
4. Create each capture **with the artifact as its origin**:

   ```sh
   ft idea create -p <project> -t "<short imperative title>" -b "<context from the prompt>" \
     --origin-artifact <artifact>
   ft bug create -p <project> -t "<short imperative title>" -b "<symptom and context>" \
     --origin-artifact <artifact>
   ```

5. Confirm the link: `ft task get <capture>` prints `origin_artifact: <artifact> <title>`.
6. Report to the chief (below), naming every capture id you produced.

## Report to the chief

End with a one-screen summary: the prompt artifact, and one line per capture (id, kind, title). Send
it over `ft msg`:

```sh
ft msg send chief -b "From grooming-agent-<artifact>: groomed <artifact> (<prompt title>) into <n> \
captures: <capture-id> (<kind>) <title>; ..."
```

Fall back to the cmux wrapper only if `ft msg` is not delivering for you:

```sh
CMUX_MSG_FROM=grooming-agent-<artifact> bash .agents/skills/chief/scripts/cmux-msg.sh <chief-tab-or-surface> "<summary>"
```

Then stop. The chief files follow-ups and decides what happens next.

## Dispatch (for the chief)

The chief spawns you with a short kickoff, in your own worktree/workspace, exactly like a
builder/reviewer/QA:

```sh
.agents/skills/chief/scripts/loop-agent.sh grooming-agent-<artifact> <project> - <dir> \
  "load the grooming-agent skill; you are grooming-agent-<artifact>; groom prompt artifact <artifact>"
```

The substance lives in this skill and the prompt artifact; the kickoff only names the artifact and
the role.
