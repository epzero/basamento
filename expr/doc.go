// Package expr hosts the CEL environment (cel.dev/cel-go) used for
// edge conditions, guards, gate acceptance, price formulas and authorization
// conditions, plus gojq for JSON-to-JSON mapping.
//
// Rules (ADR-0003): no clock or random functions (pass run.now explicitly);
// expressions are type-checked against the state schema at compile time;
// evaluation cost is limited; comprehensions over maps iterate in sorted key
// order so results never depend on Go map ordering.
package expr
