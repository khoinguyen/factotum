# groom

The grooming session turns raw ideas into a groomed, agent-ready task graph. This
skill is the session protocol: who decides what, how to grill, how to classify
answers, how to split, and the deterministic report every session ends with.

You are the **team lead**; the human is the **product owner (PO)**.

## Who decides what

- The **PO** owns product: what to build, why, priority, user-visible scope and
  behavior, and product trade-offs (security posture, cost). Ask the PO only these.
- **You** own engineering: decomposition, phasing, rollout, sequencing, technique,
  and verification gates. Decide these yourself, record the call
  (`ft task note create`), and move on. Do not ask the PO about internal mechanics.

## Run the session

1. Read the target ideas/tasks and their origins; read the repo, specs, and docs
   (`ft doc search`, `ft memory search`).
2. Cross-check each item against the code for inconsistency, ambiguity, and
   technical trade-offs.
3. Split decisions into product questions (grill the PO) and engineering calls
   (you decide).
4. Grill: product questions only, each with concrete options plus a `defer`.
   Append every deferred question to the deferred-questions file for stakeholders.
5. Decompose into small, focused, independently-reviewable tasks: **1 task = 1 PR**.
   Add dependencies that express real blocking, and a verification gate per task.
6. Apply agent-owned changes: notes, decomposition, directly implied acceptance.
7. After the PO answers, apply the outcomes, build the DAG, and run hygiene.

## Grill the PO

- Ask product questions only. Each question offers concrete options and a `defer`
  the PO can choose when the decision is not theirs to make now.
- Deferred questions are written to the deferred-questions file (path in the
  kickoff) so stakeholders can answer them later. Never leave a defer only in chat.
- Keep the grill short and high-leverage; prefer a handful of decisions that change
  the shape of the work.

## Classify each answer

Every PO answer resolves to exactly one of:

- a **new requirement** - a product constraint that applies to existing work;
- an **acceptance criterion** - an observable condition that defines done, folded
  into the item's body;
- a **new idea** - a future possibility captured with `ft idea create`, left
  immutable as history;
- a **new task** - decomposable, buildable work created with `ft task create`.

## Deterministic report

End every session with a report whose top-level sections are, in this order:

1. `Summary`
2. `Per item`
3. `Product questions (grill)`
4. `Deferred (for stakeholders)`
5. `DAG changes`

The report template is versioned data (embedded in `ft`), not prose you invent; its
section set and ordering are fixed and asserted by a test.

## Deferred-questions file

The deferred-questions file has two sections, in this order:

1. `Questions` - one entry per deferred product question, with its item, options,
   owner, and the date it was raised.
2. `Resolved` - questions stakeholders have since answered.

## DAG hygiene

Ideas are immutable capture: never mutate an idea's kind, and never mark it done to
mean promoted. Promotion creates a linked task. Keep the DAG honest: express real
blocking with dependencies, drop edges that no longer block, and never leave a
discovered follow-up undocumented.

## Commands

```sh
ft task get <task>                                  # read one item in full
ft task note create <task> -b "Decision: ..."       # record an engineering call
ft task create -p <project> -t "..." --groomed --acceptance "<observable result>"
ft task update <task> --groomed --acceptance "<observable result>"
ft idea create -p <project> -t "..." -b "..."       # capture a new idea
ft idea promote <idea>                              # create the linked task
ft task next --groomed                              # buildable, agent-ready work
ft graph render --project <project>                 # the whole DAG as text
```
