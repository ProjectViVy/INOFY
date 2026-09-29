# S04 — Bounded Repeat Container

## Status: Done

## Rulings
- Repeat body "input" binds the loop state packet, not the run root
  input: init stamps ctlPacket{out: initial state, $iter:0, $path};
  ctl emits the next iteration's state the same way. The uniform
  ToField("input") mapping then feeds every body node.
- Iteration identity rides inside the packet as $iter/$path so any
  body node resolves its logical key container/<iter>/<nodeID> via
  runtimePath without executor awareness; retries keep the key
  because attempts re-run the same lambda on the same packet.
- Eino `WithMaxRunSteps` is rejected in dag mode (compile error), so
  the defensive bound is the explicit ctl counter (max_iterations)
  rather than a step ceiling — cap exhaustion yields `iteration_limit`
  or the declared on_error literal.
- ctl pre/post state handlers carry repeatCtrl{Iter,StatePacket}
  across iterations; registered via schema.RegisterName[*repeatCtrl]
  ("inofy_repeat_ctrl") for future Eino checkpointing.
- until evaluates only after body END settles (ctl is downstream of
  the body workflow node), so dormant ports can never copy stale
  state — unchosen region nodes simply never ran.
- Nested repeat (depth > 1) rejected with invalid_definition;
  zero/negative max_iterations rejected at compile.
- retryPolicy/timeout_ms now accept native JSON ints as well as
  json.Number (test docs use native ints).

## Deferred (minor)
- repeatCtrl state is checkpointable but no checkpoint store wiring
  yet — resume lands in S06.
- Eino-level step ceiling inside cyclic graph unavailable in dag
  mode; only the semantic iteration cap bounds the loop.

## Evidence
- go test ./... -count=3 all green; go vet clean.
- TestRepeatStateAndLimit: one-pass until, state carry, zero cap
  rejected, cap→iteration_limit, on_error fallback.
- TestRepeatNestedBranchIdentity: switch-in-body convergence, dormant
  port isolation, distinct iteration keys, retry key stability,
  cancellation stops new work after completed child.
