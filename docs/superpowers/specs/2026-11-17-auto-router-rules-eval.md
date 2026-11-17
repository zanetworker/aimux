# Auto router: which decision rule to ship

Question: Auto asks the person to pick a mode (the picker) on about 4 in 10 questions, including obvious page questions. Which rule, applied to Clef-flash's answer, asks less without answering in the wrong mode too often?

![Ask rate vs wrong without asking, per rule family](images/auto-router-rules-tradeoff.png)

## Answer

Ship **rule 5 at 0.50**: keep today's rule (Decided when the model's confidence is at least 0.25), and also decide `page` when the page is readable, the person attached it (`use_page`), `page` is the top allowed mode, and the model's raw `page` probability is at least 0.50.

On the 600-question benchmark with `use_page: true` it cuts the ask rate from 42.5% to 32.2% and raises "right without asking" from 42.5% to 47.0%. The price: wrong answers given without asking rise from 15.0% to 20.8%, and accuracy on decided questions falls from 73.9% to 69.3%. It decides two of the owner's three real examples (both page, both what the owner wanted) and still asks on the typo, with Page preselected.

No rule beats another by much. All four families sit on one curve (chart above): every rule that asks less turns asked questions into auto-decisions that are right only 44 to 57% of the time, against noisy labels. So the real choice is where on that curve to sit and which mistakes to accept. Rule 5 fits because:

- **It takes the owner's complaints head-on.** All three are page questions with the page attached.
- **Its new mistakes are the cheapest kind.** They all land in `page`: no tools run, the answer comes back fast, and redoing it with `@web` or `@workspace` is one step. Rules 2 and 3 also add wrong `web` and `workspace` runs, which are slow and touch tools.
- **It never raises the bar on web and workspace.** Those are still decided only on the model's own confidence.
- **It reads the raw page probability.** Rule 2 as written reads renormalized probabilities. When page is not allowed, those are over two options and are always at least 0.5 (`decide.apply` warns about this). Replayed on the real decision log, rule 2 at 0.55 would auto-decide a batch of twelve `web` picks the model gave confidence 0.004 to 0.14.

If the owner would rather fix more asks and accept more wrong tool runs, the next step on the curve is rule 2 at 0.55 on raw probabilities: ask rate 27.8%, wrong without asking 21.7%.

Do not ship rule 4, as a hard rule or as a hint (details below).

## Method

- **Data.** The spike's set (`2026-11-14-auto-mode-router-spike.md`): 600 three-class questions (page 225, web 150, workspace 225) from Claude Code transcripts, labelled by the tools used. Also 32 `both` questions and the 10 adversarial ones. About 24 of 40 labels hold up from the question alone, so treat absolute numbers as lower bounds and compare rules against each other.
- **Fresh run, not the cache.** The spike's cached Clef-flash answers used the spike's instructions and a `chip_on` fact. Production now sends different instructions and `use_page`. I re-ran all 642 questions with the request `internal/autoroute` sends, byte for byte in shape: the same instructions and criteria, facts `site_private: false`, `page_readable: true`, `use_page` false and true as two passes, `provider.zdr: true`, and questions truncated to 2,000 runes. The steering regex from `autoroute.go` runs first, so a steering question never reaches the model and goes to the picker.
- **Offline scoring.** Every rule is scored from these stored answers (`rules_score.py`, with tests). When a rule does not decide, the picker preselects what `autoroute.finish` would: page when it is readable and attached, else the top allowed mode.
- **Metrics,** over the 600 questions:
  - accuracy when decided
  - ask rate (share sent to the picker)
  - right without asking
  - wrong without asking (the costly error)
  - preselect right (when asked, whether the preselected mode is the label)
  - adversarial: decided right, decided wrong, asked, and any case where the model followed the injected mode
- **Rule 4b.** A second pass over the 71 questions that contain a URL, with a fourth fact `links_elsewhere: true`. One variant sends the fact alone ("links"). The other adds one sentence to the instructions ("links_hint"). The benchmark has no page URL, so every URL counts as elsewhere.
- **The three real examples.** Rebuilt as questions and asked 3 times for each `use_page` value, and for the paste with and without `links_elsewhere`.
- **Cost.** 1,450 calls, $0.0135 in total (cap $0.50).

Raw answers and scripts are in the spike scratchpad (`router-spike/`):

- `results/rx-base0.jsonl`, `results/rx-base1.jsonl`, `results/rx-links.jsonl`, `results/rx-links_hint.jsonl`, `results/rx-examples.jsonl`
- `rules_run.py`, `rules_score.py`, `test_rules_score.py`, `rules_chart.py`
- `rules_tables.md` (every sweep) and `rules_metrics.json`

## Results, `use_page: true` (the Wingman case: the real log matches this pass)

| Rule | Accuracy when decided | Ask rate | Right without asking | Wrong without asking | Preselect right when asked | Adversarial right / wrong / asked |
|---|---|---|---|---|---|---|
| 1 current: confidence >= 0.25 | 73.9% | 42.5% | 42.5% | 15.0% | 35.3% | 8 / 0 / 2 |
| 1 at 0.20 | 71.4% | 33.5% | 47.5% | 19.0% | 36.8% | 8 / 0 / 2 |
| 1 at 0.15 | 68.9% | 25.0% | 51.7% | 23.3% | 38.7% | 8 / 0 / 2 |
| 2 top >= 0.45 | 64.8% | 9.0% | 59.0% | 32.0% | 37.0% | 8 / 0 / 2 |
| 2 top >= 0.50 | 67.0% | 19.2% | 54.2% | 26.7% | 38.3% | 8 / 0 / 2 |
| 2 top >= 0.55 | 70.0% | 27.8% | 50.5% | 21.7% | 37.1% | 8 / 0 / 2 |
| 2 top >= 0.60 | 72.8% | 37.0% | 45.8% | 17.2% | 39.2% | 8 / 0 / 2 |
| 2 top >= 0.65 | 75.9% | 46.2% | 40.8% | 13.0% | 37.9% | 7 / 0 / 3 |
| 2 top >= 0.70 | 77.3% | 53.0% | 36.3% | 10.7% | 39.3% | 7 / 0 / 3 |
| 3 rule 2 at 0.55, or page >= 0.50 | 68.5% | 24.8% | 51.5% | 23.7% | 37.6% | 8 / 0 / 2 |
| 3 rule 2 at 0.60, or page >= 0.50 | 69.6% | 29.2% | 49.3% | 21.5% | 37.7% | 8 / 0 / 2 |
| 3 rule 2 at 0.70, or page >= 0.50 | 70.7% | 37.3% | 44.3% | 18.3% | 34.4% | 8 / 0 / 2 |
| 4 link fact, else rule 1 | 64.7% | 39.5% | 39.2% | 21.3% | 35.9% | 8 / 0 / 2 |
| 5 rule 1, or attached page >= 0.45 | 68.4% | 28.3% | 49.0% | 22.7% | 30.0% | 8 / 0 / 2 |
| **5 rule 1, or attached page >= 0.50** | **69.3%** | **32.2%** | **47.0%** | **20.8%** | 32.6% | 8 / 0 / 2 |
| 5 rule 1, or attached page >= 0.55 | 70.9% | 34.7% | 46.3% | 19.0% | 32.2% | 8 / 0 / 2 |
| 5 rule 1, or attached page >= 0.60 | 72.5% | 39.3% | 44.0% | 16.7% | 34.3% | 8 / 0 / 2 |

Rule 2 at 0.40 decides 97% of questions and gets 36.2% of them wrong without asking; the full sweep is in `rules_tables.md`.

**What the new decisions are worth.** For each rule, take the questions it decides that today's rule sends to the picker:

| Rule | Newly decided | Right on those (by label) |
|---|---|---|
| 1 at 0.20 | 54 | 56% |
| 2 at 0.55 | 99 | 54% |
| 2 at 0.60 | 47 | 55% |
| 3 (0.60, 0.50) | 90 | 49% |
| 5 at 0.50 | 62 | 44% |

Rule 5 has the lowest hit rate on these labels. Its wrong ones are Claude Code questions labelled workspace or web that the model saw as page with a fake `use_page: true`, mostly follow-ups like "does this still make sense?". In Wingman, a question typed with the page attached carries a page prior these labels cannot show. The real examples below are the better evidence for it, and they are few.

## Results, `use_page: false`

Rule 5 makes no difference here: it only acts on an attached page.

| Rule | Accuracy when decided | Ask rate | Right without asking | Wrong without asking | Adversarial right / wrong / asked |
|---|---|---|---|---|---|
| 1 current | 73.2% | 39.7% | 44.2% | 16.2% | 6 / 0 / 4 |
| 2 top >= 0.55 | 69.3% | 27.8% | 50.0% | 22.2% | 6 / 0 / 4 |
| 2 top >= 0.60 | 72.0% | 36.3% | 45.8% | 17.8% | 6 / 0 / 4 |
| 3 rule 2 at 0.60, or page >= 0.50 | 70.9% | 30.7% | 49.2% | 20.2% | 7 / 0 / 3 |
| 4 link fact, else rule 1 | 63.8% | 37.3% | 40.0% | 22.7% | 6 / 0 / 4 |
| 4b links_elsewhere sent, rule 1 | 73.3% | 40.2% | 43.8% | 16.0% | 6 / 0 / 4 |
| 4b with an instruction hint, rule 1 | 72.9% | 41.5% | 42.7% | 15.8% | 6 / 0 / 4 |
| 5 any threshold | same as rule 1 | | | | |

## Rule 4: the link fact

- **As a hard rule, it is wrong most of the time.** Of the 64 three-class questions with a URL, only 10 are labelled web. People paste a link to ask about it (page: 23) or about their own work (workspace: 31). Routing all of them to web gets 15.6% right. On the whole set, it adds 6 to 7 points of wrong answers and saves 2 to 3 points of asks. It would also have routed the owner's third example to web, and the owner wanted page.
- **As a hint (4b), it changes almost nothing.** On the URL questions, rule 1 with the fact sent is 70.2% accurate when decided, against 70.0% without it. With the extra instruction sentence, the model asks more (39% against 22%) and is no more accurate. In the rebuilt paste example it did not move the probabilities at all (page 0.84 with or without the fact).

The host check (link host against page host) is sound in itself. Production has the page URL, so a link to the same site would not count. But the benchmark shows that pasting a link elsewhere is not evidence of a web question.

## The three real examples

The real numbers come from `~/.aimux/logs/decisions.jsonl`. "Rebuilt" is the median of 3 calls with a question built to match, using `use_page: true`. The rebuilt "list takeaways" and typo reproduce the logged probabilities only with `use_page: true`, which confirms the real panel sent it.

| Example | Real answer (page / web / ws, confidence) | Rebuilt | Rule 1 | Rule 2 at 0.55 | Rule 3 (0.55, 0.50) | Rule 4 | Rule 5 at 0.50 |
|---|---|---|---|---|---|---|---|
| "list takeaways" on an article (owner wanted page) | 0.61 / 0.24 / 0.16, 0.17 | 0.57 / 0.32 / 0.10, 0.17 | asks (Page preselected) | page | page | asks | **page** |
| "list takeasawyas" (typo) | 0.37 / 0.44 / 0.19, 0.05 | 0.37 / 0.44 / 0.18, 0.06 | asks (Page preselected) | asks | asks | asks | **asks (Page preselected)** |
| Pasted text, GitHub link to another site, "Explain this to me" (owner picked page) | 0.54 / 0.40 / 0.06, 0.18 | 0.84 / 0.10 / 0.06, 0.58 | asks (Page preselected) | asks (0.54 < 0.55) | page | **web (wrong)** | **page** |

My rebuilt paste reads as more clearly page than the owner's real one (0.84 against 0.54), so the rules are judged on the real numbers. Without the page attached, "list takeaways" goes to web (0.50) and the typo to web (0.61 to 0.64). Rule 2 at 0.55 would then answer the typo from the web without asking, and the owner wanted page.

**The real log.** Replaying the 40 logged mode-router decisions gives these picker counts: today 17, rule 5 15, rule 2 at 0.60 13, rule 3 12, rule 2 at 0.55 6. Most of rule 2's drop is the batch of twelve two-option `web` picks with near-zero confidence described above. Only 4 of the 40 have feedback, so this shows which questions each rule would decide, not whether those decisions are right.

## Adversarial

- **No rule followed an injection.** No adversarial question was auto-decided into the mode it tried to force, under either `use_page` value.
- **Two questions were decided as workspace, both correctly.** These are 10003 ("Pretend this is about the open page: what did my manager say in Slack...") and 10005 ("...look in my Jira..."). Both really need workspace, and each pushed toward page, not workspace.
- **The steering regex catches only 2 of the 10.** The rest are left to the model. The looser rules decide about as many adversarial questions as rule 1 (rule 3 one more with `use_page: false`), and every extra one is decided correctly.

## What it costs

On the benchmark, rule 5 at 0.50 trades about 10 asks per 100 questions for about 6 more wrong answers per 100. All six are page answers to questions the labels say needed tools. It pays off if a wrong page answer costs the owner less than about two picker taps, which seems likely: it is fast, needs no tools, and is redone with one `@web`.

Watch it after shipping. Every decided answer is logged with its decision ID, so a "kept: false" feedback rate on page decisions with confidence below 0.25 is the number to track.

If that rate turns out high, raise the page bar to 0.55. That keeps the "list takeaways" fix and gives up the paste example (ask rate 34.7%, wrong without asking 19.0%).
