package ranke

// The canary for scripts/rule-citations.sh, kept from the Go test that gate
// replaced. A citation is a rule id in backticks, so prose reaching for a word
// shaped like one — a V-SHAPED curve, an R-RATED film — is not a citation and
// must not be reported as an id the spec fails to declare. A gate that reads
// these two as citations is loose enough to be edited into a nuisance, and a
// nuisance gate gets switched off.
