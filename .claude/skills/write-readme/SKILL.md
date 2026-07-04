---
name: write-readme
description: Write or rewrite a README (or its top sections) so a developer new to THIS project gets oriented and running fast. The reader is a competent developer, possibly early-career — never a non-technical reader. Use when the user says "write the README", "rewrite the README", "improve the intro/quickstart", "make the README clearer", or "the README is too dense". Teaches plain-language rules (short sentences, active voice, no filler — plainlanguage.gov), a two-tier vocabulary rule (baseline developer terms like API/HTTP/SQL used bare; only project-specific or niche terms like SLSA/pgx/OIDC glossed or linked), orientation-not-reference structure (one-sentence what-it-is, who-it's-for, copy-paste quickstart, minimal config, help, licence — diataxis.fr), and Google-developer-style tone. Uses checkable rules, not vibes: intro ≤ 3 sentences, a quickstart runnable by copy-paste alone (verified, not assumed), an allowlist-filtered acronym grep over the top sections, and a final read-aloud pass. Progressive disclosure — depth moves down or into linked docs, it is not deleted.
---

# /write-readme — a README a newcomer can actually follow

A README is **orientation, not reference**. Its job is to get a reader from "what is this?" to "it's
running" with the least friction — *not* to document every flag. Depth belongs lower in the file or
in linked docs.

**Audience: a competent developer who has never seen THIS project** — possibly early-career,
**never a non-technical reader**. They know how to build software; they do not know your project. So
plain language serves **speed of orientation, not simplification of computing itself**: never
explain what an API or a database *is*, but do say plainly what *this* one does and how to run it.
Achieve brevity through **progressive disclosure** (surface the essential, link the rest), not by
deleting information.

## Plain-language rules (top sections)
Grounded in <https://www.plainlanguage.gov/guidelines/> and <https://developers.google.com/style>:
- **Short sentences** — cap around **20 words** in the top sections. One idea per sentence.
- **Active voice, present tense** — "the app reads config at startup", not "config is read".
- **No filler** — cut "simply", "just", "powerful", "blazing-fast", "production-ready" unless you
  immediately back it with a fact and a link.

## Vocabulary — two tiers (top sections)
Match the reader: a working developer. Do **not** explain computing to them; **do** explain what is
specific to this project.

- **Baseline developer vocabulary — use bare, never define.** These are second-year-developer words;
  defining them condescends and wastes the reader's time. Maintain this allowlist:
  `API`, `REST`, `HTTP`, `HTTPS`, `JSON`, `SQL`, database, `PostgreSQL`, `Go`, `Git`, `GitHub`,
  `CLI`, `CRUD`, env var, container, `Docker`, `Podman`, `CI`, `CD`, `TLS`, `UUID`, `URL`, `YAML`,
  `OAuth`.
- **Project-specific and niche terms — gloss at first use (six words or fewer) or link.** A gloss is
  a parenthetical or a link, never a lecture; if it needs a full sentence, it belongs in the body.
  For this repo: `SLSA`, `ASVS`, `OIDC`, `pgx`, `chi`, `goose`, `pgxmock`, keyset/cursor pagination,
  `distroless`, `SBOM`, `GoReleaser`, `Air`.
- **Rule of thumb:** would a second-year developer recognise it? If yes, use it bare. If it is
  specific to this stack or to a security / supply-chain standard, gloss or link it.

## Calibration — anchor the register, don't infer it
Three one-liners for the same project, so the target register is explicit rather than guessed:

- **Too simple (wrong):** "a web API — a service other programs call over HTTP — backed by a PostgreSQL database." — *stops to define what an API and HTTP are for a developer who already knows.*
- **Too dense (wrong):** "Opinionated chi/pgx layered REST template with SLSA L3 provenance, keyset pagination, and contract-tested OpenAPI." — *front-loads five niche terms before saying what the thing is.*
- **Right:** "A production-ready template for building REST APIs in Go, backed by PostgreSQL. Fork it, rename it, add your features — the security, testing, and release tooling is already wired." — *names the thing precisely in baseline vocabulary, and leaves the niche terms for the sections that need them.*

## Structure (orientation, in order)
Per <https://diataxis.fr/> (a README is the front door, not the manual):
1. **What it is** — one sentence. What the thing is and what it does.
2. **Who it's for** — one or two sentences. Who should use it, and any assumed knowledge/tools.
3. **Quickstart** — a **copy-paste** path from nothing to running. Prerequisites named, then the
   fewest commands that end in a verifiable "it works" (a `curl`, a URL, an expected line).
4. **Minimal config** — only what's needed to run; link to the full table below.
5. **Where to get help** — issues link, docs link.
6. **Licence** — one line + link.
Everything else (architecture, every env var, deployment, scaling) lives **below** or in linked
docs. Moving detail down is the goal; deleting it is not.

## Checkable rules (not vibes)
Verify these, don't eyeball them:
- **Intro is three sentences maximum.** Count them.
- **The quickstart runs by copy-paste alone.** Actually run it in a clean-as-possible environment
  (for this repo: the dev stack via podman). Every prerequisite is stated; no undocumented step. If
  you cannot run it, mark it **unverified in your report** — never in the README.
- **Acronym grep, filtered by the baseline allowlist.** `grep -oE '\b[A-Z]{2,}\b'` the top sections,
  drop the baseline allowlist (API, REST, HTTP, HTTPS, JSON, SQL, CLI, CRUD, CI, CD, TLS, UUID, URL,
  YAML, OAuth), and confirm every **remaining** hit (e.g. SLSA, ASVS, OIDC, SBOM) is glossed at first
  use or linked. Baseline terms are expected bare — they are not findings.
- **Read-aloud pass.** Read the top sections out loud; anything you stumble over or run out of
  breath on gets shortened or split.

## Applying it to this repo (first job)
Rewrite the **top sections** of this repository's [`README.md`](../../../README.md) — what-it-is,
who-it's-for, quickstart — to meet the rules above. Constraints:
- **The technical body below stays intact.** Project layout, configuration tables, deployment,
  scaling, releases, Makefile reference — untouched.
- **Move displaced detail down, don't delete it.** Fork-time nuance (e.g. the repo-wide module
  rename and *why* it's repo-wide) moves to its own section lower in the file, keeping its rationale.
- **Verify the quickstart end-to-end** with the dev stack: run `make setup` / `make run` and hit the
  health endpoints. Report the actual result; if podman is unavailable, say the quickstart is
  unverified — do not weaken the README to match an environment you couldn't test.

## Output format
1. **Top sections rewritten** — the new what-it-is / who-it's-for / quickstart.
2. **Checks** — intro sentence count; the acronym grep result; "quickstart ran: <result>" or "unverified: <reason>"; "read-aloud pass done".
3. **What moved** — detail relocated (not deleted) and where it went.

## Non-negotiables
- **Orientation, not reference** — the README gets a newcomer running; depth lives below or linked.
- **Two-tier vocabulary** — baseline developer terms (API, HTTP, SQL, …) bare and never defined; only project-specific or niche terms (SLSA, pgx, OIDC, …) glossed at first use or linked.
- **A copy-paste quickstart, verified** — or explicitly reported unverified.
- **Progressive disclosure** — shorten by moving detail down, never by deleting it.
