# Keep-awake API (canonical Phase 1)

`NewProvider() Provider` selects the platform provider.
`Acquire(ctx, runID) (release func(), status Status, err error)` returns an
idempotent, concurrency-safe release function. Retain it and call it at pause,
terminal state and shutdown. Cancellation automatically releases and reaps
the child. The context must cover the active run, not just acquisition.

On Linux the fixed command is `systemd-inhibit --what=sleep:idle --mode=block
--who=Qwenchik --why=Active local research run -- <current-executable>
--qagent-keepawake-helper`. No shell, root or daemon/power-policy changes are
used. The inhibitor is resolved only under `/usr/bin` and `/bin`. The run ID
is not sent to the host command. Acquisition is bounded to
3 s. A native re-exec helper runs before application startup and authenticates
a random 256-bit nonce over stdin/stdout. The readiness message can only be
sent after systemd-inhibit executes its child under the acquired lock.
Environment inheritance is restricted to fixed PATH/locale and helper mode/token.
Stderr is capped at 4096 bytes and is never included in status or errors.

`Available` means successful acquisition, not executable presence. `Active`
is true only after the verified readiness handshake. Returned status describes
that acquisition instant; after release/cancel the worker must project inactive
status. `PowerSource` is `unknown` here; the resource provider supplies separately
observed AC/battery telemetry. Executable presence or lock-listing alone never
marks this provider available. Missing/denied/broken inhibitors return inactive
status with a safe failure reason and `ErrInhibitorUnavailable`; workers should
display the warning and continue subject to resource guard policy. Context
cancellation returns its context error. Non-Linux providers explicitly report
unavailable. Import this package into the executable to enable helper init.

The helper exits when the parent stdin pipe closes, including parent crash.
Linux also sets a process group and parent-death SIGKILL; release and cancellation
close pipes, kill the group and wait/reap the launcher. No application inference
or argument dispatch executes in helper mode. Invalid helper tokens exit 126.
This lock does not guarantee overriding firmware, lid or desktop policy.

Tests use fake launchers and direct re-exec of the Go test binary. They acquire
no real inhibitor and include readiness forgery, cancellation, repeated release,
bounded stderr, process reaping and parent-pipe EOF cleanup.
