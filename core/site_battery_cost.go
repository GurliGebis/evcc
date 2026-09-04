package core

import (
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/metrics"
)

// batteryCostBasisResult wraps batteryCostBasis's result for use with util.Cached, which
// requires a single return value.
type batteryCostBasisResult struct {
	price, co2 *float64
}

// cachedBatteryCostBasis returns the site.batteryCostBasisCached wrapper's current value,
// throttled to at most once per tariff.SlotDuration since the underlying persisted history
// only changes at that granularity (#33251 step 3).
func (site *Site) cachedBatteryCostBasis() (price, co2 *float64) {
	res, _ := site.batteryCostBasisCached()
	return res.price, res.co2
}

const (
	// batteryReserveSocFallback is used as the usable-energy floor when no configured
	// battery meter reports its own reserve via api.BatterySocLimiter (#33251 step 3)
	batteryReserveSocFallback = 10 // %

	// batteryCostLookback bounds how far back the replay queries persisted battery/tariff
	// history. In practice this is expected to cover only a day or two of slots (see plan
	// doc), this is a generous upper bound, not the typical lookback depth.
	batteryCostLookback = 30 * 24 * time.Hour
)

// batteryReserveSoc returns the SoC floor below which stored battery energy is considered
// unusable, preferring any configured battery meter's own api.BatterySocLimiter.GetSocLimits(),
// falling back to batteryReserveSocFallback when none report it (#33251 step 3).
func (site *Site) batteryReserveSoc() float64 {
	for _, dev := range site.batteryMeters {
		if bsl, ok := api.Cap[api.BatterySocLimiter](dev.Instance()); ok {
			min, _ := bsl.GetSocLimits()
			return min
		}
	}
	return batteryReserveSocFallback
}

// batteryCapacityReliable reports whether every configured battery meter provides a real
// capacity reading, i.e. state.battery.Capacity is a true kWh figure and not a meter count
// (see updateBatteryMeters in site.go).
func (site *Site) batteryCapacityReliable() bool {
	if len(site.batteryMeters) == 0 {
		return false
	}
	for _, dev := range site.batteryMeters {
		if _, ok := api.Cap[api.BatteryCapacity](dev.Instance()); !ok {
			return false
		}
	}
	return true
}

// batteryCostBasis computes the home battery's current per-kWh price and CO2 basis by
// replaying its charge history backward (#33251 step 3): anchor on the currently stored
// usable energy (capacity * max(0, soc-reserveSoc) / 100), then walk persisted battery-energy
// and tariff slots backward - FIFO, oldest stored energy is assumed dispatched first - until
// that energy is accounted for, or persisted history runs out (whichever comes first; a
// partial answer from incomplete history is preferred over none). 1/eta then converts the
// accumulated per-kWh-in cost/CO2 to a per-kWh-out value, accounting for round-trip losses.
//
// Where a slot's charge can't be attributed to grid vs. solar (no retrospective source
// split is computed here), price/CO2 are blended 50/50 between the grid and feed-in tariff
// (solar is valued at the feed-in price and zero CO2) - "we cannot know it, so a middle
// ground seems fair".
//
// Returns nil, nil when the battery capacity is unreliable (any configured battery meter
// missing api.BatteryCapacity, see batteryCapacityReliable), when there is no usable stored
// energy to price, or when no persisted history is available at all.
func (site *Site) batteryCostBasis() (price, co2 *float64) {
	if !site.batteryCapacityReliable() {
		return nil, nil
	}

	state := site.state()
	reserveSoc := site.batteryReserveSoc()
	usableKWh := state.battery.Capacity * max(0, state.battery.Soc-reserveSoc) / 100
	if usableKWh <= 0 {
		return nil, nil
	}

	now := time.Now()
	from := now.Add(-batteryCostLookback)

	energySlots, err := metrics.QueryEnergy(from, now, "15m", true, metrics.EnergyFilter{Group: metrics.Battery})
	if err != nil || len(energySlots) == 0 {
		return nil, nil
	}

	var slots []metrics.Slot
	for _, s := range energySlots {
		if s.Group == metrics.Battery {
			slots = s.Data
			break
		}
	}
	if len(slots) == 0 {
		return nil, nil
	}

	tariffSlots, err := metrics.QueryTariffs(from, now)
	if err != nil {
		return nil, nil
	}
	tariffByStart := make(map[int64]metrics.TariffSlot, len(tariffSlots))
	for _, t := range tariffSlots {
		tariffByStart[t.Start.Unix()] = t
	}

	return replayBatteryCost(slots, tariffByStart, usableKWh)
}

// replayBatteryCost is the pure accumulation core of batteryCostBasis, split out for
// testability. It walks slots backward (most-recent-first, as passed in) FIFO-style,
// accumulating charge-in energy and its slot's price/CO2 (blended 50/50 grid/feed-in,
// see batteryCostBasis) until usableKWh is covered or slots run out, then converts the
// accumulated per-kWh-in cost/CO2 to a per-kWh-out value via 1/eta. Energy in slots without
// a matching persisted tariff still counts against usableKWh (it's still physically stored
// energy), but is excluded from the price/CO2 average rather than diluting it with a zero.
func replayBatteryCost(slots []metrics.Slot, tariffs map[int64]metrics.TariffSlot, usableKWh float64) (price, co2 *float64) {
	remaining := usableKWh
	var costSum, pricedEnergy, co2Sum, co2PricedEnergy float64

	// FIFO: walk backward from the most recent slot, oldest stored energy considered
	// dispatched first as remaining stored kWh is accounted for
	for i := len(slots) - 1; i >= 0 && remaining > 0; i-- {
		slot := slots[i]
		if slot.Energy <= 0 {
			continue
		}

		energy := min(remaining, slot.Energy)
		remaining -= energy

		t := tariffs[slot.Start.Unix()]
		switch {
		case t.Grid != nil && t.FeedIn != nil:
			costSum += energy * (*t.Grid + *t.FeedIn) / 2
			pricedEnergy += energy
		case t.Grid != nil:
			costSum += energy * *t.Grid
			pricedEnergy += energy
		case t.FeedIn != nil:
			costSum += energy * *t.FeedIn
			pricedEnergy += energy
		}

		if t.Co2 != nil {
			// solar-sourced share is treated as zero-emission, blended 50/50 like price
			co2Sum += energy * *t.Co2 / 2
			co2PricedEnergy += energy
		}
	}

	if pricedEnergy > 0 {
		pricePerKWh := costSum / pricedEnergy / eta
		price = &pricePerKWh
	}

	if co2PricedEnergy > 0 {
		co2PerKWh := co2Sum / co2PricedEnergy / eta
		co2 = &co2PerKWh
	}

	return price, co2
}
