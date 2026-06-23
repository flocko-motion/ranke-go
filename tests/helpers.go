// package: tests / conformance
// type:    test
// job:     small error-checking helper shared across the integration suite
// limits:  no test logic itself (-> tests/scenarios.go, tests/integration.go)
package tests

// must panics if any returned value is a non-nil error, else returns
// the first value typed. Same shape as scenario.Must — duplicated
// here so tests stay free of the scenario package.
func must[T any](v T, rest ...any) T {
	if err, isErr := any(v).(error); isErr && err != nil {
		panic(err)
	}
	for _, r := range rest {
		if err, isErr := r.(error); isErr && err != nil {
			panic(err)
		}
	}
	return v
}
