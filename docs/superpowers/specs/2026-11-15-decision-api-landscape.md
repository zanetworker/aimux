# Decision API landscape for the Wingman Auto-mode decider

## The job

Given a short user question plus three booleans, pick one of `page`, `web`, `workspace` (optionally `both`) and return a confidence. Baselines measured so far: Haiku 5.5 via `claude -p` at 62% on noisy labels (71% when confidence is at least 0.7), about 2.8 s and $0.28 per 1k calls; local Arch-Router 1.5B at 47% and 107 ms.

The input is tiny (roughly 150 to 300 tokens with the option descriptions), the output is one label, and we want a calibrated probability. That is exactly the shape of the new "decision model" category, which returns a typed choice plus a probability per option instead of generated text.

## Key findings

1. **The category is real and already on OpenRouter.** The OpenRouter catalog filtered to `output_modalities=decisions` lists 41 entries (aliases and `:free` variants included), about 30 distinct models from 17 makers, as of 2026-11-15 ([catalog API](https://openrouter.ai/api/v1/models?output_modalities=decisions)). The owner's "about 15" undercounts: OpenRouter's own Jev post counted 21 models from 13 makers on 2026-10-16 ([OpenRouter, What is Jev](https://openrouter.ai/blog/insights/what-is-jev/)).
2. **One request shape covers all of them.** `POST https://openrouter.ai/api/alpha/decisions` with `model`, `state` and `questions`; a `choice` question takes `criteria` (option key to description) and returns `choice`, `probabilities` (summing to 1.0) and `confidence` ([OpenRouter Decisions API reference](https://openrouter.ai/docs/api/reference/decisions)). Swapping models is a one-string change, so benchmarking 5 to 7 of them costs the same harness as benchmarking one.
3. **Price is negligible.** Decision models bill input tokens only, output is free. At 250 input tokens per call, Jev costs about $0.01 per 1k calls and Clef-flash about $0.005, versus the $0.28 we measured for Haiku via `claude -p` (prices from the [catalog API](https://openrouter.ai/api/v1/models?output_modalities=decisions); per-1k figures are our arithmetic).
4. **Latency claims are vendor-only.** The only published table is Cloudflare's: Clef-flash median 38.8 ms (p95 122.4 ms), Clef 209.3 ms, Jev 524.1 ms, measured by Cloudflare on its Decision Index suite ([Clef-flash model card](https://huggingface.co/Cloudflare/clef-flash); [Cloudflare blog](https://blog.cloudflare.com/clef-decision-models/)). OpenRouter reports `latency_last_30m: null` for every decision endpoint today ([example endpoint listing](https://openrouter.ai/api/v1/models/typesafe/jev-1.14/endpoints)), so we must measure ourselves, including the OpenRouter hop.
5. **Data handling is good by default.** 32 decision endpoints appear on OpenRouter's zero-data-retention list ([ZDR endpoints API](https://openrouter.ai/api/v1/endpoints/zdr)), and `"provider": {"zdr": true}` enforces it per request ([OpenRouter ZDR docs](https://openrouter.ai/docs/guides/features/zdr)). Not on that list: HeyGen, Google Gemini Decision 1, Parallel, Respan Span-02.
6. **Local is harder than it looks.** The open-weight decision models use a custom decision head and ship a CUDA serving runtime (SGLang for Clef, a patched OneJev runtime for Baseten's models), not GGUF for Ollama ([Clef-flash card](https://huggingface.co/Cloudflare/clef-flash); [Joey 1 card](https://huggingface.co/baselabs/joey-1); [Decider Flash v1.2 card](https://huggingface.co/perplexity-ai/pplx-decider-flash-v1.2)). On this Mac the practical local path is a small generative model under Ollama's JSON-schema `format` constraint ([Ollama structured outputs](https://docs.ollama.com/capabilities/structured-outputs)), which gives a label but no native probabilities.

## How to read the tables

- **Returns:** "choice + probs + conf" means the Decisions API contract above. "label (JSON)" means a generative model forced into a schema; confidence must be self-reported or derived from logprobs.
- **Access:** OR = callable today with the owner's existing OpenRouter key. Key = needs a separate vendor account. Local = runs on our hardware.
- **$/1k calls:** our estimate at 250 input tokens and, for generative models, 30 output tokens. Source prices are linked.
- **Latency:** every figure is a vendor claim unless marked otherwise.
- **Fit (1 to 5):** 5 = typed choice with probabilities, cheap, fast, low-friction for this exact job.

## A. Decision models (typed choice with probabilities)

All prices from the [OpenRouter catalog API](https://openrouter.ai/api/v1/models?output_modalities=decisions) unless noted; ZDR status from the [ZDR endpoints API](https://openrouter.ai/api/v1/endpoints/zdr); provider HQ and policy links from the [OpenRouter providers API](https://openrouter.ai/api/v1/providers).

| # | Model (OpenRouter id) | Vendor | Returns | Access | Price per 1M input (output free) | $/1k calls | Latency claim | Data | Weights / license | Fit |
| :-- | :-- | :-- | :-- | :-- | :-- | :-- | :-- | :-- | :-- | :-- |
| 1 | [Jev 1.14](https://openrouter.ai/typesafe/jev-1.14) (`typesafe/jev-1.14`, alias `~typesafe/jev-latest`) | TypeSafe | choice + probs + conf; also score, noul (yes/no), box | OR; early access since 2026-09-15 ([OpenRouter](https://openrouter.ai/blog/insights/what-is-jev/)) | $0.042 | ~$0.011 | 524.1 ms median, measured by Cloudflare ([Clef-flash card](https://huggingface.co/Cloudflare/clef-flash)) | ZDR on TypeSafe endpoint; [privacy policy](https://typesafe.ai/legal/privacy-policy) | Proprietary, no weights ([OpenRouter](https://openrouter.ai/blog/insights/what-is-jev/)) | 5: category reference, the API others copy |
| 2 | [Clef-flash](https://openrouter.ai/cloudflare/clef-flash) (`cloudflare/clef-flash`) | Cloudflare | choice + probs + conf, one logit per option then softmax ([DataLLM Lab](https://www.datallmlab.com/blog/decision-models.html), secondary) | OR (Cloudflare, DeepInfra fp8, PrimeIntellect); Key for Workers AI direct | $0.019 | ~$0.005 | 38.8 ms median, 122.4 ms p95, vendor ([card](https://huggingface.co/Cloudflare/clef-flash)) | ZDR on all three providers; Workers AI does not train on or retain content ([Cloudflare privacy](https://developers.cloudflare.com/workers-ai/privacy)) | 9B, Apache-2.0, Qwen3.5-9B base ([HF](https://huggingface.co/Cloudflare/clef-flash)) | 5: fastest claimed, cheapest, open weights |
| 3 | [Clef](https://openrouter.ai/cloudflare/clef) (`cloudflare/clef`) | Cloudflare | same as Clef-flash | OR; Key for Workers AI | $0.192 | ~$0.048 | 209.3 ms median, 238.6 ms p95, vendor ([blog](https://blog.cloudflare.com/clef-decision-models/)) | ZDR | 27B, Apache-2.0 ([HF](https://huggingface.co/Cloudflare/clef)) | 4: accuracy ceiling check for Clef-flash |
| 4 | [Clef Omni](https://openrouter.ai/cloudflare/clef-omni) | Cloudflare | same, plus audio and video input | OR | $0.15 | ~$0.038 | none published | ZDR | 30B-A3B MoE, Apache-2.0 ([HF](https://huggingface.co/Cloudflare/clef-omni)) | 2: multimodal we do not need |
| 5 | [Decider V1.3 (free)](https://openrouter.ai/perplexity/pplx-decider-v1.3:free) (`perplexity/pplx-decider-v1.3:free`) | Perplexity | choice + probs; also yes/no and rubric score ([DataLLM Lab](https://www.datallmlab.com/blog/decision-models.html), secondary) | OR, free tier | $0 | $0 | none published | ZDR | v1.3 weights not listed on OpenRouter | 4: free, but free tiers are usually rate-limited |
| 6 | [Decider V1.2](https://openrouter.ai/perplexity/pplx-decider-v1.2) (`perplexity/pplx-decider-v1.2`) | Perplexity | same | OR; Key for Perplexity direct (endpoint reported as `POST https://api.perplexity.ai/v1/decisions`, unverified, [LLM Reference](https://www.llmreference.com/model/pplx-decider-v1.2/perplexity-api), secondary) | $0.02 (cut from $0.04, [usagepricing](https://www.usagepricing.com/blueprint/activity/perplexity-ai-2026-10-15-decisions-api-price-cut), secondary) | ~$0.005 | none published | ZDR; [Perplexity privacy](https://www.perplexity.ai/hub/legal/privacy-policy) | 27B, Apache-2.0 ([HF](https://huggingface.co/perplexity-ai/pplx-decider-v1.2)) | 4: vendor claims 85.71% across 11 benchmarks vs Jev 84.51% ([DataLLM Lab](https://www.datallmlab.com/blog/decision-models.html), secondary) |
| 7 | [Decider Flash V1.2](https://openrouter.ai/perplexity/pplx-decider-flash-v1.2) / [V1.3 free](https://openrouter.ai/perplexity/pplx-decider-flash-v1.3:free) | Perplexity | same | OR; Local (heavy) | $0.01 / $0 | ~$0.0025 / $0 | none published | ZDR | 4B, Apache-2.0, Qwen3.5-4B base ([HF](https://huggingface.co/perplexity-ai/pplx-decider-flash-v1.2)) | 4: small enough to self-host later |
| 8 | [d1.1](https://openrouter.ai/liquid/d1.1) (`liquid/d1.1`, alias `~liquid/d1-latest`) | Liquid AI | choice, score or yes/no with probabilities | OR | $0.02 | ~$0.005 | none published | ZDR; [Liquid privacy](https://www.liquid.ai/privacy-policy) | Proprietary | 4: cheap second opinion from a non-Qwen lineage |
| 9 | [d1-ultra](https://openrouter.ai/liquid/d1-ultra) | Liquid AI | same | OR | $0.042 | ~$0.011 | none published | ZDR | Proprietary | 3: larger d1, 32K context is plenty |
| 10 | [Microsoft-Decision-1](https://openrouter.ai/microsoft/microsoft-decision-1) | Microsoft | calibrated probability per fixed option | OR (Azure) | $0.042 | ~$0.011 | none published | ZDR on Azure | Proprietary | 3: text only, fine for us; no differentiator |
| 11 | [GPT-6 Luna Decisions](https://openrouter.ai/openai/gpt-6-luna-decisions) | OpenAI | typed probabilistic answers | OR (OpenAI, Azure) | $0.10 | ~$0.025 | none published | ZDR on Azure endpoint only | Proprietary | 3: big-model accuracy check, 2.5x Jev price |
| 12 | [Gemini Decision 1](https://openrouter.ai/google/gemini-decision-1) | Google | true/false, choice (up to 255 options), score | OR (Google AI Studio) | $0.10 | ~$0.025 | none published | Not on ZDR list | Proprietary | 2: not ZDR via OpenRouter |
| 13 | [Mammoth 1](https://openrouter.ai/baseten/mammoth-1) / [Elephant 1](https://openrouter.ai/baseten/elephant-1) / [Joey 1](https://openrouter.ai/baseten/joey-1) | Baseten (Base Labs) | yes/no, choice, ordinal score with probabilities | OR; Local (CUDA runtime) | $0.10 / $0.035 / $0.01 | ~$0.025 / ~$0.009 / ~$0.0025 | none published | ZDR | 27B / 9B / 2B, MIT ([Joey 1 HF](https://huggingface.co/baselabs/joey-1)) | 4 (Joey 1): 2B, MIT, cheapest local candidate |
| 14 | [Drex v2.0](https://openrouter.ai/nace-ai/drex-v2.0), [Drex Max](https://openrouter.ai/nace-ai/drex-max), [Drex v1.5](https://openrouter.ai/nace-ai/drex-v1.5) | Nace.AI | typed questions with calibrated probability | OR (DeepInfra) | $0.06 / $0.06 / $0.04 | ~$0.015 / ~$0.015 / ~$0.01 | none published | ZDR | v1.5 9B, license "other" ([HF](https://huggingface.co/nace-ai/drex-v1.5)) | 2: no reason to prefer over Clef-flash |
| 15 | [Solar Decide](https://openrouter.ai/upstage/solar-decide) / [Solar Decide Flash](https://openrouter.ai/upstage/solar-decide-flash) | Upstage | choice, score, yes/no | OR | $0.05 | ~$0.0125 | Flash marketed as "low-latency", no number | ZDR | Proprietary | 3: alternative if Jev-compatible models disagree |
| 16 | [Reflex](https://openrouter.ai/morph/reflex) | Morph | choice, noul, score; state prefix-cached | OR | $0.042 ($0.002 cached) | ~$0.011 | none published | ZDR; US datacenters | Proprietary | 3: caching helps only if the instruction block is large |
| 17 | [Classify-1](https://openrouter.ai/scaledown/classify-1) | ScaleDown | choice, yes/no, ordered scale, calibrated probabilities | OR | $0.04 | ~$0.01 | none published | ZDR | Proprietary | 3: name matches the job, no evidence beyond that |
| 18 | [Tev1 4B Experimental](https://openrouter.ai/togethercomputer/tev1-4b-experimental) | Together AI | one option from 2 to 24 | OR | $0.042 | ~$0.011 | none published | ZDR | 4B, no license on the card ([HF](https://huggingface.co/togethercomputer/Tev1-4B-experimental)) | 2: experimental, no license |
| 19 | [Kev 4B](https://openrouter.ai/jaredpalmer/kev-4b) | Jared Palmer | Jev-compatible `/v1/systemone` contract | OR (SiliconFlow fp8) | $0.042 | ~$0.011 | none published | ZDR | LoRA plus pointer head on Qwen3.5-4B-Base, Apache-2.0 ([HF](https://huggingface.co/jaredpalmer/kev-4b)) | 2: hobby-scale |
| 20 | [HeyGen Decision 1](https://openrouter.ai/heygen/heygen-decision-1) / Turbo | HeyGen | noul, choice, score | OR | $0.14 / $0.09 | ~$0.035 / ~$0.0225 | none published | Not on ZDR list | Proprietary | 1: newest, pricier, no ZDR |
| 21 | [Span-02](https://openrouter.ai/respan/span-02) | Respan | choice, yes/no, score | OR | $0.01 | ~$0.0025 | none published | Not on ZDR list | Described as open weight; no HF id on OpenRouter | 2: cheap, no ZDR |
| 22 | [Decisions Fast](https://openrouter.ai/parallel/decisions-fast) | Parallel | typed answers after live web research | OR | $0.001 per request | $1.00 | none published | Not on ZDR list; US | Proprietary | 1: web research is the wrong tool for routing |

Also in the catalog but out of scope: Cloudflare [Clairic](https://openrouter.ai/cloudflare/clairic) (extraction), Respan [Span-01](https://openrouter.ai/respan/span-01) (behavior scoring), ScaleDown Summarize and Extract, and TypeSafe [Jev Router](https://openrouter.ai/typesafe/jev-router) (picks a generative model per request, not our labels).

## B. LLM routers (choose a model, not a label)

These pick which LLM answers a prompt. Our job is to pick a retrieval source, so most fit poorly unless they let us define custom labels.

| # | Name | Vendor | Returns | Access | How to call | Pricing | Latency claim | Data | License | Fit |
| :-- | :-- | :-- | :-- | :-- | :-- | :-- | :-- | :-- | :-- | :-- |
| 23 | Arch-Router 1.5B | Katanemo | route name from user-defined domain/action policies, as generated text ([paper](https://arxiv.org/abs/2506.16655)) | Local (installed: `hf.co/katanemo/Arch-Router-1.5B.gguf:Q4_K_M` in Ollama) | Ollama chat | free | <100 ms e2e, 50 ms on L40S, developer HN reply ([HN](https://hn.svelte.dev/item/44436031), secondary) | local | Katanemo research license ([LICENSE](https://huggingface.co/katanemo/Arch-Router-1.5B/blob/main/LICENSE)) | 2: measured 47% on our set |
| 24 | Not Diamond | Not Diamond | recommended model; trainable custom router | Key | SDK/API alongside your own gateway ([AWS Marketplace](https://aws.amazon.com/marketplace/pp/prodview-erda4iu26h4tm)) | free to 100k routing calls/month, then reported $0.001/call ([AWS Marketplace](https://aws.amazon.com/marketplace/pp/prodview-erda4iu26h4tm); [LLM Reference](https://www.llmreference.com/router/not-diamond), secondary) | not found | hosted, policy not checked | Proprietary | 2: custom router could learn our labels, but needs a training set and an account |
| 25 | Martian | Martian | routed completion | Key | OpenAI-compatible base URL `https://withmartian.com/api/openai/v1` ([developersdigest](https://www.developersdigest.tech/llm-gateways/martian), secondary) | not published ([LLM Reference](https://www.llmreference.com/router/martian), secondary) | not found | unknown | Proprietary | 1: returns an answer, not a decision |
| 26 | RouteLLM | LMSYS | strong vs weak model score | Local | Python library | free | n/a | local | Apache-2.0, last push 2024-08 ([repo](https://github.com/lm-sys/RouteLLM)) | 1: binary strong/weak only, stale |
| 27 | NVIDIA LLM Router blueprint + Prompt Task and Complexity Classifier | NVIDIA | task type and complexity scores from a DeBERTa multi-head classifier ([NGC](https://catalog.ngc.nvidia.com/orgs/nvidia/nemo/models/prompt-task-and-complexity-classifier)) | Local / NIM | blueprint services ([repo](https://github.com/NVIDIA-AI-Blueprints/llm-router)) | free | n/a | local | Apache-2.0 repo; classifier under NVIDIA Open Model License ([build.nvidia.com](https://build.nvidia.com/nvidia/llm-router/modelcard)) | 1: fixed task taxonomy; repo now points to NeMo Switchyard |
| 28 | Unify | Unify | routed completion | Key | not verified | not verified | not verified | not verified | Proprietary | 1: not researched further; same model-routing shape as Martian |

## C. Small generative models with structured output (control group)

Any OpenRouter model with `structured_outputs` can be forced to return `{"route": "page|web|workspace|both", "confidence": 0..1}`. The label is constrained; the confidence is self-reported, which is how Haiku produced our 71%-at-0.7 figure. Prices from the [OpenRouter models API](https://openrouter.ai/api/v1/models).

| # | Model | Price per 1M in / out | $/1k calls | Access | Fit |
| :-- | :-- | :-- | :-- | :-- | :-- |
| 29 | [Claude Haiku 5.5](https://openrouter.ai/anthropic/claude-haiku-5.5) via OpenRouter | $0.10 / $0.50 | ~$0.04 | OR | 3: same model as our baseline, minus the `claude -p` overhead; tells us how much of 2.8 s and $0.28 is the CLI |
| 30 | [Gemini 2.5 Flash Lite](https://openrouter.ai/google/gemini-2.5-flash-lite) | $0.10 / $0.40 | ~$0.04 | OR | 3: fast generative control |
| 31 | [GPT-5 nano](https://openrouter.ai/openai/gpt-5-nano) | $0.05 / $0.40 | ~$0.025 | OR | 2: reasoning model, latency risk |
| 32 | [Mistral Small 3.2 24B](https://openrouter.ai/mistralai/mistral-small-3.2-24b-instruct) | $0.10 / $0.30 | ~$0.034 | OR | 2: EU vendor ([Mistral HQ FR](https://openrouter.ai/api/v1/providers)) if region matters |
| 33 | [Qwen3.5 9B](https://openrouter.ai/qwen/qwen3.5-9b) | $0.021 / $0.031 | ~$0.006 | OR | 2: same base as Clef-flash, isolates what the decision head adds |

## D. Local options

| # | Option | What it needs | Returns | License | Fit |
| :-- | :-- | :-- | :-- | :-- | :-- |
| 34 | Ollama + small model (installed: `qwen3:1.7b`) with `format` JSON schema ([Ollama docs](https://docs.ollama.com/capabilities/structured-outputs)) | nothing new | constrained label, self-reported confidence | Qwen: Apache-2.0 | 3: zero cost, offline; probabilities need a logprob path the Ollama docs do not describe |
| 35 | Decider Flash v1.2 4B self-hosted | `requirements.txt` with torch and transformers, custom `[255, 2560]` decision head ([HF](https://huggingface.co/perplexity-ai/pplx-decider-flash-v1.2)) | choice + probabilities | Apache-2.0 | 3: real decision model locally; Apple Silicon support untested |
| 36 | Joey 1 2B self-hosted | patched OneJev runtime, one CUDA GPU ([HF](https://huggingface.co/baselabs/joey-1)) | choice + probabilities | MIT | 2: CUDA-only runtime rules out the Mac |
| 37 | Clef-flash 9B self-hosted | SGLang `dev-clef` image, tested on H200 ([HF](https://huggingface.co/Cloudflare/clef-flash)) | choice + probabilities, `/v1/systemone` | Apache-2.0 | 2: GPU server only |

## Recommendation: shortlist to benchmark

Run every OpenRouter candidate through one harness on the Decisions API with `"provider": {"zdr": true}`, a single `choice` question whose `criteria` describe `page`, `web`, `workspace` and `both`, and the three booleans written into `state`. Score accuracy overall and at `confidence >= 0.7`, and record our own p50/p95 latency from this Mac.

**Runnable now with the existing OpenRouter key**

1. **TypeSafe Jev 1.14** (`typesafe/jev-1.14`): $0.042/1M input, ~$0.011 per 1k calls; reference model of the category. ([OpenRouter](https://openrouter.ai/typesafe/jev-1.14))
2. **Cloudflare Clef-flash** (`cloudflare/clef-flash`): $0.019/1M, ~$0.005 per 1k; 38.8 ms median vendor claim; Apache-2.0 weights. ([OpenRouter](https://openrouter.ai/cloudflare/clef-flash))
3. **Perplexity Decider V1.3 free** plus **Decider Flash V1.3 free** (`perplexity/pplx-decider-v1.3:free`, `perplexity/pplx-decider-flash-v1.3:free`): $0; fall back to paid V1.2 at $0.02/1M if rate-limited. ([OpenRouter](https://openrouter.ai/perplexity/pplx-decider-v1.3:free))
4. **Liquid d1.1** (`liquid/d1.1`): $0.02/1M, ~$0.005 per 1k; non-Qwen lineage as a diversity check. ([OpenRouter](https://openrouter.ai/liquid/d1.1))
5. **Claude Haiku 5.5 via OpenRouter structured output** (`anthropic/claude-haiku-5.5`): ~$0.04 per 1k; control that separates model quality from `claude -p` overhead. ([OpenRouter](https://openrouter.ai/anthropic/claude-haiku-5.5))

**Needs a new key (only if the OpenRouter hop dominates latency)**

6. **Clef-flash on Cloudflare Workers AI direct** (`@cf/cloudflare/clef-flash` via `https://api.cloudflare.com/client/v4/accounts/$ACCOUNT/ai/run/...`, [Cloudflare blog](https://blog.cloudflare.com/clef-decision-models/)): same model at the edge, no OpenRouter hop; Workers AI does not train on or retain content ([privacy](https://developers.cloudflare.com/workers-ai/privacy)).

**Local**

7. **Ollama `qwen3:1.7b` with a JSON-schema `format`** (already installed, free): offline fallback for Auto mode when the network is down. Try **Decider Flash v1.2 4B** under transformers on Apple Silicon only if a local model with real probabilities becomes a requirement.

Skip for now: model routers (Not Diamond, Martian, RouteLLM, NVIDIA blueprint, Unify) pick an LLM rather than a label; Arch-Router already measured at 47%; HeyGen, Gemini Decision 1, Parallel and Span-02 are not on the ZDR list.

## Open questions

- **Our own latency.** No independent latency data exists for OpenRouter's decision endpoints today; the benchmark has to produce it.
- **Free-tier limits.** OpenRouter does not state rate limits for the Decisions API in its reference ([docs](https://openrouter.ai/docs/api/reference/decisions)); confirm before relying on the Perplexity `:free` variants in production.
- **`both` as an option.** Decision models return one `choice`; a `both` label can be a fourth option, or we can ask two `noul` questions (needs web, needs workspace) and read both probabilities. Benchmark both framings.

<!-- no-images: landscape comparison tables; a figure would duplicate the tables -->
