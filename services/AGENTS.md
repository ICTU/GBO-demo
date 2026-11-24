# Default: ports and adapters for substantial services

Use ports and adapters (hexagonal architecture) for every substantial service.
The application/domain core owns the use cases and the interfaces (ports) it
needs. HTTP, databases, filesystems, queues, FSC and other external systems are
adapters that implement those ports.

The dependency direction is always inward:

```text
driving adapters (HTTP, CLI, worker)
                |
                v
        application/domain core
                ^
                |
driven adapters (database, filesystem, FSC, remote APIs)
```

- Put business rules and use-case orchestration in a core package that does not
  import transport, persistence or framework packages.
- Define ports next to their consumer in the core. Keep them small and shaped
  around the use case, not around an infrastructure API.
- Put each materially different adapter in its own package. Adapters may import
  the core; the core must never import adapters.
- Keep `main`/`cmd` as the composition root: configuration, construction,
  wiring, process lifecycle and nothing else.
- Prefer one deep use-case entry point over many shallow helper interfaces.
- Test business behaviour at the core boundary with in-memory fakes. Test
  protocol, serialization and persistence details in adapter tests.
- Do not introduce abstraction for a hypothetical future dependency. Create a
  port when there is a real I/O boundary or a real second implementation.

Apply this structure when a service reaches **1,000 non-test lines of code or
10 production source files**, whichever comes first. Apply it earlier when the
service has any of these complexity signals:

- more than one driving mechanism, such as HTTP plus a CLI or worker;
- three or more external systems or storage mechanisms;
- persistent workflow state;
- a security, identity, authorization or trust boundary.

Smaller services may remain in one package when they have one clear entry
point and at most one external dependency. They should still keep business
logic independent from handlers where practical. Crossing a threshold does
not require a big-bang rewrite: establish the boundary around the next use case
that changes, and migrate adjacent behaviour incrementally.
