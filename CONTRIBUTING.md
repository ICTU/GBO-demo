# Contributing

Thanks for taking the time to look at this reference-architecture demo. Bug reports, questions, and pull requests are welcome.

## Reporting issues

Please use GitHub Issues for bug reports and feature discussions, with the bug report or feature request template. Describe:

- Current and wanted behaviour, in terms of behaviour and contracts (API responses, flow steps, UI results), not file paths
- Acceptance criteria that can each be checked on their own, and what is out of scope (for bugs: if you know them)
- For bugs: how to reproduce it and your environment (OS, Docker version)

For security issues, see [SECURITY.md](SECURITY.md) — please do **not** open a public issue.

## Pull requests

Small, focused PRs are easier to review and land. If you're planning a larger change (new service, new flow, structural refactor), open an issue first so we can align on scope.

Before opening a PR:

1. Run the local checks:
   ```bash
   # Go lint + tests per service
   cd services/<name> && go test ./...

   # Rego compile check, format diff + tests (same image as CI)
   make policy-test

   # Frontend type-check
   cd <frontend> && npx tsc --noEmit

   # No real OINs, internal hostnames or secrets (demo OINs: 99999999...)
   make check-safety
   ```

2. Keep the demo runnable end-to-end (`make demo` must still work).

3. Match the code style of the file you're editing. New Go services get the same `.golangci.yml` treatment as existing ones.

4. Fill in the PR template (`.github/pull_request_template.md`). Evidence means real
   output or a screenshot, before and after, not a description of it.
   Diagrams can go in a ```` ```mermaid ```` block. With gh 2.105 or newer you can
   attach a screenshot from the command line:
   `gh pr create --attach './after.png#After'` (also works with `gh pr edit`).

## Scope

This repository is a **reference-architecture demonstration**. It shows how the components fit together; it is not a production-ready implementation. Contributions that clarify or extend the demonstration are welcome. Contributions that turn mock components into production ones (e.g. real BSNk, real DigiD) are out of scope for this repo but may fit better in downstream projects.

## Code of conduct

Be kind. Assume good intent. Focus on the code and the ideas, not the person.

## License

By contributing, you agree that your contributions will be licensed under [EUPL 1.2](LICENSE).
