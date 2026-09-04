package core

import (
	"testing"
)

func isEqualFloat64(a, b *float64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func TestEnergyMetrics(t *testing.T) {
	f := func(f float64) *float64 { return &f }

	type tcStep = struct {
		kWh, greenShare                float64
		gridPrice, feedInPrice, effCo2 *float64
	}

	tc := []struct {
		title                                              string
		steps                                              []tcStep
		totalWh, solarPercentage                           float64
		price, pricePerKWh, gridCost, solarCost, co2PerKWh *float64
	}{
		{
			"initial state",
			[]tcStep{},
			0, 0, nil, nil, nil, nil, nil,
		},
		{
			"energy value",
			[]tcStep{
				{0.1, 0, nil, nil, nil},
				{0.2, 0, nil, nil, nil},
			},
			200, 0, nil, nil, nil, nil, nil,
		},
		{
			"ignore lower energy value",
			[]tcStep{
				{0.2, 0, nil, nil, nil},
				{0.1, 0, nil, nil, nil},
			},
			200, 0, nil, nil, nil, nil, nil,
		},
		{
			"half solar",
			[]tcStep{
				{0.1, 1, nil, nil, nil},
				{0.2, 0, nil, nil, nil},
			},
			200, 50, nil, nil, nil, nil, nil,
		},
		{
			"only solar",
			[]tcStep{
				{0.1, 1, nil, nil, nil},
				{0.2, 1, nil, nil, nil},
			},
			200, 100, nil, nil, nil, nil, nil,
		},
		{
			"static price, grid only",
			[]tcStep{
				{1, 0, f(0.5), nil, nil},
			},
			1000, 0, f(0.5), f(0.5), f(0.5), nil, nil,
		},
		{
			"dynamic price, grid only",
			[]tcStep{
				{1, 0, f(1), nil, nil},
				{2, 0, f(0), nil, nil},
			},
			2000, 0, f(1), f(0.5), f(1), nil, nil,
		},
		{
			"dynamic price, grid only",
			[]tcStep{
				{2, 0, f(1), nil, nil},
				{4, 0, f(0), nil, nil},
			},
			4000, 0, f(2), f(0.5), f(2), nil, nil,
		},
		{
			"static co2",
			[]tcStep{
				{1, 0, nil, nil, f(500)},
			},
			1000, 0, nil, nil, nil, nil, f(500),
		},
		{
			"dynamic co2",
			[]tcStep{
				{1, 0, nil, nil, f(1000)},
				{2, 0, nil, nil, f(0)},
			},
			2000, 0, nil, nil, nil, nil, f(500),
		},
		{
			"grid only, half solar w/ feed-in, full solar, half solar w/ feed-in, grid only",
			[]tcStep{
				{1, 0, f(2), f(1), f(200)},
				{2, 0.5, f(1), f(1), f(50)},
				{3, 1, f(0), f(1), f(0)},
				{4, 0.5, f(1), f(1), f(50)},
				{5, 0, f(2), f(1), f(200)},
			},
			5000, 40, f(7), f(1.4), f(5), f(2), f(100),
		},
	}

	for _, tc := range tc {
		var s EnergyMetrics

		for _, tc := range tc.steps {
			s.SetEnvironment(tc.greenShare, tc.gridPrice, tc.feedInPrice, tc.effCo2)
			s.Update(tc.kWh)
		}

		if s.TotalWh() != tc.totalWh {
			t.Errorf("%s: TotalWh was incorrect, got: %.3f, want: %.3f.", tc.title, s.TotalWh(), tc.totalWh)
		}
		if s.SolarPercentage() != tc.solarPercentage {
			t.Errorf("%s: SolarPercentage was incorrect, got: %.3f, want: %.3f.", tc.title, s.SolarPercentage(), tc.solarPercentage)
		}
		price := s.Price()
		if !isEqualFloat64(price, tc.price) {
			t.Errorf("%s: Price was incorrect, got: %v, want: %v.", tc.title, price, tc.price)
		}
		pricePerKWh := s.PricePerKWh()
		if !isEqualFloat64(pricePerKWh, tc.pricePerKWh) {
			t.Errorf("%s: PricePerKWh was incorrect, got: %v, want: %v.", tc.title, pricePerKWh, tc.pricePerKWh)
		}
		gridCost := s.GridCost()
		if !isEqualFloat64(gridCost, tc.gridCost) {
			t.Errorf("%s: GridCost was incorrect, got: %v, want: %v.", tc.title, gridCost, tc.gridCost)
		}
		solarCost := s.SolarCost()
		if !isEqualFloat64(solarCost, tc.solarCost) {
			t.Errorf("%s: SolarCost was incorrect, got: %v, want: %v.", tc.title, solarCost, tc.solarCost)
		}
		co2PerKWh := s.Co2PerKWh()
		if !isEqualFloat64(co2PerKWh, tc.co2PerKWh) {
			t.Errorf("%s: Co2PerKWh was incorrect, got: %v, want: %v.", tc.title, co2PerKWh, tc.co2PerKWh)
		}
	}

	// reset
	var s EnergyMetrics
	s.SetEnvironment(1, f(1), f(1), f(1))
	s.Update(1)
	s.Reset()
	if s.TotalWh() != 0 || s.SolarPercentage() != 0 || s.Co2PerKWh() != nil || s.Price() != nil || s.PricePerKWh() != nil || s.GridCost() != nil || s.SolarCost() != nil {
		t.Errorf("Metrics not properly reset %+v", s)
	}
}

// TestEnergyMetricsGreenShareSplit verifies the pv/battery cost split introduced in #33251
// step 2: PvCost/BatteryCost must always sum to SolarCost (both are valued at feed-in price
// until step 3 introduces a real battery cost basis), and stay nil when SetGreenShareSplit
// was never called (backward compatible with callers that only use SetEnvironment).
func TestEnergyMetricsGreenShareSplit(t *testing.T) {
	f := func(f float64) *float64 { return &f }

	t.Run("split unused stays nil", func(t *testing.T) {
		var s EnergyMetrics
		s.SetEnvironment(0.5, f(1), f(1), nil)
		s.Update(2)

		if s.PvCost() != nil || s.BatteryCost() != nil {
			t.Errorf("expected nil PvCost/BatteryCost without SetGreenShareSplit, got %v/%v", s.PvCost(), s.BatteryCost())
		}
		if s.SolarCost() == nil {
			t.Error("expected SolarCost to still be tracked")
		}
	})

	t.Run("split sums to solar cost", func(t *testing.T) {
		var s EnergyMetrics
		s.SetEnvironment(0.5, f(1), f(2), nil) // greenShare 0.5, feedin 2
		s.SetGreenShareSplit(0.3, 0.2)         // pv 0.3, battery 0.2 of 0.5 green share
		s.Update(10)                           // 10kWh charged: 5kWh grid, 5kWh green (3kWh pv, 2kWh battery)

		gridCost, solarCost := s.GridCost(), s.SolarCost()
		pvCost, batteryCost := s.PvCost(), s.BatteryCost()
		if gridCost == nil || solarCost == nil || pvCost == nil || batteryCost == nil {
			t.Fatalf("expected all costs to be set, got grid=%v solar=%v pv=%v battery=%v", gridCost, solarCost, pvCost, batteryCost)
		}
		if got, want := *pvCost+*batteryCost, *solarCost; got != want {
			t.Errorf("pvCost(%.3f)+batteryCost(%.3f) = %.3f, want solarCost %.3f", *pvCost, *batteryCost, got, want)
		}
		if want := 6.0; *pvCost != want { // 3kWh * feedin 2
			t.Errorf("pvCost = %.3f, want %.3f", *pvCost, want)
		}
		if want := 4.0; *batteryCost != want { // 2kWh * feedin 2
			t.Errorf("batteryCost = %.3f, want %.3f", *batteryCost, want)
		}
	})

	t.Run("reset clears split", func(t *testing.T) {
		var s EnergyMetrics
		s.SetEnvironment(1, f(1), f(1), f(1))
		s.SetGreenShareSplit(0.6, 0.4)
		s.Update(1)
		s.Reset()
		if s.PvCost() != nil || s.BatteryCost() != nil {
			t.Errorf("expected PvCost/BatteryCost to be reset, got %v/%v", s.PvCost(), s.BatteryCost())
		}
	})
}
