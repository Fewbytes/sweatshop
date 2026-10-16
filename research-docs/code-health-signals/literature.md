# Code-Health Signals: The Literature Behind Kaizen's Measures

Reference notes, 2026-10-15. Companion to [`README.md`](README.md) (section 8,
"The literature behind the behavioral signals" and "Agent-written code
studies") and to `line/guidance/kaizen.md` section 3. Purpose: verify the
definitions Kaizen will implement against the primary sources, record where
each comes from, and say how much weight each can bear.

Evidence grades as in the README: **[strong]** peer-reviewed and replicated
(or a large peer-reviewed benchmark), **[moderate]** a single peer-reviewed
study or consistent preprints, **[weak]** vendor material, self-description
or second-hand reports. Read status is stated for every source: **full**,
**partial** (named sections), **abstract only**, or **not read**. Nothing in
this note states results of a source that was not read.

Method note: papers were fetched from author pages, arXiv, institutional
repositories and the Wayback Machine. Several fetches went through a
summarising reader, so before quoting an exact number in a commit message or
card, check it against the PDF linked here.

---

## 1. Summary by signal

| Kaizen signal | Best-supported definition | Grade | Caveats |
|---|---|---|---|
| Hotspot (churn × complexity) | Change count per file over a window (code-maat `revisions`), joined with a size or complexity proxy (LOC, indentation, cognitive complexity) and ranked. For a single score, use the decayed change count with weights from Graves et al. 2000: Σ e^(−α·age) · log(lines changed), with α ≈ 0.75 per year. | strong for churn as a predictor; weak for the product | No source defines "churn × complexity" as a product, or a 90-day window. Tornhill ranks by revisions and shows LOC beside them; CodeScene's ranking is proprietary. Static complexity barely changes between releases (Rahman and Devanbu 2013), so the churn term does most of the work. |
| Relative churn | Nagappan and Ball M1 = churned LOC / total LOC; M3 = files churned / file count; Kamei LA/LT | moderate (single system), used by many later studies | Defect *density* has size in its denominator, which partly builds in the correlation. |
| Change (temporal) coupling | Zimmermann support count = number of transactions containing both entities; confidence(a⇒b) = support / freq(a). Or code-maat degree = shared / mean(revs_a, revs_b), with defaults min-revs 5, min-shared 5, min-degree 30%, and commits over 30 entities dropped. | strong | Groups of commits need defining for agents: one task, bead or session per transaction. Results are unstable with little history (Kirbas 2017). |
| Hidden coupling | Mo et al. Modularity Violation: a pair co-changes more than `cochange_thr` times (2 in the paper) and has **no structural dependency in either direction** | strong-ish (peer-reviewed, 19 projects, same group only) | Most co-change has no structural link (Oliva 2016; Fluri 2005), so a ratio of "pairs without an edge ÷ all pairs" is high by default. Rank pairs; do not use the ratio as a health score. Valid only once imports resolve. |
| Cycles / architecture | Clique = strongly connected component of the file graph; Package Cycle (Mo et al. TSE); propagation cost = sum(reflexive transitive closure) / n² (MacCormack et al. 2006); Lakos CCD/ACD/NCCD | moderate | Propagation cost is comparative (trend or delta), not absolute. Lakos is secondary-sourced. |
| Cognitive Complexity | SonarSource specification (Campbell); gocognit for Go | weak (spec) plus moderate (validation) | Meta-analysis: r = 0.54 with comprehension time, −0.13 with correctness, 0.00 with fMRI (one study). Little data above 15. |
| Cohesion (Go package) | Proposed: density of the package declaration graph, 2|E| / (|V|(|V|−1)) (Briand's Co′), plus connected components as split candidates | theory strong; Go validation none | LCOM3 and LCOM4 were not fault predictors in Al Dallal and Briand 2012. Our confidence that this predicts anything in Go is low. |
| Clones | Inconsistent near-miss clones (Juergens 2009); live clones = similarity-preserving co-change (Mondal et al. 2014); late-propagation events (Barbour et al. 2013) | strong (Juergens, replicated) | Many clones are deliberate (Kapser and Godfrey). Clones are volatile when young (Kim et al. 2005), so apply a minimum age before refactoring. |
| Ownership / authorship | Bird et al.: count of minor contributors (<5% of commits) | strong at Microsoft; weak or absent in OSS | Confounded with size. "Contributor" must be redefined for agents. |
| Change entropy | Hassan 2009 HCM over feature-introducing changes; Kamei per-change entropy | moderate | Unstable across systems in D'Ambros et al. 2010. |
| Size control | Report every metric with size controlled: per LOC, as a residual on log LOC, or within size bands | strong (El Emam et al. 2001; Gil and Lalouche 2017) | Evanco 2003 disputes treating size as a confounder; the data itself is not disputed. |
| Mutation score | Fraction of generated mutants killed by the tests, computed on changed lines only, with equivalent and "arid" mutants suppressed; report the surviving mutants, not just the ratio (section 13) | moderate for "tracks real-fault detection" (Just et al. 2014; Java, 5 programs); weak-to-moderate once suite size is controlled (Papadakis et al. 2018, as summarised by their survey) | 27% of real faults were not coupled to any commonly generated mutant (17% to none at all). The absolute score is not actionable and not computable at scale (Google); use the incremental, per-change form. Equivalent mutants are undecidable. Not validated for Go or agent-written tests. |
| Rework of agent code | Survival of introduced static-analysis issues at HEAD (Liu et al. 2026) is the only measure over time; nothing measures rewritten or reverted *lines* of agent code. GitClear 2025 reports a moved-line share falling from 25% to under 10% (vendor) | weak (vendor) plus moderate (Liu et al.) | Liu et al. have no human-commit baseline, so 22.7% survival cannot be compared with anything. See section 11.6. |

**Corrections to the README and kaizen.md**

1. "Adams et al. 2006 (function-level coupling)" does not exist as cited. The
   function and entity-level co-change source is Zimmermann et al.
   (ICSE 2004 / TSE 2005). The Adams paper is Adams, Jiang and Hassan,
   ICSE 2010, and it is about mining crosscutting concerns.
2. "Gall et al. 2003 (HistoryFinder)": the 2003 paper was not readable, and
   nothing seen connects it to a tool called HistoryFinder. Cite Gall,
   Hajek and Jazayeri 1998 for the term "logical coupling" and Zimmermann
   for the definitions.
3. Hotspot = "churn × complexity over ~90 days" is our own choice. No source
   defines it. codelore's percentile product is codelore's own, not from the
   literature.
4. Fisher-exact significance on co-change pairs is a codelore idea; none of
   the sources read uses it. The literature uses support/confidence (Zimmermann),
   degree thresholds (code-maat), n-coupling (D'Ambros 2009) or
   interquartile-range cut-offs on support (Oliva and Gerosa). Fisher is
   defensible but should be labelled as our choice.
5. Agent-code studies. arXiv 2609.04208 is a registered report with no
   results. CodeThread (arXiv 2606.21804) and the ICML 2026 paper are the
   same work. The ICML version is a poster at the workshop "Deep Learning for
   Code: Towards Human-Centered Coding Agents", not a main-conference paper.
   The "AI-Generated Smells" "law" rests on about 20 generated repos and is
   not adjusted for size.

---

## 2. Tornhill: hotspots and temporal coupling

### 2.1 code-maat (source code) [weak as evidence; exact as definition]

github.com/adamtornhill/code-maat. Read in full at source level (`cmd_line.clj`,
`analysis/logical_coupling.clj`, `coupling_algos.clj`, `sum_of_coupling.clj`,
`entities.clj`) and maat-scripts (`merge_comp_freqs.py`,
`complexity_calculations.py`).

- **Revisions** = number of distinct commits touching an entity within the
  log range supplied. The tool has no window of its own; the window is the
  `git log --after` range.
- **Coupling degree** = int(100 · shared / ((revs_a + revs_b) / 2)). Pairs are
  unordered.
- Commits with more than `max-changeset-size` entities are removed before
  anything is counted, including the per-file revisions.
- **Filter:**
  - mean revisions of the pair ≥ min-revs (the mean, not each file)
  - shared ≥ min-shared-revs
  - min-coupling ≤ degree ≤ max-coupling
- **Defaults:** min-revs 5, min-shared-revs 5, min-coupling 30, max-coupling
  100, max-changeset-size 30. `-t temporal-period` (for example one day)
  merges commits within a period into one logical change; it has no default.
- **Sum of coupling** = Σ over commits of (entities in commit − 1), per entity.
- **Hotspot recipe:** an inner join of revisions with `cloc` code lines,
  sorted by revisions. There is no product and no normalisation.
- **Indentation complexity** per non-blank line = leading tabs + leading
  spaces / 4. Reported as total, mean, sd and max.

**Kaizen: adopt** as the baseline definition of coupling degree and its
filters. Store raw shared and revision counts, because the degree is unstable
at low revision counts.

### 2.2 CodeScene documentation [weak]

docs.codescene.io guides: hotspots, X-Ray and code-health (partial);
docs.enterprise.codescene.io 3.1.0 hotspots and temporal-coupling (partial).
The current temporal-coupling page returned 404.

- **Hotspots:** change frequency = commits per file. LOC is the stated
  complexity proxy. The current version uses change frequency with low Code
  Health. The ranking is proprietary, with no published window or cut-offs.
- **Temporal coupling:** files are coupled if they change in the same commit,
  or by the same author within a configured period, or under the same ticket
  ID. Example configuration: files with fewer than 10 commits ignored,
  changesets over 50 files ignored. The hotspot map uses a 20% coupling
  threshold.
- **X-Ray:** function-level hotspots and coupling over at most the latest 200
  revisions. Rename tracking is heuristic.
- **Code Health:** 25+ factors, including LCOM4 low cohesion, Brain Method,
  Bumpy Road and DRY. The KPIs are LOC-weighted.

**Kaizen: adapt.** Use ticket/bead-ID logical changesets, because agents
split one task across several commits.

### 2.3 Tornhill and Borg 2022, "Code Red" [moderate]

Tornhill A., Borg M., "Code Red: The Business Impact of Code Quality – A
Quantitative Study of 39 Proprietary Production Codebases", TechDebt 2022,
arXiv:2203.04374. Read in full (arXiv HTML).

- **Data:** 39 codebases from CodeScene customers, 30,737 files, 14 languages
  (mostly C++), 6–12 months of Jira.
- **Code Health bands:**
  - Healthy (≥8): 89% of files
  - Warning: 9.9%
  - Alert (<4): 1.1%
- **Defects per file:** 0.25 / 0.94 / 3.70 across the three bands. Alert files
  have about 15× as many as Healthy ones (Kruskal-Wallis χ²(2) = 1645).
- **Time-in-development:** mean +124% for Alert files. The maximum is 9× that
  of Healthy files.
- **Correlations with resolution time:** Code Health r = −0.58; LOC r = 0.13.
- **Limitations:** a single vendor with a proprietary metric, mostly C++,
  correlational. The paper does not define "hotspot". No independent
  replication was seen.

**Kaizen:** cite as motivation that unhealthy code is slower to change. Do
not adopt Code Health itself (proprietary, unreproducible).

### 2.4 Other Tornhill sources

- **InfoQ report of Tornhill's 2015 QCon talk** [weak, second-hand single
  case]: 400 KLOC, 89 developers. Hotspots covered 7 of the 8 most
  defect-dense parts; "4% of the code was responsible for 72% of all defects".
- **The books** (*Your Code as a Crime Scene*, *Software Design X-Rays*): not
  read (paywalled). Thresholds above come from code-maat and the CodeScene
  documentation, not the books.

---

## 3. Mo, Cai, Kazman et al.: hotspot patterns and architecture anti-patterns

### 3.1 The WICSA 2015 paper (not read)

Mo, Cai, Kazman, Xiao, Feng, "Hotspot Patterns: The Formal Definition and
Automatic Detection of Architecture Smells", WICSA 2015, pp. 51–60,
DOI 10.1109/WICSA.2015.12. The publisher copy and the DTIC copy were both
blocked.

A secondary summary (Acolyer, The Morning Paper, 2016) lists five patterns:
- Unstable Interface
- Implicit Cross-module Dependency
- Unhealthy Inheritance
- Cross-Module Cycle
- Cross-Package Cycle

The same summary says the data were 9 Apache projects and 1 commercial
project. Thresholds from WICSA are not verified.

### 3.2 The TSE journal version [strong-ish]

Mo R., Cai Y., Kazman R., Xiao L., Feng Q., "Architecture Anti-patterns:
Automatically Detectable Violations of Design Principles", IEEE TSE 47(5),
2021, DOI 10.1109/TSE.2019.2910856. Read pp. 1–17 of the accepted manuscript
(par.nsf.gov/servlets/purl/10118809).

**Data and inputs:**
- 19 projects: 15 Apache Java and 4 commercial C#, full history up to the
  chosen release.
- File dependencies come from Understand: depend (calls) and inherit.
- #cochange(x, y) = number of commits containing both files.
- Bugs are linked by issue ID in the commit message.

**Definitions:**
- **Unstable Interface (UIF):** file f has more than StructImpact_thr
  dependents, and more than HistoryImpact_thr of them co-changed with f more
  than cochange_thr times.
- **Modularity Violation Group (MVG):** pairs with no structural relation in
  either direction and #cochange > cochange_thr. Pairs are grouped by greedy
  set cover.
- **Unhealthy Inheritance Hierarchy:** a parent depends on a child, or a
  client depends on both a parent and its children.
- **Crossing:** a file with more than crossing_thr dependents and more than
  crossing_thr dependees, each co-changing with it more than cochange_thr
  times.
- **Clique:** a strongly connected component of the file dependency graph.
- **Package Cycle:** two packages each contain a file that depends on a file
  in the other.

**Thresholds** (chosen from "observations and experience"; configurable):
- StructImpact = HistoryImpact = 10
- crossing_thr = 4
- cochange_thr = 2

The formulas use ">" while the prose speaks of excluding pairs that
co-changed once, so whether the rule is ≥2 or ≥3 is ambiguous. An impact
threshold of 2 produced hundreds of trivial instances.

**Results:**
- Every project contains every anti-pattern.
- Files with anti-patterns versus files without: Wilcoxon p ≤ 1.2e-5 for bug
  frequency, bug churn, change frequency and change churn. Hedges' g =
  1.8 / 1.2 / 2.7 / 1.8.
- The more anti-patterns a file has, the worse its measures (Pearson r mostly
  0.8–0.97).
- UIF and Crossing have the largest impact; Package Cycle the smallest.
- Only 24% of Avro's UIF files are in the top 10% by LOC, so the patterns are
  not just size.

**Limitations:** thresholds are ad hoc; the analysis is retrospective and
correlational; Java and C# only; size is not modelled multivariately. No
independent replication was read.

**Kaizen: adopt MVG as the definition of hidden coupling.** That means no
import, call or reference edge in either direction, and co-change above a
threshold. Combine it with the code-maat degree and changeset cap. Adopt UIF
(10/10), Crossing (4/4), Clique (= SCC) and Package Cycle as named findings.

### 3.3 DV8 documentation (not read)

archdia.com refused the connection. No DV8 defaults are verified.

---

## 4. Logical coupling: Gall, Zimmermann, and the "Adams 2006" citation

### 4.1 Gall, Hajek, Jazayeri 1998 [moderate, as origin of the term]

"Detection of Logical Coupling Based on Product Release History", ICSM 1998,
pp. 190–198. Read in full (plg.uwaterloo.ca/~migod/846/papers/gall-coupling.pdf).

- **Data:** one telecom system of more than 10 MLOC, 20 releases, version
  numbers only.
- **Method:** programs that share a sequence of releases in which they changed
  are "logically coupled". Coupling is verified against change reports.
- **Results:** qualitative only, with no thresholds and no precision.

**Kaizen:** cite for the term only.

### 4.2 Gall, Jazayeri, Krajewski 2003 (not read)

"CVS Release History Data for Detecting Logical Couplings", IWPSE 2003. The
paper is paywalled and the repository copy was bot-blocked, so its
transaction-reconstruction rules are not verified. Do not cite its
specifics.

### 4.3 Zimmermann, Weißgerber, Diehl, Zeller [strong]

"Mining Version Histories to Guide Software Changes": ICSE 2004 and TSE
31(6) 2005. TSE read in full; ICSE read in part
(thomas-zimmermann.com/publications/files/zimmermann-tse-2005.pdf).

**Transactions:**
- Changes by the same author with the same log message, each at most 200 s
  after the previous one (a sliding window), form one transaction.
- Transactions touching more than 30 entities are ignored, because CVS merges
  show up as one large transaction.

**Granularity:** entities by default (functions, methods, classes, fields),
with a file-level mode also evaluated.

**Definitions:**
- freq(x) = number of transactions containing x
- support(x1⇒x2) = freq(x1 ∪ x2), deliberately a raw count; a support of 10
  is called "rather strong" in any project
- confidence(x1⇒x2) = support / freq(x1)

**Results** (8 OSS projects, more than 100,000 transactions):
- Navigation setting (support 1, confidence 0.1): recall 33%, precision 29%,
  and a correct item among the top 3 suggestions 70% of the time.
- File level: recall 25%.
- Error-prevention setting (support 3, confidence 0.9): suggestions for only
  3% of queries, but recall about 75% and precision above 66%.
- Weighting recent history helped a little on Eclipse and not at all on GCC.

**Replication:** replicated (Ying et al.; Kagdi et al.), widely used.

**Kaizen: adopt** support (count) and directional confidence. Cap transaction
size at about 30 entities and calibrate it, because agent commits are large.
Use "support ≥ 3 and confidence ≥ 0.9" for "you probably forgot to change Y"
warnings at submit time.

### 4.4 The "Adams et al. 2006" citation is wrong

No 2006 paper by Adams on function-level logical coupling was found. Use
these instead:

- **Zimmermann et al. 2004/2005** (above): entity-level, including
  function-level, co-change.
- **Adams, Jiang, Hassan, "Identifying Crosscutting Concerns Using Historical
  Code Changes"**, ICSE 2010. Read in full (eecs.yorku.ca/~zmjiang).
  - Method: tracks co-added and co-removed calls inside changed functions, and
    weights edges by pointwise mutual information,
    I(x;y) = log2(p(x,y) / (p(x)p(y))). Transactions with more than 100
    entities are dropped.
  - Data: PostgreSQL and NetBSD.
  - Grade: moderate. It is concern mining, not a coupling metric.
- **Fluri, Gall, Pinzger, "Fine-Grained Analysis of Change Couplings"**, SCAM
  2005. Read in full.
  - Data: one Eclipse plugin, 805 transactions.
  - Result: 31.7% of transactions had no structural (AST) change in any file;
    51.7% had at least one file without one.
  - Grade: moderate-weak.
  - **Kaizen: adapt.** Count only files with non-trivial AST changes when
    building transactions.
- **Robbes, Pollet, Lanza, "Logical Coupling Based on Fine-Grained Change
  Information"**, WCRE 2008. Read in full (Wayback copy).
  - Method: IDE sessions separated by more than 1 h of inactivity. A
    time-weighted measure (1 for simultaneous edits, falling linearly to 0 at
    1 h) did best.
  - Grade: weak-moderate.
  - **Kaizen:** relevant to coupling over agent sessions.

### 4.5 Change coupling and defects

**D'Ambros, Lanza, Robbes 2009** [strong], "On the Relationship Between Change
Coupling and Software Defects", WCRE 2009. Read in full (Wayback copy).

- **Data:** ArgoUML, Eclipse JDT Core and Mylyn; transactions with more than
  100 classes dropped.
- **Definitions:**
  - Two classes are n-coupled if they share at least n transactions.
  - NOCC = number of classes n-coupled with c.
  - SOC = sum of the shared transaction counts.
  - EWSOC and LWSOC are exponentially and linearly time-weighted versions of
    SOC.
- **Results:**
  - Spearman correlation with bug count above 0.5 in all three systems, above
    0.8 in Eclipse.
  - The best n is about 3 (ArgoUML, Mylyn) and about 10 (Eclipse).
  - Coupling beats CK metrics but not number of changes, except for major
    bugs.
  - Time-weighted variants were worse ("better not to forget the past").

**Kirbas et al. 2017** [moderate-strong], "The relationship between
evolutionary coupling and defects in large industrial software", JSEP 29(4)
e1842. Read in full (uhra.herts.ac.uk).
- **Data:** two companies, 7 years, 176k files. Changes grouped by
  modification request, because developers seldom committed more than one
  file; commits over 30 files ignored.
- **Results:** coupling correlated significantly with defects in 59% of
  modules at company 1 and 6 of 11 at company 2. Correlations were mostly low
  to moderate, and weaker in small modules and with few developers.

**Kaizen: adopt** NOCC/SOC with n calibrated per repo, and group by bead or
task. Expect a weak signal in young or small modules.

### 4.6 How much co-change is structural?

This determines whether the README's "hidden-coupling ratio" means anything.

- **Oliva, PhD thesis**, USP 2016 [moderate]. Abstract and chapter 1 read.
  - Data: 45 Apache Java projects.
  - When A depends on B and B changes, A co-changes 32% of the time on
    average, about 20% more often than without a dependency.
  - "The majority of co-changes do not relate with structural dependencies."
- **Oliva and Gerosa 2011** (SBES): not read (paywalled). A widely repeated
  "91% of logical dependencies are non-structural" figure is unverified; do
  not cite it.
- **Oliva and Gerosa, "Change Coupling Between Software Artifacts"** (book
  chapter), partial. Gives threshold heuristics for support: above Q3, Q3 +
  1.5·IQR, or Q3 + 3·IQR.
- **Ajienka, Capiluppi, Counsell 2018** [moderate], EMSE 23(3).
  - Data: 79 small Java projects.
  - Result: no significant correlation between semantic coupling and change
    coupling. At least 70% of semantic links co-change.
  - The paper's statements about direction are internally inconsistent.
- **Ajienka and Capiluppi 2017** (JSS): abstract only.

**Kaizen:** the README's ratio (co-change pairs without a static edge ÷
co-change pairs) will be high in most repositories by construction. Report
the ranked MVG pairs (section 3.2) after filtering tests, large commits and
non-AST changes. Use the count of pairs above threshold, not the ratio, as
the trend. Fold test files into their subject before pairing (README
section 3).

---

## 5. Process metrics as defect predictors

### 5.1 Graves, Karr, Marron, Siy 2000 [strong]

"Predicting Fault Incidence Using Software Change History", IEEE TSE 26(7).
Read in full.

- **Data:** a 1.5 MLOC telephone-switch subsystem, 80 modules, about 130,000
  changes. Two years of later faults are predicted.
- **Prediction error** (lower is better):
  - log(lines): 1271
  - log(deltas): 985
  - past faults: 757
  - deltas + age: 697
- **Age effect:** about 0.64× the faults per extra year of age.
- **Best model**, weighted time damp: Σ e^(−α(t − T_m)) · log(lines changed
  in change m), with α = 0.75 per year (about 50% decay per year). Its error
  was 631. The fitted α is unstable: on a one-year window its 95% CI was
  [0.375, 1.5].
- **Other findings:** complexity metrics correlated 0.88–0.99 with LOC. Number
  of developers did not help.

**Kaizen: adopt** as the base hotspot score, with α as a tunable half-life.

### 5.2 Nagappan and Ball 2005 [moderate]

"Use of Relative Code Churn Measures to Predict System Defect Density", ICSE
2005. Read in full.

- **Data:** Windows Server 2003 to SP1, 2,465 binaries. The outcome is SP1
  defects/KLOC.
- **Measures:**
  - M1 = churned LOC / total LOC
  - M2 = deleted / total LOC
  - M3 = files churned / file count
  - M4 = churn count / files churned
  - M5 = weeks of churn / file count
  - M6 = lines worked on / weeks of churn
  - M7 = churned / deleted
  - M8 = lines worked on / churn count
- **Spearman correlation with defect density:** M1 0.883, M3 0.868, M2 0.798.
- **Model fit:** absolute measures R² 0.05; relative measures R² 0.81. A model
  trained on 2/3 and tested on 1/3 reached Spearman 0.89–0.93.
- **Limitations:** a single system. Defect density divides by size, which
  partly builds in the result.

**Kaizen: adopt** M1–M3 and M8. Drop "weeks of churn", since agents have no
check-out time.

### 5.3 Hassan 2009 [moderate]

"Predicting Faults Using the Complexity of Code Changes", ICSE 2009. Read in
full.

- **Data:** six OSS projects, at subsystem level.
- **Inputs:** only feature-introducing changes. Maintenance changes such as
  copyright or re-indentation are excluded.
- **Entropy:** H = −Σ p_j log2 p_j over the files modified in a period, where
  p_j is file j's share of the lines modified. It is normalised by log2 n.
  The case study defines periods by bursts: a new period starts after 1 h
  without changes.
- **HCM** = Σ over periods of c · H, with c = 1, p_j, or 1/|F|.
  **HCM1d** adds exponential decay (φ = 10).
- **Results:** HCM had 13–42% less error than prior modifications. HCM1d
  beat prior faults by 15–38% (significant in 4 of 6 projects).
- **Replication:** in D'Ambros et al. 2010, HCM was mid-ranked and unstable.
- **Correction to the brief:** the decay names EDHCM and LDHCM come from
  D'Ambros 2010, not Hassan.

**Kaizen: adapt.** Use per-change entropy as a "scattered change" signal, and
exclude bulk or mechanical agent edits, as Hassan excludes maintenance
changes.

### 5.4 Moser, Pedrycz, Succi 2008 (abstract only)

ICSE 2008, DOI 10.1145/1368088.1368114. The PDF was blocked.

- **Abstract:** Eclipse files; change metrics were "more efficient" than code
  metrics; over 75% classified correctly, recall above 80%, false-positive
  rate below 30%.
- **Not verified:** the list of 17 metrics. Check it against the PDF before
  implementing.

### 5.5 D'Ambros, Lanza, Robbes 2010 [strong as a benchmark]

"An Extensive Comparison of Bug Prediction Approaches", MSR 2010. Read in full
(author copy). The EMSE 2012 extension was not read.

- **Data:** five Java systems with bi-weekly snapshots. The outcome is
  post-release class defects.
- **Best predictors:** weighted churn of source-code metrics (WCHU) and
  linearly decayed entropy of metric changes (LDHH). Spearman ρ was about
  0.28–0.56.
- **Weaker predictors:** LOC alone ρ 0.17–0.48, unstable. Single metrics such
  as NR, NFIX and HCM were unstable across systems.
- **Fix detection:** fixes detected by commit-message keywords were a poor
  proxy for tracker-validated fixes.

**Kaizen:**
- Use linear decay.
- "Churn of complexity" (Δ cognitive complexity per snapshot) fits a
  refactoring agent.
- Take fix labels from bead types, not keywords.

### 5.6 Rahman and Devanbu 2013 [strong]

"How, and Why, Process Metrics Are Better", ICSE 2013. Read in full (author
draft).

- **Data:** 12 Apache projects, 85 releases. Each model is trained on one
  release and tested on the next.
- **AUC:** process metrics about 0.9 versus about 0.75 for code metrics,
  across four learners.
- **Why:** code metrics barely change from release to release (Spearman about
  0.96), so their models keep naming the same large files.

**Kaizen:** dynamic signals (recent churn, recent coupling) should drive
prioritisation. Static complexity is for choosing *what to change* inside a
hotspot, not for finding hotspots.

### 5.7 Bird, Nagappan, Murphy, Gall, Devanbu 2011 [strong at Microsoft; contested in OSS]

"Don't Touch My Code! Examining the Effects of Ownership on Software
Quality", ESEC/FSE 2011. Read in full (Microsoft Research PDF).

- **Data:** Windows Vista and Windows 7, at binary level, with pre- and
  post-release failures.
- **Definitions:**
  - ownership = the top contributor's share of commits
  - minor contributor < 5% of commits; major ≥ 5% (a 2–10% sensitivity check
    gave similar results)
  - MINOR, MAJOR, TOTAL = contributor counts
- **Results:**
  - Adding MINOR raised adjusted R² from 26% to 46% (Vista pre-release) and
    from 24% to 70% (Windows 7 pre-release).
  - Post-release gains were smaller (Windows 7: 18% to 21%).
  - OWNERSHIP added little.
- **Replications:**
  - Greiler, Herzig, Czerwonka 2015, MSR (read in full): replicated at
    Microsoft at file and directory level. Weakly owned files had about 6× more
    bugs, and directory level was stronger.
  - Foucault, Falleri, Blanc 2014, EASE (abstract only): weak or no relation
    in 7 Java OSS projects; module size dominated.

**Kaizen: adapt with caution.** For agents, "contributor" can mean the
station or role, the prompt or skill version, or a session that had not read
the module before. Bird's mechanism is lack of expertise, and an agent
session that has not read the code is the closest analogue. Control for
size. This is the least transferable signal here.

### 5.8 Kamei et al. 2013 [strong]

"A Large-Scale Empirical Study of Just-in-Time Quality Assurance", IEEE TSE
39(6):757–773. Read in full.

- **Data:** 11 projects (6 OSS, 5 commercial), more than 250,000 changes
  (not 6 projects, as the brief said). Labels come from SZZ.
- **The 14 change features:**
  - Diffusion:
    - NS: subsystems touched
    - ND: directories touched
    - NF: files touched
    - Entropy: −Σ p_k log2 p_k over each file's share of modified lines,
      divided by log2 n
  - Size:
    - LA: lines added
    - LD: lines deleted
    - LT: lines in the files before the change
  - Purpose:
    - FIX: defect fix, from message keywords
  - History:
    - NDEV: developers who changed the files before
    - AGE: mean time since the last change
    - NUC: unique prior changes
  - Experience:
    - EXP: the developer's prior changes
    - REXP: prior changes weighted by 1/(years ago + 1)
    - SEXP: prior changes in the same subsystem
- **Preprocessing:** ND and REXP dropped as collinear. LA and LD divided by
  LT; LT and NUC divided by NF. Log transform.
- **Classification results:** accuracy 68%, recall 64%, precision 34%, AUC
  0.73.
- **Effort-aware results** (effort = LA + LD): inspecting the riskiest 20% of
  changed lines finds 35% of defect-inducing changes, against 20% for a random
  order.
- **Follow-ups:**
  - Yang et al. 2016 (abstract only): simple unsupervised rankings rivalled
    supervised models.
  - Fu and Menzies 2017 (read in full) reproduced Yang's numbers but disputed
    the general conclusion. Ranking by smallest LT is a strong baseline.

**Kaizen: adopt** the feature definitions and the effort-aware evaluation
(defects found at 20% effort, Popt), for any risk score on changes, including
Kaizen's own. Replace FIX keywords with the bead type. Redefine the
experience features (NDEV, EXP, SEXP) for agents. Always compare against the
smallest-first (1/LT) baseline.

---

## 6. Size as a confounder

### 6.1 El Emam, Benlarbi, Goel, Rai 2001 [strong]

"The Confounding Effect of Class Size on the Validity of Object-Oriented
Metrics", IEEE TSE 27(7):630–650. Read in full in the NRC technical report
version (ERB-1062, 1999, revised 2000); the TSE text was not read.

- **Data:** a C++ telecom framework, 174 classes, with 192 field faults.
- **Correlation with LOC:** WMC 0.88, RFC 0.88, NMA 0.86, CBO 0.46, LCOM 0.24.
- **Result:** WMC, CBO, RFC and NMA were each significantly associated with
  faults, but none remained significant once size was in the model. Size
  itself stayed significant.

### 6.2 Counter-arguments and later work (abstracts only)

- **Evanco 2003**, TSE 29(7): size does not come before OO metrics in time,
  so it is not a confounder; adding it can misspecify the model.
- **Gil and Lalouche 2017**, EMSE 22: a metric's apparent validity is
  predicted by its correlation with size (R² up to 0.97). Controlling for size
  removes most predictive power.

**Kaizen: adopt as a rule.** Every signal (complexity, smells, coupling
counts, clones) is reported with size controlled: per LOC, as a residual
against log LOC, or ranked within size bands. A refactoring that "improves" a
metric by shrinking or growing code is judged on the size-controlled value.
This is the same lesson as the agent-code studies in section 10.

---

## 7. Architecture: propagation cost, hidden structure, Lakos

### 7.1 MacCormack, Rusnak, Baldwin 2006 [moderate]

"Exploring the Structure of Complex Software Designs: An Empirical Study of
Open Source and Proprietary Code", Management Science 52(7):1015–1030. Read
pp. 1–24 of the HBS working paper 05-016; the end tables were not viewed.

**Method:**
- A file-level design structure matrix of function-call dependencies.
- Compares Linux with Mozilla, and Mozilla before and after its 1998
  redesign.

**Propagation cost:**
- V = binarise(Σ_{k=0..n} D^k), the reflexive transitive closure.
- PC = Σ V / n², the mean share of files each file can affect.
- Fan-in and fan-out visibility are the column and row sums divided by n.

**Results:**
- Mozilla 17.35% versus Linux 5.16%.
- After Mozilla's redesign: 2.78%, mostly from removing dependencies (density
  fell from 2.4 to 1.3 per 1000 pairs).

**Limitations:** two products, exploratory, no outcome data.

**Kaizen: adopt** PC on the file import graph as a trend and per-change delta,
not as an absolute target.

### 7.2 Baldwin, MacCormack, Rusnak 2014 (abstract only)

"Hidden structure: Using network methods to map system architecture",
Research Policy 43(8):1381–1397.

- **Abstract:** 1,286 releases of 17 applications. Most are core-periphery,
  with one large cyclic group called the "Core". Core size varies even among
  products with the same function.
- **Not verified:** the rules for classifying files as core, shared, control
  or periphery.

**Kaizen:** track the size of the largest SCC as a share of files. Do not
implement the four-way classification until the paper is read.

### 7.3 MacCormack and Sturtevant 2016 (not read)

"Technical debt and system architecture", JSS 120, was paywalled.
Sturtevant's 2013 MIT thesis was read as an abstract only. It reports, for one
firm, about 50% lower productivity and 3× defect density in architecturally
complex files [weak as read].

### 7.4 Lakos 1996 [weak as outcome evidence]

*Large-Scale C++ Software Design*, 1996. The book was not read. Definitions
come from three secondary sources:
- pub.dev `lakos` package documentation
- Lattix metrics documentation
- Apoung Kamga, Université Paris-Sud course slides (2012)

**Definitions:**
- CD(v) = number of components v depends on, directly or transitively,
  including itself.
- CCD = Σ CD.
- ACD = CCD / n.
- NCCD = CCD / CCD of a balanced binary tree of n nodes, where
  CCD_tree(N) = (N+1)(log2(N+1) − 1) + 1. This is exact only for N = 2^k − 1.
- PC = CCD / n² (MacCormack's propagation cost).

**Rules of thumb:** NCCD ≤ 1 is described as "horizontal" (good); above 2
probably means cycles. Lattix also reports cyclicality = share of elements in
a cycle.

**Kaizen:** compute CCD once and derive ACD, NCCD and PC from it. Report
cyclicality (share of files in non-trivial SCCs) next to the import-cycle
count.

---

## 8. Cognitive Complexity

### 8.1 Campbell's specification [weak (vendor), exact as definition]

Campbell G.A., "Cognitive Complexity: A new way of measuring
understandability", SonarSource white paper. Only v1.7 (2023) is online; it
was read in full except Appendix A
(sonarsource.com/docs/CognitiveComplexity.pdf).

**Basic rules:**
1. Ignore shorthand that reads as one statement. A method scores 0, and so
   does null-coalescing.
2. Add +1 for each break in linear flow.
3. Add more when flow-breaking structures are nested.

**+1 each:**
- if, else if, else, ternary
- switch, once for the whole switch
- each loop
- catch; try and finally score 0
- goto, and break or continue to a label or number
- each sequence of like binary logical operators: `a && b && c` = +1,
  `a || b && c || d` = +3
- each method in a recursion cycle

Early return and plain break or continue score 0.

**Nesting:**
- if, ternary, switch, loops and catch also add the current nesting level.
- else and else if do not.
- Nested functions and lambdas raise the nesting level but score 0
  themselves.

**Threshold:** none in the paper. Sonar rule S3776 defaults to 15 per
function (25 for C-family languages). Per Campbell on the Sonar community
forum, they started at 10 and found it noisy. The rules.sonarsource.com page
itself was not readable.

**gocognit** (Go; github.com/uudashr/gocognit; README read):
- Increments: the same structures as Sonar, plus `select`.
- Nesting is raised by function literals.
- Recursion is inferred unless `-exact-recursion` is set, which needs code
  that builds.
- `gocognit:ignore` excludes a function.
- golangci-lint's default threshold is 30, though it recommends 10–20.

### 8.2 Muñoz Barón, Wyrich, Wagner 2020 [moderate]

"An Empirical Validation of Cognitive Complexity as a Measure of Source Code
Understandability", ESEM 2020, arXiv 2007.12520. Read in full (the
forest-plot intervals could not be read).

- **Method:** a systematic search found 10 datasets: 427 snippets, about 24,000
  evaluations, 5 languages. Random-effects meta-analysis.
- **Pooled correlations with Cognitive Complexity:**
  - comprehension time: r = 0.54 (9 studies)
  - correctness: −0.13
  - subjective difficulty: −0.29
  - physiological measures: 0.00 (1 study, 12 snippets)
  - composite: 0.40
- **Caveats:** most snippets scored low, and only 2 studies had snippets above
  15. Snippets were edited to compile. No threshold was validated. Not
  replicated.

### 8.3 Other validation studies

- **Lavazza et al. 2023**, JSS 197 (abstract only): Cognitive Complexity
  correlates with understandability about as much as LOC and McCabe do, and
  adding it does not improve models.
- **Peitek et al. 2021**, ICSE (fMRI; partial: abstract and introduction): 19
  participants, 41 metrics. Metrics explain cognition "to a limited degree".
  Textual size drives attention.
- **Hao et al. 2023**, Frontiers in Neuroscience (EEG; about 60% read):
  Cognitive Complexity inverts some rankings and does not scale at high
  values. The sample was small.
- **Scalabrino et al. 2019**, TSE (partial): 121 metrics, none captures
  understandability (maximum |τ| ≈ 0.16).

**Kaizen: adopt** gocognit for Go and codemetrics elsewhere (same
specification). Use the score as a "do not increase" constraint and a
within-hotspot pointer, with a 15 per-function flag. Reject refactorings that
lower it by inflating LOC or the number of functions (section 6). Do not
treat a lower score as proof of better understandability.

---

## 9. Cohesion

### 9.1 Class cohesion measures

Briand, Daly, Wüst 1998, "A Unified Framework for Cohesion Measurement in
Object-Oriented Systems", EMSE 3:65–117 [strong as theory]. The definitions,
properties, Table 2 and the validation summary were read. The original papers
by Chidamber and Kemerer, Hitz and Montazeri, Bieman and Kang, and
Henderson-Sellers were not read; their definitions below are as given by
Briand et al.

**Definitions:**
- **LCOM1:** number of method pairs that share no attribute.
- **LCOM2** (Chidamber and Kemerer): P = pairs sharing no attribute, Q = pairs
  sharing at least one. LCOM2 = |P| − |Q| if |P| > |Q|, else 0.
- **LCOM3** (Hitz and Montazeri): connected components of the graph of
  methods, with an edge where two methods share an attribute.
- **LCOM4:** as LCOM3, plus an edge where one method calls the other.
- **Co** = 2(|E| − (|V| − 1)) / ((|V| − 1)(|V| − 2)).
- **TCC and LCC** (Bieman and Kang): the share of public-method pairs that are
  directly (TCC) or transitively (LCC) connected through attributes.
  Constructors are excluded.
- **LCOM5** (Henderson-Sellers) = ((1/a) Σ_j μ(A_j) − m) / (1 − m).

**Properties a cohesion measure should satisfy:**
- non-negative and normalised
- a null value and a maximum value
- monotonic: adding relationships never lowers cohesion
- merging two unconnected classes does not raise cohesion

**Which measures satisfy them:**
- Only TCC, LCC and RCI satisfy all four.
- LCOM1–4 are not normalised.
- LCOM5 can exceed 1.
- Co can be negative. Briand et al.'s fix, **Co′ = 2|E| / (|V|(|V| − 1))**,
  satisfies all four.

**Validity:**
- **Basili, Briand, Melo 1996** (as summarised by Briand et al.): LCOM was not
  significant for fault-proneness.
- **Al Dallal and Briand 2012**, TOSEM 21(2) [moderate]; read on the Simula
  copy.
  - Data: 4 Java systems, 11 cohesion metrics.
  - LCOM3 (p = 0.29) and LCOM4 (p = 0.65, the worst ROC) were the only ones
    *not* significant.
  - Their LSCC was best (ROC about 0.64).
  - Excluding constructors and accessors helped.
- **Marcus, Poshyvanyk, Ferenc 2008**, TSE (abstract only): the textual
  cohesion measure C3 adds to structural metrics in fault prediction.

Note: CodeScene's "Low Cohesion" check uses LCOM4, the measure with the
weakest fault evidence here.

### 9.2 Package metrics

- **Martin 1994**, "OO Design Quality Metrics" (read in full) [weak, no
  validation]:
  - I = Ce / (Ca + Ce)
  - A = abstract classes / all classes
  - D′ = |A + I − 1|
  - Martin's own D = |(A + I − 1)/2| is inconsistent with his stated range.
- **Martin 2000**, "Design Principles and Design Patterns" (read): REP, CCP
  ("classes that change together, belong together") and CRP. CCP is the
  principle behind Kaizen's P7 "split by cohesion".
- **Relational cohesion** H = (R + 1) / N, where R counts internal
  relationships and N the types. The 1.5–4.0 range comes from NDepend and
  CodeQL documentation [weak]. The attribution to Martin was not verified, and
  H is not normalised.
- **Mancoridis et al. 1998**, IWPC (read): MQ, with intra-connectivity
  A_i = μ_i / N_i² and inter-connectivity E_ij = ε_ij / (2 N_i N_j). A_i is the
  density of the dependency graph inside a cluster.
- **Ponisio 2006** PhD thesis (read in part): contextual cohesion = the
  fraction of pairs of interface classes that share a client. Validated only
  by qualitative case studies.
- **Not read:** Abdeen, Ducasse, Sahraoui 2011; Mišić 2001; Allen and
  Khoshgoftaar 1999.
- **Go tools:**
  - `lcom4go` (yahoojapan, archived 2025; source read) computes LCOM4 per
    named type over its methods and ignores package-level functions.
  - `spm-go` computes Martin's I/A/D only.
  - No Go package-cohesion metric and no Go validation were found.

### 9.3 Proposed definition of Go package cohesion

**Package declaration graph.** Built over the non-test, non-generated files of
one package.
- **Nodes:** one per top-level declaration:
  - each function
  - each named type, with its methods folded in
  - each var or const spec
- **Edges:** undirected. X–Y if X's signature or body refers to Y.
  - Resolve references with `types.Info.Uses`, keeping objects in the
    package scope.
  - Map selectors through `types.Info.Selections` to the receiver type.
  - Generic instances map to their origin object.
- **Optional:** add T–I edges where type T implements a package-local
  interface I.

**Measures:**
1. **Components:** the connected components and their members. This is the
   Go analogue of LCOM3/4. Use it to *propose* splits or moves, not as a
   score. A component with no exported member, not reachable from `init`, is
   a dead-code candidate (Kaizen P8).
2. **Density** = 2|E| / (|V|(|V| − 1)). This is Briand's Co′ and Mancoridis's
   A_i, and it satisfies all four cohesion properties. Compare packages only
   within size bands (section 6).
3. **Optional, for comparison with NDepend:** H.
4. **Optional, our own:** co-change cohesion, the share of a package's
   changes that touch more than one of its components. This follows from
   CCP; no source validates it.

**Failure modes:**
- **Test files:** exclude them.
- **Generated code:** exclude it as nodes but keep it as edge targets.
- **Hub declarations** (`init`, `New*` constructors, Config structs, loggers,
  `Err*` sentinels): report density with and without them. This follows the
  constructor exclusion in Bieman and Kang and in Al Dallal and Briand.
- **Packages with two or fewer declarations:** report n/a.
- **Hidden links:** reflection, and interface satisfaction without the
  optional edges, are missed.
- **Build tags:** say which configuration was analysed.
- **LLM-style many tiny helpers:** these produce many small components, so
  rank components by size.

**Confidence:**
- **High** that measures 1 and 2 faithfully carry the established definitions
  over to Go and can be computed exactly.
- **Medium** that they are useful refactoring triggers.
- **Low** that they predict defects or comprehension cost in Go: there is no
  Go validation, and LCOM4 failed in Al Dallal and Briand 2012.

---

## 10. Clones

### 10.1 Kapser and Godfrey 2008 [moderate]

"'Cloning Considered Harmful' Considered Harmful: Patterns of Cloning in
Software", EMSE 13(6) (extended from WCRE 2006). The key sections of the EMSE
version were read; the WCRE version was not read separately.

- **Method:** a catalogue of cloning patterns (forking, templating,
  customisation). Apache httpd and Gnumeric clones were sampled and rated by
  one author.
- **Results** (Apache):
  - minimum 30 tokens: 71% of clones rated good, 14% harmful
  - minimum 60 tokens: 42% good, 39% harmful
  - "Parameterized code" clones were mostly harmful.
- **Limitations:** a single rater; the results are sample-dependent.

**Kaizen: adopt** the triage principle that many clones are deliberate. This
is already Kaizen's P2 caveat.

### 10.2 Juergens, Deissenboeck, Hummel, Wagner 2009 [strong]

"Do Code Clones Matter?", ICSE 2009. Read in full (cqse.eu).

- **Data:** 5 systems (3 Munich Re C#, Cobol, Java). Near-miss clones of at
  least 10 statements, with edit distance at most 5. Developers rated every
  inconsistent group.
- **Results:**
  - 52% of clone groups were inconsistent.
  - 28% of the inconsistencies were unintentional.
  - Overall, 15% of inconsistent groups contained a fault.
  - About half of the *unintentional* inconsistencies were faults (mean 0.50,
    range 0.33–0.64).
  - 107 faults were found, 17 of them crash or data-loss.
- **Replication:** Wagner et al. 2016, SANER (abstract only): 17% of type-3
  clones faulty, a weaker link attributed to developers knowing about the
  clones.

**Kaizen: adopt.** Inconsistent near-miss clones are the highest-value clone
finding. Route them to review or a bug card, not to automatic unification.

### 10.3 Clone evolution

- **Göde and Koschke 2011**, ICSE: abstract only ("most clones are rarely
  changed"). The often-quoted 14.8% figure is unverified.
- **Göde 2009** and **Krinke 2007/2008**: not read.
- **Kim, Sazawal, Notkin, Murphy 2005** [moderate-strong], "An Empirical Study
  of Code Clone Genealogies", ESEC/FSE 2005. The full UW technical report
  version was read.
  - Data: two small Java systems.
  - 27–38% of clone genealogies changed consistently.
  - 64–69% could not be factored out locally.
  - 54–72% of genealogies that disappeared did so within about 8 check-ins.
  - **Kaizen:** apply a minimum age before refactoring a clone.
- **Mondal, Roy, Schneider 2014** [moderate], CSMR-WCRE 2014 (full) and ICSME
  2014 (key sections).
  - A *similarity-preserving change* is a commit after which the changed
    clones remain clones.
  - SPCP clones (those that received such changes) were 7.04% of all clones.
  - Association rules among clones use Zimmermann support and confidence.
  - Clones in the same clone class predicted co-change with 80% recall and
    74% precision, against 27% and 17% for non-clone code.
- **Barbour, Khomh, Zou 2013** [moderate-strong], JSEP 25(11) (key sections).
  - *Late propagation* means the clones diverge, then a later change
    reconciles them.
  - It occurs in 8–21% of changed genealogies.
  - These genealogies are more fault-prone; a reconciliation that leaves the
    other copy untouched is the riskiest.

**Kaizen: adopt** "live clone" = SPCP: a clone pair that co-changes while
staying similar, ranked by Zimmermann support and confidence. This is the
README's "join clones with change coupling", with a citation. Flag late
propagation (diverged, then reconciled) as high risk. Apply Kim's minimum
age.

---

## 11. Studies of agent-written code

None of the five measures churn, change frequency, survival, reverts or rework
of agent-written code in real repositories. Kaizen's proposed "rework rate"
signal has no direct literature support or contradiction.

### 11.1 Coppola, Esposito, Kazman, Lenarduzzi 2026 (no results)

"AI Writes Code, Humans Pay the Debt. An Empirical Study on the
Sustainability and Evolution of Agent-Generated Code", arXiv 2609.04208 v1
(June 2026). Read in full (HTML).

- **Status:** a Stage 1 registered report accepted at ESEM 2026. It is a
  study plan with **no results**.
- **Plan:**
  - Data: the SQuaD dataset (450 projects, about 628k issues), Java, issues
    before 2021.
  - Agents: the OpenCode framework with GLM-5, Qwen3.5 and Kimi K2.5.
  - Tools: SonarQube, Understand, CodeScene, CK, PMD and DV8.
  - Measures: 110 maintainability metrics.
  - RQ3 projects debt growth across releases counterfactually, which the
    authors call a "first-order approximation". It does not observe rework.

**Kaizen:** nothing to cite as evidence. Revisit at Stage 2.

### 11.2 Zhu, Tsantalis, Rigby 2026 [weak to moderate]

"AI-Generated Smells: An Analysis of Code and Architecture in LLM and
Agent-Driven Development", arXiv 2605.02741 v1 (May 2026, preprint). Read in
full (HTML).

**Experiment 1:**
- 90 CodeContests problems, 5 models, Python, smells detected by PyExamine.
- Long Method dominates. Counts are small: for example Qwen3-Coder 480B 11
  zero-shot, against 1 for the human solutions. Few-shot prompting made it
  worse.
- No statistical tests.

**Experiment 2:**
- MetaGPT with one model, 5 applications at 4 complexity stages (about 20
  repositories).
- LOC versus architectural smells: Spearman 0.94. LOC versus structural
  smells: 0.59. LOC versus code-level smells: 0.53.
- Prompt stage had no effect.
- The authors call the 0.94 a "Volume-Quality Inverse Law".

**Not measured:** coupling beyond RFC, correctness, and anything over time.

**Limitations:** few data points, one framework, one smell tool, and smell
counts not normalised by size. More code means more smells almost by
definition.

**Kaizen: adapt.** Agents produce more code, so size-normalise every smell and
complexity count (section 6). Do not cite the "law".

### 11.3 Patel, Hou, et al. 2026 (CodeThread; also the ICML 2026 workshop paper) [moderate]

Patel S.P., Hou B.L., Purohit A., Xu K., Pan J., He H., Chen V., "Is Agent
Code Less Maintainable Than Human Code?", arXiv 2606.21804 (v1 June 2026, v2
29 Sept 2026; v2 read in full).

The ICML 2026 listing, "Do AI Agents Write Less Maintainable Code Than Human
Developers?" (icml.cc/virtual/2026/82761), is the same work. It is a poster at
the workshop "Deep Learning for Code: Towards Human-Centered Coding Agents".
The OpenReview page was bot-blocked, so the camera-ready version was not read.

**Design:**
- Function bodies are removed. Either an agent or the original human code
  supplies them (PR1), and then an agent solves the benchmark issue on top
  (PR2).
- 1,377 tasks from SWE-Bench Verified, Multilingual, Pro and FeatBench: 78
  repositories, 10 languages.
- 4 models (Claude 4.5 Sonnet, GPT-5, GLM 4.7, MiniMax M2.5) on SWE-Agent, one
  run each.

**Results:**
- PR2 resolve rate was usually lower on agent-written PR1 code: down by up to
  13.1 points (GLM, SWE-Bench Pro). A few cells were positive.
- By task type the mean drop was largest for refactoring (−8.2 points, n = 70)
  and smallest for bug fixes (−4.1).
- In the 454 tasks where only one condition succeeded, human code won 64.3%.

**What predicted the gap** (logistic regression, McFadden R² 0.069):
- Predictive:
  - the agent's PR1 changed input validation, fallback or error-handling
    behaviour (odds ratio 1.83)
  - the PR2 patch was larger (odds ratio 1.88)
  - task difficulty
- Not predictive: deltas in cyclomatic complexity, cognitive complexity,
  Halstead volume and LOC.

**Survival** (LLM-judged, n = 262): the agent's behaviour change survived
into the next edit 85.9% of the time.

**Limitations:**
- Two-step chains only, so "compounding" over longer chains is untested.
- The PR1 step is synthetic.
- The tests were written for the human code, which favours it.
- There is no agent-then-human condition.
- The model explains very little of the variance.
- The ICML abstract's claim of higher complexity and verbosity in agent code
  is not reported as levels in the arXiv text.

**Kaizen: adapt.**
1. Complexity deltas did not predict downstream failure. Keep them as
   ranking aids, not as debt measures.
2. The predictive factor is drift in behavioural contracts. Kaizen refactors
   must preserve input validation, defaults, fallbacks and error contracts
   exactly; target characterization tests and mutation testing at those
   paths.
3. Record downstream patch size as a cost signal.

### 11.4 Gəruslu (Garousi), Aliyeva, Tüzün 2026 [weak to moderate]

"Factors Influencing the Quality of AI-Generated Code: A Synthesis of
Empirical Evidence", arXiv 2603.25146 v1 (March 2026, preprint). The main
text and Appendix B were read from the PDF.

- **Type:** a systematic review of 24 peer-reviewed studies from 2022–2025,
  mostly Copilot and ChatGPT on snippets; almost none agentic. ChatGPT
  assisted with screening.
- **Findings:** qualitative, "mixed, context-dependent".
  - Maintainability is inferred from static proxies "rather than from
    downstream maintenance effort".
  - In one primary study, passing tests did not correlate with SonarQube
    quality.
- **Limitations:** internal inconsistencies (22 versus 24 studies).

**Kaizen: ignore** as evidence, apart from confirming that static-analysis
cleanliness and functional correctness do not track each other.

### 11.5 Nunes, Figueiredo, Rocha, Nadi, Ferreira, Esteves 2025 [moderate]

"Evaluating the Effectiveness of LLMs in Fixing Maintainability Issues in
Real-World Projects", SANER 2025, arXiv 2502.02368. The arXiv PDF was read in
full. The proceedings version reportedly gives slightly different figures
(44.8% versus 44.9%) and was not read.

- **Data:** 10 Java/Maven projects. 127 SonarQube issues sampled across 10
  rules.
- **Setups:** Copilot Chat, Llama 3.1 70B zero-shot, and Llama 3.1 70B
  few-shot. Each took one shot with no feedback loop. The result was compiled,
  tested and re-analysed.

**Results** (Copilot / Llama zero-shot / Llama few-shot):

| Outcome | Copilot | Llama zero-shot | Llama few-shot |
|---|---|---|---|
| Fixed | 32.3% | 30% | 44.9% |
| Compile error | 25.2% | 33% | 29.1% |
| Test failure | 7.1% | 15% | 14.2% |
| New SonarQube issues | 4.7% | 4% | 0 |

- About 32–48% of attempts broke the build or the tests.
- New issues were checked only after compile and tests passed, so that
  column undercounts.

**By rule:**
- **Fixed well** (local, mechanical changes): commented-out code (12/13 with
  few-shot), TODO removal (12/16 with few-shot; zero-shot "implemented" the
  TODOs), nested ternaries, mergeable ifs.
- **Fixed badly:**
  - Cognitive complexity through Extract Method: 6–7 of 26 fixed, 11–15
    compile errors, behaviour changed in 4–7 cases.
  - Unused parameters: 0–1 of 10, because signature changes broke callers.
  - Generic exceptions.
  - Identical branches: dropped conditions changed the logic.

**Readability:** students preferred the LLM version in 59.5% of ratings. No
statistical test was reported.

**Limitations:** Java only, 2024-era models, no feedback loop (so a lower
bound for a modern agent), small numbers per rule.

**Kaizen: adopt** for the station's procedure:
- Gate every refactoring on compile, tests, the target finding gone, and no
  new analyser findings, plus mutation testing on the touched code.
- Run a build and test feedback loop, with a few-shot recipe per refactoring
  type.
- Make signature changes only through project-wide tools (LSP rename), never
  by hand.
- Treat Extract Method for complexity, parameter removal and exception-type
  changes as high risk.

### 11.6 Rework leads (read 2026-10-16)

The three arXiv papers were read in full from their arXiv HTML versions (text
extracted locally, not through a summarising reader); the numbers below are
from that text.

- **arXiv 2601.20109** (Cynthia, Muttakin, Roy; MSR 2026) **[weak to moderate]**.
  1,210 merged agent bug-fix PRs from 206 Python repositories (AIDev dataset,
  of 8,106 fix PRs; Codex 949, Copilot 106, Devin 100, Cursor 40, Claude Code
  15). SonarQube is run on the parent and the merged commit, and new issues are
  counted. Issues per KLOC of changed lines shows no significant difference
  between agents (Kruskal-Wallis); Cursor is the exception, with a higher
  median. Code smells dominate; no vulnerabilities; duplicated lines under 3%.
  *Churn only normalises counts. Nothing follows the code after merge: no
  rework, no survival.* Claude Code's n = 15 makes its per-agent figures noise.
  **Use:** confirms that issue counts must be normalised by change size (as
  "Size control" in section 1 already says).
- **arXiv 2603.28592** (Liu, Widyasari, Zhao, Irsan, Chen, Lo; v3) **[moderate]**.
  302,579 commits in 6,299 repositories (Python, JS, TS only; >= 100 stars)
  attributed to Copilot, Claude, Cursor, Gemini and Devin through git metadata
  (author name/email, bot login, Co-authored-by). ESLint, Pylint and Semgrep
  run on the file before and after each commit; an issue is "introduced" only
  if unmatched and on a changed line. 484,366 issues introduced (89.3% smells,
  6.0% correctness, 4.7% security); 9.1% of commits introduce at least one;
  per-tool 7.5% (Copilot) to 14.8% (Gemini). Survival at HEAD, matched by rule
  plus surrounding code: **105,364 of 464,900 = 22.7%**, with no monotone trend
  by age (19.4% at 6-9 months, 28.2% at 3-6, 22.8% above 9). AI commits fix
  slightly more smells than they introduce (439,817 vs 432,748) but introduce
  about 1.5 times as many security issues as they fix. The authors validated 99
  sampled issues: precision 85.9%, survival-classification accuracy 84.8%
  (recall 81.2%), so roughly one survival label in six is wrong.
  *Limits:* no human-commit baseline, so 22.7% is not "worse than humans";
  the metric is lint issues, not rewritten lines; repositories where an AI is
  named in git metadata are a self-selected subset; "survives" includes
  "nobody touched the file". The paper does record whether the file was
  modified again before HEAD. **Use:** the attribution rule (git metadata
  trailers) and the "unmatched issue on a changed line" rule are directly
  reusable for the line; see sweatshop-8t5.16.
- **arXiv 2601.21276** (Huang, Jaisri, Shimizu, Chen, Nakashima, Rodriguez-Perez;
  MSR 2026) **[weak to moderate]**. Python repositories with > 500 stars from
  AIDev; 3,858 PRs for the metrics and sentiment analysis; the redundancy
  analysis is **one repository (crewAI, 617 PRs)**. Cyclomatic complexity and
  LOC differ little between agent and human PRs; 85% of file pairs have zero
  complexity change. The paper's own "Max Redundancy Score" (maximum cosine
  similarity of CodeSage embeddings between each new function and existing
  functions, after excluding PyRef-detected moves and renames) averages 0.2867
  for agents against 0.1532 for humans (Mann-Whitney p < 0.001). The score is a
  similarity of embeddings, not a clone detector; no threshold is validated
  against real clones, and the manual check of the refactoring filter covered 10
  samples. Reviewer sentiment is more neutral or positive for agent PRs.
  **Use:** supports tracking *new code similar to existing code* (a Type-3/4
  clone-at-birth signal) as a candidate, but with one repository and an
  unvalidated metric it does not justify a threshold.
- **GitClear, "AI Copilot Code Quality: 2025"** **[weak: vendor]**. Only the
  landing-page abstract was readable; the PDF is behind a lead form. Abstract
  claims: 211 million changed lines, 2020-2024, from repositories owned by
  Google, Microsoft, Meta and enterprise customers; refactoring-type (moved)
  lines fell from 25% of changed lines in 2021 to under 10% in 2024; "copy/pasted"
  lines rose from 8.3% to 12.3%. The page title says "4x growth in code clones"
  and press coverage says 8x for duplicated blocks; neither could be checked
  against the report, so do not quote them. The dataset is private, the metric
  definitions are GitClear's own, and the study does not identify which lines
  an AI wrote, so it shows a time trend that coincides with AI adoption, not an
  effect of it.

**Conclusion:** the gap stands. Liu et al. come closest and show survival of
*issues*; nobody has measured the fate of *lines* an agent wrote, nor compared
against human-written lines in the same repositories. sweatshop-8t5.16 should
therefore report agent-line survival beside the same measure for human lines in
the same period, or label the figure as unbaselined.

---

## 13. Mutation testing

Read 2026-10-16. Text of Just et al. (FSE 2014), Petrovic et al. (arXiv
2102.11378) and the Papadakis et al. survey was extracted from the PDFs
locally; every number below is from that text.

### 13.1 Just, Jalali, Inozemtseva, Ernst, Holmes, Fraser 2014 [moderate]

Are mutants a valid substitute for real faults? 357 real faults in five Java
programs (Chart, Closure, Math, Time, Lang; 321 KLOC), developer-written and
generated test suites.
- Mutation score of the fixing test suite rose over the pre-fix suite for 362
  of 480 developer test pairs (75%); statement coverage rose for 222 (46%). The
  association between the two is significant (chi-square 78.13, p < 0.001), so
  coverage must be controlled.
- With coverage controlled, mutation score still rose for 153 of 258 pairs
  (59%) that added no coverage.
- Correlation of mutation score with real-fault detection is positive, "usually
  strong or moderate", and significantly higher than statement coverage's for
  every program except Time (Wilcoxon, p < 0.05). Effect sizes A12 0.66 to 0.81.
- **73% of faults are coupled to a mutant from commonly used operators; 27%
  (95/357) are not.** Of those 95, 25 need a stronger operator, 7 a new one, and
  63 (17.6% of all faults) have no obvious mutant. The conclusion text reports
  this as "17%".
- Conditional-operator, relational-operator and statement-deletion mutants are
  most often coupled to real faults, and are not known to produce many equivalent
  mutants.
- Threats: five Java programs, all well tested; faults were linked through
  bug-tracker identifiers, which is a biased sample.

### 13.2 Papadakis, Shin, Yoo, Bae, ICSE 2018 [moderate; read at survey level only]

The paper itself was not read. What was read: its row in the Papadakis et al.
survey (section 13.4): mutation score and test-suite size both correlate with
fault detection, "but often the individual (and joint) correlations are weak";
suites with very high mutation scores do significantly better than those with
lower scores. A search-engine summary of the abstract adds that the correlations
are weak when suite size is controlled, and that the top-ranked suites by
mutation score still beat random suites of the same size. **That second
sentence is unverified against the paper.** Read the PDF before quoting.
Net effect on Just et al.: mutation score is a useful *ranking* signal for
tests, a poor *absolute* predictor, and part of its correlation comes from
suite size.

### 13.3 Petrovic, Ivankovic, Fraser, Just, "Practical Mutation Testing at Scale" (Google; arXiv 2102.11378) [moderate: one company, developer-feedback labels]

- The whole-codebase score is infeasible (about 2 billion LOC, over 500 million
  test executions a day) and "neither concrete nor actionable". Google
  mutates *only changed, covered lines during code review*.
- Developers initially judged **85%** of reported mutants unproductive
  (trivially equivalent, or detectable but not worth a test). Suppression rules
  for "arid" AST nodes (logging, memoisation lookups, initial capacities,
  guaranteed-nonnegative `size()` comparisons) raised the productive share from
  15% to **89%**; across the whole dataset **82%** of surfaced mutants with
  feedback were labelled "Please fix".
- Median mutants per change: 820 unfiltered, 77 with one per line, **7** with
  arid suppression plus one per line.
- 87.5% of generated mutants are killed by the initial suite; the survivors are
  what a developer sees. Survival varies by operator (unary operator insertion
  9.5%, about half that of logical-connector replacement) and by language
  (Dart higher).
- Scale of the data: about 17 million mutants over 760,000 changes; 2 million
  mutants surfaced.
- Equivalent mutants "cannot generally be detected automatically"; Google's
  answer is language-specific heuristics, in Go for example gathering deleted
  symbols into a slice so the compiler does not reject an unused import.
- Threat acknowledged by the authors: the data is specific to Google's style and
  review process.

### 13.4 Papadakis, Kintis, Zhang, Jia, Le Traon, Harman 2019, "Mutation Testing Advances" (survey, Advances in Computers) [moderate; secondary]

Read in text form, selectively (equivalence, redundancy and the test-criteria
tables), not cover to cover.
- Equivalent-mutant identification is undecidable. Trivial Compiler Equivalence
  (compare compiled object code) is conservative and identifies at least 30% of
  equivalent mutants; as a redundancy detector it finds on average 21% (C) and
  5.4% (Java) of mutants. Second-order mutation cuts equivalent mutants by 80 to
  90% at about 10% or less loss in test effectiveness (Papadakis and Malevris).
- The survey's own words on real-fault correlation: "neither clear
  understanding nor much empirical evidence concerning whether and when mutants
  are correlated with real faults".
- Duplicate or redundant mutants inflate the score and make it "hard to
  interpret".

### 13.5 What this means for Kaizen

1. **Per-change, not per-repository.** Score only mutants on lines the card
   touched and the tests cover, the Google design. This is what
   sweatshop-8t5.5 already scopes.
2. **Report survivors with their operator, and suppress known-arid ones.**
   A bare ratio is inflated by equivalent mutants and by suite size; the
   surviving mutants are the actionable output. Starting rules from Google:
   logging, memoisation lookups, capacities and timeouts, non-negative-size
   comparisons.
3. **Prefer the operators Just et al. found coupled to real faults**
   (conditional, relational, statement deletion) when a sample must be cut.
4. **Do not treat the score as a gate or a quality number.** 17% of real faults
   have no coupled mutant, Papadakis 2018 finds weak correlations once suite
   size is held fixed, and nothing here is validated on Go or on tests written
   by agents (who can write assertions that kill mutants, not faults).
   Record it and watch it, which is what sweatshop-8t5.15 does, and check
   against closed bug cards in sweatshop-8t5.8.
5. **Treat a rising score with a falling count of surviving productive mutants
   as the signal; treat a score on its own as a trend.** Compare like with like:
   same tool, same version, same operator set (a version change forces a new
   baseline, as the epic already states).

Cards affected: sweatshop-8t5.5 (scope, arid-node suppression, operator
priority), sweatshop-8t5.15 (what to record: operator and survivor identity,
not only the ratio; do not treat as a predictor until validated),
sweatshop-8t5.8 (validate against real faults here), sweatshop-8t5.16 (baseline
for agent-line survival).

---

## 12. Sources not read (and why)

Second attempt 2026-10-16. The books could not be read: they are
copyrighted and no legal full text is available in this environment, so they
stay unread rather than being summarised from secondary sources.

| Source | Reason |
|---|---|
| Tornhill, *Your Code as a Crime Scene*; *Software Design X-Rays* | Books, paywalled; no open copy |
| MacCormack, Baldwin, Rusnak, *Hidden Structure* (book) | Book; not available. Journal papers in section 7 stand in |
| Lakos, *Large-Scale C++ Software Design* (1996) | Book; secondary sources used |
| Mo et al., WICSA 2015 | IEEE and DTIC copies blocked; TSE version read instead |
| DV8 documentation | Connection refused |
| Gall, Jazayeri, Krajewski 2003 | IEEE paywall; no open copy found in a second search |
| Oliva and Gerosa, SBES 2011 (DOI 10.1109/SBES.2011.39; 91% of logical dependencies join structurally unrelated artifacts, by its abstract) | Closed access. The open Oliva-Gerosa paper on the *origins* of logical dependencies (first 100k revisions of the Apache SVN repository, confidence >= 50% covering about 78% of dependencies) was opened instead; it is a different paper and was only skimmed, so nothing in this note rests on it |
| Baldwin, MacCormack, Rusnak 2014; MacCormack and Sturtevant 2016 | 403; abstract only |
| Moser et al. 2008 | ACM PDF blocked; abstract only |
| D'Ambros et al. EMSE 2012 | The author and repository copies returned HTML, not the PDF; MSR 2010 version read |
| Foucault et al. 2014; Yang et al. 2016; Evanco 2003; Gil and Lalouche 2017 | Abstracts only |
| Campbell, TechDebt 2018 | No open copy |
| Lavazza et al. 2023 | 403; abstract only |
| Göde and Koschke 2011 (abstract: most clones are rarely changed and unintentional inconsistent changes are few); Göde 2009; Krinke 2007/2008 | No open PDF; abstract only |
| Abdeen et al. 2011; Mišić 2001; Allen and Khoshgoftaar 1999 | Blocked or not found |
| Original LCOM papers (Chidamber-Kemerer, Hitz-Montazeri, Bieman-Kang, Henderson-Sellers) | Definitions taken from Briand, Daly, Wüst 1998 |
| GitClear 2025 report body | Behind a lead form; landing-page abstract only (section 11.6) |
| Papadakis et al., ICSE 2018 (full paper) | Not opened; survey summary only (section 13.2) |
| Mutation-testing literature beyond sections 13.1 to 13.4 (Chekam et al. 2017; Google's 2018 ICSE-SEIP paper; Grun, Schuler, Zeller on equivalent mutants) | Not read |
