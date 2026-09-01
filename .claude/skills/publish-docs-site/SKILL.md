---
name: publish-docs-site
description: Use when editing anything under docs/, adding a documentation page, or fixing an MkDocs build failure in this repo. Covers the strict build, the required nav entry, and the relative-vs-absolute link rule that breaks the published site.
---

# The docs site

`docs/` is published to <https://bharadwaj6.github.io/ageOfAgents/> by `.github/workflows/docs.yml` on
every push to `main`, built with MkDocs Material.

```bash
pip install -r docs/requirements.txt
mkdocs serve              # http://127.0.0.1:8000, live-reloads
mkdocs build --strict     # what CI runs — a broken cross-reference fails the build
```

Run `mkdocs build --strict` before pushing a docs change. `make check` does **not** cover it.

## Two rules that break the build or the site

1. **A new page must be listed in `nav:` in `mkdocs.yml`.** The nav is explicit on purpose: `docs/` holds a
   design archive that must not sit beside the user-facing pages, and `--strict` fails on anything
   reachable but unlisted.
2. **Links to files outside `docs/`** (`README.md`, `SECURITY.md`, `go.mod`, source files) must be
   absolute `https://github.com/bharadwaj6/ageOfAgents/...` URLs. A relative one resolves on github.com
   and 404s on the published site.

Inside `docs/`, use ordinary relative links (`design/adr/README.md`) — `--strict` checks them.

## Where things belong

| Content | Page |
|---|---|
| A command or flag | `docs/cli.md` |
| An `aoa.toml` field | `docs/config-reference.md` |
| A backend/harness | `docs/harnesses/<name>.md` + the README table |
| A design decision | an ADR under `docs/design/adr/` (see `change-architecture`) |

Keep the README lean: it is the pitch and the first five minutes, not a manual. Detail goes to the site.
