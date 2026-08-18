# Projects and resources source map

- `server/cmd/multica/cmd_project.go` registers project `list`, `get`, `create`, `update`, `delete`, and `status`.
- The same file registers `project resource list/add/update/remove`.
- `project create --repo` attaches `github_repo` resources during project creation.
- `project create` / `project update` accept `--start-date` / `--due-date` (calendar days, `YYYY-MM-DD`), mapping to the project `start_date` / `due_date` columns (migration `166_project_dates`); an empty `--start-date ""`/`--due-date ""` on update clears the date, mirroring the issue date flags in `cmd_issue.go`.
- `project create` / `project update` accept `--repo-inheritance-policy workspace_fallback|project_only`, mapping to `project.repo_inheritance_policy` (migration `414_project_repo_inheritance_policy`). The default preserves workspace repo inheritance; `project_only` keeps an empty project repo set authoritative.
- Project update/status writes use `project.revision` (migration `415_project_revision`) as an atomic CAS token. The handler requires JSON `revision`; `428`/`400`/`412` are fail-closed outcomes for missing/invalid/stale tokens. The CLI performs a GET first and supplies the current revision.
- `project resource add` supports shortcuts for `github_repo` (`--url`, non-JSON `--ref` for checkout ref, `--default-branch-hint`) and `local_directory` (`--local-path`, `--daemon-id`, `--ref-label`), or generic JSON `--ref '<json>'`.
- `project resource update` merges shortcut edits with existing `resource_ref` so a partial edit does not clobber required fields; non-JSON `--ref` updates `github_repo.resource_ref.ref`.
- `server/cmd/server/router.go` exposes `/api/projects` plus `/api/projects/{projectId}/resources` routes.
- `server/pkg/db/queries/project_resource.sql` is the CRUD query surface for `project_resource` rows.
- Project resources are written into `.multica/project/resources.json` for agent workdirs.
- `github_repo.resource_ref.ref` is lifted into daemon `RepoData.Ref` by `server/internal/handler/daemon.go`; `server/internal/daemon/daemon.go` stores it per task, and `server/internal/daemon/health.go` uses it as the default `/repo/checkout` ref when the checkout request does not explicitly pass one.
- `server/internal/handler/daemon.go` applies `project.repo_inheritance_policy` consistently to issue, chat, and quick-create claims. Unknown policy values fail closed and never enable workspace repo fallback.
- Claims also carry `repo_source=project|workspace_fallback|none`. Daemons advertising `project-repo-scope-v1` register an active task scope: a `project` source or `project_only` policy rejects workspace repositories even if they remain cached or workspace-configured; older daemons cannot claim `project_only` tasks.
- A project's `description` is injected as durable context for every task in the project. The claim handler (`server/internal/handler/daemon.go`) reads `proj.Description` onto the claim response (`ProjectDescription`, `server/internal/handler/agent.go`); the daemon carries it through `Task` (`server/internal/daemon/types.go`) and `TaskContextForEnv` (`server/internal/daemon/execenv/execenv.go`) into the brief's `## Project Context` section (`server/internal/daemon/execenv/runtime_config.go`) and into `.multica/project/resources.json` as `project_description` (`server/internal/daemon/execenv/context.go`).
