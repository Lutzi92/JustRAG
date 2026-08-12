# Runbook: cutting and rolling back a release

JustRAG uses SemVer `0.x`. The **annotated git tag is the only source of
truth** — there is no version constant in the Go code and no version file to
forget to bump.

| Bump | When |
|---|---|
| **minor** (`0.1.0` → `0.2.0`) | New features, **and anything breaking**. The leading `0` means breaking changes do not force a major bump before 1.0. |
| **patch** (`0.1.0` → `0.1.1`) | Fixes only. No migration, no changed `site_config` default, no re-ingest. |

`v1.0.0` is reserved for the first public release. The app version is
independent of the **API** version — `/api/v1/*`, the OpenAI-compat layer, MCP
`ask_kb`, and the ILIAS shim are path-versioned as `v1` and stay that way.

## What CI publishes

| Trigger | Image tags |
|---|---|
| push to `main` | `:<sha>`, `:edge` |
| push tag `v*` | `:vX.Y.Z`, `:vX.Y`, `:stable` |

`:latest` is not published. It was the mechanism by which production drifted
forward on unrelated pod restarts.

`GET /version` reports `git describe --tags --always`: exactly `v0.1.0` on a
release, `v0.1.0-12-gabc1234` on a main build.

## Cutting a release

1. **Be on a clean `main`, in sync with origin.**

   ```bash
   git checkout main && git pull && git status --short   # must be empty
   ```

2. **Regenerate the changelog** for the version you are about to cut:

   ```bash
   git cliff --tag vX.Y.Z -o CHANGELOG.md
   ```

3. **Hand-write the `### ⚠ Upgrade notes` block** under the new heading. This
   is the part no generator can produce, and the reason this step is not
   automated. Record every item that applies:

   - **Migrations** — the highest number this release requires:
     `ls go-backend/migrations/main/ | grep -oE '^[0-9]+' | sort -n | tail -1`
   - **Changed `site_config` defaults** — especially any flag flipping ON.
   - **Re-ingest requirements** — parser or chunking changes that make
     existing rows stale.
   - **New required env vars or operator grants.**

   If none apply, write "No upgrade actions required." — do not omit the block.

4. **Bump `package.json`** to the same version (nothing reads it, but a
   fictional version is worse than none).

5. **Commit, tag, push.**

   ```bash
   git add CHANGELOG.md package.json
   git commit -m "docs: changelog for vX.Y.Z"
   git tag -a vX.Y.Z -m "vX.Y.Z"
   git push origin main
   git push origin vX.Y.Z
   ```

   The tag must be **annotated** (`-a`) — `git describe` prefers annotated
   tags, and a lightweight tag produces a different build id.

6. **Watch the build.** Confirm the workflow pushed `:vX.Y.Z`, `:vX.Y`, and
   `:stable`:

   ```bash
   docker buildx imagetools inspect ghcr.io/lutzi92/justrag:vX.Y.Z
   ```

## Deploying a release

**Compose** — one variable in `.env`:

```bash
JUSTRAG_VERSION=vX.Y.Z
docker compose -f docker-compose.yml -f docker-compose.production.yml up -d
```

**Kubernetes** — bump the literal pin in `k8s/worker-{quick,heavy,batch}.yml`
and apply:

```bash
kubectl -n justrag apply -f k8s/worker-quick.yml -f k8s/worker-heavy.yml -f k8s/worker-batch.yml
kubectl -n justrag rollout status deploy/worker-quick --timeout=5m
```

> **The `go-server` / nginx Deployment is not in this repository.** `k8s/`
> contains only the three workers plus docling. Pin the server deployment to
> the same version wherever its manifest lives — this is a manual step, and
> skipping it leaves the server on whatever tag it was pinned to while the
> workers move. Bringing that manifest in-repo is a known follow-up.

Confirm what is actually running:

```bash
curl -s https://<host>/version   # expect {"version":"vX.Y.Z"}
```

## Rolling back

> **Rolling back the image does not roll back the database.**
>
> `cmd/migrate` is up-only by design. If the release's upgrade notes list a
> migration, re-pointing the image tag is **not sufficient** — the old binary
> may not tolerate the new schema. Reverting the schema requires the goose
> sequence in [`migration-rollback.md`](./migration-rollback.md).
>
> **A release whose upgrade notes list a migration has no one-step rollback.**
> Check `CHANGELOG.md` before assuming otherwise.

For a release with **no** migration:

- **Compose:** set `JUSTRAG_VERSION` to the previous version, `up -d`.
- **k8s:** revert the pin in the three worker manifests and apply. Do not use
  `kubectl rollout undo` — it reverts to the previous ReplicaSet, which may
  not match what the manifests say, leaving git and the cluster disagreeing.

Release images are retained indefinitely: the GHCR prune step excludes
`stable,edge,v*`, so `keep-n-tagged: 10` only ages out `main`'s SHA images.
