// package: ranke / verification
// type:    logic
// job:     VerificationRun — a handle to a (possibly long-running) verification pass
// limits:  placeholder surface; the full progress/result/cancel design is still to be worked out
package ranke

// VerificationRun is a handle to a verification pass. Verification runs
// over an immutable closure, so it parallelises and may take a while; the
// run lets a caller observe progress and obtain the terminal result.
//
// TODO: this is a placeholder. The real surface — progress (checked/total),
// a Done signal, the terminal result/error, and cancellation — is part of
// the verification design still to be settled.
type VerificationRun interface {
	verificationRun()
}
