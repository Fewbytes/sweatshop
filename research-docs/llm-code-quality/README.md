# Good Code for LLM Writers and Readers

Research notes, 2026-10-11. Question: what does industry and academic research
say about "good code", which quality signals are measurable, and how should
code that is *generated and read by LLMs* differ from the legacy notion of
good code?

Documents:

- [`README.md`](README.md) — research notes and evidence (this file)
- [`line/guidance/writer.md`](../../line/guidance/writer.md) — guide for agents that write code
- [`line/guidance/kaizen.md`](../../line/guidance/kaizen.md) — guide for agents that refactor and simplify code

The guides are embedded into station prompts by the `line/guidance` Go
package; edit them there.

Evidence grading used below: **[strong]** replicated / peer-reviewed,
**[moderate]** single peer-reviewed study or consistent preprints,
**[weak]** vendor report, secondary summary, or unverified preprint.

---

## 1. What the legacy research actually supports

Most "clean code" canon (short functions, DRY, SOLID) is practitioner
folklore with thin controlled evidence. What survives scrutiny:

- **Size dominates.** Once LOC is controlled for, many complexity and
  Chidamber–Kemerer metrics lose much of their defect-predictive power
  (El Emam et al., 2001). **[strong]**
- **Process beats product.** Churn, number of authors, ownership and change
  history predict defects better than static code metrics (Moser et al. 2008;
  Rahman & Devanbu 2013; Bird et al. 2011 "Don't touch my code"). Nagappan,
  Murphy & Basili found organizational structure metrics the best predictor of
  Windows Vista post-release defects. **[strong]**
- **Change coupling is real.** Files that co-changed in the past keep
  co-changing (Ball et al.; Oliva & Gerosa). Misalignment between architecture
  and team communication (socio-technical congruence, Cataldo et al.) correlates
  with slower work and more incidents. **[moderate]**
- **Readability models measure taste, not comprehension.** Buse–Weimer and
  Scalabrino models are trained on subjective ratings. Scalabrino et al. 2017
  ("Automatically assessing code understandability: how far are we?") found
  metrics barely correlate with measured understandability. **[moderate]**
- **Cognitive Complexity is the exception.** A meta-analysis over ~24k
  evaluations of 427 snippets found modest positive correlation with
  comprehension time and subjective understandability (Muñoz Barón et al.
  2020). **[moderate]**
- **fMRI:** complexity metrics explain programmer cognition only "to a limited
  degree"; textual size drives attention, vocabulary size loads working memory
  (Peitek et al.). **[moderate]**
- **Non-linear returns.** CodeScene reports defects drop steeply as Code Health
  rises from 1 to 5 and plateau between 5 and 8; claims 15x more defects in
  low-health code across 39 proprietary codebases. **[weak — vendor]**

**Takeaway:** legacy "good code" is mostly about *change management*
(localized changes, aligned coupling, ownership) plus a weaker human
comprehension layer.

---

## 2. Measurable signals and analysis techniques

### 2a. Structural (semantic graph) properties

| Signal | Metric | Technique |
|---|---|---|
| Coupling / cohesion | CBO, LCOM, fan-in/out, instability (Martin) | static call/import graph |
| Modularity | DSM propagation cost (MacCormack), SCC / cycle count, layer violations | dependency structure matrix, architecture conformance |
| Hidden coupling | co-change pairs lacking a static edge | mining software repositories (git log) |
| Hotspots | complexity × churn | behavioral code analysis (Tornhill) |
| Duplication | clone classes, types 1–4 | token / AST clone detection |
| Blast radius | transitive reverse-dependency closure | graph reachability |
| Test strength | mutation score (not line coverage) | mutation testing |
| Socio-technical fit | ownership vs. module graph congruence | org + VCS mining |

### 2b. Human-interface properties

| Signal | Metric | Evidence |
|---|---|---|
| Control-flow load | Cognitive Complexity, nesting depth | moderate |
| Naming | identifier quality, length, dictionary words | strong for comprehension |
| Vocabulary | distinct identifiers per unit | fMRI: working memory load |
| Comments / docs | density, staleness | mixed |
| Layout / style | formatting consistency | weak alone; acts as "beacons" |
| Readability score | Buse–Weimer, Scalabrino | weak |

### 2c. Analysis techniques

- **Static:** AST queries (ast-grep), symbol indexes (ctags, LSP), call/import
  graphs, DSM, clone detection, type checking, linters.
- **Historical (MSR):** churn, co-change / logical coupling, ownership, hotspot
  ranking, defect-fix commit mining.
- **Dynamic:** coverage, mutation testing, profiling, runtime call graphs.
- **Socio-technical:** congruence of team communication with module graph.
- **Agent-behavioral (new):** paired-trial benchmarks measuring agent tokens,
  file revisits and success on the same task across code variants.

---

## 3. Evidence specific to LLM readers and writers

- **Identifier names matter, possibly more than to humans.** Identifier
  renaming produces large performance drops in code comprehension benchmarks.
  A 2026 preprint finds reasoning-tuned models share human difficulty patterns
  under obfuscation while coder-tuned models show near-zero correlation
  (arXiv 2606.31725). **[moderate / weak for the 2026 preprint]**
- **Wrong docs are worse than no docs.** Incorrect documentation greatly hinders
  LLM code understanding; missing or incomplete documentation does not
  significantly hurt (Macke & Doyle, NAACL Findings 2024). **[moderate]**
- **LLM-written comments are often wrong.** Roughly a fifth of comments from
  the best model contained demonstrably inaccurate statements (Kang, Milliken
  & Yoo, arXiv 2406.14836). **[moderate]**
- **Types as machine feedback.** Type-constrained decoding cut compile errors by
  more than half and improved functional correctness on HumanEval/MBPP
  (Mündler et al., PLDI 2025). No clean study isolates prompt-level type
  annotations. **[moderate]**
- **Canonical forms help.** A DSL with one canonical form per operation
  outperformed Python on generation accuracy (Anka, arXiv 2512.23214).
  **[weak]**
- **Structure graph aids localization.** Agents navigate mostly by lexical
  search and miss imports, call chains, type hierarchies, code–test links.
  Graph-anchored expansion: +13.9 pts file-level Acc@5 on LocBench (LARGER,
  2026). RepoGraph: +32.8% avg relative on SWE-bench frameworks. Repository
  context use remains hard even for frontier models (RECUBE). **[moderate]**
- **Clean code mostly buys efficiency.** SonarSource paired trials (660 Claude
  Code runs on minimal-pair repos): success rate roughly unchanged; 7–8% fewer
  tokens and 34% fewer file revisits on clean code. **[weak — secondary
  summary, methodology disputed]**
- **Unhealthy code raises AI defect risk.** CodeScene (Borg & Tornhill) claims
  ≥30% higher defect risk for AI edits in unhealthy code. **[weak — vendor]**
- **Agent-authored code degrades later agent work.** Downstream task resolve
  rates drop 1–13 pts when building on agent-authored versus human-authored
  code (ICML 2026, "Do AI Agents Write Less Maintainable Code Than Human
  Developers?"). **[moderate]**
- **The codebase is the prompt.** GitClear (211M changed lines, 2020–2024):
  8x rise in duplicated 5+ line blocks in 2024; first year copy/paste exceeded
  moved (refactored) code. **[weak — vendor, correlational]**

---

## 4. Legacy vs. LLM-era: what transfers, inverts, or is new

| Legacy rule | Human rationale | LLM-era verdict |
|---|---|---|
| Short functions, many small files | working memory ~4 chunks | **Partly inverts.** Each indirection hop costs a lookup/tool call. Prefer *locality*: cohesive units with linear flow, sized to a context budget. |
| DRY | single point of change | **Split.** DRY for *knowledge* (invariants, business rules, constants) is stricter — agents edit one clone and miss others. Relax for incidental similarity; explicit repetition beats a clever premature abstraction. |
| Good names | comprehension | **More important.** Names are the model's semantic anchor *and* its navigation index; they must be unique and greppable. |
| Comments explain why | intent | **Riskier.** Stale comments actively mislead. Move claims into checkable form (types, asserts, contracts, tests); keep prose for rationale. |
| Abstraction / DI / metaprogramming / reflection | flexibility | **Costly.** Breaks static graph and grep. Explicit over implicit, more strongly than before. |
| Static types | safety, IDE | **Much more valuable.** Fast verifier inside the agent loop; machine-readable spec. |
| Tests | regression safety | **Become the oracle.** Must be fast, deterministic, local. Measure by mutation score. |
| Formatting / style | readability | **Mostly irrelevant to models** except token cost. *Consistency* matters because the model imitates the repo. |
| Cognitive complexity | human mental load | **Becomes context cost:** tokens an agent must read to change this safely. |
| Low coupling | change isolation | **Same, sharper.** Hidden coupling (co-change without a static edge) is invisible to an agent. |

**Core shift.** Legacy quality optimizes human cognition plus change
management. LLM-era quality optimizes **(1) localization cost** — how cheaply
an agent finds what it must read, **(2) verifiability** — how completely a
change can be checked mechanically, and **(3) pattern hygiene** — how good the
patterns the model will copy are. Human readability does not vanish; it
concentrates on the *review surface*: interfaces, tests, diffs, specs.

---

## 5. Proposed LLM-era metrics

Not established in the literature; proposed for this project. Computable with
ctags, ast-grep, git history and bughunt.

1. **Context closure** — tokens in the transitive definitions needed to
   understand a function (callees, types, constants it references).
2. **Grep ambiguity** — identifier collision rate with unrelated symbols;
   share of call edges resolvable only dynamically (reflection, string
   dispatch, DI).
3. **Hidden-coupling ratio** — co-change file pairs without a static
   dependency edge ÷ all co-change pairs.
4. **Spec density** — types, contracts, asserts, tests per behavioral unit.
5. **Verification latency** — wall time from edit to trustworthy pass/fail.
6. **Pattern entropy** — distinct idioms used for the same concern (error
   handling, I/O, config, logging). Lower = better model prior.
7. **Doc–code drift** — comments/docs contradicted by code or tests (detect via
   test execution, cf. arXiv 2406.14836).
8. **Agent-efficiency benchmark** — tokens, file revisits, success rate on a
   fixed task set before/after refactors (paired-trial design).

---

## 6. Open questions and gaps

- No controlled study isolates prompt-level type annotations on generation
  quality.
- No study directly measures stale comments' effect on *generation* (only
  comprehension).
- Optimal unit size for LLM readers is unmeasured; "locality vs. small
  functions" is inferred, not tested.
- Vendor data (CodeScene, GitClear, Sonar) dominates the agent-efficiency
  evidence; independent replication needed.
- Whether human-oriented readability still matters for model *output* quality
  (as opposed to efficiency) is unresolved.

---

## Sources

- El Emam et al. 2001, "The confounding effect of class size on the validity of object-oriented metrics", IEEE TSE
- Moser, Pedrycz, Succi 2008, "A comparative analysis of the efficiency of change metrics and static code attributes for defect prediction", ICSE
- Rahman & Devanbu 2013, "How, and why, process metrics are better", ICSE
- Bird et al. 2011, "Don't touch my code! Examining the effects of ownership on software quality", FSE
- Nagappan, Murphy, Basili 2008, "The influence of organizational structure on software quality", ICSE — [summary](https://www.oreilly.com/library/view/making-software/9780596808310/ch11s03.html)
- Scalabrino et al. 2017, "Automatically assessing code understandability: how far are we?", ASE
- [Muñoz Barón et al., Cognitive Complexity validation](https://arxiv.org/pdf/2007.12520)
- [Peitek et al., fMRI program comprehension](https://www.se.cs.uni-saarland.de/publications/docs/PAP+21.pdf)
- [Source code metrics for defect prediction](https://arxiv.org/pdf/2301.08022)
- [Oliva & Gerosa, change coupling](https://www.ime.usp.br/~gerosa/papers/changecoupling.pdf)
- [Socio-technical congruence](https://arxiv.org/pdf/1201.4142)
- [CodeScene Code Health](https://codescene.io/docs/guides/technical/code-health.html)
- [CodeScene, AI-Ready Code whitepaper](https://codescene.com/hubfs/whitepapers/AI-Ready-Code-How-Code-Health-Determines-AI-Performance.pdf)
- [The Code Barrier (review)](https://www.themoonlight.io/en/review/the-code-barrier-what-llms-actually-understand)
- [arXiv 2606.31725, LLM vs human failure modes under obfuscation](https://arxiv.org/abs/2606.31725)
- [Macke & Doyle, documentation effect on LLM understanding (review)](https://www.themoonlight.io/tw/review/testing-the-effect-of-code-documentation-on-large-language-model-code-understanding)
- [Kang, Milliken, Yoo, inaccurate LLM comments](https://arxiv.org/pdf/2406.14836)
- [Mündler et al., type-constrained code generation](https://arxiv.org/abs/2504.09246)
- [Anka DSL](https://arxiv.org/pdf/2512.23214)
- [RepoGraph](https://arxiv.org/html/2410.14684v2)
- [LARGER, graph-anchored localization](https://www.emergentmind.com/papers/2605.16352)
- [Dyretriever, partial dependency graph retrieval](https://arxiv.org/pdf/2608.01927)
- [RECUBE](https://www.alphaxiv.org/abs/2603.25770.md)
- [ICML 2026, agent-written code maintainability](https://icml.cc/virtual/2026/67974)
- [Code cleanliness and agents (Sonar trials summary)](https://www.developersdigest.tech/blog/code-cleanliness-affects-ai-coding-agents)
- [GitClear 2025 via LeadDev](https://leaddev.com/software-quality/how-ai-generated-code-accelerates-technical-debt)
