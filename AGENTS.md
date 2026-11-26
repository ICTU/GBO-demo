# Purpose and scope

This repository is a reference demo. It exists to validate the GBO
architecture in running code and to surface the requirements that the
architecture implies. It is not a production implementation.

When you plan or propose work here:

- Start from what the demo has to show. Implement a standard flow, such as
  an OpenID4VCI or OpenID4VP flow, as far as the demo needs it, and follow
  the normative requirements of the standard for the parts you build.
- Do not turn a change into a production-readiness or conformance
  programme. Certification, trust registration, conformance test suites
  and full hardening are out of scope unless the task asks for them.
  Conformance matrices in `conformance/` track the standards the demo
  implements; keep them current, but do not extend them to standards the
  demo does not implement.
- Shortcuts are allowed when they are explicit. Mark each one as a demo
  shortcut and say what a real deployment would need instead.
- When a standard leaves a security or design choice open, name the choice
  and its risk. Do not settle it silently with a larger design.
- Behavioural choices belong to the person you work with: which cases must
  hold, which refusal reason applies, and what happens when something is
  unavailable. Ask instead of assuming.

The architecture rules below apply to the code you build; this section
decides what is worth building.

# Repository architecture

Services use ports and adapters; see [services/AGENTS.md](services/AGENTS.md)
before creating or restructuring a service.

# Debugging the EUDI wallet flow

Before debugging the EUDI wallet flow (a QR scan that does not produce a
credential), read [docs/debugging-eudi-wallet.md](docs/debugging-eudi-wallet.md).

# Pull requests and issues

Write PR descriptions with the sections of `.github/pull_request_template.md`,
also when you pass the body on the command line (`gh pr create --body`).
Show before and after in Solution: screenshots for UI changes; otherwise the
diagram that shows the change most clearly (e.g. sequence, state, flowchart,
class, ER, C4/architecture, or a table; Mermaid renders on GitHub and GitLab),
or "No visual comparison: <reason>".
Issues follow `.github/ISSUE_TEMPLATE/`.
