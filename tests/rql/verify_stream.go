// package: tests/rql / integration
// type:    tool
// job:     the answer rules of §Results and streaming — every element tagged with what it holds,
// and the report last and only where one was asked for
// limits:  reads the elements alone; nothing here touches the archive (-> verify_scope.go)
package rql

import (
	"context"
	"fmt"

	"github.com/rankegraph/ranke-go"
)

// tagPayload reports whether the payload r's tag names is present. A reader must
// never inspect a payload to learn what an element holds, so the tag has to be true.
func tagPayload(r ranke.QueryResult) (ok, known bool) {
	switch r.Kind {
	case ranke.KindClaimId:
		return r.ClaimId != nil, true
	case ranke.KindPathId:
		return len(r.PathId) > 0, true
	case ranke.KindClaimNative:
		return r.ClaimNative != nil, true
	case ranke.KindPathNative:
		return len(r.PathNative) > 0, true
	case ranke.KindClaimEncoded:
		return r.ClaimEncoded != nil, true
	case ranke.KindPathEncoded:
		return len(r.PathEncoded) > 0, true
	case ranke.KindReport:
		return r.Report != nil, true
	}
	return false, false
}

// ruleElementTag: `R-QSTREAM` — the tag is set, is one the vocabulary defines, and
// the payload it names is there.
func ruleElementTag(_ context.Context, _ int, r ranke.QueryResult, _ *answerUnderVerification) error {
	if r.Kind == "" {
		return fmt.Errorf("untagged element: a reader cannot tell what it holds without inspecting it")
	}
	ok, known := tagPayload(r)
	if !known {
		return fmt.Errorf("tag %q is not a ResultKind the vocabulary defines", r.Kind)
	}
	if !ok {
		return fmt.Errorf("tag %q names a payload the element does not carry", r.Kind)
	}
	return nil
}

// ruleReportPlacement: `R-QREPORT` — when, and only when, execution.report is set,
// the final element is a report. One report, and last.
func ruleReportPlacement(_ context.Context, t *answerUnderVerification) []Violation {
	var out []Violation
	asked := t.q.Execution.Report != ""

	for i, el := range t.elements {
		if el.Kind != ranke.KindReport {
			continue
		}
		if !asked {
			out = append(out, Violation{Index: i, Err: fmt.Errorf(
				"report element present, but execution.report asked for none")})
			continue
		}
		if i != len(t.elements)-1 {
			out = append(out, Violation{Index: i, Err: fmt.Errorf(
				"report is element %d of %d — a report is the FINAL element", i, len(t.elements))})
		}
	}
	if asked && t.report == nil {
		out = append(out, Violation{Index: -1, Err: fmt.Errorf(
			"execution.report is %q and no element carries a report", t.q.Execution.Report)})
	}
	return out
}
