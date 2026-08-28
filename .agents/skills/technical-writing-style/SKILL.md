---
name: technical-writing-style
description: technical, writing, style, enforce, clear, direct, software, development, documents, including
---

# Technical Writing Style

Write like a human. Kill corporate jargon, strip LLM tells, and make every sentence earn its place.

This skill rewrites or reviews technical prose for clarity, directness, and human tone. It targets software development documents — not creative writing, fiction, or stylistic flourish unless explicitly requested.

## When to Use

- Writing or editing OKRs, PRDs, tech specs, ADRs, or similar planning documents
- Drafting git commit messages, PR descriptions, or changelogs
- Reviewing READMEs, runbooks, API docs, incident postmortems, or onboarding guides
- User says output "sounds like AI" or "feels generic" in a technical context

## When NOT to Use

- Fiction, poetry, or creative writing
- Academic papers or legal documents
- Content where the user wants a deliberately formal, literary, or stylized tone
- Marketing landing pages or ad copy (different rules apply)

## Core Philosophy

- Prefer the shortest version that still teaches or informs.
- If two versions are equally clear, choose the shorter one.
- When editing existing text, preserve meaning but favor deletion over rewriting. If a sentence can be removed without loss, remove it.

## Sentence Structure

- Keep sentences clear and concise.
- Vary sentence length for rhythm. Short sentences punch. Longer ones let ideas breathe and connect.
- Break up complex sentences. If a sentence has more than two clauses, split it.
- Avoid repeating the same sentence structure across consecutive sentences.

## Voice and Tone

- Write like humans speak. Avoid corporate jargon and marketing fluff.
- Be confident and direct. Don't soften with "I think," "maybe," or "it could be argued."
- Use active voice over passive. "The team shipped the feature" not "The feature was shipped by the team."
- Use positive phrasing — say what something *is* rather than what it *isn't*.
- Say "you" more than "we" when addressing external audiences.
- Use contractions like "I'll," "won't," and "can't" for a warmer tone.

## Introductions

- Open with the most concrete or surprising fact.
- Never open with meta commentary about the topic. "This document describes the architecture of..." is dead weight. Start with the decision, the constraint, or the number.

## Specificity and Evidence

- Be specific with facts and data instead of vague superlatives.
- Back up claims with concrete examples or metrics.
- Highlight customers and community members over company achievements.
- Use realistic, product-based examples instead of `foo/bar/baz` in code.
- Make content concrete, visual, and falsifiable. If a reader can't verify it or picture it, rewrite it.

## Title Creation

- Make a promise in the title so readers know exactly what they get.
- Share something uniquely helpful that makes readers better at meaningful aspects of their work.
- Avoid vague titles like "My Thoughts On XYZ." Titles should be opinions or shareable facts.
- Write a placeholder title first, finish the content, then iterate on the title at the end.

## Banned Words

These words weaken writing. Replace or remove them:

| Banned | Replacement |
|---|---|
| a bit, a little | remove |
| actually/actual | remove |
| arguably | remove |
| assistance | "help" |
| attempt | "try" |
| battle tested | remove |
| best practices | "proven approaches" |
| blazing fast / lightning fast | quantify: "builds 40% faster" |
| commence | "start" |
| delve | "go into" |
| disrupt/disruptive | remove |
| facilitate | "help" or "ease" |
| game-changing | state the specific benefit |
| great | remove or be specific |
| implement | "do" or "build" |
| individual | "person" |
| initial | "first" |
| innovative | remove |
| just | remove |
| leverage | "use" |
| mission-critical | "important" |
| modern/modernized | remove |
| numerous | "many" |
| out of the box | remove |
| performant | "fast and reliable" |
| pretty/quite/rather/really/very | remove |
| referred to as | "called" |
| remainder | "rest" |
| robust | "strong" |
| seamless/seamlessly | "automatic" |
| sufficient | "enough" |
| that | often removable — reread without it |
| thing | be specific |
| utilize | "use" |
| webinar | "online event" |

## Banned Phrases

| Banned | Replacement |
|---|---|
| I think / I believe / we believe | state it directly |
| it seems | remove |
| sort of / kind of | remove |
| pretty much | remove |
| a lot / a little | be specific |
| By developers, for developers | remove |
| We can't wait to see what you'll build | remove |
| We obsess over ___ | remove |
| The future of ___ | remove |
| We're excited | "We look forward" or remove |
| Today, we're excited to | remove — just say what happened |

## Avoid LLM Patterns

These are the tells that make writing sound AI-generated. Catch and fix them every time:

- Never open with "Great question!", "You're right!", or "Let me help you."
- Skip "Let's dive into..." and "Let's explore..."
- Kill cliché intros: "In today's fast-paced digital world," "In the ever-evolving landscape of."
- Avoid "it's not just [x], it's [y]" constructions.
- Remove self-referential disclaimers: "As an AI," "I'm here to help you with."
- No high-school essay closers: "In conclusion," "Overall," "To summarize."
- No "Hope this helps!" or similar sign-offs.
- Stop overusing transition words: "Furthermore," "Additionally," "Moreover." Just start the next sentence.
- Don't stack hedges: "may potentially," "it's important to note that."
- Avoid perfectly symmetrical paragraph structures or "Firstly... Secondly... Thirdly..." patterns.
- Avoid repeating the same sentence structure across consecutive sentences.
- Avoid connective clichés: "As you can see," "This means that," "In other words" (unless truly needed).
- Use sentence-case headings, not Title Case.
- Remove Unicode artifacts when copy-pasting: smart quotes, non-breaking spaces, stray special characters.
- Delete empty citation placeholders like "[1]" with no actual source.

## Punctuation and Formatting

- Use Oxford commas consistently.
- Use exclamation points sparingly. One per document is plenty.
- Sentences can start with "But" and "And" — but don't overuse either.
- Use periods instead of commas when possible for clarity.
- Avoid overusing parentheses. Prefer rewriting the sentence so the parenthetical becomes its own statement.

## Self-Review Checklist

After writing, run through this before delivering:

1. Read every sentence aloud. If it sounds like a press release, rewrite it.
2. Search for every word in the Banned Words table. Replace or remove each one.
3. Check the opening line. Does it start with a cliché, an LLM pattern, or meta commentary? Cut it. Open with the most concrete fact.
4. Check the closing line. Does it end with "Hope this helps" or "In conclusion"? Cut it.
5. Count your exclamation points. More than one? Pick the best one, remove the rest.
6. Verify specificity. Any sentence with "many," "significant," or "various" should get a number, name, or example instead.
7. Check for parallel sentence openings. If three sentences in a row start with the same word, vary them.
8. Check for sentences that can be deleted without losing meaning. Delete them.

## Examples

**Before (LLM-sounding PRD intro):**
> In today's rapidly evolving landscape, it's important to note that leveraging innovative best practices can be truly game-changing for your organization. We're excited to delve into this robust solution that seamlessly facilitates your mission-critical workflows. Let's dive in!

**After:**
> This approach cut our deploy time by 60%. Here's how it works and why your team should consider it.

**Before (bloated tech spec):**
> We believe that utilizing our blazing fast platform will actually facilitate numerous improvements across your organization's initial implementation attempts.

**After:**
> The platform builds projects in under 3 seconds. Teams that switched shipped their first release a week earlier on average.

**Before (meta commentary opening):**
> This document describes the architecture of the new authentication service and outlines the various components that make up the system.

**After:**
> The new auth service handles 12,000 requests per second using a stateless JWT architecture with three components: token issuer, validation middleware, and key rotation scheduler.
