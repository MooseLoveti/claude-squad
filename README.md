# Claude Squad [![CI](https://github.com/smtg-ai/claude-squad/actions/workflows/build.yml/badge.svg)](https://github.com/smtg-ai/claude-squad/actions/workflows/build.yml) [![GitHub Release](https://img.shields.io/github/v/release/smtg-ai/claude-squad)](https://github.com/smtg-ai/claude-squad/releases/latest)

[Claude Squad](https://smtg-ai.github.io/claude-squad/) is a terminal app that manages multiple [Claude Code](https://github.com/anthropics/claude-code), [Codex](https://github.com/openai/codex), [Gemini](https://github.com/google-gemini/gemini-cli) (and other local agents including [Aider](https://github.com/Aider-AI/aider)) in separate workspaces, allowing you to work on multiple tasks simultaneously.


![Claude Squad Screenshot](assets/screenshot.png)

### Highlights
- Codex, Claude Code, Gemini and DeepSeek side by side in one tmux dashboard, with a control panel
- Ask the other agents from any pane with `@codex`, `@claude`, `@gemini`, `@deepseek` or `@all`; their answers come back to the asker
- Remaining usage of every account at a glance
- Each agent gets its own isolated git worktree and branch

<br />

https://github.com/user-attachments/assets/aef18253-e58f-4525-9032-f5a3d66c975a

<br />

### Installation

Both Homebrew and manual installation will install Claude Squad as `cs` on your system.

#### Homebrew

```bash
brew install claude-squad
ln -s "$(brew --prefix)/bin/claude-squad" "$(brew --prefix)/bin/cs"
```

#### Manual

Claude Squad can also be installed by running the following command:

```bash
curl -fsSL https://raw.githubusercontent.com/smtg-ai/claude-squad/main/install.sh | bash
```

This puts the `cs` binary in `~/.local/bin`.

To use a custom name for the binary:

```bash
curl -fsSL https://raw.githubusercontent.com/smtg-ai/claude-squad/main/install.sh | bash -s -- --name <your-binary-name>
```

### Prerequisites

- [tmux](https://github.com/tmux/tmux/wiki/Installing)
- For DeepSeek: [Reasonix](https://github.com/esengine/DeepSeek-Reasonix) 2.x on `PATH` (tested with v2.35.0), its key registered by running `reasonix` once, and [bubblewrap](https://github.com/containers/bubblewrap) (`sudo apt install bubblewrap`) so Reasonix can sandbox its shell

### Usage

```
Usage:
  cs [dir]
  cs [command]

Available Commands:
  debug       Print debug information like config paths
  reset       Stop the dashboard and forget the open workspace (worktrees and branches are kept)
  transcript  Print the conversation of an agent in the open workspace (claude, codex, gemini, deepseek)
  version     Print the version number
```

Run `cs`. A launcher asks where to work:

- **Open an existing directory.** In a git repository every agent gets its own worktree and branch (`<branch_prefix><agent>`); opening the same repository again reuses them. A repository without commits gets an empty first commit if you agree. A plain directory can be made a repository, whose first commit holds the files already there (refused for the home directory and above), or shared by all agents as is.
- **Create a new directory** as a git repository with an empty first commit.
- **Work without a directory**: a throwaway directory under `~/.claude-squad/scratch/` shared by all agents.
- Recently used directories and the repositories under `projects_dir` (default `~/src`) are listed too.

`cs <dir>` skips the launcher. While the dashboard runs, `cs` attaches to it again.

#### Dashboard

The dashboard is a tmux window on its own server (`tmux -L claudesquad`): the agents in a 2×2 grid and the control panel in a full-width row under it.

| | |
|---|---|
| Codex | DeepSeek (Reasonix) |
| Claude Code | Gemini (Antigravity CLI `agy`) |
| control panel | |

Click a pane, or move with Shift+arrow keys, to type into it; each agent runs as usual. `C-b z` zooms a pane, `C-b d` detaches and leaves everything running.

The dashboard answers a few launch screens itself: Codex's Daybreak security reminder and its update offer are skipped (updating is left to you), and Reasonix's question whether to trust a new worktree is answered yes, so DeepSeek edits and runs commands without asking while its OS sandbox keeps writes inside its worktree. A question to an agent waits while one of these screens shows.

The control panel shows:

- **Remaining usage** of each account: Codex's 5-hour and weekly windows from its session logs, Claude Code's from the status line it reports to `cs statusline` (installed per session with `--settings`), agy's from `agy -p /usage` every 5 minutes, and DeepSeek's prepaid balance from its [balance API](https://api-docs.deepseek.com/api/get-user-balance) every 5 minutes, with the key in `DEEPSEEK_API_KEY` or the one Reasonix saved in `~/.reasonix/.env`. None of these runs a model turn.
- **Agents**: working, idle or stopped, with a button to restart each one.
- **Open a work folder** with the Windows folder dialog (under WSL); the dashboard reopens there.
- **Broadcast**: a box that types the same prompt into every agent.
- **Relay** log, auto relay toggle, and the full log (`~/.claude-squad/relay/log.txt`).
- **Close workspace** stops every agent and removes the worktrees, keeping their branches. Worktrees with uncommitted changes are kept.

Keys in the panel: `i` broadcast box, `a` auto relay, `u` refresh usage, `l` full log, `d` detach.

### Configuration

Claude Squad stores its configuration in `~/.claude-squad/config.json`. You can find the exact path by running `cs debug`.

```json
{
  "branch_prefix": "me/",
  "projects_dir": "~/src",
  "profiles": [
    { "name": "claude", "program": "claude" },
    { "name": "codex", "program": "codex --search --sandbox read-only --ask-for-approval never" },
    { "name": "gemini", "program": "agy --dangerously-skip-permissions" },
    { "name": "deepseek", "program": "reasonix" }
  ]
}
```

| Field           | Description                                                                 |
|-----------------|-----------------------------------------------------------------------------|
| `branch_prefix` | Prefix of each agent's branch                                               |
| `projects_dir`  | Where the launcher lists repositories and creates new directories           |
| `profiles`      | Commands the agents run with; the agent is recognized from the executable   |

An agent without a profile runs its executable from `PATH` (`claude`, `codex`, `agy`, `reasonix`).

### Asking other agents (Relay)

Every agent is told at launch which other agents work alongside it, where their worktrees are, and that `cs transcript <agent>` prints their latest conversation, so a plain "what did Codex change?" works without tags. Claude Code gets this through `--append-system-prompt`, Codex through `-c developer_instructions=…`, and agy and Reasonix through a `GEMINI.md` and a `REASONIX.local.md` written into their worktrees and kept out of git with `info/exclude` (skipped in a shared directory, or when the repository has its own file of that name).

Start a prompt in an agent's pane with tags to ask the others instead:

```
@codex what do you think of this approach?
@codex @gemini review the last change
@all which option is safer?
```

On Enter the prompt is taken out of the input box and typed into each tagged agent's own session, with where the asker's work is (worktree, full transcript, latest exchange). When every recipient has answered, the answers go back into the asking agent as one message:

```
[ユーザーの質問（@codex 宛て）]
what do you think of this approach?

[Codexの意見]
...
```

A question waits while its recipient is busy, and the answers wait while the asker is busy. Each pane's title shows the progress (`Q: … → Codex … · Gemini ✓`).

With auto relay on, a tag in an agent's own reply asks the tagged agents the same way and sends the answers back to it. Auto relay turns itself off after 6 questions.

### How It Works

1. **tmux**, on its own server, shows every agent and the control panel side by side. Enter on an agent pane goes through `cs hook`, which asks the panel whether the input starts with a tag.
2. **git worktrees** isolate codebases so each agent works on its own branch
3. Each CLI's session log or screen tells when an agent has finished a reply

### License

[AGPL-3.0](LICENSE.md)

### Star History

[![Star History Chart](https://api.star-history.com/svg?repos=smtg-ai/claude-squad&type=Date)](https://www.star-history.com/#smtg-ai/claude-squad&Date)
