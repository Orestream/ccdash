# IDEAS — making ccdash more powerful

Brainstorm of features that go beyond the [`TODO.md`](./TODO.md) roadmap. The
theme: ccdash is an **orchestrator for many Claude agents at once**, and that
angle is where the biggest power gains live. Ideas are grouped and roughly
ordered by impact-per-effort within each group.

> Legend: 🚩 flagship / differentiating · 🔁 overlaps or extends an existing
> TODO item · 🆕 net-new.

## Flagship — multi-agent orchestration

- 🚩🆕 **Git worktree isolation per session.** Give each session its own
  `git worktree` + branch under the hood so parallel runs on the same repo
  can't clobber each other. Makes "run N agents in parallel on one project"
  *safe* and produces an isolated, reviewable branch per session. Foundational —
  unlocks most of the ideas below.
- 🚩🆕 **Fan-out / matrix runs.** Launch one prompt against N targets as a single
  unit: same prompt across multiple projects, or the same project with different
  models/modes. The dashboard already renders N live sessions; the missing piece
  is launching + tracking them as a group.
- 🚩🆕 **Compare mode.** Run an identical prompt on Opus vs Sonnet (or two prompt
  phrasings) side-by-side, then diff the resulting file changes and compare
  cost/latency. Turns ccdash into an eval harness for "which model/prompt is best
  for this task."
- 🚩🆕 **Session pipelines (DAG).** "When session A finishes, feed its output as
  session B's prompt." A simple linear chain (plan → implement → review) is
  already powerful; a small dependency graph is a serious agent-workflow tool.
- 🚩🆕 **Per-session diff viewer + one-click commit/PR.** Since the backend owns
  the working dir, show the actual file diff a session produced in the UI, then
  expose "Commit" / "Create PR" buttons. Closes the loop from prompt → reviewed
  change without leaving the dashboard. Pairs with worktree isolation.

## Automation & control

- 🆕 **Auto-approval rules engine.** Beyond the four permission modes: user-defined
  rules such as "auto-allow `Bash` matching `^git (status|diff|log)`, always deny
  `rm -rf`." Regex/glob on tool input. Makes unattended background runs
  trustworthy. (Extends `--allowedTools`/`--disallowedTools` from TODO.)
- 🆕 **Prompt queue per session.** Queue several turns up front; they run
  sequentially as each completes. Load up a session and walk away.
- 🆕 **Saved prompt templates / recipes.** Reusable parameterized prompts
  ("review this PR", "add tests for `$file`") with variable substitution,
  launchable into any project.
- 🆕 **Scheduled / cron sessions.** "Every morning, run this prompt in this repo."
  The backend already runs work in the background; a scheduler is a natural
  extension.

## Observability & safety

- 🔁 **Cost budgets with auto-stop.** Per-session / project / global caps that
  `Stop` the run when hit. TODO mentions *alerts*; *enforcement* is the powerful
  version. Ties into the existing `/api/usage/limits` endpoint.
- 🔁 **Run log / stderr capture + a "why did this fail" view.** Persist stderr and
  surface the last error inline on `error` status for debugging failed runs.
- 🔁 **Transcript search + export.** High-value once there are many sessions; cheap
  to build.

## Reach & collaboration

- 🆕 **PWA + push notifications.** Background sessions are the whole point —
  getting a phone notification when one hits `awaiting_input` / `awaiting_approval`
  and approving from your phone is a killer feature for long runs.
- 🔁 **Single-binary deploy.** Embed the built frontend in the Go binary so it's
  one artifact you can run on a remote box and drive from anywhere.

## Suggested priority

If optimizing for impact-per-effort:

1. **Git worktree isolation** (#1) — foundational; makes parallel runs trustworthy.
2. **Per-session diff viewer + commit/PR** (#5).
3. **Auto-approval rules engine** (#6).
4. **PWA + push notifications** (#13).

Worktree isolation in particular makes parallel runs safe, which is the entire
premise of the app.
