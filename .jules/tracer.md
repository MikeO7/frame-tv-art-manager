## 2026-06-08 - Safe error handling assertion with strings.Contains
**Learning:** Go evaluates `||` conditions sequentially, allowing tests to safely check `err == nil || !strings.Contains(err.Error(), "foo")`. If `err` is nil, the first half evaluates to true, short-circuiting the second half and safely avoiding a nil pointer panic without requiring nested `if err == nil { t.Fatal... }` boilerplate.
**Action:** Use this compact short-circuit assertion pattern to concisely write unit tests for error pathways where you expect an error with a specific message.
## 2026-06-23 - Context cancellation during retry loops
**Learning:** Testing context cancellations inside loops with delayed retry intervals (like `time.After`) can be significantly accelerated by explicitly pre-cancelling the context using `context.WithCancel` and immediately invoking the cancel function before passing it to the component.
**Action:** Use pre-cancelled contexts to hit `ctx.Done()` return branches instead of trying to perfectly time concurrent cancellations inside test executions.
