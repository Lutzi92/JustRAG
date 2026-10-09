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

## The flow at a glance

```
release-prep commit ──tag vX.Y.Z-rc.1──▶ CI builds :vX.Y.Z-rc.1 ──▶ staging (compose)
        ▲                                                              │
        └──── fix on main / release branch, tag vX.Y.Z-rc.2 ◀── broken ┤
                                                                       │ good
promote-release.yml (rc_tag = vX.Y.Z-rc.N) ◀───────────────────────────┘
   └─▶ same digest tagged :vX.Y.Z, :vX.Y, :stable  +  git tag vX.Y.Z on the RC's commit
         └─▶ prod (:stable): kubectl rollout restart
```

**Build once, promote by digest.** A release is never rebuilt: the image
staging tested is the image prod runs. A rebuild from the same commit would
not be the same bits (base-image layers, OS packages and build time all
drift), so a rebuilt release would make the staging round meaningless.

## What CI publishes

| Trigger | Image tags |
|---|---|
| push to `main` | `:<sha>`, `:edge` |
| push tag `vX.Y.Z-rc.N` | `:vX.Y.Z-rc.N` (exactly the git tag) |
| **Promote Release Candidate** workflow (manual) | `:vX.Y.Z`, `:vX.Y`, and `:stable` — added to the RC's existing image, nothing is built |
| push tag `vX.Y.Z` by hand | **nothing** — use the promote workflow |

`:stable` is the default for every unpinned compose deployment
(`${JUSTRAG_VERSION:-stable}`), so it only moves forward: promoting a hotfix
on an older line (`v0.11.3` while `v0.12.0` is out) tags `:v0.11.3` and
`:v0.11` but leaves `:stable` alone.

**Prod runs `:stable`** (workers and `go-server`, with `imagePullPolicy:
Always`). That is safe only because `:stable` now moves exclusively through a
promotion — after staging, and after the release's migrations have run (see
"Deploying a release"). The flip side: **promoting is deploying.** From that
moment any prod pod that restarts, for whatever reason, comes up on the new
release, so never promote before the migrations are in. Going back is the
**Roll Back Stable** workflow (see "Rolling back").

`:latest` is not published. It was the mechanism by which production drifted
forward on unrelated pod restarts. What suppresses it is the `flavor:
latest=false` input on the workflow's metadata step — `docker/metadata-action`
defaults to `latest=auto`, which generates `:latest` for tag-based rules all
on its own, so simply not listing `latest` under `tags:` is **not** enough. Do
not remove that input.

`GET /version` reports `git describe --tags --always` of the build:
`v0.1.0-rc.2` on a release candidate, `v0.1.0-12-gabc1234` on a main build.
**A promoted release keeps reporting the RC it came from** — prod on
`v0.12.0` answers `{"version":"v0.12.0-rc.2"}`, because it is that binary.
That is the honest answer, not a bug: it tells you which candidate was
promoted. The frontend's update check only compares for inequality, so it is
unaffected.

The promote workflow refuses to run unless the RC is release-ready: the RC's
tree must carry a `## vX.Y.Z` section in `CHANGELOG.md` and `package.json` at
`X.Y.Z`. It also never
re-points an existing release: a git tag or image tag `vX.Y.Z` that already
exists elsewhere fails the run (re-running a promotion that already
succeeded is harmless).

## Prerequisites

- **git-cliff** is not installed by anything in this repo and is not a
  `package.json` dependency. `npx --yes git-cliff@2.13.1 …` runs it without
  installing; substitute that for `git cliff` below if you have no binary.
- **`docker login ghcr.io`** on your machine and on the staging server — the
  package is private. A GitHub PAT with `read:packages` as the password works.
- **`kubectl`** context pointing at the cluster, for the prod deploy.
- The promote workflow pushes a git tag with `GITHUB_TOKEN`. If a tag ruleset
  protects `v*`, allow GitHub Actions to bypass it.

## Cutting a release

1. **Pick the base.** A normal release is cut from `main`:

   ```bash
   git checkout main && git pull && git status --short   # must be empty
   ```

   A **hotfix** for the current release while `main` already holds unreleased
   features is cut from a release branch instead, so the features stay out:

   ```bash
   git checkout -b release/0.11 v0.11.2      # once per minor line; reuse it afterwards
   git cherry-pick <fix-sha>                 # the fix lands on main first
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

   Use the **release** version here, not the RC: the section is what ships.

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

4. **Bump `package.json`** to the release version (nothing reads it at
   runtime, but the promote workflow checks it):
   `npm version X.Y.Z --no-git-tag-version --workspaces-update=false`.

5. **Commit, tag the first release candidate, push.**

   ```bash
   git add CHANGELOG.md package.json package-lock.json
   git commit -m "docs: changelog and version for vX.Y.Z"
   git tag -a vX.Y.Z-rc.1 -m "vX.Y.Z-rc.1"
   git push origin HEAD            # main, or release/X.Y for a hotfix
   git push origin vX.Y.Z-rc.1
   ```

   The tag must be **annotated** (`-a`) — `git describe` prefers annotated
   tags, and a lightweight tag produces a different build id.

6. **Wait for the RC build** (Actions → *Build and Push Docker Image*) and
   confirm the image exists:

   ```bash
   docker buildx imagetools inspect ghcr.io/ki4jlu/justrag:vX.Y.Z-rc.1
   ```

7. **Deploy the RC to staging and test it** (see "Staging" below).

   If staging finds a problem: fix it on the same branch, amend the changelog
   section if the fix belongs in it, and tag `vX.Y.Z-rc.2` on the new commit —
   steps 5–7 again. Never move an existing RC tag; every candidate gets its
   own number, so it is always clear which build staging tested.

8. **Run the release's migrations on prod** with the RC image — "Deploying a
   release", step 1. Skip only if the upgrade notes list no migration.
   Until step 10 the **previous** release keeps running against the new
   schema, so a release's migrations must be additive (new tables, nullable
   or defaulted columns, `CONCURRENTLY` indexes). A migration that renames
   or drops something the previous release still reads needs a two-release
   split: the release that stops reading it first, the removal after.

9. **Promote.** Actions → **Promote Release Candidate** → *Run workflow*,
   `rc_tag` = the RC that passed (e.g. `vX.Y.Z-rc.2`). It adds `:vX.Y.Z`,
   `:vX.Y` and (if newest) `:stable` to that exact image and pushes the git
   tag `vX.Y.Z` on the RC's commit. Then `git fetch --tags`.

10. **Restart prod** onto the new `:stable` — "Deploying a release", step 3.

11. **After a hotfix:** make sure every fix on `release/X.Y` is also on
    `main`, and fold the hotfix's changelog section into `main`'s
    `CHANGELOG.md` so the next release's `--prepend` does not lose it. A
    hotfix on an **older** line does not move `:stable`, so prod (on
    `:stable`) does not get it — that is only for installs pinned to `:vX.Y`.

## Staging

Staging is a compose server for developers to test release candidates on; it
does not need the worker split of prod. It runs **exactly one pinned RC** —
never `:stable` or `:edge`, so what is on staging is always a known
candidate.

On the staging server, check out the RC's tree (so the compose files are the
candidate's too) and pin the image to it:

```bash
git fetch --tags && git checkout vX.Y.Z-rc.N
# .env
JUSTRAG_VERSION=vX.Y.Z-rc.N
docker compose -f docker-compose.yml -f docker-compose.production.yml pull
docker compose -f docker-compose.yml -f docker-compose.production.yml up -d
docker compose logs migrate       # confirm the goose run finished cleanly
curl -s https://<staging-host>/version   # expect {"version":"vX.Y.Z-rc.N"}
```

Compose applies migrations automatically, so staging is also where a
release's migrations run first. Two consequences:

- **Staging's schema moves ahead of prod** with every RC that carries a
  migration, and `cmd/migrate` is up-only. To go back (an abandoned RC, or to
  re-test an upgrade from the current prod version), restore staging's
  database from a snapshot rather than migrating down. Take a snapshot before
  deploying an RC with a migration.
- How long a migration takes depends on the data. If staging's database is
  much smaller than prod's, a `CREATE INDEX CONCURRENTLY` or a backfill that
  is instant on staging can still hit `/app/migrate`'s 5-minute cap on prod —
  read the release's upgrade notes for those.

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

**Do this before promoting.** Run `/app/migrate` out of the **RC image** you
are about to promote (`:vX.Y.Z-rc.N` — the same digest the release will be)
as a one-shot pod, reusing the
workers' existing config and secrets — `worker-config` carries `DB_*` /
`VECTOR_DB_*` and `worker-secrets` carries the passwords plus `JWT_SECRET`.
That covers what the migrate pod itself touches, but `config.Load()` also
hard-fails startup without `ALLOWED_ORIGINS` once `NODE_ENV=production`
(set in `k8s/configmap.yml`) — neither `worker-config` nor `worker-secrets`
sets that var here, so if the cluster's real `worker-config` doesn't carry it
either, this pod dies with a CORS-shaped error, not an obviously
migration-related one. Substitute the real RC in the
pod name using dashes (`migrate-v0-2-0-rc-1`); dots are not valid there. If your
GHCR package is private, add
`"imagePullSecrets":[{"name":"ghcr-secret"}]` inside `spec` in the override,
the same way the worker manifests do.

```bash
kubectl -n justrag run migrate-vX-Y-Z-rc-N \
  --image=ghcr.io/ki4jlu/justrag:vX.Y.Z-rc.N \
  --restart=Never --attach --rm \
  --overrides='{"spec":{"containers":[{"name":"migrate","image":"ghcr.io/ki4jlu/justrag:vX.Y.Z-rc.N","command":["/app/migrate"],"envFrom":[{"configMapRef":{"name":"worker-config"}},{"secretRef":{"name":"worker-secrets"}}]}]}}'
```

Then confirm the schema is where the release expects it:

```bash
kubectl -n justrag run migrate-status-vX-Y-Z-rc-N \
  --image=ghcr.io/ki4jlu/justrag:vX.Y.Z-rc.N \
  --restart=Never --attach --rm \
  --overrides='{"spec":{"containers":[{"name":"migrate","image":"ghcr.io/ki4jlu/justrag:vX.Y.Z-rc.N","command":["/app/migrate","--status"],"envFrom":[{"configMapRef":{"name":"worker-config"}},{"secretRef":{"name":"worker-secrets"}}]}]}}'
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

**Step 2 — promote** the RC (Actions → **Promote Release Candidate**). This
moves `:stable`; from here on any restarting pod comes up on the release.

**Step 3 — restart everything that pulls `:stable`**, so all pods move now
and together instead of one by one as they happen to restart:

```bash
kubectl -n justrag rollout restart deploy/worker-quick deploy/worker-heavy deploy/worker-batch deploy/<go-server>
for d in worker-quick worker-heavy worker-batch <go-server>; do
  kubectl -n justrag rollout status deploy/$d --timeout=5m
done
```

The worker manifests in `k8s/` do not change between releases (they pull
`:stable`), so there is nothing to `kubectl apply` unless the manifests
themselves changed in this release — then apply them first.

> **The `go-server` / nginx Deployment is not in this repository.** `k8s/`
> contains only the three workers plus docling. Its manifest, wherever it
> lives, must use `:stable` **with `imagePullPolicy: Always`** like the
> workers. Kubernetes only defaults to `Always` for `:latest` or an untagged
> image, so `:stable` without the explicit policy means `IfNotPresent`: a
> node with an older `:stable` cached keeps running it, and server pods end
> up on different releases. Bringing that manifest in-repo is a known
> follow-up.

Confirm what is actually running:

```bash
curl -s https://<host>/version   # expect {"version":"vX.Y.Z-rc.N"} — the promoted RC
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
- **k8s (prod on `:stable`):** run Actions → **Roll Back Stable** with
  `release` = the version to go back to (e.g. `v0.11.2`), then restart as in
  "Deploying a release", step 3. Nothing is built and no git tag changes;
  `:stable` is pointed at that release's existing image. The workflow refuses
  if any later release added a migration, unless `accept_newer_schema` is
  ticked — read the box above first. A later promotion moves `:stable`
  forward again as usual.

Release and release-candidate images are retained indefinitely: the GHCR
prune step excludes `stable,edge,v*`, so `keep-n-tagged: 10` only ages out
`main`'s SHA images.
