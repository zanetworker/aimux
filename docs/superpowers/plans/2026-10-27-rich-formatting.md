# Rich formatting in both clients. Implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Claude's replies and the owner's messages render with full formatting in the iPhone app and the web client (headings, nested and numbered lists, task lists, tables, code blocks with copy, quotes, rules, emphasis, highlighted links), references become links (GitHub PRs, issues and commits of the session's repo, Jira keys, repo file paths, bare URLs), media links become cards, and Claude Code's saved wrappers disappear from history bubbles.

**Architecture:** One shared definition of the result: JSON fixtures in `api/testdata/clients/markdown/` (Markdown in, a normalized block tree out) and `api/testdata/clients/references/` (text plus repo context in, spans out). The web client gets a small Markdown parser of its own (no dependencies) and a DOM renderer that never uses `innerHTML`; the iPhone app parses with Apple's `AttributedString(markdown:)` full syntax and normalizes its `PresentationIntent` into the same tree, then renders SwiftUI views. The server adds the session's repo (web URL and branch) and a configured Jira base URL so references can link.

**Tech Stack:** Go 1.25; Swift 6/SwiftUI iOS 27 (Foundation Markdown, AVKit for inline video); plain ES modules with `node --test`; Playwright for the web check.

**Spec:** this plan is the spec; owner decisions from the conversation: "superb formatting, connecting to metadata, links well highlighted, best in class"; references as links (yes); "videos, events, everything gets the right formatting"; "fix formatting, the bullets, the tables, code everything". Controller's calls, stated to the owner before planning: built-in parser on iOS, own parser on web, shared fixtures; media as cards (no remote thumbnails: the web CSP allows `img-src 'self' data:` only, and loading third-party thumbnails would leak the phone's address); "events" read as the chat's own items looking polished plus wrapper cleanup.

## Global Constraints

- Branch `feat/clients`, worktree `aimux-worktrees/clients`. Commit with `&&` after tests pass; never push; never `git stash`. Trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Web: no framework, no build step, no npm dependencies, CSP unchanged (`default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:`). Server text never reaches `innerHTML`; build DOM with `textContent` and `createElement` only. Remove the old regex `renderMarkdown`.
- iOS: no new package dependencies; logic (parse, normalize, references) in AimuxKit with no SwiftUI; views in `App/`.
- Links: only `http:` and `https:` become tappable, everywhere. References never invent a URL when the repo or Jira base is unknown: they stay plain text.
- Visual system: near-monochrome; links in the platform tint (iOS `.tint`, web `--link` token: `#0a66d6` light, `#4c9bff` dark) with underline on web, the only colour besides status dots; code blocks on a neutral surface; tables with hairline borders. Light and dark.
- Bidi: paragraphs and list items take their direction from their text (`dir="auto"` on web; natural alignment on iOS); code is always left-to-right.
- Fixtures invented: no real session text, paths, repos, Jira keys from the owner's work.
- Docs updated in the last task.

## Review Focus

- **Streaming half-written Markdown** (an unclosed ``` fence, a table whose rows are still arriving, `**bold` without its end): renders sensibly at every prefix, never throws, and an unclosed fence renders as code to the end. Pinned by fixtures `stream-unclosed-fence.json`, `stream-partial-table.json`, `stream-open-emphasis.json` in Task 1.
- **Wide content on a phone:** a 12-column table and a 300-character code line scroll sideways inside their block; the page itself never scrolls sideways. Pinned by the Task 3 and Task 4 screenshots at 390 pt width.
- **Hostile text:** `[x](javascript:alert(1))`, raw `<script>`, `<img onerror>`, and HTML entities render as inert text, links with other schemes are plain text. Pinned by fixture `hostile.json` in Task 1 and a Playwright assertion in Task 3.
- **References that are not references:** `#1` in "step #1", a 7-hex word that is English ("deadbee" is fine, "cafe" too short), `ABC-1` in an unknown project, a path that does not exist in the repo: no false links where the repo context is missing; with context, only the documented patterns link. Pinned by `references/false-positives.json` in Task 2.
- **Very long replies** (8 KiB, 200 list items, a 300-row table): parse and render without visible lag; the parser is linear. Pinned by a timing test in Tasks 3 and 4 (parse of an 8 KiB fixture under 20 ms on the test machine).

## Task 1: Shared Markdown fixtures and the block tree

**Files:**
- Create: `api/testdata/clients/markdown/*.json`, a "Markdown" part in `api/testdata/clients/README.md`

**Interfaces:**
- Produces the normalized tree both clients must output, one JSON object per block, in order:
  - `{type:"heading", level:1-6, inlines:[…]}`
  - `{type:"paragraph", inlines:[…]}`
  - `{type:"list", ordered:bool, start:int|null, tight:bool, items:[{task:null|true|false, blocks:[…]}]}` (nested lists live in an item's blocks)
  - `{type:"code", lang:string|null, text:string}` (text without the trailing newline)
  - `{type:"quote", blocks:[…]}`
  - `{type:"table", align:["left"|"center"|"right"|null…], header:[[inlines]…], rows:[[[inlines]…]…]}`
  - `{type:"rule"}`
  - inlines: `{t:"text", v}`, `{t:"code", v}`, `{t:"em", c:[…]}`, `{t:"strong", c:[…]}`, `{t:"del", c:[…]}`, `{t:"link", href, c:[…]}` (only http/https; other schemes become their text), `{t:"br"}` (hard break only)
  - Adjacent text runs are merged; soft line breaks become a single space; HTML is never interpreted (raw HTML is text).
- Coverage is GitHub-flavoured Markdown as Claude Code writes it: ATX headings (Setext optional, not required), `-`/`*`/`+` and `1.`/`1)` lists with nesting by indentation, task items `- [ ]`/`- [x]`, fenced code with ``` and ~~~ and a language, indented code is NOT required (Claude does not emit it; treat as paragraph), block quotes, pipe tables with alignment row, `---`/`***` rules, inline code, `*`/`_` emphasis, `**`/`__` strong, `~~` strikethrough, `[text](url)`, `<https://…>` autolinks, and bare `https://…` URLs (GFM autolink extension) with trailing punctuation `.,;:!?)` excluded.

- [ ] **Step 1: Write fixtures** `{name, markdown, blocks}`: `headings.json`, `paragraphs-and-breaks.json`, `emphasis.json` (nesting, `snake_case_word` not emphasis), `lists-bullets.json`, `lists-numbered.json` (start 3, `1)` style), `lists-nested.json` (3 levels, mixed ordered/unordered), `lists-tasks.json`, `code-fences.json` (lang, no lang, ~~~, fence inside a list item), `inline-code.json` (backticks containing `*` and links), `quotes.json` (nested, with a list inside), `tables.json` (alignment, inline code and links in cells, escaped `\|`, ragged rows padded with empty cells), `rules.json`, `links.json` (inline, autolink, bare URL with trailing `.` and `)`, a URL inside code stays code), `hostile.json` (javascript: link, `<script>`, `<img onerror>`, `&lt;` entities: entities are decoded to text, tags stay literal text), `arabic.json`, `stream-unclosed-fence.json`, `stream-partial-table.json` (header and alignment row only, then one partial row), `stream-open-emphasis.json` (`**bold` unclosed renders as text `**bold`), `claude-reply.json` (a realistic invented Claude reply mixing a heading, bullets, a table, a code block and a link).
- [ ] **Step 2: Write the README "Markdown" part:** the tree, every rule above, and the streaming rules.
- [ ] **Step 3: Commit** `feat(clients): shared Markdown fixtures`.

## Task 2: References: server repo context, Jira base, shared fixtures

**Files:**
- Modify: `internal/remoteapi/api.go`, `internal/remoteapi/local.go` (optional `repo` on `SessionDetail`), `internal/config/config.go` (`RemoteAccessConfig.Links{ JiraBase string yaml:"jira_base"; JiraProjects []string yaml:"jira_projects" }`), `api/remote-v1.openapi.yaml` (`repo: {web: uri, branch: string}` optional on `SessionDetail`; optional `links: {jiraBase: uri, jiraProjects: [string]}` on `Health`), `docs-site/src/content/docs/guides/remote-access.mdx`
- Create: `internal/gitremote/gitremote.go` (+ test): `func WebURL(remote string) (string, bool)` converts `git@github.com:o/r.git`, `https://github.com/o/r(.git)`, `ssh://git@github.com/o/r.git`, and the same for `gitlab.com` and self-hosted GitLab (`git@gitlab.example.com:group/sub/r.git`) to `https://host/path` without `.git`; anything else false. `func Lookup(dir string) (web, branch string, ok bool)` reads `origin` from `git -C dir config --get remote.origin.url` and the branch from `git -C dir rev-parse --abbrev-ref HEAD` with a 2 s timeout, cached per dir for 60 s.
- Create: `api/testdata/clients/references/*.json`, a "References" part in the fixtures README

**Interfaces:**
- Reference spans, applied to text inlines only (never inside code or existing links): `{t:"ref", kind:"pr"|"commit"|"jira"|"path"|"url", v:text, href}`; when no target can be built, the text stays text.
  - `#123` (preceded by start or whitespace or `(`, followed by a word boundary, 1-6 digits) → `{repo.web}/issues/123` (GitHub redirects issues to PRs; GitLab: `/-/issues/123`); also `owner/repo#123` → that repo on the same host. Not linked when the preceding word is `step`, `item`, `option`, `line`, `no.` or `number` (case-insensitive), so "step #1" stays text.
  - commit: 7-40 lowercase hex as a whole word containing at least one digit and one letter → `{repo.web}/commit/{sha}` (GitLab `/-/commit/`).
  - Jira: `[A-Z][A-Z0-9]+-[0-9]+` whole word whose project part is in `jiraProjects` → `{jiraBase}/browse/{KEY}`; both come from config (`remote_access.links.jira_base`, `remote_access.links.jira_projects`) via `Health.links`. An empty project list links nothing, so `UTF-8` or `ISO-9001` never become links.
  - path: a relative path with a slash and a file extension, optional `:line` (e.g. `internal/api/server.go:42`), or the same inside inline code → `{repo.web}/blob/{branch}/{path}#L{line}` (GitLab `/-/blob/`); absolute paths starting with the session's project folder are made relative first; other absolute paths stay text.
- Fixture format: `{name, context:{repo:{web,branch}|null, jiraBase|null, project|null}, text, spans:[…]}`.

- [ ] **Step 1: Failing Go tests** for `WebURL` (each form above, and an unknown host) and `Lookup` (a temp repo with a remote; no remote; not a repo); detail carries `repo` for a session whose project is a repo, omits it otherwise; health carries `links` only when `jira_base` is configured. Contract test validates both.
- [ ] **Step 2: Implement; run** `go build ./... && go vet ./... && go test ./... -timeout 60s`.
- [ ] **Step 3: Fixtures:** `prs.json`, `commits.json`, `jira.json`, `paths.json` (relative, with line, inside inline code, absolute under project, absolute elsewhere), `gitlab.json`, `no-context.json` (nothing links), `false-positives.json` (`step #1`, `option #2`; `cafe`, `deadbeef` (no digit) not commits; `UTF-8` and `ISO-9001` not Jira when not in `jiraProjects`; a path without an extension not linked). The fixture context carries `jiraProjects:[…]`, and `Health.links` is `{jiraBase, jiraProjects}`.
- [ ] **Step 4: README part; commit** `feat(remote): repo context and Jira links for references`.

## Task 3: Web parser, references and renderer

**Files:**
- Create: `clients/web/webapp/markdown.mjs` (`parseMarkdown(src) -> blocks`), `clients/web/webapp/references.mjs` (`linkReferences(blocks, context) -> blocks`), `clients/web/webapp/render.mjs` (`renderBlocks(blocks) -> DocumentFragment`), tests `markdown.test.mjs`, `references.test.mjs`
- Modify: `clients/web/webapp/chatView.mjs` (replies and user bubbles use the new path; remove `renderMarkdown`), `clients/web/webapp/app.css`, `clients/web/webclient_test.go` (served-module list), `.claude/CLAUDE.md` file list

**Interfaces:** the tree and spans from Tasks 1 and 2. Rendering: headings scaled (h1 20px … h6 15px, semibold), lists with proper markers and nesting indentation, task items with read-only checkboxes, code blocks with a language label, a copy button (`navigator.clipboard.writeText`) and horizontal scroll, tables inside a horizontally scrolling wrapper with sticky header row, quotes with a left rule, links underlined in `--link`, references styled as links with a small kind hint in their `title` attribute. User bubbles: same renderer, inline-only plus links and line breaks (no headings or tables), so pasted code still shows as code.

- [ ] **Step 1: Failing tests:** every `markdown/*.json` and `references/*.json` fixture (directory read with a count guard); a linear-time check (8 KiB `claude-reply` repeated, parse under 20 ms); the renderer never sets `innerHTML` (a test that renders `hostile.json` into a JSDOM-free fake document, or assert by static scan of `render.mjs` for `innerHTML`/`insertAdjacentHTML`).
- [ ] **Step 2: Implement; run** `node --test "clients/web/webapp/*.test.mjs"` and `go test ./clients/web/`.
- [ ] **Step 3: Playwright check** (scratchpad, mocked `/v1`): a reply with every block type renders; the table and a long code line scroll inside their block while `document.documentElement.scrollWidth <= innerWidth`; copy button copies; `hostile.json` text is inert (no dialog, no `<script>` element in the DOM); a `#12` reference links to the mocked repo. Screenshots 390x844 light and dark.
- [ ] **Step 4: Commit** `feat(web): full Markdown, references and code blocks in the chat`.

## Task 4: iPhone parser normalization, references and renderer

**Files:**
- Create: `clients/ios/AimuxKit/Sources/MarkdownBlocks.swift` (`func markdownBlocks(_ src: String) -> [MDBlock]` from `AttributedString(markdown:options: .init(interpretedSyntax: .full))` and its `presentationIntent`/`inlinePresentationIntent`/`link` runs; streaming rules from the README applied as pre-processing where Foundation differs, e.g. close an unclosed fence before parsing), `References.swift` (`func linkReferences(_ blocks: [MDBlock], context: ReferenceContext) -> [MDBlock]`), tests `MarkdownBlocksTests.swift`, `ReferencesTests.swift` reading the shared fixtures (folder guard)
- Create: `clients/ios/App/Components/MarkdownView.swift` (renders `[MDBlock]`: headings, lists, tasks, code blocks with language label, copy button and horizontal `ScrollView`, tables in a horizontal `ScrollView` with `Grid`, quotes, rules; links tinted and tappable, opened with `openURL` only for http/https)
- Modify: `TimelineRows.swift` (`ReplyText` uses `MarkdownView`; remove `replySegments`/`replyInline` if unused, keeping their tests' behaviour covered by the new fixtures), user bubbles inline-only like the web, `SessionDetail.swift` (decode `repo`), the health decode (`links`), `project.yml` build +1

**Interfaces:** Swift `MDBlock`/`MDInline` enums mirroring the tree exactly (Equatable, Codable for fixture comparison).

Where Foundation's parser cannot produce a fixture's expected tree (e.g. bare URL autolinks, which Foundation does not link), post-process the inlines in AimuxKit to match the README; the fixture is the authority.

- [ ] **Step 1: Failing tests** for every fixture, a parse-time test (8 KiB under 20 ms on the simulator), link scheme filtering.
- [ ] **Step 2: Implement; full xcodebuild test; swiftlint --strict.**
- [ ] **Step 3: Screenshots** (14 Pro iOS 27 `BDA646FE-5717-4244-A935-C3917E94689F`, light and dark, and one AX5) of a demo chat containing every block type, a wide table and a long code line; compare with the web screenshots from Task 3.
- [ ] **Step 4: Commit** `feat(ios): full Markdown, references and code blocks in the chat`.

## Task 5: Media cards

**Files:** shared fixture `api/testdata/clients/references/media.json` (README note); `clients/web/webapp/references.mjs`/`render.mjs`; `clients/ios/AimuxKit/Sources/References.swift`, `clients/ios/App/Components/MediaCard.swift`.

**Interfaces:** a paragraph whose only content is a link (or a bare URL) to media becomes `{type:"media", kind:"youtube"|"vimeo"|"video"|"image"|"audio", href, title}` where `title` is the link text or the host plus last path segment; YouTube (`youtube.com/watch?v=`, `youtu.be/`, `youtube.com/shorts/`), Vimeo (`vimeo.com/<digits>`), video files (`.mp4 .mov .m4v .webm`), images (`.png .jpg .jpeg .gif .webp`), audio (`.mp3 .m4a .wav`). Inline media links inside prose stay links. Cards show a kind glyph, the title and the host; no remote thumbnails. iPhone: tapping a `video` or `audio` card plays it inline with `AVKit` `VideoPlayer` (loads only on tap); other kinds open in Safari. Web: every card opens the URL in a new tab (`rel="noopener noreferrer"`).

- [ ] **Step 1: Fixture and failing tests in both clients.**
- [ ] **Step 2: Implement; run both clients' suites; screenshots of the cards.**
- [ ] **Step 3: Commit** `feat(clients): media cards`.

## Task 6: Wrapper cleanup and chat item polish

**Files:** `clients/web/webapp/timeline.mjs` or `chatView.mjs`, `clients/ios/AimuxKit/Sources/...` (user text shown in bubbles), shared fixture cases.

- User bubbles show the message without Claude Code's saved wrappers (`The user sent a new message while you were working:` prefix; `<pasted_content …>` tags, keeping the content; `[Image #n]` placeholders become a small "Image" chip). Reuse the existing unwrap from the saved-prompt match (`api/testdata/clients/terminal/saved-prompt-match.json` rules); add display fixtures.
- Thought text, step targets and plan entries render inline Markdown (code spans, emphasis, links) through the same inline renderer; step targets that are paths get repo links.
- Error items render their text as inline Markdown.

- [ ] **Step 1: Failing tests (both clients); implement; run suites; screenshots.**
- [ ] **Step 2: Commit** `feat(clients): clean user bubbles; formatted chat items`.

## Task 7: Docs

**Files:** `clients/ios/README.md`, `clients/ios/docs/project-context.md`, `.claude/CLAUDE.md`, `docs-site/src/content/docs/guides/remote-access.mdx` (formatting, references, the `links` config: `remote_access.links.jira_base`, `jira_projects`), `RELEASE_NOTES.md`.

- [ ] **Step 1: Update; verify file lists; `cd docs-site && npm run build`.**
- [ ] **Step 2: Commit** `docs: rich formatting`.
