## Problem

<!-- What goes wrong or is missing, and why it matters. Link the issue (Closes #NN). -->

## Solution

<!-- What changed and how it fixes the problem. Add the smallest visual that makes it clear, when one helps (diagram, table, screenshot). -->

## Evidence

<!-- Before and after: a screenshot or real output (command, test run, curl). Not a description of it. -->

## Merge danger

<!-- Easy to revert or not (migrations, renamed contracts, data)? What else can break (other services, flows, the demo)? -->

## TO IDs

<!-- REQ-, SL- and F-IDs from ICTU/GBO-TO this implements or touches, or "none". -->

## Checklist

- [ ] `go test ./...` passes for touched services
- [ ] `opa test /policies -v` passes (if policies touched)
- [ ] `tsc --noEmit` passes for touched frontends
- [ ] `make demo` still runs end-to-end
- [ ] README / docs updated if behaviour visible to users changed
