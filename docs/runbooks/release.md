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
| push tag `v*-rc.N` (prerelease) | `:X.Y.Z-rc.N` only — **no `v` prefix, no `:vX.Y` companion, no `:stable`** |

> **Prerelease tags lose their `v` prefix.** `docker/metadata-action`'s
> `procSemver` discards the configured `pattern=` for any tag with a semver
> prerelease component and substitutes a hardcoded `{{version}}`, which never
> carries the `v` — so tag `v0.2.0-rc.1` publishes exactly one image tag,
> `:0.2.0-rc.1`, not `:v0.2.0-rc.1`. Two consequences: an operator setting
> `JUSTRAG_VERSION=v0.2.0-rc.1` would reference a nonexistent manifest — the
> correct value is `0.2.0-rc.1`. And the prune step's
> `exclude-tags: stable,edge,v*` does not match a tag with no `v`, so
> prerelease images are **not** retention-protected — they age out under
> `keep-n-tagged: 10` like `main`'s SHA images. No amount of tweaking
> `exclude-tags` fixes this; the published tag genuinely has no `v` to match.

`:stable` is deliberately withheld from prereleases: it is the default for
every unpinned compose deployment (`${JUSTRAG_VERSION:-stable}`), so moving it
to an RC would ship the RC to everyone who never set the variable.

`:latest` is not published. It was the mechanism by which production drifted
forward on unrelated pod restarts. What suppresses it is the `flavor:
latest=false` input on the workflow's metadata step — `docker/metadata-action`
defaults to `latest=auto`, which generates `:latest` for `type=semver` all on
its own, so simply not listing `latest` under `tags:` is **not** enough. Do not
remove that input.

`GET /version` reports `git describe --tags --always`: exactly `v0.1.0` on a
release, `v0.1.0-12-gabc1234` on a main build.

## Prerequisites

- **git-cliff** is not installed by anything in this repo and is not a
  `package.json` dependency. `npx --yes git-cliff@2 …` runs it without
  installing; substitute that for `git cliff` below if you have no binary.
- **`docker login ghcr.io`** — the `imagetools inspect` check in step 7 reads
  the registry and fails with an auth error otherwise. A GitHub PAT with
  `read:packages` as the password works.
- **`kubectl`** context pointing at the cluster, for the k8s deploy path.

## Cutting a release

1. **Be on a clean `main`, in sync with origin.**

   ```bash
   git checkout main && git pull && git status --short   # must be empty
   ```

2. **Generate the new release's changelog section incrementally.** Use
   `--unreleased --prepend`, never `-o` / `--output`: `-o` fully regenerates
   `CHANGELOG.md` from git history and overwrites the file, which would wipe
   every earlier release's hand-written `### ⚠ Upgrade notes` block — the
   only record of that release's migrations, flipped `site_config` defaults,
   and re-ingest requirements. `--prepend` inserts only the new commits
   (everything since the last tag) as a new section at the top and leaves
   the rest of the file — all prior releases' upgrade notes included —
   untouched.

   ```bash
   git cliff --unreleased --tag vX.Y.Z --prepend CHANGELOG.md
   ```

   > **Do not reword the preamble at the top of `CHANGELOG.md` on its own.**
   > `--prepend` keeps the file to a single preamble by stripping
   > `[changelog].header` from `cliff.toml` off both the generated output and
   > the existing file, then writing it back once. That strip is an exact
   > string match, so the two must stay **byte-for-byte identical**. Edit the
   > preamble in `cliff.toml` and copy the result into `CHANGELOG.md` (or the
   > reverse) in the same commit — otherwise every future release prepends
   > another copy of the "# Changelog / All notable changes…" block.
   > Verified against git-cliff 2.13.1.

3. **Hand-write the `### ⚠ Upgrade notes` block** under the new heading that
   command just added. This is the part no generator can produce, and the
   reason this step is not automated — it only needs to cover the release
   you just cut, not history (history's blocks are already in the file and
   untouched by step 2). Record every item that applies:

   - **Migrations** — the highest number this release requires:
     `ls go-backend/migrations/main/ | grep -oE '^[0-9]+' | sort -n | tail -1`
   - **Changed `site_config` defaults** — especially any flag flipping ON.
   - **Re-ingest requirements** — parser or chunking changes that make
     existing rows stale.
   - **New required env vars or operator grants.**

   If none apply, write "No upgrade actions required." — do not omit the block.

4. **Bump `package.json`** to the same version (nothing reads it, but a
   fictional version is worse than none).

5. **Bump the k8s image pin** in `k8s/worker-quick.yml`,
   `k8s/worker-heavy.yml`, and `k8s/worker-batch.yml` to `:vX.Y.Z`.

   This belongs in the **tagged commit**, not in the deploy step. Otherwise
   the tree at tag `vX.Y.Z` ships manifests still pinned to the previous
   release while `CHANGELOG.md` beside them says `vX.Y.Z`, and
   `git checkout vX.Y.Z && kubectl apply -f k8s/` deploys the wrong workers
   — permanently, since the tag is immutable. It also contradicts the rule
   at the top of this document: the tag is the sole source of truth, so
   everything the tag describes must be inside it.

   That the image does not exist yet at tag time is harmless — nothing pulls
   it until "Deploying a release" below, which you only start once step 7 has
   confirmed the build.

6. **Commit, tag, push.**

   ```bash
   git add CHANGELOG.md package.json \
           k8s/worker-quick.yml k8s/worker-heavy.yml k8s/worker-batch.yml
   git commit -m "docs: changelog and version pins for vX.Y.Z"
   git tag -a vX.Y.Z -m "vX.Y.Z"
   git push origin main
   git push origin vX.Y.Z
   ```

   The tag must be **annotated** (`-a`) — `git describe` prefers annotated
   tags, and a lightweight tag produces a different build id.

7. **Watch the build.** Confirm the workflow pushed `:vX.Y.Z`, `:vX.Y`, and
   `:stable` (needs `docker login ghcr.io`, see Prerequisites):

   ```bash
   docker buildx imagetools inspect ghcr.io/lutzi92/justrag:vX.Y.Z
   ```

## Deploying a release

### Compose

Migrations run automatically: the `migrate` one-shot service applies them and
`go-server` / `go-worker` gate on `service_completed_successfully`, so they
cannot start against an old schema.

```bash
JUSTRAG_VERSION=vX.Y.Z            # in .env
docker compose -f docker-compose.yml -f docker-compose.production.yml up -d
docker compose logs migrate       # confirm the goose run finished cleanly
```

### Kubernetes

**Step 1 — apply migrations. This is mandatory and there is nothing in the
cluster that does it for you.** `k8s/` has no migrate Job, and neither binary
self-migrates: `cmd/server` never invokes goose, and the worker only calls
`migrate.EnsureVectorTables` (the dim-keyed vector tables), not the main
migration set. Applying the Deployments first runs new binaries against the
old schema.

Run `/app/migrate` out of the **release image** as a one-shot pod, reusing the
workers' existing config and secrets — `worker-config` carries `DB_*` /
`VECTOR_DB_*` and `worker-secrets` carries the passwords plus `JWT_SECRET`.
That covers what the migrate pod itself touches, but `config.Load()` also
hard-fails startup without `ALLOWED_ORIGINS` once `NODE_ENV=production`
(set in `k8s/configmap.yml`) — neither `worker-config` nor `worker-secrets`
sets that var here, so if the cluster's real `worker-config` doesn't carry it
either, this pod dies with a CORS-shaped error, not an obviously
migration-related one. Substitute the real version in the
pod name using dashes (`migrate-v0-2-0`); dots are not valid there. If your
GHCR package is private, add
`"imagePullSecrets":[{"name":"ghcr-secret"}]` inside `spec` in the override,
the same way the worker manifests do.

```bash
kubectl -n justrag run migrate-vX-Y-Z \
  --image=ghcr.io/lutzi92/justrag:vX.Y.Z \
  --restart=Never --attach --rm \
  --overrides='{"spec":{"containers":[{"name":"migrate","image":"ghcr.io/lutzi92/justrag:vX.Y.Z","command":["/app/migrate"],"envFrom":[{"configMapRef":{"name":"worker-config"}},{"secretRef":{"name":"worker-secrets"}}]}]}}'
```

Then confirm the schema is where the release expects it:

```bash
kubectl -n justrag run migrate-status-vX-Y-Z \
  --image=ghcr.io/lutzi92/justrag:vX.Y.Z \
  --restart=Never --attach --rm \
  --overrides='{"spec":{"containers":[{"name":"migrate","image":"ghcr.io/lutzi92/justrag:vX.Y.Z","command":["/app/migrate","--status"],"envFrom":[{"configMapRef":{"name":"worker-config"}},{"secretRef":{"name":"worker-secrets"}}]}]}}'
```

`--status` prints one `migration status db=main version=NNNN` line per database
and exits. It does **not** list pending migrations — compare the printed
`version` against the highest file in the tagged tree
(`ls go-backend/migrations/main/ | grep -oE '^[0-9]+' | sort -n | tail -1`,
which is also the number recorded in this release's Upgrade notes). Compare
them **numerically** — goose prints `version=63` where the filename is
`0063_…`. They must match before you continue.

Two things worth knowing about `/app/migrate`:

- It serializes on a Postgres advisory lock
  (`internal/migrate/migrate.go`), so a concurrent second run waits rather
  than corrupting anything — safe, just pointless.
- Its context is capped at **5 minutes** (`cmd/migrate/main.go`). A slow
  migration — a large backfill, a non-concurrent index build — can hit that
  wall. If it does, the pod exits non-zero; re-run it, and check the runbook
  in [`migration-rollback.md`](./migration-rollback.md) before assuming the
  schema is intact.

**Step 2 — apply the manifests.** The pins are already correct in the tagged
tree (step 5), so there is nothing to edit and nothing to commit here:

```bash
kubectl -n justrag apply -f k8s/worker-quick.yml -f k8s/worker-heavy.yml -f k8s/worker-batch.yml
for w in quick heavy batch; do
  kubectl -n justrag rollout status deploy/worker-$w --timeout=5m
done
```

> **The `go-server` / nginx Deployment is not in this repository.** `k8s/`
> contains only the three workers plus docling. Pin the server deployment to
> the same version wherever its manifest lives — this is a manual step, and
> skipping it leaves the server on whatever tag it was pinned to while the
> workers move. Bringing that manifest in-repo is a known follow-up.

> **`imagePullPolicy: IfNotPresent`** means a node that already has the tag
> cached will not re-pull it. That is the point for immutable release tags,
> but if a tag is ever overwritten in GHCR, force the new image with
> `kubectl -n justrag rollout restart deploy/worker-{quick,heavy,batch}`.

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
- **k8s:** revert the pin in the three worker manifests **on `main`** and
  commit it, then apply. The pin is tracked in git, so an unreverted `main`
  plus a rolled-back cluster is exactly the git/cluster divergence the next
  person will trip over.

  `vPREV` below is the release you are rolling back *to*; its tagged tree
  already carries the right pins, so take them straight from it:

  ```bash
  git checkout main
  git checkout vPREV -- k8s/worker-quick.yml k8s/worker-heavy.yml k8s/worker-batch.yml
  git commit -m "chore: roll workers back to vPREV"
  git push origin main
  kubectl -n justrag apply -f k8s/worker-quick.yml -f k8s/worker-heavy.yml -f k8s/worker-batch.yml
  for w in quick heavy batch; do
    kubectl -n justrag rollout status deploy/worker-$w --timeout=5m
  done
  ```

  Do not use `kubectl rollout undo` — it reverts to the previous ReplicaSet,
  which may not match what the manifests say, leaving git and the cluster
  disagreeing.

Release images are retained indefinitely: the GHCR prune step excludes
`stable,edge,v*`, so `keep-n-tagged: 10` only ages out `main`'s SHA images.
