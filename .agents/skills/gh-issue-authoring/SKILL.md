---
name: gh-issue-authoring
description: >-
  Interview the user, then write and file a short GitHub issue — a bug report,
  a feature request, or a task. Use when the user wants a GitHub issue opened,
  drafted, or cleaned up, or invokes `/gh-issue`.
---

# GitHub Issue Authoring

A GitHub issue is read in the browser, in a list of thirty others. It wins attention with a title that triages itself and a body someone scans in under a minute. **Short and literal beats complete prose**: bullets, code fences, exact values. Anything that does not change what a maintainer does next stays out.

You start from what the user gives you, which is almost never enough. The interview is the work; the draft is a transcription of it.

## 1. Pick the type

_Done when the user has named the type, and the repo's own template for that type is in hand or known to be absent._

Ask the user which type it is, and say what each one means, since picking wrong is the common failure:

- **Bug** — something built behaves wrong. Read [`references/bug.md`](references/bug.md).
- **Feature** — something is missing and a user would want it. Read [`references/feature.md`](references/feature.md).
- **Task** — work with no user-facing behaviour of its own: a refactor, an upgrade, a chore, docs. Read [`references/task.md`](references/task.md).

Then look for `.github/ISSUE_TEMPLATE/` (`*.md`, `*.yml`) and `.github/ISSUE_TEMPLATE.md`. **The repo's template wins** over the reference file — fill its fields, keep its order, keep its headings. A form (`.yml`) gives you the exact labels and required fields to answer.

Also settle which repo the issue lands in when the session does not already say.

## 2. Interview

_Done when every field of the template has a specific answer, and you can state the issue in one sentence the user would agree with._

- **Read before you ask.** The code, the README, the failing test, the linked PR, existing open issues. Search first — `gh issue list --search "<keywords>" --state all` — because a duplicate is the fastest way to waste a maintainer's time. Say so if you find one and ask whether to comment there instead.
- **Ask in rounds, not in one dump.** Three to five questions per message, numbered, each carrying your best guess so the user corrects rather than composes.
- **Keep going until the picture is clear.** A vague answer earns a follow-up round: "it's slow" leaves the number, the input size, and the baseline open. Two or three rounds is normal.
- **Stop when the answers stop changing the issue.** Detail that no one acts on is not worth a question.

## 3. Draft

_Done when the body fills the template, holds nothing invented, and stays under 250 words._

- **Literal.** The exact command, the error verbatim, the version as `4.12.0`, the label in quotes. Every reference names the thing it points at.
- **Fence what is machine text**: commands, stack traces, config, log lines, JSON. Use a language tag. Fenced text does not count against the word budget.
- **Bullets and short sentences.** A section is one paragraph at most, and a list beats a paragraph whenever the items are parallel.
- **One issue, one outcome.** Two failures are two issues; two deliverables are two issues plus a tracking issue. Split it, cross-reference with `#123`, and tell the user.
- **Checkboxes for the work**, so progress is visible in the list: `- [ ] <observable statement>`.
- **Never invent a version, an environment, or a stack trace.** Unknown stays unknown, and the interview is where you fix that.
- **Apply the Voice section below** — this is written on the user's behalf.

## 4. Verify

_Done when every check passes._

- **The title triages the issue on its own** — area plus the broken behaviour or wanted outcome, sentence case, under 70 characters, no issue number, no `[BUG]` prefix unless the repo's template uses one.
- **The type is right** — wrong behaviour is a bug, missing behaviour a feature, invisible work a task.
- **A stranger can act on it** without asking a question that the interview already answered.
- **Expected and actual are two different observable facts** for a bug, and every acceptance box is checkable by using the product.
- **Reproduction is complete**: starting state, three to eight steps, version, environment.
- **Every link resolves** and says what it shows. Issue and PR references use `#123`, commits use the short SHA.
- **No placeholder survives** — no `<...>`, no quoted prompt from the template, no empty heading.

## 5. File it

_Done when the user has approved the draft and you have reported the issue number and URL._

- **Show the draft and wait for a go-ahead.** Filing is outward-facing: it notifies watchers and cannot be quietly undone.
- Write the body to a file and create it:

  ```sh
  gh issue create --repo <owner>/<repo> --title "<title>" --body-file <path>
  ```

  Add `--label`, `--assignee`, `--milestone`, or `--project` only for values the user named. A heredoc mangles Markdown, so keep the file.

- For an existing issue, use `gh issue edit <number> --body-file <path>`.
- Report the issue number and URL.
- **Screenshots and recordings are the user's to attach** — `gh` cannot upload them. Say which ones the issue still needs.

## Voice

Every sentence reads as the user's own:

- Friendly, confident, informal. Active voice, contractions, short sentences.
- Plain English a non-native speaker follows on the first read. Everyday words: "use" over "utilize", "start" over "commence". A rarer word earns its place only as a technical term, defined on first use.
- Lead with the point, state it once, stop. Background becomes a link.
- Claims stay proportionate: a bug is a bug, not a critical blocker, unless it blocks.
- Keep it plain: no hype, no filler openers, no hedging, no clauses chained with dashes, no stacked emoji.
