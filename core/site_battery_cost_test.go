package core

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// fakeCapacityMeter implements api.BatteryCapacity on top of a plain api.Meter, since
// mockgen does not generate a mock for that interface.
type fakeCapacityMeter struct {
	api.Meter
	capacity float64
}

func (f fakeCapacityMeter) Capacity() float64 { return f.capacity }

func TestBatteryCapacityReliable(t *testing.T) {
	ctrl := gomock.NewController(t)
	dev := func(m api.Meter) config.Device[api.Meter] {
		return config.NewStaticDevice(config.Named{}, m)
	}

	t.Run("no battery meters", func(t *testing.T) {
		s := &Site{}
		assert.False(t, s.batteryCapacityReliable())
	})

	t.Run("all report capacity", func(t *testing.T) {
		m1 := fakeCapacityMeter{Meter: api.NewMockMeter(ctrl), capacity: 5}
		m2 := fakeCapacityMeter{Meter: api.NewMockMeter(ctrl), capacity: 10}
		s := &Site{batteryMeters: []config.Device[api.Meter]{dev(m1), dev(m2)}}
		assert.True(t, s.batteryCapacityReliable())
	})

	t.Run("one meter missing capacity", func(t *testing.T) {
		m1 := fakeCapacityMeter{Meter: api.NewMockMeter(ctrl), capacity: 5}
		m2 := api.NewMockMeter(ctrl) // no BatteryCapacity
		s := &Site{batteryMeters: []config.Device[api.Meter]{dev(m1), dev(m2)}}
		assert.False(t, s.batteryCapacityReliable())
	})
}

func TestBatteryReserveSoc(t *testing.T) {
	ctrl := gomock.NewController(t)
	dev := func(m api.Meter) config.Device[api.Meter] {
		return config.NewStaticDevice(config.Named{}, m)
	}

	t.Run("no battery meters: fallback", func(t *testing.T) {
		s := &Site{}
		assert.Equal(t, float64(batteryReserveSocFallback), s.batteryReserveSoc())
	})

	t.Run("no meter reports limits: fallback", func(t *testing.T) {
		m := api.NewMockMeter(ctrl)
		s := &Site{batteryMeters: []config.Device[api.Meter]{dev(m)}}
		assert.Equal(t, float64(batteryReserveSocFallback), s.batteryReserveSoc())
	})

	t.Run("meter reports limits: used instead of fallback", func(t *testing.T) {
		bsl := api.NewMockBatterySocLimiter(ctrl)
		bsl.EXPECT().GetSocLimits().Return(15.0, 95.0).AnyTimes()
		m := &struct {
			api.Meter
			api.BatterySocLimiter
		}{Meter: api.NewMockMeter(ctrl), BatterySocLimiter: bsl}
		s := &Site{batteryMeters: []config.Device[api.Meter]{dev(m)}}
		assert.Equal(t, 15.0, s.batteryReserveSoc())
	})
}

func TestReplayBatteryCost(t *testing.T) {
	base := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	f := func(v float64) *float64 { return &v }

	mkSlots := func(n int, energyPerSlot float64) []metrics.Slot {
		res := make([]metrics.Slot, n)
		for i := range res {
			res[i] = metrics.Slot{
				Start:  base.Add(time.Duration(i) * 15 * time.Minute),
				Energy: energyPerSlot,
			}
		}
		return res
	}

	t.Run("single slot, full coverage", func(t *testing.T) {
		slots := mkSlots(1, 2.0)
		tariffs := map[int64]metrics.TariffSlot{
			slots[0].Start.Unix(): {Grid: f(0.30), FeedIn: f(0.10), Co2: f(200)},
		}

		price, co2 := replayBatteryCost(slots, tariffs, 2.0)
		require.NotNil(t, price)
		require.NotNil(t, co2)
		// blended price (0.30+0.10)/2 = 0.20, divided by eta (0.9)
		assert.InDelta(t, 0.20/eta, *price, 1e-9)
		// co2 blended 50/50 against zero-emission solar: 200/2 = 100, divided by eta
		assert.InDelta(t, 100.0/eta, *co2, 1e-9)
	})

	t.Run("multiple slots, weighted average", func(t *testing.T) {
		slots := mkSlots(2, 1.0)
		tariffs := map[int64]metrics.TariffSlot{
			slots[0].Start.Unix(): {Grid: f(0.10), FeedIn: f(0.10)}, // older
			slots[1].Start.Unix(): {Grid: f(0.50), FeedIn: f(0.50)}, // newer
		}

		// usableKWh covers both slots (FIFO backward: newest first, then older)
		price, _ := replayBatteryCost(slots, tariffs, 2.0)
		require.NotNil(t, price)
		assert.InDelta(t, 0.30/eta, *price, 1e-9) // (0.10+0.50)/2 averaged 1:1 by energy
	})

	t.Run("partial history: uses whatever is covered", func(t *testing.T) {
		slots := mkSlots(1, 1.0) // only 1kWh of history, but battery holds 5kWh
		tariffs := map[int64]metrics.TariffSlot{
			slots[0].Start.Unix(): {Grid: f(0.40), FeedIn: f(0.20)},
		}

		price, _ := replayBatteryCost(slots, tariffs, 5.0)
		require.NotNil(t, price, "partial history should still yield a best-effort price")
		assert.InDelta(t, 0.30/eta, *price, 1e-9)
	})

	t.Run("no history at all: nil", func(t *testing.T) {
		price, co2 := replayBatteryCost(nil, nil, 5.0)
		assert.Nil(t, price)
		assert.Nil(t, co2)
	})

	t.Run("slot without matching tariff does not dilute the average", func(t *testing.T) {
		slots := mkSlots(2, 1.0)
		tariffs := map[int64]metrics.TariffSlot{
			// only the newer slot has a price; the older one is missing entirely
			slots[1].Start.Unix(): {Grid: f(0.40), FeedIn: f(0.40)},
		}

		price, co2 := replayBatteryCost(slots, tariffs, 2.0)
		require.NotNil(t, price)
		// average must be computed over the 1kWh that had a price, not diluted to 0.20 by
		// treating the unpriced kWh as zero-cost
		assert.InDelta(t, 0.40/eta, *price, 1e-9)
		assert.Nil(t, co2, "co2 stays nil when no slot reports it")
	})

	t.Run("only grid price known", func(t *testing.T) {
		slots := mkSlots(1, 1.0)
		tariffs := map[int64]metrics.TariffSlot{
			slots[0].Start.Unix(): {Grid: f(0.50)},
		}

		price, _ := replayBatteryCost(slots, tariffs, 1.0)
		require.NotNil(t, price)
		assert.InDelta(t, 0.50/eta, *price, 1e-9)
	})

	t.Run("zero usable energy: nil", func(t *testing.T) {
		slots := mkSlots(1, 1.0)
		tariffs := map[int64]metrics.TariffSlot{
			slots[0].Start.Unix(): {Grid: f(0.50), FeedIn: f(0.50)},
		}

		price, _ := replayBatteryCost(slots, tariffs, 0)
		assert.Nil(t, price)
	})
}
