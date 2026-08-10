# HiveCrew product identity compatibility map

## Purpose

HiveCrew is an independent product, but the imported system has several kinds of
old identifiers. They cannot all be replaced safely in one operation. This map
separates product identity from compatibility ABI and defines the removal sequence.

## Classification

| Class | Meaning | B1 rule | Examples |
| --- | --- | --- | --- |
| P0 product surface | Visible to an owner or end user | Replace with HiveCrew now | window title, metadata, installer, desktop name, helper copy |
| P1 network ownership | Can contact or publish to another product | Remove or fail local now | cloud defaults, update feed, release repository, docs links |
| C1 application compatibility | Needed to read an existing local installation | New HiveCrew path first; legacy read-only fallback with receipt | `~/.multica`, `multica://` |
| C2 code/package ABI | Broad internal dependency graph | Keep temporarily; never show as product identity | `@multica/*`, Go module path, import paths |
| C3 data/schema ABI | Persistent rows, environment names and migrations | Keep until a versioned dual-read migration exists | `MULTICA_*`, database name, SQL identifiers |
| H0 provenance | Historical/source/legal evidence | Preserve and label; never present as current product | original LICENSE, initial-source record, baseline commit |

## B1 first-slice acceptance

- New desktop builds identify as HiveCrew in package metadata, installer names,
  application ID, protocol registration, window title and renderer errors.
- New packaged installs default to the local HiveCrew development endpoints and
  never contact the previous cloud service when configuration is absent.
- No desktop release or CLI bootstrap path points at the previous GitHub project.
- New desktop configuration lives under `~/.hivecrew`; an existing legacy config
  may be read once as a compatibility fallback but is never written back.
- Web metadata and system actor names say HiveCrew.
- A repository check prevents previous-brand URLs and release ownership from
  returning to the active desktop/Web product surfaces.

## Deferred compatibility identifiers

The following remain temporarily in B1 and do not mean upstream tracking exists:

- workspace package namespace `@multica/*`;
- Go module `github.com/multica-ai/multica/server` and internal Go imports;
- legacy CLI binary and command directory `multica`;
- existing `MULTICA_*` environment variables and database defaults;
- database migrations and historical issue keys;
- historic changelog, source comments, tests and provenance records.

They are removed only through explicit migration work orders with compatibility
tests, rollback and data/installer transition plans.

## No-upstream invariant

No compatibility identifier authorizes network access, update checks, publishing,
fetching, merging, rebasing or patch ingestion from the former project. HiveCrew's
only configured Git remote is the internal DGX repository.
