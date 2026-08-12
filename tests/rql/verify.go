// package: tests/rql / integration
// type:    tool
// job:     the answer verifier — one registered rule per `R-Q*` obligation an answer alone can
// carry, so a rule nobody checks reads as an absent entry; test-facing, wired into Run
// limits:  SOUNDNESS ONLY: green means nothing returned is wrong, never that the answer is
// complete — 3 correct claims of 5 satisfy every rule here. Judges what it is given and
// never derives the expected set, which would be a second engine (-> tests/matrix)
package rql

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/flocko-motion/ranke-go"
)

// Violation is one answer-rule failure: the rule, the element index (-1 where the
// rule judged the whole answer), and why.
type Violation struct {
	Rule  string
	Index int
	Err   error
}

// Error renders the violation, with its element index where it has one.
func (v Violation) Error() string {
	if v.Index < 0 {
		return fmt.Sprintf("%s: %v", v.Rule, v.Err)
	}
	return fmt.Sprintf("%s [element %d]: %v", v.Rule, v.Index, v.Err)
}

// AnswerRule describes a registered answer rule: Name is the stable identifier
// (WithSkipRules), Rule the statement printed on violation.
type AnswerRule struct{ Name, Rule string }

// AnswerRuleSet lists the rules in application order — the list to read when asking
// which `R-Q*` rules an answer is held to at all.
func AnswerRuleSet() []AnswerRule {
	out := make([]AnswerRule, len(answerRules))
	for i, r := range answerRules {
		out[i] = AnswerRule{Name: r.name, Rule: r.rule}
	}
	return out
}

// VerifyOption configures a verification pass.
type VerifyOption func(*verifyConfig)

type verifyConfig struct{ skipRule map[string]bool }

// WithSkipRules omits the rules named by AnswerRule.Name, so a costly rule is left
// out deliberately and by name. Unknown names are ignored.
func WithSkipRules(names ...string) VerifyOption {
	return func(c *verifyConfig) {
		if c.skipRule == nil {
			c.skipRule = make(map[string]bool, len(names))
		}
		for _, n := range names {
			c.skipRule[n] = true
		}
	}
}

// answerUnderVerification is the read-only surface a rule inspects.
type answerUnderVerification struct {
	q        ranke.Query
	elements []ranke.QueryResult // every element, the report among them
	results  []ranke.QueryResult // the results alone
	report   *ranke.QueryReport  // the report element's payload, nil when none
	arc      ranke.Archive
	u        ranke.Universe
}

// answerRule is one answer invariant: a name, the rule in words, and a check scoped
// per element (each) or over the whole answer (answer).
type answerRule struct {
	name   string
	rule   string
	each   func(ctx context.Context, i int, r ranke.QueryResult, t *answerUnderVerification) error
	answer func(ctx context.Context, t *answerUnderVerification) []Violation
}

// answerRules is the ordered rule set; an obligation's statement, scope and
// implementation all live on its one entry. Absent so far, deliberately, and left
// unbackticked because nothing here binds them: R-QDETAIL, R-QFORM, R-QCONTENT,
// per-element R-QANCHOR.
var answerRules = []answerRule{
	{
		name: "element tag",
		rule: "every element carries a tag naming what it holds, and the payload that tag names is present (`R-QSTREAM`)",
		each: ruleElementTag,
	},
	{
		name:   "report placement",
		rule:   "a report is the final element, and present exactly when execution.report asked for one (`R-QREPORT`)",
		answer: ruleReportPlacement,
	},
	{
		name:   "result bound",
		rule:   "the results are within limit.results, and a report claims truncation only where a bound could have cut the read (`R-QLIMIT`)",
		answer: ruleResultBound,
	},
	{
		name:   "answer order",
		rule:   "the returned sequence is non-descending under the order keys, then (created_at, id) (`R-QSORT`); pairs whose values are not comparable, and answers carrying no claims, are passed over",
		answer: ruleAnswerOrder,
	},
	{
		name:   "scope membership",
		rule:   "every returned claim is in the scope's graph, by ClaimsInBranches (`R-QSCOPE`); a $universe scope and the narrowing by Select.Head go unchecked, both needing a closure walk, so R-QHEAD stays uncited here",
		answer: ruleScopeMembership,
	},
}

// VerifyAnswer returns every violation in elements, not the first — three broken
// checks report three. See the header on what a green result does not mean.
func VerifyAnswer(ctx context.Context, arc ranke.Archive, u ranke.Universe, q ranke.Query,
	elements []ranke.QueryResult, opts ...VerifyOption) []Violation {
	cfg := &verifyConfig{}
	for _, o := range opts {
		o(cfg)
	}
	t := &answerUnderVerification{q: q, elements: elements, arc: arc, u: u}
	for _, r := range elements {
		if r.Kind == ranke.KindReport {
			t.report = r.Report
			continue
		}
		t.results = append(t.results, r)
	}

	var out []Violation
	for _, rule := range answerRules {
		if cfg.skipRule[rule.name] {
			continue
		}
		if rule.each != nil {
			for i, el := range elements {
				if err := rule.each(ctx, i, el, t); err != nil {
					out = append(out, Violation{Rule: rule.name, Index: i, Err: err})
				}
			}
		}
		if rule.answer != nil {
			for _, v := range rule.answer(ctx, t) {
				v.Rule = rule.name
				out = append(out, v)
			}
		}
	}
	return out
}

// SkipRulesEnv names rules to skip, comma-separated — the lever for a rule whose
// cost the fast gate cannot carry.
const SkipRulesEnv = "RANKE_SKIP_ANSWER_RULES"

// skipEnvRules reads SkipRulesEnv into an option for Run.
func skipEnvRules() []VerifyOption {
	raw := os.Getenv(SkipRulesEnv)
	if raw == "" {
		return nil
	}
	var names []string
	for _, n := range strings.Split(raw, ",") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return []VerifyOption{WithSkipRules(names...)}
}

// violationsError renders every violation as one error, so a caller checking only
// err still sees them all.
func violationsError(q ranke.Query, vs []Violation) error {
	lines := make([]string, 0, len(vs)+1)
	lines = append(lines, fmt.Sprintf("answer violates %d rule check(s) — %s", len(vs), Describe(q)))
	for _, v := range vs {
		lines = append(lines, "  "+v.Error())
	}
	return fmt.Errorf("%s", strings.Join(lines, "\n"))
}
