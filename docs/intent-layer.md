# The Intent Layer

Read this before you create or change an `AGENTS.md` file or a doc in `docs/`.
It is the only place where these rules are written down; when a rule turns out
not to work, change it here first.

The Intent Layer is the set of small `AGENTS.md` files ("nodes") in this
repository. An agent working on a file reads the node of that directory and
every node above it (some tools load them by themselves; the root `AGENTS.md`
tells the rest to), so it gets the context of the part it works on and not the
rest. People read the same files.

The method is inspired by
[The Intent Layer](https://intent-systems.com/blog/intent-layer) and
[Context Is Your Constraint](https://intent-systems.com/blog/context-is-your-constraint),
but not followed to the letter.

**Guidelines, not hard rules.** The numbers and rules below are starting points.
The question that always comes first: what does an agent working here need, and
what does it not need? If another layout, size or form fits a part of the demo
better, choose it and say why in the pull request. Only what must hold
structurally is checked hard, by [`scripts/intent-layer.sh check`](#checks).

## Two goals

1. **Compress**: a node summarises a part of the code in as few tokens as an
   agent needs to work there safely. A node of 10k tokens for 20k tokens of code
   adds weight instead of compressing.
2. **Surface hidden knowledge**: invariants the code does not enforce, why
   something is the way it is, contracts between services, pitfalls people know
   but that are written down nowhere.

## Only AGENTS.md

Nodes are always called `AGENTS.md`. No `CLAUDE.md`, `GEMINI.md` or
`.cursorrules`, no symlinks and no pointer files that load `AGENTS.md`: every
tool the team uses reads `AGENTS.md`, and one file per directory means one place
to update.

## Where a node goes

- **A boundary**, where a different responsibility starts: a service (its own
  `Dockerfile`, `go.mod` or `package.json`), a frontend, `policies/`,
  `fsc-infra/`, `conformance/`.
- **An internal package** gets a node when it has a responsibility of its own
  that an agent needs to know about separately. Size (say over about 20k tokens)
  is a hint, not a requirement.
- **Small directories** without invariants of their own usually get no node;
  their knowledge goes in the node above.
- **Never** a node in `vendor/` (the `vendor/nl-wallet` submodule) or in
  generated directories.
- **One large flat package** (such as `services/eudi-adapter`): one node with a
  section per group of files. Splitting the code is a separate decision.

[`services/AGENTS.md`](../services/AGENTS.md) holds what applies to every
service (ports and adapters); the root [`AGENTS.md`](../AGENTS.md) holds what
every task needs.

## What a node contains

Every heading is optional: a node has only the sections with real content, in
this order. A line earns its place when an agent could not get it quickly from
the code it is about to change, or when it points to a real source (a pull
request, issue, ADR or requirement).

| Heading        | Content                                                                                                                                                                |
| -------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Purpose        | What this part is for and what it explicitly does not do.                                                                                                              |
| Contracts      | Invariants and agreements the code does not enforce or show, often between services.                                                                                   |
| Dependencies   | Callers and callees that are hard to find from here.                                                                                                                   |
| Patterns       | Steps for a recurring change that spans several files.                                                                                                                 |
| Pitfalls       | Traps someone actually hit, each with a link.                                                                                                                          |
| Verify         | Commands that prove a change here, beyond the obvious test run.                                                                                                        |
| Decisions      | One line per deliberate choice, with a link to the pull request or ADR; without a link it goes. Whoever reverses the choice updates the line in the same pull request. |
| Open questions | What is genuinely still open, with an issue link once the pull request is merged; a draft node may ask reviewers instead.                                              |
| Links          | Docs and nodes that do not load by themselves, with when to open them.                                                                                                 |

The LDV (Logboek Dataverwerkingen) services were the first to get nodes and
serve as examples: [`services/ldv-logboek`](../services/ldv-logboek/AGENTS.md),
[`services/ldv-client`](../services/ldv-client/AGENTS.md) and
[`services/data-usage-index`](../services/data-usage-index/AGENTS.md).

## Writing rules

- No filler: no restated code facts, generic advice or speculation; leave a
  heading out rather than invent content for it. A line that changes no
  behaviour goes. Write for an agent without memory, not as documentation for
  people.
- Agents do not load README files by themselves. Anything an agent must know
  before changing the code goes in the node, even when a README says it too;
  link to the README only for detail.
- **Size**: as small as possible, and clearly smaller than the code the node
  covers. When a node grows far, see whether it can be split at a boundary, or
  whether something is only needed sometimes and can move to a separate doc. The
  root has no target size: it holds what every task needs.
- Knowledge that applies to several directories is written once, in the lowest
  node that covers them all. When it applies to services that do not share a
  directory, it goes in a flow or topic doc.
- A reference says what the doc is and when to open it ("read
  `docs/debugging-eudi-wallet.md` when a QR scan does not produce a
  credential").
- Terms follow the [glossary](https://ictu.github.io/GBO-TO/main/glossary/) of
  GBO-TO, the GBO Technisch Ontwerp (technical design). Language: English for
  nodes and new docs; existing Dutch docs become English when they are
  rewritten.
- No commit hashes or line numbers; refer to a file plus a symbol.
- Diagrams in Mermaid, not as images: text can be reviewed in a diff, an agent
  can read it, and GitHub renders it.

## Separate docs

Nodes load by themselves; docs in `docs/` do not. Put in a doc what is only
needed sometimes, or what spans several parts, and link to it from the nodes it
concerns:

- **Flow doc**: a path from start to end through two or more services, as a
  sequence of steps with a link to each service's node. Details of one service
  belong in its node.
- **Topic doc**: a subject that touches several parts but is not a flow
  (onboarding of sources, trust lists), or something that is only needed
  sometimes (a runbook such as
  [`debugging-eudi-wallet.md`](debugging-eudi-wallet.md)).

## Measuring

To compare sizes: code size in tokens is the bytes of the source files divided
by 4, without tests, test data, images, lock files, Markdown and `vendor/`.
Nodes and docs are measured the same way (bytes divided by 4).

```bash
scripts/intent-layer.sh size services/ldv-logboek
scripts/intent-layer.sh size services/ldv-logboek/AGENTS.md
```

## Building the layer

Per part of the repository, as a way of working (adapt it where a part asks for
that):

1. **Split into chunks** of code that belong together and can be read in one go;
   what matters most is what a reviewer can read in one review. What does not
   belong together is connected through a node above it, not by merging it.
2. **Bottom up, clear before hard.** First the nodes of packages and services; a
   node above summarises the nodes below it, not the code. The root comes last.
3. **Per chunk**: read the code, write a draft node and ask questions (why is
   this so, is this path still used, what must never happen here). Whoever knows
   the part answers them; repeat until it is right. What stays open goes under
   Open questions (see the table above).
4. **Sources** everyone can reach: the code, `git log`, the pull request
   discussions, the existing docs and READMEs, `TROUBLESHOOTING.md`, GBO-TO.

## Keeping it current

In every pull request that changes code:

1. Bottom up: read the diff and the nodes that cover the changed files; if
   behaviour or a contract changed, update the node in the same pull request.
2. The reviewer checks this in the review.

Further:

- **New directory**: if an agent needs knowledge of its own there (a new service
  almost always), it gets a node in the same pull request.
- **Rename, move, split, merge**: the node moves along (`git mv`); downlinks in
  the node above, flow docs and READMEs that point to it are updated in the same
  pull request.
- **Delete**: the node and the steps in flow docs go along.
- **Contradiction found** (code and node say different things): fix it in the
  same pull request, or open an issue and add it under Open questions.

## Checks

`scripts/intent-layer.sh check` (also `make check-intent-layer`, and a job in
CI) fails when:

- a `CLAUDE.md`, `GEMINI.md` or `.cursorrules` file exists, or a node is a
  symlink;
- a node sits in a vendored or generated directory;
- a relative link in a node or in this guide does not resolve;
- a service or frontend (a directory at the top level or under `services/` with
  its own `Dockerfile`, `go.mod` or `package.json`) has no node and is not
  listed in `scripts/intent-layer-exceptions.txt`, or that list names a
  directory that has a node or is no longer a service or frontend.

The check looks at the files git tracks or would add; files ignored by
`.gitignore` are left alone.

Size is only a warning: a node over 10% of the code below it is reported, not
failed. The root node has no size warning.
