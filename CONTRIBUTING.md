# Contributing to RelayPine

Use [GitHub Issues](https://github.com/ufcenterxyz/relaypine.com/issues) for reproducible bugs and feature proposals. For a large change, describe the problem and intended behavior in an issue before starting implementation. Security issues belong in a [private vulnerability report](SECURITY.md).

## Local development

Follow the [README quick start](README.md#quick-start). Use your own administrator email and local credentials. The default local environment uses Docker Compose for MySQL and Redis, Go for the gateway, and Yarn Classic for the console.

Before opening a pull request, run the checks relevant to your change:

```sh
make check
make test-integration
make build
```

The integration script uses a dedicated test schema. Never use a production database for tests. Gateway changes affecting accounting, authorization, routing, or concurrency should include a regression test. For UI changes, check both languages, light/dark themes, keyboard navigation, and a narrow mobile viewport. The console uses HeroUI; keep components within that system.

## Pull requests

- Keep each PR focused on one problem and explain the resulting behavior.
- Include reproduction steps, relevant test results, and screenshots for visible UI changes.
- Document API or configuration changes, including migration behavior.
- Do not commit credentials, environment files, customer data, request bodies, database dumps, or deployment execution logs. Replace sensitive values in examples with placeholders.
- Preserve native provider protocols, tenant/project boundaries, fail-closed authentication, and integer money accounting. Changes to these boundaries need explicit tests.

CI checks pull requests without deployment credentials. The maintainer's release workflow only runs for release tags in the upstream repository; contributors do not need access to hosted infrastructure.

By submitting a contribution, you agree to license it under the project's [MIT license](LICENSE). Dependencies retain their own licenses. Be respectful and give enough context for another contributor to reproduce your findings.
