"""The Go contract rules (contract_rules.py): guards, defaults, fallbacks and
error contracts, as tree-sitter queries capturing @contract."""

from lang_go import GO
from syntax import ContractRule, ContractRuleSet

GO_CONTRACTS = ContractRuleSet(
    language=GO,
    fixed_identifiers=frozenset({"panic", "recover", "errors", "fmt", "Errorf", "New"}),
    rules=(
        # The condition of an if that leaves the function: input validation and nil/error checks.
        ContractRule("guard-condition", """
            (if_statement condition: (_) @contract
              consequence: (block (statement_list [
                (return_statement)
                (expression_statement (call_expression function: (identifier) @fn (#eq? @fn "panic")))])))"""),
        # `if x == zero { x = default }`
        ContractRule("default-assignment", """
            (if_statement
              condition: (binary_expression operator: "==")
              consequence: (block (statement_list (assignment_statement))) !alternative) @contract"""),
        ContractRule("fallback-branch", """
            (if_statement alternative: (_) @contract)
            (default_case) @contract"""),
        ContractRule("error-return", """
            (return_statement (expression_list (identifier) @id (#match? @id "^(err|.*Err|.*Error)$"))) @contract"""),
        ContractRule("error-construction", """
            (call_expression
              function: (selector_expression operand: (identifier) @pkg field: (field_identifier) @fn)
              (#match? @pkg "^(errors|fmt)$") (#match? @fn "^(New|Errorf)$")) @contract
            (call_expression function: (identifier) @fn (#match? @fn "^(panic|recover)$")) @contract"""),
    ),
)
