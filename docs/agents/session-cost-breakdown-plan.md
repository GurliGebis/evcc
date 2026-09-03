# Session Cost Breakdown: Grid, Direct Solar, Battery

Design/rollout plan for evcc-io/evcc#33251. Read this before touching session
cost, `EnergyMetrics`, or the battery history feature. Written after aligning
with @naltatis's comments on the issue; supersedes any earlier plan discussed
only in chat history.

## Problem

A charging session's `Price`/`PricePerKWh` is a single blended number. Users
can't see how much they actually paid the grid vs. how much solar "cost" them
in foregone feed-in revenue vs. (eventually) how much came from the home
battery and at what value.

Home battery energy is currently documented (see
[FAQ savings calculation](https://docs.evcc.io/en/faq/#savings-calculation))
as treated like lossless self-produced solar. That assumption predates grid
battery-charging (arbitrage) and the optimizer's round-trip efficiency
(`core/site_optimizer.go:34`, `eta = 0.9`), so it's no longer accurate for
setups that use either.

## Staged rollout (do not skip ahead of maintainer sign-off)

### Step 1 — grid vs. green split (implemented on this branch, PR #33252 closed unmerged)

`Session.GridCost` / `Session.SolarCost`, split inside `EnergyMetrics`
(`core/energy_metrics.go`), fed by `Site.gridPrice()`/`feedInPrice()`
(`core/site_tariffs.go`) instead of the old pre-blended `effectivePrice()`.
"Green" here still means PV + battery discharge combined (today's
`greenShare()`, unchanged). Note: PR #33252 (which introduced this) is
**closed, not merged** upstream — the code only exists on this local branch
(`feature/session-cost-breakdown`), not on `evcc-io/evcc:master`.

UI: `SessionDetailsModal.vue` shows "Imported power" / "Direct solar" as muted
sub-rows under the existing total cost, gated on solar actually having been
used (`showPriceBreakdown`).

### Step 2 — split green into PV vs. battery share (implemented, in-memory only)

Added `Site.greenShareBySource(powerFrom, powerTo) (pvShare, batteryShare float64)`
next to the existing `greenShare()`. Convention: PV serves lower power ranges
before battery discharge does (matches real inverter dispatch priority —
battery only makes up a deficit PV can't cover). This is provably safe:
`pvShare + batteryShare` always equals today's combined `greenShare()` result
(verified algebraically for both `pv >= powerFrom` and `pv < powerFrom`
cases, and covered by `TestGreenShare` in `core/site_test.go`), so existing
aggregate stats (Solar %, `keys.GreenShareLoadpoints`, telemetry) are
unaffected — this step is a pure refactor for cost-attribution purposes only.
Existing dispatch/priority logic (who gets solar/battery energy first:
home/consumers before loadpoints) is unchanged and must stay that way.

**Persistence deliberately deferred** (naltatis, issue comment): *"I'd do the
persistence change in sessions only after the other two are done. This way we
don't change data structure and semantics multiple times."* So this step adds
`EnergyMetrics.SetGreenShareSplit`/`PvCost()`/`BatteryCost()` as in-memory,
optional, additive tracking (published as live state under
`sessionPvCost`/`sessionBatteryCost`, see `Publish()`) — **no new `Session`
struct fields, no DB columns, no migration, no CSV/API/UI changes** yet.
Those land together with Step 3 once its design is settled, to avoid two
separate schema changes.

**Until Step 3 lands**, battery-sourced energy in the cost split is valued at
the feed-in price, same as direct solar (`PvCost`/`BatteryCost` both derive
from the same `currentFeedInPrice`) — so Step 3 only has to change *how* the
battery share is priced, not the plumbing.

CO2 parity note (naltatis): CO2 should eventually get the same
grid/PV/battery attribution treatment as cost (grid-charged battery energy
counted at the grid CO2 tariff, solar-charged at zero) — not implemented in
this step (today's `effectiveCo2`/`Co2PerKWh` stay blended), but the split
computed here (`greenShareBySource`) is the same input Step 3's CO2 handling
will need.

Rename note (already applied in Step 1): the direct-solar row is labeled
**"Direct solar"**, not "Opportunity cost of exported solar" — reflecting
that once Step 3 ships, this row only ever means solar that bypassed the
battery.

### Step 3 — real battery cost basis (implemented)

This was the "larger can of worms" @naltatis flagged; implemented after
resolving the open questions below with him.

**Rejected approach:** inferring price from "what was the battery last
charged from" (grid/solar/mixed) — too simplistic given dynamic tariffs and
partial PV use during a single charge event (naltatis's explicit pushback on
GurliGebis's initial suggestion).

**Rejected approach (superseded):** a live, continuously-updated in-memory
cost-basis ledger persisted via `db/settings` (my own earlier proposal this
session). Workable, but duplicates data that's already durably stored and
doesn't match naltatis's stated direction.

**Implemented direction (naltatis, issue comment):** extend the existing
(experimental) battery history feature. It already persists, per battery
meter, per 15-min slot: `Energy` (charged in), `ReturnEnergy` (discharged),
`SocTemp` (SoC at slot start) — see `core/metrics/collector.go`,
`core/metrics/db_history.go`. Tariff prices (grid/feed-in/CO2/temperature)
are independently persisted at the same 15-min slot boundaries
(`core/site_tariffs.go:persistTariffs`, `metrics.PersistTariffs`). Both share
timestamps and are joinable with **no schema change**.

Algorithm: anchor on the battery's current usable stored energy
(`capacity * max(0, soc - reserveSoc) / 100`). Walk backward through
persisted slots, FIFO-style, accumulating charge-in energy (and its
per-slot price/CO2) until the currently stored usable kWh is fully
accounted for, or history runs out (whichever comes first). This bounds
the lookback naturally (typically ≤ a day or two of slots, not "forever")
and needs no new persisted runtime state — everything needed already
lives in the metrics DB.

Apply `1 / eta` (`core/site_optimizer.go:34`, currently `0.9`) when
converting the accumulated per-kWh-in cost to a per-kWh-out price, to account
for round-trip losses — this is the piece the FAQ's current "battery = free
solar" assumption misses.

Applied the same treatment to CO2 (naltatis, latest comment): *"we should not
only focus on price/cost but also build this for co2 ... grid charged
battery energy should be treated with the same emissions as its source"* —
the backward replay accumulates both price *and* CO2 per slot, not cost
alone.

**Resolved open questions (GurliGebis, follow-up on issue):**

1. Compute the battery cost basis on-demand via backward replay, not a
   denormalized cost column in the history table (no schema/migration).
2. Reuse the optimizer's `eta = 0.9` for now; a separately configurable
   battery efficiency is out of scope, a possible future step.
3. Aggregate site-wide battery cost basis only (matches today's
   `state.battery` aggregation across meters), not per-device.
4. **Price/CO2 when the charge source of a slot can't be attributed**
   (e.g. no retrospective grid/PV/battery split available for that slot):
   use a 50/50 blend of grid import and feed-in price/CO2 for that slot —
   "we cannot know it, so a middle ground seems fair."
5. **Reserve-SoC floor:** use `api.BatterySocLimiter.GetSocLimits()` min
   when the device implements it (most named vendor batteries do — SMA,
   LG ESS, Powerwall, E3DC, Zendure, generic Modbus/SunSpec, etc.);
   otherwise fall back to a hardcoded 10% floor, matching what most home
   batteries reserve.
6. **`state.battery.Capacity` degraded to a meter count** (any configured
   battery meter missing `api.BatteryCapacity`): return `nil BatteryCost`.
   Same graceful-degradation pattern as `GridCost`/`SolarCost` today, no
   new fallback logic needed.
7. **Backward-replay stopping condition:** FIFO model. If persisted
   history runs out before the target usable kWh is covered (fresh
   install, battery/tariff only recently configured), stop and use the
   price/CO2 computed from whatever was accumulated so far rather than
   returning nil — a partial, best-effort answer is preferred over none.

**Still open:**

8. Update the FAQ savings-calculation doc once Step 3 ships, since it
   currently documents the lossless-solar assumption this step removes.

## Known limitations (flagged, not blocking)

- Batteries that self-manage grid-charging internally without exposing a
  controllable mode to evcc (`site.batteryMode` never reflects it) would be
  misattributed as solar-charged in any mode-based heuristic. Not fixable
  without dedicated battery-charge-source metering.
- `state.battery.Capacity` is only a true kWh figure when *every* configured
  battery meter reports capacity — otherwise it degrades to a meter count
  (`core/site.go:updateBatteryMeters`). Any battery-cost feature must guard on
  this the same way the frontend already gates `kWhAvailable` in
  `assets/js/views/Battery.vue`, and simply leave `BatteryCost` `nil` (same
  graceful-degradation pattern as `GridCost`/`SolarCost` today) when it
  doesn't hold.

## Reference: files touched

Step 1 (implemented on this branch, PR #33252 closed unmerged upstream):
- `core/energy_metrics.go`, `core/energy_metrics_test.go`
- `core/loadpoint.go`, `core/site.go`, `core/site_tariffs.go`
- `core/loadpoint_session.go`, `core/session/session.go`
- `assets/js/components/Sessions/types.ts`,
  `assets/js/components/Sessions/SessionDetailsModal.vue`
- `assets/js/types/evcc.ts`, `server/openapi.state.yaml`, `server/mcp/openapi.json`
  (generated — regenerate with `vp run openapi`, never hand-edit)
- `i18n/en.json`, `i18n/de.json` (`session.priceGrid`, `session.priceSolar`,
  `sessions.csv.gridcost`, `sessions.csv.solarcost`)

Step 2 (implemented, in-memory only, no persistence/UI):
- `core/site_tariffs.go` (`greenShareBySource`), `core/site_test.go`
- `core/energy_metrics.go` (`SetGreenShareSplit`, `PvCost`, `BatteryCost`),
  `core/energy_metrics_test.go`
- `core/loadpoint.go` (`SetGreenShareSplit`), `core/site.go` (`updater`
  interface, `updatePower`)

Step 3 (implemented, in-memory only, no persistence/UI):
- `core/site_battery_cost.go` (`batteryCostBasis`, `replayBatteryCost`,
  `batteryReserveSoc`, `batteryCapacityReliable`), `core/site_battery_cost_test.go`
- `core/metrics/tariffs.go` (`QueryTariffs`, `TariffSlot`), `core/metrics/tariffs_test.go`
- `core/energy_metrics.go` (`SetBatteryCostBasis`, `BatteryCo2PerKWh`),
  `core/energy_metrics_test.go`
- `core/loadpoint.go` (`SetBatteryCostBasis`), `core/site.go` (`updater`
  interface, `updatePower`, `batteryCostBasisCached`)
