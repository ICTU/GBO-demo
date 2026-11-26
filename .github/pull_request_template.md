## Problem

<!-- What goes wrong or is missing, and why it matters. Link the issue (Closes #NN). -->

## Solution

<!-- What changed and how it fixes the problem. Name each demo shortcut and what a
     real deployment would need instead, and each open choice a standard leaves
     (with its risk).

     Always show before and after, in the form that fits:
     - UI change: screenshots side by side.
     - Behaviour, flow or state change: a Mermaid diagram of the old and the new
       situation (sequenceDiagram for flows between services, stateDiagram-v2 for
       state, flowchart or classDiagram for structure). GitHub and GitLab render it.
     - Nothing to compare (typo, dependency bump, CI config):
       write "No visual comparison: <reason>". Don't leave it out.

     Example, screenshots:

     | Before | After |
     |---|---|
     | ![Before](./before.png) | ![After](./after.png) |

     Example, flow (one block for before, one for after):

     ```mermaid
     sequenceDiagram
         Wallet->>Verifier: presentation
         Verifier-->>Wallet: accepted
     ```
-->

## Evidence

<!-- Proof that the after state works: a screenshot of the result, test or command
     output, or a demo step. Real output, not a description of it.
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
