# Creation admission

limits.create_per_minute_per_key defaults to 30 and accepts 1..10000. Each API
key has an independent token bucket whose capacity is the configured rate,
refilling continuously over one minute. Authentication happens first; admitted
creation attempts consume a token before JSON validation, quota checks or
runtime effects. Failed attempts also consume tokens. Exec, files, reads and
deletion are unaffected. Global/per-key sandbox quotas remain enforced.

A denied request returns HTTP 429 with error.code=rate_limited and Retry-After
as an integer number of seconds. The Go client's Error.RetryAfterSeconds exposes
this delay. It does not retry mutations automatically.

There are at most 10000 tracked keys. When full, entries idle for two minutes
are reclaimed; if none can be reclaimed, a new key is denied with Retry-After
120. This fails closed rather than resetting active buckets. Backwards clock
adjustments do not grant tokens. No per-key goroutines or timers are created.
Buckets reset on daemon restart. API keys themselves are never bucket labels,
logged values or persisted bucket state; only authenticated key IDs are used.
