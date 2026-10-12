# Contributing to wenyuan

Thanks for your interest in improving wenyuan. This document explains how
to propose changes, what we expect from a pull request, and the conventions the
codebase follows.

## Code of conduct

By participating you agree to abide by the [Code of Conduct](CODE_OF_CONDUCT.md).
Report unacceptable behavior to the maintainers listed in that document.

## Getting started

```sh
git clone https://github.com/studyzy/wenyuan
cd api
make check     # build + test + vet + pgx build
```

Requirements: Go 1.24 or newer. The default build is standard-library-only and
offline; the PostgreSQL driver path is compiled with the `pgx` build tag.

## Ways to contribute

- **Bug reports** — open an issue using the bug report template. Include the
  exact request, the observed response, and the expected response.
- **Feature requests** — open an issue using the feature request template.
  Describe the public contract you need, not an implementation.
- **Pull requests** — see below.

## Compatibility is the contract

This project is an **API-compatible** implementation. The public HTTP surface
(routes, JSON field names, status codes, headers) and the PostgreSQL schema
(tables, columns, enums) are compatibility surface and must be preserved
exactly. A change that alters any of them is a breaking change and must be
called out as such in the pull request.

## Before you open a pull request

1. **Keep the diff small.** One change per pull request. Do not mix a fix with
   an unrelated refactor.
2. **Match the existing style.** The codebase favors the standard library,
   small focused files, and table-driven tests.
3. **Add tests.** Every behavior change needs a test that fails without it.
   New public behavior needs an end-to-end test through the served handler.
4. **Run the full gate:**

   ```sh
   make check
   ```

   All four of `build`, `test`, `vet`, and the `pgx` build must pass.
5. **Keep files within the size limits.** Code files stay under 500 lines and
   test files under 800; split along module boundaries rather than growing a
   file.
6. **Do not add a dependency without need.** Prefer the standard library. New
   dependencies must be MIT / Apache-2.0 / BSD licensed.

## Commit and pull request conventions

- Write commit messages in the imperative mood: "add bank alias routing", not
  "added" or "adds".
- Reference the issue a change closes: `Closes #123`.
- Describe *why* in the pull request body; the diff shows *what*.
- Mark breaking changes explicitly.

## Reporting a security issue

Do not open a public issue for a security problem. See [SECURITY.md](SECURITY.md).

## License

By contributing you agree that your contributions are licensed under the
[MIT License](LICENSE).