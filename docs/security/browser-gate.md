# Browser capability gate

**Status:** deferred; no browser tool is registered.

Google Chrome is present in the development host, but a browser binary alone does not satisfy the browser threat model. The current HTTP fetcher has a custom resolver/dialer that pins only public IP targets. A direct Playwright/Chrome launch would resolve page subresources inside Chromium after interception, leaving a DNS-rebinding gap; a fresh browser context also does not by itself prevent `file://` navigation or local-file exposure.

The current browser gate requires a verified design that enforces all of the following before registration:

- every navigation and subresource is routed through a public-IP-pinning fetch/proxy path;
- redirects and DNS rebinding are revalidated at connection time;
- `file://`, localhost, private, link-local, and reserved targets are blocked before content is rendered or returned;
- a fresh per-task context has no cookies, credentials, or personal profile;
- downloads and form/login/submit actions remain disabled unless separately approved;
- output bytes, actions, screenshots, traces, and wall time are bounded and cleaned up.

Until those conditions are verified by browser smoke tests and SSRF tests, planner validation rejects `browser` steps. Static public HTML is supported through the SSRF-safe fetcher. This avoids silently presenting the installed Chrome binary as an isolated browser subsystem.

PDF extraction has a separate [runtime policy and verification boundary](../pdf-limits-repair.md). The 33-test frontend suite and mocked Chrome run passed, verifying presentation/replay of source failures. The gated live PDF test verified a file-backed download only; `RootlessExtractor` was skipped because `PDF_EXTRACTOR_IMAGE` was unset and the runtime check was unavailable. These results do not establish successful parsing of the real PDF. Neither browser availability nor rootful Docker may be reported as isolated PDF extraction readiness.
