package service

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"golang.org/x/sync/singleflight"
)

// ModelSquareOrder lists model names for the model square sorts. Models
// missing from a list follow it by name. It holds no usage figures, because
// the model square can be public while the rankings page is off.
type ModelSquareOrder struct {
	Recommended []string `json:"recommended"`
	Requests    []string `json:"requests"`
	Tokens      []string `json:"tokens"`
}

type modelUsageCacheItem struct {
	expiresAt time.Time
	totals    []model.ModelUsageTotal
}

var (
	modelUsageCacheMu sync.Mutex
	modelUsageCache   = map[int]modelUsageCacheItem{}
	// modelUsageLoads runs one usage query per period at a time, as
	// rankingBuilds does for the rankings: the public /api/pricing asks for
	// the usage on every model square visit, so concurrent visits without
	// usable totals wait for the same quota_data aggregation.
	modelUsageLoads singleflight.Group
	// loadModelUsageTotals queries the usage; tests replace it.
	loadModelUsageTotals = model.GetModelUsageTotals
)

// GetModelUsageTotals returns each model's usage over the last days, cached
// like the rankings: totals that expired less than rankingCacheTTL ago are
// returned at once while one background query replaces them; without totals,
// or with older ones, the caller waits for the shared query. Without
// dashboard data there is no usage.
func GetModelUsageTotals(days int) ([]model.ModelUsageTotal, error) {
	if !common.DataExportEnabled {
		return nil, nil
	}
	now := time.Now()
	modelUsageCacheMu.Lock()
	item, ok := modelUsageCache[days]
	modelUsageCacheMu.Unlock()
	if ok && now.Before(item.expiresAt) {
		return item.totals, nil
	}
	key := strconv.Itoa(days)
	if ok && now.Before(item.expiresAt.Add(rankingCacheTTL)) {
		// The result channel is buffered, so nobody needs to read it.
		modelUsageLoads.DoChan(key, func() (any, error) { return loadAndCacheModelUsage(days) })
		return item.totals, nil
	}

	value, err, _ := modelUsageLoads.Do(key, func() (any, error) { return loadAndCacheModelUsage(days) })
	if err != nil {
		return nil, err
	}
	return value.([]model.ModelUsageTotal), nil
}

// loadAndCacheModelUsage queries the usage of a period and caches it. A
// failed query caches nothing, so the next request tries again. A panic is
// returned as an error: a background query has no request to recover it.
func loadAndCacheModelUsage(days int) (totals []model.ModelUsageTotal, err error) {
	defer func() {
		if r := recover(); r != nil {
			totals, err = nil, fmt.Errorf("loading the model usage of %d days panicked: %v", days, r)
		}
	}()
	now := time.Now()
	// A query that finished after the caller read the cache has already
	// replaced the totals: use them rather than query again.
	modelUsageCacheMu.Lock()
	item, ok := modelUsageCache[days]
	modelUsageCacheMu.Unlock()
	if ok && now.Before(item.expiresAt) {
		return item.totals, nil
	}
	totals, err = loadModelUsageTotals(now.AddDate(0, 0, -days).Unix(), now.Unix())
	if err != nil {
		return nil, err
	}
	modelUsageCacheMu.Lock()
	modelUsageCache[days] = modelUsageCacheItem{expiresAt: now.Add(rankingCacheTTL), totals: totals}
	modelUsageCacheMu.Unlock()
	return totals, nil
}

// BuildModelSquareOrder orders the listed models for the model square sorts.
// Names that are not listed are left out.
func BuildModelSquareOrder(pricing []model.Pricing) ModelSquareOrder {
	listed := make(map[string]struct{}, len(pricing))
	for _, item := range pricing {
		listed[item.ModelName] = struct{}{}
	}
	setting := operation_setting.GetModelSquareSetting()
	totals, err := GetModelUsageTotals(setting.SortDays)
	if err != nil {
		common.SysError(common.LogText("failed to load model usage for the model square: %s", err.Error()))
	}
	order := ModelSquareOrder{
		Requests: modelNamesByUsage(totals, operation_setting.ModelSquareMetricRequests, listed),
		Tokens:   modelNamesByUsage(totals, operation_setting.ModelSquareMetricTokens, listed),
	}
	byMetric := order.Tokens
	if setting.SortMetric == operation_setting.ModelSquareMetricRequests {
		byMetric = order.Requests
	}
	if setting.SortMode == operation_setting.ModelSquareSortUsage {
		order.Recommended = byMetric
		return order
	}

	order.Recommended = make([]string, 0, len(setting.SortModels))
	for _, name := range setting.SortModels {
		if _, ok := listed[name]; ok && !slices.Contains(order.Recommended, name) {
			order.Recommended = append(order.Recommended, name)
		}
	}
	if setting.SortMode == operation_setting.ModelSquareSortMixed {
		pinned := len(order.Recommended)
		for _, name := range byMetric {
			if !slices.Contains(order.Recommended[:pinned], name) {
				order.Recommended = append(order.Recommended, name)
			}
		}
	}
	return order
}

// modelNamesByUsage lists the listed models that have usage of the metric,
// most used first and equal usage in byte order, as the web console does.
func modelNamesByUsage(totals []model.ModelUsageTotal, metric string, listed map[string]struct{}) []string {
	usage := make(map[string]int64, len(totals))
	names := make([]string, 0, len(totals))
	for _, total := range totals {
		value := total.Tokens
		if metric == operation_setting.ModelSquareMetricRequests {
			value = total.Requests
		}
		if _, ok := listed[total.ModelName]; ok && value > 0 {
			usage[total.ModelName] = value
			names = append(names, total.ModelName)
		}
	}
	slices.SortFunc(names, func(a, b string) int {
		return cmp.Or(cmp.Compare(usage[b], usage[a]), strings.Compare(a, b))
	})
	return names
}
