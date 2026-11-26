## Problem

<!-- What goes wrong or is missing, and why it matters. Link the issue (Closes #NN). -->

## Solution

<!-- What changed and how it fixes the problem. Name each demo shortcut and what a
     real deployment would need instead, and each open choice a standard leaves
     (with its risk). Add the smallest visual that makes it clear, when one helps
     (a ```mermaid diagram, a table, a screenshot). -->

## Evidence

<!-- Before and after: a screenshot or real output (command, test run, curl). Not a
     description of it. Docs-only: a rendered link or "n/a".
     Screenshot from the command line (gh 2.105+): gh pr edit --attach './after.png#After'
     appends it; a ![After](./after.png) in a body passed in the same command is replaced
     by the uploaded image. -->

## Risk and rollback

<!-- Breaking changes (API, contract, config, migration)? How easy to revert?
     What else can break (other services, flows, the demo)? -->

## Related GBO-TO IDs

<!-- Requirement IDs from the technical design (https://github.com/ICTU/GBO-TO)
     this implements or touches: REQ-…, SL-…, F-…. Or "none". -->

## Checklist

- [ ] `go test ./...` passes for touched services
- [ ] `make policy-test` passes (if policies touched)
- [ ] `npx tsc --noEmit` passes for touched frontends
- [ ] `make check-safety` passes
- [ ] `make demo` still runs end-to-end
- [ ] `conformance/` matrices updated if the implemented part of a standard changed
- [ ] README / docs updated if behaviour visible to users changed
