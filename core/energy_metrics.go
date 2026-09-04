package core

// EnergyMetrics calculates stats about the charged energy and gives you details about price or co2s
type EnergyMetrics struct {
	totalKWh            float64  // Total amount of energy used (kWh)
	solarKWh            float64  // Self-produced energy (kWh)
	pvKWh               float64  // Direct-solar share of solarKWh (kWh, #33251 step 2)
	batteryKWh          float64  // Battery-discharge share of solarKWh (kWh, #33251 step 2)
	gridCost            *float64 // Cost of grid-imported energy (Currency)
	solarCost           *float64 // Opportunity cost of self-consumed solar energy, i.e. foregone feed-in revenue (Currency)
	pvCost              *float64 // Opportunity cost attributable to the direct-solar share of solarCost (Currency, #33251 step 2)
	batteryCost         *float64 // Cost attributable to the battery-discharge share of solarCost (Currency, #33251 step 2/3)
	batteryCo2          *float64 // Amount of CO2 attributable to the battery-discharge share (gCO2eq, #33251 step 3)
	co2                 *float64 // Amount of emitted CO2 (gCO2eq)
	currentGreenShare   float64  // Current share of solar energy of site (0-1)
	currentPvShare      float64  // Current direct-solar share of site energy (0-1, #33251 step 2)
	currentBatteryShare float64  // Current battery-discharge share of site energy (0-1, #33251 step 2)
	currentGridPrice    *float64 // Current grid import price per kWh
	currentFeedInPrice  *float64 // Current feed-in price per kWh, used to value self-consumed solar energy
	currentBatteryPrice *float64 // Current battery per-kWh cost basis (#33251 step 3, see Site.batteryCostBasis); falls back to currentFeedInPrice when nil
	currentBatteryCo2   *float64 // Current battery per-kWh CO2 basis (#33251 step 3, see Site.batteryCostBasis); nil when unknown
	currentCo2          *float64 // Current co2 emissions
}

// SetEnvironment updates site information like solar share, grid and feed-in price, and co2 for use in later calculations
func (em *EnergyMetrics) SetEnvironment(greenShare float64, gridPrice, feedInPrice, effCo2 *float64) {
	em.currentGreenShare = greenShare
	em.currentGridPrice = gridPrice
	em.currentFeedInPrice = feedInPrice
	em.currentCo2 = effCo2
}

// SetGreenShareSplit records the direct-solar vs. battery-discharge split of the green share
// (#33251 step 2, additive/optional: pvShare+batteryShare is expected to equal the greenShare
// passed to SetEnvironment, see Site.greenShareBySource).
func (em *EnergyMetrics) SetGreenShareSplit(pvShare, batteryShare float64) {
	em.currentPvShare = pvShare
	em.currentBatteryShare = batteryShare
}

// SetBatteryCostBasis records the battery's real per-kWh price/CO2 basis, computed by replaying
// its charge history backward (#33251 step 3, see Site.batteryCostBasis). When price is nil
// (e.g. unreliable battery capacity, or no persisted history yet), battery-sourced energy keeps
// being valued at the feed-in price, same as direct solar, until a basis becomes available.
func (em *EnergyMetrics) SetBatteryCostBasis(price, co2 *float64) {
	em.currentBatteryPrice = price
	em.currentBatteryCo2 = co2
}

// Update sets the a new value for the total amount of charged energy and updated metrics based on environment values.
// It returns the added total and green energy.
func (em *EnergyMetrics) Update(chargedKWh float64) (float64, float64) {
	added := chargedKWh - em.totalKWh
	// nothing changed or invalid lower value
	if added <= 0 {
		return 0, 0
	}
	em.totalKWh = chargedKWh
	addedGreen := added * em.currentGreenShare
	addedGrid := added - addedGreen
	em.solarKWh += addedGreen
	// pv/battery split (#33251 step 2), only tracked once SetGreenShareSplit has been called
	addedPv := added * em.currentPvShare
	addedBattery := added * em.currentBatteryShare
	if em.currentPvShare != 0 || em.currentBatteryShare != 0 {
		em.pvKWh += addedPv
		em.batteryKWh += addedBattery
	}
	// optional values
	if em.currentGridPrice != nil {
		addedCost := *em.currentGridPrice * addedGrid
		newCost := addedCost
		if em.gridCost != nil {
			newCost = *em.gridCost + newCost
		}
		em.gridCost = &newCost
	}
	if em.currentFeedInPrice != nil {
		addedCost := *em.currentFeedInPrice * addedGreen
		newCost := addedCost
		if em.solarCost != nil {
			newCost = *em.solarCost + newCost
		}
		em.solarCost = &newCost

		// pv cost split (#33251 step 2): still valued at feed-in price, same as solarCost
		if em.currentPvShare != 0 || em.currentBatteryShare != 0 {
			addedPvCost := *em.currentFeedInPrice * addedPv
			newPvCost := addedPvCost
			if em.pvCost != nil {
				newPvCost = *em.pvCost + newPvCost
			}
			em.pvCost = &newPvCost
		}
	}
	// battery cost/CO2 split (#33251 step 2/3): valued at the real cost basis when available
	// (Site.batteryCostBasis via SetBatteryCostBasis), falling back to the feed-in price like
	// direct solar otherwise
	if em.currentPvShare != 0 || em.currentBatteryShare != 0 {
		batteryPrice := em.currentBatteryPrice
		if batteryPrice == nil {
			batteryPrice = em.currentFeedInPrice
		}
		if batteryPrice != nil {
			addedBatteryCost := *batteryPrice * addedBattery
			newBatteryCost := addedBatteryCost
			if em.batteryCost != nil {
				newBatteryCost = *em.batteryCost + newBatteryCost
			}
			em.batteryCost = &newBatteryCost
		}
		if em.currentBatteryCo2 != nil {
			addedBatteryCo2 := *em.currentBatteryCo2 * addedBattery
			newBatteryCo2 := addedBatteryCo2
			if em.batteryCo2 != nil {
				newBatteryCo2 = *em.batteryCo2 + newBatteryCo2
			}
			em.batteryCo2 = &newBatteryCo2
		}
	}
	if em.currentCo2 != nil {
		addedCo2 := *em.currentCo2 * added
		newCo2 := addedCo2
		if em.co2 != nil {
			newCo2 = *em.co2 + newCo2
		}
		em.co2 = &newCo2
	}
	return added, addedGreen
}

// Reset sets all calculations to initial values
func (em *EnergyMetrics) Reset() {
	em.totalKWh = 0
	em.solarKWh = 0
	em.pvKWh = 0
	em.batteryKWh = 0
	em.gridCost = nil
	em.solarCost = nil
	em.pvCost = nil
	em.batteryCost = nil
	em.batteryCo2 = nil
	em.co2 = nil
}

// TotalWh returns the total energy in Wh
func (em *EnergyMetrics) TotalWh() float64 {
	return em.totalKWh * 1e3
}

// SolarPercentage returns the share of self-produced energy in percent
func (em *EnergyMetrics) SolarPercentage() float64 {
	if em.totalKWh == 0 {
		return 0
	}
	return 100 / em.totalKWh * em.solarKWh
}

// GridCost returns the cost of grid-imported energy in Currency
func (em *EnergyMetrics) GridCost() *float64 {
	if em.totalKWh == 0 || em.gridCost == nil {
		return nil
	}
	return em.gridCost
}

// SolarCost returns the opportunity cost of self-consumed solar energy in Currency,
// i.e. the feed-in revenue foregone by using the solar energy for charging instead of exporting it
func (em *EnergyMetrics) SolarCost() *float64 {
	if em.totalKWh == 0 || em.solarCost == nil {
		return nil
	}
	return em.solarCost
}

// PvCost returns the opportunity cost attributable to the direct-solar share of SolarCost in
// Currency (#33251 step 2). Valued at feed-in price, same as SolarCost.
func (em *EnergyMetrics) PvCost() *float64 {
	if em.totalKWh == 0 || em.pvCost == nil {
		return nil
	}
	return em.pvCost
}

// BatteryCost returns the cost attributable to the battery-discharge share of SolarCost in
// Currency (#33251 step 2/3). Valued at the battery's real cost basis (see
// Site.batteryCostBasis/SetBatteryCostBasis) when available, falling back to the feed-in
// price like direct solar otherwise.
func (em *EnergyMetrics) BatteryCost() *float64 {
	if em.totalKWh == 0 || em.batteryCost == nil {
		return nil
	}
	return em.batteryCost
}

// BatteryCo2PerKWh returns the average CO2 emissions per kWh attributable to the
// battery-discharge share of energy (gCO2eq, #33251 step 3), nil when the battery's real CO2
// basis is unavailable (see Site.batteryCostBasis/SetBatteryCostBasis).
func (em *EnergyMetrics) BatteryCo2PerKWh() *float64 {
	if em.batteryKWh == 0 || em.batteryCo2 == nil {
		return nil
	}
	co2 := *em.batteryCo2 / em.batteryKWh
	return &co2
}

// Price returns the total energy price in Currency, the sum of grid cost and solar opportunity cost
func (em *EnergyMetrics) Price() *float64 {
	if em.totalKWh == 0 || (em.gridCost == nil && em.solarCost == nil) {
		return nil
	}
	var price float64
	if em.gridCost != nil {
		price += *em.gridCost
	}
	if em.solarCost != nil {
		price += *em.solarCost
	}
	return &price
}

// PricePerKWh returns the average energy price in Currency
func (em *EnergyMetrics) PricePerKWh() *float64 {
	price := em.Price()
	if em.totalKWh == 0 || price == nil {
		return nil
	}
	perKWh := *price / em.totalKWh
	return &perKWh
}

// Co2PerKWh returns the average co2 emissions per kWh
func (em *EnergyMetrics) Co2PerKWh() *float64 {
	if em.totalKWh == 0 || em.co2 == nil {
		return nil
	}
	co2 := *em.co2 / em.totalKWh
	return &co2
}

// Publish publishes metrics with a given prefix
func (em *EnergyMetrics) Publish(prefix string, p publisher) {
	p.publish(prefix+"Energy", em.TotalWh())
	p.publish(prefix+"SolarPercentage", em.SolarPercentage())
	p.publish(prefix+"PricePerKWh", em.PricePerKWh())
	p.publish(prefix+"Price", em.Price())
	p.publish(prefix+"GridCost", em.GridCost())
	p.publish(prefix+"SolarCost", em.SolarCost())
	p.publish(prefix+"PvCost", em.PvCost())
	p.publish(prefix+"BatteryCost", em.BatteryCost())
	p.publish(prefix+"Co2PerKWh", em.Co2PerKWh())
	p.publish(prefix+"BatteryCo2PerKWh", em.BatteryCo2PerKWh())
}
