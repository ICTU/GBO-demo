## Summary

<!-- What changes, in one or two sentences. -->

## Why

<!-- The reason for the change. If it fixes an issue, link it (Closes #NN). -->

## How to test

<!-- Steps a reviewer can run to verify. Ideally a copy-paste-able curl / make target. -->

## Checklist

- [ ] `go test ./...` passes for touched services
- [ ] `opa test /policies -v` passes (if policies touched)
- [ ] `tsc --noEmit` passes for touched frontends
- [ ] `make demo` still runs end-to-end
- [ ] README / docs updated if behaviour visible to users changed
