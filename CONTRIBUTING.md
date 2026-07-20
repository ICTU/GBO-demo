# Contributing

Thanks for taking the time to look at this reference-architecture demo. Bug reports, questions, and pull requests are welcome.

## Reporting issues

Please use GitHub Issues for bug reports and feature discussions. Include:

- What you tried to run (`make demo`, a specific `curl`, a UI interaction)
- What you expected to happen
- What actually happened (error message, screenshot, log snippet)
- Your environment (OS, Docker version)

For security issues, see [SECURITY.md](SECURITY.md) — please do **not** open a public issue.

## Pull requests

Small, focused PRs are easier to review and land. If you're planning a larger change (new service, new flow, structural refactor), open an issue first so we can align on scope.

Before opening a PR:

1. Run the local checks:
   ```bash
   # Go lint + tests per service
   cd services/<name> && go test ./...

   # Rego format + tests
   docker run --rm -v $(pwd)/policies:/w -w /w openpolicyagent/opa:latest fmt --diff /w
   docker run --rm -v $(pwd)/policies:/w -w /w openpolicyagent/opa:latest test /w -v

   # Frontend type-check
   cd <frontend> && npx tsc --noEmit
   ```

2. Keep the demo runnable end-to-end (`make demo` must still work).

3. Match the code style of the file you're editing. New Go services get the same `.golangci.yml` treatment as existing ones.

4. Include a short PR description: what changes, why, and how you verified it works.

## Scope

This repository is a **reference-architecture demonstration**. It shows how the components fit together; it is not a production-ready implementation. Contributions that clarify or extend the demonstration are welcome. Contributions that turn mock components into production ones (e.g. real BSNk, real DigiD) are out of scope for this repo but may fit better in downstream projects.

## Code of conduct

Be kind. Assume good intent. Focus on the code and the ideas, not the person.

## License

By contributing, you agree that your contributions will be licensed under [EUPL 1.2](LICENSE).
