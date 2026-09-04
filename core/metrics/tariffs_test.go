package metrics

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/db"
	"github.com/stretchr/testify/require"
)

func TestPersistTariffs(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, db.Instance.AutoMigrate(new(tariffValue)))

	slot := time.Date(2026, 4, 15, 16, 15, 0, 0, time.UTC)
	grid, co2 := 0.3, 250.0

	// nil values omitted
	require.NoError(t, PersistTariffs(slot, &grid, nil, &co2, nil))

	var res tariffValue
	require.NoError(t, db.Instance.First(&res).Error)
	require.Equal(t, slot.Unix(), res.Timestamp)
	require.InDelta(t, 0.3, *res.Grid, 0.001)
	require.Nil(t, res.FeedIn)
	require.InDelta(t, 250, *res.Co2, 0.001)
	require.Nil(t, res.Temperature)

	// duplicate slot ignored, first values kept
	other := 0.4
	require.NoError(t, PersistTariffs(slot, &other, nil, nil, nil))

	var count int64
	require.NoError(t, db.Instance.Model(new(tariffValue)).Count(&count).Error)
	require.Equal(t, int64(1), count)

	require.NoError(t, db.Instance.First(&res).Error)
	require.InDelta(t, 0.3, *res.Grid, 0.001)

	// all nil: no row
	require.NoError(t, PersistTariffs(slot.Add(15*time.Minute), nil, nil, nil, nil))
	require.NoError(t, db.Instance.Model(new(tariffValue)).Count(&count).Error)
	require.Equal(t, int64(1), count)

	// all values set: each column mapped independently
	next := slot.Add(30 * time.Minute)
	feedin, temp := 0.08, 21.5
	require.NoError(t, PersistTariffs(next, &grid, &feedin, &co2, &temp))

	require.NoError(t, db.Instance.Where("ts = ?", next.Unix()).First(&res).Error)
	require.InDelta(t, 0.3, *res.Grid, 0.001)
	require.InDelta(t, 0.08, *res.FeedIn, 0.001)
	require.InDelta(t, 250, *res.Co2, 0.001)
	require.InDelta(t, 21.5, *res.Temperature, 0.001)
}

func TestQueryTariffs(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, db.Instance.AutoMigrate(new(tariffValue)))

	base := time.Date(2026, 4, 15, 16, 0, 0, 0, time.UTC)
	grid1, feedin1, co2a := 0.3, 0.08, 250.0
	grid2 := 0.4

	require.NoError(t, PersistTariffs(base, &grid1, &feedin1, &co2a, nil))
	require.NoError(t, PersistTariffs(base.Add(15*time.Minute), &grid2, nil, nil, nil))
	require.NoError(t, PersistTariffs(base.Add(30*time.Minute), nil, nil, nil, nil)) // all-nil: no row persisted

	// unbounded
	res, err := QueryTariffs(time.Time{}, time.Time{})
	require.NoError(t, err)
	require.Len(t, res, 2)
	require.Equal(t, base, res[0].Start.UTC())
	require.InDelta(t, 0.3, *res[0].Grid, 0.001)
	require.InDelta(t, 0.08, *res[0].FeedIn, 0.001)
	require.InDelta(t, 250, *res[0].Co2, 0.001)
	require.Equal(t, base.Add(15*time.Minute), res[1].Start.UTC())
	require.InDelta(t, 0.4, *res[1].Grid, 0.001)
	require.Nil(t, res[1].FeedIn)

	// bounded, excludes the second slot
	res, err = QueryTariffs(base, base.Add(15*time.Minute))
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.Equal(t, base, res[0].Start.UTC())

	// empty range
	res, err = QueryTariffs(base.Add(time.Hour), base.Add(2*time.Hour))
	require.NoError(t, err)
	require.Empty(t, res)
}
