// package: ranke / query_codec
// type:    io
// job:     the RQL wire codec — a Query to and from the canonical JSON that ranke-graph's
// rql.schema.json fixes, plus the shape checks a query must pass before an engine sees it
// limits:  shape only; which claims a read returns is the executor's (-> query_default)
package ranke

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// DecodeQuery reads a query from its canonical JSON. An absent field keeps its zero
// value; what a caller's silence becomes is the binding's to decide.
func DecodeQuery(data []byte) (Query, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var w wireQuery
	if err := dec.Decode(&w); err != nil {
		return Query{}, Wrap(errDecodeQuery, err)
	}
	q, err := w.query()
	if err != nil {
		return Query{}, err
	}
	if err := ValidateQuery(q); err != nil {
		return Query{}, err
	}
	return q, nil
}

// EncodeQuery renders a query as canonical JSON, omitting every zero-valued field,
// so DecodeQuery returns the query it started as.
func EncodeQuery(q Query) ([]byte, error) {
	if err := ValidateQuery(q); err != nil {
		return nil, err
	}
	out, err := json.Marshal(newWireQuery(q))
	if err != nil {
		return nil, Wrap(errEncodeQuery, err)
	}
	return out, nil
}

// ValidateQuery holds a query to the schema's rules plus the one it cannot state, a
// step's Min against its Max. Wire and Go callers share it, so both reach one verdict.
func ValidateQuery(q Query) error {
	if q.Select.Branch == "" {
		return ErrQueryNoScope
	}
	if q.Select.Branch == BranchUniverse && q.Select.Head == nil {
		return ErrQueryNoHead
	}
	for i, step := range q.Select.Path {
		if err := validateStep(step); err != nil {
			return WithDetail(err, "select.path["+strconv.Itoa(i)+"]")
		}
	}
	if q.Where != nil {
		if err := validateWhere(*q.Where); err != nil {
			return err
		}
	}
	if err := validateOutput(q.Output); err != nil {
		return err
	}
	// A cap is a count, which the schema bounds at zero. Zero is the unbounded read.
	if q.Limit.Results < 0 {
		return WithDetail(ErrQueryBounds, "limit.results "+strconv.Itoa(q.Limit.Results))
	}
	if q.Limit.Time < 0 {
		return WithDetail(ErrQueryBounds, "limit.time "+q.Limit.Time.String())
	}
	for i, key := range q.Order {
		// A sort key names the field it orders on; an empty one names nothing.
		if key.Field == "" {
			return WithDetail(ErrQueryOrderField, "order["+strconv.Itoa(i)+"]")
		}
		if err := oneOf("order.compare", string(key.Compare), "", string(CompareNumeric), string(CompareLexical), string(CompareTemporal)); err != nil {
			return err
		}
		if err := oneOf("order.dir", string(key.Dir), "", string(SortAsc), string(SortDesc)); err != nil {
			return err
		}
	}
	// A Go caller's empty Layer is an ABSENT one; whitespace states a name and gives
	// none. The wire tells absent from empty and refuses the latter (`R-QLAYER`).
	if q.Execution.Layer != "" && strings.TrimSpace(q.Execution.Layer) == "" {
		return WithDetail(ErrQueryLayerName, "execution.layer")
	}
	return oneOf("execution.report", string(q.Execution.Report), "",
		string(ReportError), string(ReportWarn), string(ReportInfo), string(ReportDebug), string(ReportTrace))
}

// validateStep checks dir and hops. Max 0 is unbounded, so only a bounded Max can sit under Min.
func validateStep(step PathStep) error {
	if err := oneOf("dir", string(step.Dir), "", string(DirProvenance), string(DirUses), string(DirConnections)); err != nil {
		return err
	}
	// A hop count is a count, which the schema bounds at zero on both ends. A negative
	// one breaks that bound as well as the step rule, so it matches ErrQueryBounds too:
	// a caller watching traversals reads ErrQueryHops, one checking every schema minimum
	// reads ErrQueryBounds, and neither has to know the other's sentinel.
	hopBound := alsoMatches(ErrQueryHops, ErrQueryBounds)
	if step.Min != nil && *step.Min < 0 {
		return WithDetail(hopBound, "min "+strconv.Itoa(*step.Min)+" is negative")
	}
	if step.Max < 0 {
		return WithDetail(hopBound, "max "+strconv.Itoa(step.Max)+" is negative")
	}
	// A floor above a bounded ceiling breaks no bound, so it stays the step rule alone.
	if step.Max > 0 && step.MinHops() > step.Max {
		return WithDetail(ErrQueryHops, strconv.Itoa(step.MinHops())+" > "+strconv.Itoa(step.Max))
	}
	return nil
}

// validateWhere holds every node of the tree to exactly one form.
func validateWhere(w Where) error {
	forms := 0
	if len(w.And) > 0 {
		forms++
	}
	if len(w.Or) > 0 {
		forms++
	}
	if w.Not != nil {
		forms++
	}
	if w.Field != "" || w.Test != nil {
		forms++
	}
	if forms != 1 {
		return WithDetail(ErrQueryWhereForm, strconv.Itoa(forms)+" forms set")
	}
	if w.Field != "" || w.Test != nil {
		if w.Field == "" || w.Test == nil {
			return WithDetail(ErrQueryWhereForm, "a leaf carries both a field and a test")
		}
		return validateComparison(*w.Test)
	}
	for _, sub := range append(append([]Where{}, w.And...), w.Or...) {
		if err := validateWhere(sub); err != nil {
			return err
		}
	}
	if w.Not != nil {
		return validateWhere(*w.Not)
	}
	return nil
}

// validateComparison holds a comparison to one operator. An explicit empty `in` set
// counts, being present.
func validateComparison(c Comparison) error {
	ops := 0
	for _, set := range []bool{
		c.Eq != nil, c.Ne != nil, c.Lt != nil, c.Le != nil,
		c.Gt != nil, c.Ge != nil, c.In != nil, c.Glob != "",
	} {
		if set {
			ops++
		}
	}
	if ops != 1 {
		return WithDetail(ErrQueryComparisonForm, strconv.Itoa(ops)+" operators set")
	}
	return nil
}

// validateOutput checks each output axis against the values the schema fixes.
func validateOutput(o Output) error {
	if err := oneOf("output.shape", string(o.Shape), "", string(ShapeSingle), string(ShapePath)); err != nil {
		return err
	}
	if err := oneOf("output.detail", string(o.Detail), "", string(DetailID), string(DetailClaims), string(DetailEnvelope)); err != nil {
		return err
	}
	if err := oneOf("output.form", string(o.Form), "", string(FormOriginal), string(FormMaterialized)); err != nil {
		return err
	}
	if err := oneOf("output.encoding", string(o.Encoding), "", string(ResultNative), string(ResultJSON), string(ResultCBOR)); err != nil {
		return err
	}
	if err := validateEnvelopeOutput(o); err != nil {
		return err
	}
	if o.Content == nil {
		return nil
	}
	// A byte cap is a count, which the schema bounds at zero.
	if o.Content.Max < 0 {
		return WithDetail(ErrQueryBounds, "output.content.max "+strconv.Itoa(o.Content.Max))
	}
	// An absent overflow is omit (`R-QCONTENT`), so the pair needs only its cap.
	return oneOf("output.content.overflow", string(o.Content.Overflow),
		"", string(OverflowCutoff), string(OverflowOmit))
}

// validateEnvelopeOutput refuses the axes an envelope cannot answer for
// (`R-QDETAIL`). The bytes are the stored ones, so a resolved overlay is not among
// them and JSON is not what they are; both requests ask for something else under the
// name of the original.
func validateEnvelopeOutput(o Output) error {
	if o.Detail != DetailEnvelope {
		return nil
	}
	if o.Form == FormMaterialized {
		return WithDetail(ErrQueryEnvelopeAxis, "output.form materialized")
	}
	if o.Encoding == ResultJSON {
		return WithDetail(ErrQueryEnvelopeAxis, "output.encoding json")
	}
	return nil
}

// oneOf reports whether got is among allowed, naming the field when it is not.
func oneOf(field, got string, allowed ...string) error {
	for _, want := range allowed {
		if got == want {
			return nil
		}
	}
	return WithDetail(ErrQueryEnum, field+"="+strconv.Quote(got))
}

// --- the wire shapes ------------------------------------------------------
//
// A parallel set of types rather than tags on Query: an Id interface cannot unmarshal
// itself, omitempty drops a false or 0 operator, and Limit.Time is a duration the wire
// spells as a string. Pointers keep an absent field apart from a zero one.

type wireQuery struct {
	Select    wireSelect     `json:"select"`
	Where     *wireWhere     `json:"where,omitempty"`
	Output    *wireOutput    `json:"output,omitempty"`
	Order     []wireOrderKey `json:"order,omitempty"`
	Limit     *wireLimit     `json:"limit,omitempty"`
	Execution *wireExecution `json:"execution,omitempty"`
}

type wireSelect struct {
	Branch string         `json:"branch"`
	Head   *string        `json:"head,omitempty"`
	Claim  *string        `json:"claim,omitempty"`
	Path   []wirePathStep `json:"path,omitempty"`
}

type wirePathStep struct {
	Edges []string `json:"edges,omitempty"`
	Dir   string   `json:"dir,omitempty"`
	Min   *int     `json:"min,omitempty"`
	Max   *int     `json:"max,omitempty"`
	Nodes []string `json:"nodes,omitempty"`
}

type wireWhere struct {
	And   []wireWhere     `json:"and,omitempty"`
	Or    []wireWhere     `json:"or,omitempty"`
	Not   *wireWhere      `json:"not,omitempty"`
	Field string          `json:"field,omitempty"`
	Test  *wireComparison `json:"test,omitempty"`
}

type wireOutput struct {
	Shape    string       `json:"shape,omitempty"`
	Detail   string       `json:"detail,omitempty"`
	Form     string       `json:"form,omitempty"`
	Content  *wireContent `json:"content,omitempty"`
	Encoding string       `json:"encoding,omitempty"`
}

type wireContent struct {
	Max      int    `json:"max"`
	Overflow string `json:"overflow"`
}

type wireOrderKey struct {
	Field   string `json:"field"`
	Compare string `json:"compare,omitempty"`
	Dir     string `json:"dir,omitempty"`
}

type wireLimit struct {
	Results *int    `json:"results,omitempty"`
	Time    *string `json:"time,omitempty"`
}

type wireExecution struct {
	// Layer is a pointer so an ABSENT layer (the backend chooses) stays distinct from
	// a stated empty one, which pins nothing while asking to (`R-QLAYER`, minLength 1).
	Layer  *string `json:"layer,omitempty"`
	Report string  `json:"report,omitempty"`
}

// wireComparison names the one operator applied, so a false or 0 value survives.
type wireComparison struct {
	Op    string
	Value any
}

// UnmarshalJSON reads the single operator the object holds.
func (c *wireComparison) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return Wrap(errDecodeQuery, err)
	}
	if len(raw) != 1 {
		return WithDetail(ErrQueryComparisonForm, strconv.Itoa(len(raw))+" operators set")
	}
	for op, encoded := range raw {
		var value any
		if err := json.Unmarshal(encoded, &value); err != nil {
			return WrapDetail(errDecodeQuery, "where.test."+op, err)
		}
		c.Op, c.Value = op, value
	}
	return nil
}

// MarshalJSON writes the operator as the object's one key.
func (c wireComparison) MarshalJSON() ([]byte, error) {
	out, err := json.Marshal(map[string]any{c.Op: c.Value})
	if err != nil {
		return nil, WrapDetail(errEncodeQuery, "where.test."+c.Op, err)
	}
	return out, nil
}

// --- wire -> Query --------------------------------------------------------

// query maps the decoded wire shapes onto the library's types.
func (w wireQuery) query() (Query, error) {
	q := Query{}
	sel, err := w.Select.selection()
	if err != nil {
		return q, err
	}
	q.Select = sel

	if w.Where != nil {
		where, err := w.Where.where()
		if err != nil {
			return q, err
		}
		q.Where = where
	}
	if w.Output != nil {
		q.Output = w.Output.output()
	}
	for _, key := range w.Order {
		q.Order = append(q.Order, OrderKey{
			Field:   key.Field,
			Compare: Collation(key.Compare),
			Dir:     SortDir(key.Dir),
		})
	}
	if w.Limit != nil {
		limit, err := w.Limit.limit()
		if err != nil {
			return q, err
		}
		q.Limit = limit
	}
	if w.Execution != nil {
		if w.Execution.Layer != nil && strings.TrimSpace(*w.Execution.Layer) == "" {
			return q, WithDetail(ErrQueryLayerName, "execution.layer")
		}
		q.Execution = Execution{Report: ReportLevel(w.Execution.Report)}
		if w.Execution.Layer != nil {
			q.Execution.Layer = *w.Execution.Layer
		}
	}
	if err := w.wireOnlyEnums(); err != nil {
		return q, err
	}
	return q, nil
}

// wireOnlyEnums refuses the three values only a Go caller may set: the native encoding
// asks for Go objects, and report's error and warn are Go-side thresholds. ValidateQuery
// admits all three, so a Go-built query keeps them.
func (w wireQuery) wireOnlyEnums() error {
	if w.Output != nil {
		if err := oneOf("output.encoding", w.Output.Encoding, "", string(ResultJSON), string(ResultCBOR)); err != nil {
			return err
		}
	}
	if w.Execution != nil {
		if err := oneOf("execution.report", w.Execution.Report,
			"", string(ReportInfo), string(ReportDebug), string(ReportTrace)); err != nil {
			return err
		}
	}
	return nil
}

// selection maps the generator, parsing the two ids it may carry.
func (w wireSelect) selection() (Select, error) {
	sel := Select{Branch: w.Branch}
	head, err := parseOptionalId("select.head", w.Head)
	if err != nil {
		return sel, err
	}
	sel.Head = head
	claim, err := parseOptionalId("select.claim", w.Claim)
	if err != nil {
		return sel, err
	}
	sel.Claim = claim
	for _, step := range w.Path {
		sel.Path = append(sel.Path, PathStep{
			Edges: step.Edges,
			Dir:   Direction(step.Dir),
			Min:   step.Min,
			Max:   derefOr(step.Max, 0),
			Nodes: step.Nodes,
		})
	}
	return sel, nil
}

// where maps one node of the boolean tree, recursing into its subtrees.
func (w wireWhere) where() (*Where, error) {
	out := &Where{Field: w.Field}
	for _, sub := range w.And {
		mapped, err := sub.where()
		if err != nil {
			return nil, err
		}
		out.And = append(out.And, *mapped)
	}
	for _, sub := range w.Or {
		mapped, err := sub.where()
		if err != nil {
			return nil, err
		}
		out.Or = append(out.Or, *mapped)
	}
	if w.Not != nil {
		mapped, err := w.Not.where()
		if err != nil {
			return nil, err
		}
		out.Not = mapped
	}
	if w.Test != nil {
		test, err := w.Test.comparison()
		if err != nil {
			return nil, err
		}
		out.Test = test
	}
	return out, nil
}

// comparison places the wire's one operator on the library's Comparison.
func (c wireComparison) comparison() (*Comparison, error) {
	out := &Comparison{}
	switch c.Op {
	case "eq":
		out.Eq = c.Value
	case "ne":
		out.Ne = c.Value
	case "lt":
		out.Lt = c.Value
	case "le":
		out.Le = c.Value
	case "gt":
		out.Gt = c.Value
	case "ge":
		out.Ge = c.Value
	case "in":
		set, ok := c.Value.([]any)
		if !ok {
			return nil, WithDetail(ErrQueryComparisonForm, "in takes a set")
		}
		out.In = set
	case "glob":
		glob, ok := c.Value.(string)
		if !ok {
			return nil, WithDetail(ErrQueryComparisonForm, "glob takes a string")
		}
		out.Glob = glob
	default:
		return nil, WithDetail(ErrQueryComparisonForm, strconv.Quote(c.Op))
	}
	return out, nil
}

// output maps the shaping axes; each wire axis is one library axis.
func (w wireOutput) output() Output {
	out := Output{
		Shape:    Shape(w.Shape),
		Detail:   Detail(w.Detail),
		Form:     Form(w.Form),
		Encoding: ResultEncoding(w.Encoding),
	}
	if w.Content != nil {
		out.Content = &OutputContent{Max: w.Content.Max, Overflow: Overflow(w.Content.Overflow)}
	}
	return out
}

// limit maps the bounds, parsing the wire's duration string.
func (w wireLimit) limit() (Limit, error) {
	out := Limit{Results: derefOr(w.Results, 0)}
	if w.Time == nil {
		return out, nil
	}
	budget, err := time.ParseDuration(*w.Time)
	if err != nil {
		return out, WrapDetail(errDecodeQuery, "limit.time="+strconv.Quote(*w.Time), err)
	}
	out.Time = budget
	return out, nil
}

// parseOptionalId parses an id the wire may omit, naming the field on failure.
func parseOptionalId(field string, encoded *string) (Id, error) {
	if encoded == nil {
		return nil, nil
	}
	parsed, err := ParseId(*encoded)
	if err != nil {
		return nil, WrapDetail(errDecodeQuery, field, err)
	}
	return parsed, nil
}

// derefOr reads an optional wire scalar, falling back when it is absent.
func derefOr[T any](p *T, fallback T) T {
	if p == nil {
		return fallback
	}
	return *p
}

// --- Query -> wire --------------------------------------------------------

// newWireQuery renders a query into the wire shapes, omitting zero-valued fields.
func newWireQuery(q Query) wireQuery {
	w := wireQuery{Select: newWireSelect(q.Select)}
	if q.Where != nil {
		w.Where = newWireWhere(*q.Where)
	}
	if out := newWireOutput(q.Output); out != nil {
		w.Output = out
	}
	for _, key := range q.Order {
		w.Order = append(w.Order, wireOrderKey{
			Field:   key.Field,
			Compare: string(key.Compare),
			Dir:     string(key.Dir),
		})
	}
	if q.Limit.Results != 0 || q.Limit.Time != 0 {
		limit := wireLimit{}
		if q.Limit.Results != 0 {
			limit.Results = &q.Limit.Results
		}
		if q.Limit.Time != 0 {
			budget := q.Limit.Time.String()
			limit.Time = &budget
		}
		w.Limit = &limit
	}
	if q.Execution.Layer != "" || q.Execution.Report != "" {
		w.Execution = &wireExecution{Report: string(q.Execution.Report)}
		if q.Execution.Layer != "" {
			layer := q.Execution.Layer
			w.Execution.Layer = &layer
		}
	}
	return w
}

// newWireSelect renders the generator, each id in its multibase form.
func newWireSelect(sel Select) wireSelect {
	w := wireSelect{Branch: sel.Branch}
	if sel.Head != nil {
		head := sel.Head.String()
		w.Head = &head
	}
	if sel.Claim != nil {
		claim := sel.Claim.String()
		w.Claim = &claim
	}
	for _, step := range sel.Path {
		out := wirePathStep{Edges: step.Edges, Dir: string(step.Dir), Min: step.Min, Nodes: step.Nodes}
		if step.Max != 0 {
			out.Max = &step.Max
		}
		w.Path = append(w.Path, out)
	}
	return w
}

// newWireWhere renders one node of the boolean tree.
func newWireWhere(where Where) *wireWhere {
	w := &wireWhere{Field: where.Field}
	for _, sub := range where.And {
		w.And = append(w.And, *newWireWhere(sub))
	}
	for _, sub := range where.Or {
		w.Or = append(w.Or, *newWireWhere(sub))
	}
	if where.Not != nil {
		w.Not = newWireWhere(*where.Not)
	}
	if where.Test != nil {
		w.Test = newWireComparison(*where.Test)
	}
	return w
}

// newWireComparison finds the operator set. ValidateQuery has already held it to
// exactly one, so the first match is it.
func newWireComparison(c Comparison) *wireComparison {
	switch {
	case c.Eq != nil:
		return &wireComparison{Op: "eq", Value: c.Eq}
	case c.Ne != nil:
		return &wireComparison{Op: "ne", Value: c.Ne}
	case c.Lt != nil:
		return &wireComparison{Op: "lt", Value: c.Lt}
	case c.Le != nil:
		return &wireComparison{Op: "le", Value: c.Le}
	case c.Gt != nil:
		return &wireComparison{Op: "gt", Value: c.Gt}
	case c.Ge != nil:
		return &wireComparison{Op: "ge", Value: c.Ge}
	case c.In != nil:
		return &wireComparison{Op: "in", Value: c.In}
	default:
		return &wireComparison{Op: "glob", Value: c.Glob}
	}
}

// newWireOutput renders the shaping axes, nil when none is set. The native encoding
// asks for Go objects, so it drops like an unset value.
func newWireOutput(out Output) *wireOutput {
	encoding := out.Encoding
	if encoding == ResultNative {
		encoding = ""
	}
	if out.Shape == "" && out.Detail == "" && out.Form == "" && out.Content == nil && encoding == "" {
		return nil
	}
	w := &wireOutput{
		Shape:    string(out.Shape),
		Detail:   string(out.Detail),
		Form:     string(out.Form),
		Encoding: string(encoding),
	}
	if out.Content != nil {
		w.Content = &wireContent{Max: out.Content.Max, Overflow: string(out.Content.Overflow)}
	}
	return w
}
