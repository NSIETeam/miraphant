# Upstream source record

- Project: `songquanpeng/one-api`
- Source: <https://github.com/songquanpeng/one-api>
- Tag: `v0.6.10`
- Commit: `3915ce9814b8261a1ab13ed93adec58b463cd75c`
- Tag object checked locally against the repository tag ref; source snapshot has no included `.git` directory.
- License: MIT; `LICENSE` retained byte-for-byte from the upstream tag (SHA-256 `e965b65a52d90708ffeeb9632e1928cab08bc7ae1424d8407e42fc372bce6506`).

## Directory differences from the tag

The initial import preserves the upstream source tree and tracked source/assets. It omits Git metadata, built frontend output (`web/build`), dependency installs (`node_modules`), caches, logs, databases, data directories, and local runtime secrets. `.env.example` is retained as the upstream non-secret configuration template.

Miraphant build and source-management additions are:

- `UPSTREAM.md` (this provenance record)
- `BUILDING.md` (local build instructions and tool versions)
- `scripts/build-local.sh` (independent gateway build entry point)
- `web/default/package-lock.json`, `web/berry/package-lock.json`, and `web/air/package-lock.json` (generated from the existing upstream `package.json` declarations; the v0.6.10 tag contained no npm lockfiles)

No gateway payment, points, or production deployment behavior is implemented by this source import. Subsequent source changes must be documented here with their purpose and reviewed against the upstream commit above.
