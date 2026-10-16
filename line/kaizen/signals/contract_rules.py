"""The contract rules of the pre-submit contract gate (contract_gate.py): which
syntax carries a guard, a default, a fallback or an error contract.

A rule set is a table per language (contract_go.py). This file lists every set
in RULESETS, so adding a language is adding a contract_<lang>.py and one line
here; the gate holds no language knowledge.
"""

from contract_go import GO_CONTRACTS
from syntax import ContractRuleSet

RULESETS: tuple[ContractRuleSet, ...] = (GO_CONTRACTS,)
