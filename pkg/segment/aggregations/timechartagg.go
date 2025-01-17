package aggregations

import (
	"sort"
	"time"

	"github.com/axiomhq/hyperloglog"
	"github.com/siglens/siglens/pkg/segment/structs"
	"github.com/siglens/siglens/pkg/segment/utils"
	log "github.com/sirupsen/logrus"
)

type scorePair struct {
	groupByColVal string
	score         float64
	index         int
}

func GenerateTimeRangeBuckets(timeHistogram *structs.TimeBucket) []uint64 {
	timeRangeBuckets := make([]uint64, 0)
	currentTime := timeHistogram.StartTime
	for currentTime < timeHistogram.EndTime {
		timeRangeBuckets = append(timeRangeBuckets, currentTime)
		nextTime := currentTime + timeHistogram.IntervalMillis
		if nextTime > timeHistogram.EndTime {
			break
		}

		currentTime = nextTime
	}

	return timeRangeBuckets
}

// Find correct time range bucket for timestamp
func FindTimeRangeBucket(timePoints []uint64, timestamp uint64, intervalMillis uint64) uint64 {
	index := ((timestamp - timePoints[0]) / intervalMillis)
	return timePoints[index]
}

func InitTimeBucket(num int, timeUnit utils.TimeUnit, byField string, limitExpr *structs.LimitExpr, measureAggLength int) *structs.TimeBucket {
	numD := time.Duration(num)
	intervalMillis := uint64(0)
	switch timeUnit {
	case utils.TMMicrosecond:
		// Might not has effect for 'us', because smallest time unit for timestamp in siglens is ms
	case utils.TMMillisecond:
		intervalMillis = uint64(numD)
	case utils.TMCentisecond:
		intervalMillis = uint64(numD * 10 * time.Millisecond)
	case utils.TMDecisecond:
		intervalMillis = uint64(numD * 100 * time.Millisecond)
	case utils.TMSecond:
		intervalMillis = uint64((numD * time.Second).Milliseconds())
	case utils.TMMinute:
		intervalMillis = uint64((numD * time.Minute).Milliseconds())
	case utils.TMHour:
		intervalMillis = uint64((numD * time.Hour).Milliseconds())
	case utils.TMDay:
		intervalMillis = uint64((numD * 24 * time.Hour).Milliseconds())
	case utils.TMWeek:
		intervalMillis = uint64((numD * 7 * 24 * time.Hour).Milliseconds())
	case utils.TMMonth:
		intervalMillis = uint64((numD * 30 * 24 * time.Hour).Milliseconds())
	case utils.TMQuarter:
		intervalMillis = uint64((numD * 120 * 24 * time.Hour).Milliseconds())
	}

	timechartExpr := &structs.TimechartExpr{
		ByField: byField,
	}

	if len(byField) > 0 {
		if limitExpr != nil {
			timechartExpr.LimitExpr = limitExpr
		} else {
			timechartExpr.LimitExpr = &structs.LimitExpr{
				IsTop:          true,
				Num:            10,
				LimitScoreMode: structs.LSMBySum,
			}
			if measureAggLength > 1 {
				timechartExpr.LimitExpr.LimitScoreMode = structs.LSMByFreq
			}
		}
	}

	timeBucket := &structs.TimeBucket{
		IntervalMillis: intervalMillis,
		Timechart:      timechartExpr,
	}

	return timeBucket
}

func AddAggCountToTimechartRunningStats(m *structs.MeasureAggregator, allConvertedMeasureOps *[]*structs.MeasureAggregator, allReverseIndex *[]int, colToIdx map[string][]int, idx int) {
	*allReverseIndex = append(*allReverseIndex, idx)
	colToIdx[m.MeasureCol] = append(colToIdx[m.MeasureCol], idx)
	*allConvertedMeasureOps = append(*allConvertedMeasureOps, &structs.MeasureAggregator{
		MeasureCol:  m.MeasureCol,
		MeasureFunc: utils.Count,
		StrEnc:      m.StrEnc,
	})
}

func AddAggAvgToTimechartRunningStats(m *structs.MeasureAggregator, allConvertedMeasureOps *[]*structs.MeasureAggregator, allReverseIndex *[]int, colToIdx map[string][]int, idx int) {
	*allReverseIndex = append(*allReverseIndex, idx)
	colToIdx[m.MeasureCol] = append(colToIdx[m.MeasureCol], idx)
	*allConvertedMeasureOps = append(*allConvertedMeasureOps, &structs.MeasureAggregator{
		MeasureCol:  m.MeasureCol,
		MeasureFunc: utils.Sum,
		StrEnc:      m.StrEnc,
	})
	idx++
	*allReverseIndex = append(*allReverseIndex, idx)
	colToIdx[m.MeasureCol] = append(colToIdx[m.MeasureCol], idx)
	*allConvertedMeasureOps = append(*allConvertedMeasureOps, &structs.MeasureAggregator{
		MeasureCol:  m.MeasureCol,
		MeasureFunc: utils.Count,
		StrEnc:      m.StrEnc,
	})
}

// Timechart will only display N highest/lowest scoring distinct values of the split-by field
// For Single agg, the score is based on the sum of the values in the aggregation. Therefore, we can only know groupByColVal's ranking after processing all the runningStats
// For multiple aggs, the score is based on the freq of the field. Which means we can rank groupByColVal at this time.
func CheckGroupByColValsAgainstLimit(timechart *structs.TimechartExpr, groupByColValCnt map[string]int, groupValScoreMap map[string]*utils.CValueEnclosure) map[string]bool {

	if timechart == nil || timechart.LimitExpr == nil {
		return nil
	}

	index := 0
	valIsInLimit := make(map[string]bool)
	isRankBySum := IsRankBySum(timechart)
	if isRankBySum {
		scorePairs := make([]scorePair, 0)
		// []float64, 0: score; 1: index
		for groupByColVal, cVal := range groupValScoreMap {
			valIsInLimit[groupByColVal] = false
			score, err := cVal.GetFloatValue()
			if err != nil {
				log.Errorf("CheckGroupByColValsAgainstLimit: %v does not have a score", groupByColVal)
				continue
			}
			scorePairs = append(scorePairs, scorePair{
				groupByColVal: groupByColVal,
				score:         score,
				index:         index,
			})
			index++
		}

		if timechart.LimitExpr.IsTop {
			sort.Slice(scorePairs, func(i, j int) bool {
				return scorePairs[i].score > scorePairs[j].score
			})
		} else {
			sort.Slice(scorePairs, func(i, j int) bool {
				return scorePairs[i].score < scorePairs[j].score
			})
		}

		limit := timechart.LimitExpr.Num
		if limit > len(scorePairs) {
			limit = len(scorePairs)
		}

		for i := 0; i < limit; i++ {
			valIsInLimit[scorePairs[i].groupByColVal] = true
		}

	} else { // rank by freq
		// []int, 0: cnt; 1: index
		cnts := make([][]int, 0)
		vals := make([]string, 0)

		for groupByColVal, cnt := range groupByColValCnt {
			vals = append(vals, groupByColVal)
			cnts = append(cnts, []int{cnt, index})
			valIsInLimit[groupByColVal] = false
			index++
		}

		if timechart.LimitExpr.IsTop {
			sort.Slice(cnts, func(i, j int) bool {
				return cnts[i][0] > cnts[j][0]
			})
		} else {
			sort.Slice(cnts, func(i, j int) bool {
				return cnts[i][0] < cnts[j][0]
			})
		}

		limit := timechart.LimitExpr.Num
		if limit > len(vals) {
			limit = len(vals)
		}

		for i := 0; i < limit; i++ {
			valIndex := cnts[i][1]
			valIsInLimit[vals[valIndex]] = true
		}
	}

	return valIsInLimit
}

// Initial score map for single agg: the score is based on the sum of the values in the aggregation
func InitialScoreMap(timechart *structs.TimechartExpr, groupByColValCnt map[string]int) map[string]*utils.CValueEnclosure {

	if timechart == nil || timechart.LimitExpr == nil || timechart.LimitExpr.LimitScoreMode == structs.LSMByFreq {
		return nil
	}

	groupByColValScoreMap := make(map[string]*utils.CValueEnclosure, 0)
	for groupByColVal := range groupByColValCnt {
		groupByColValScoreMap[groupByColVal] = &utils.CValueEnclosure{CVal: nil, Dtype: utils.SS_INVALID}
	}

	return groupByColValScoreMap
}

func IsOtherCol(valIsInLimit map[string]bool, groupByColVal string) bool {
	isOtherCol := false
	if valIsInLimit != nil {
		inLimit, exists := valIsInLimit[groupByColVal]
		if exists {
			isOtherCol = !inLimit
		}
	}
	return isOtherCol
}

// For numeric agg(not include dc), we can simply use addition to merge them
// For string values, it depends on the aggregation function
func MergeVal(eVal *utils.CValueEnclosure, eValToMerge utils.CValueEnclosure, hll *hyperloglog.Sketch, hllToMerge *hyperloglog.Sketch,
	aggFunc utils.AggregateFunctions, useAdditionForMerge bool) {

	tmp := utils.CValueEnclosure{
		Dtype: eVal.Dtype,
		CVal:  eVal.CVal,
	}

	switch aggFunc {
	case utils.Count:
		fallthrough
	case utils.Avg:
		fallthrough
	case utils.Min:
		fallthrough
	case utils.Max:
		fallthrough
	case utils.Range:
		fallthrough
	case utils.Sum:
		aggFunc = utils.Sum
		// TODO: should merge values for not numeric agg
		// case utils.Cardinality:
		// 	if useAdditionForMerge {
		// 		aggFunc = utils.Sum
		// 	} else {
		// 		log.Error("fjl test1:", hll)
		// 		log.Error("fjl test2:", hllToMerge)
		// 		err := hll.Merge(hllToMerge)
		// 		if err != nil {
		// 			log.Errorf("MergeVal: failed to merge hyperloglog stats: %v", err)
		// 		}
		// 		eVal.CVal = hll.Estimate()
		// 		eVal.Dtype = utils.SS_DT_UNSIGNED_NUM
		// 		return
		// 	}
		// case utils.Values:
	}

	retVal, err := utils.Reduce(eValToMerge, tmp, aggFunc)
	if err != nil {
		log.Errorf("MergeVal: failed to merge eVal into otherCVal: %v", err)
		return
	}
	eVal.CVal = retVal.CVal
	eVal.Dtype = retVal.Dtype
}

func MergeMap(groupByColValCnt map[string]int, toMerge map[string]int) {

	for key, cnt := range groupByColValCnt {
		cntToMerge, exists := toMerge[key]
		if exists {
			groupByColValCnt[key] = cnt + cntToMerge
		}
	}

	for key, cnt := range toMerge {
		_, exists := groupByColValCnt[key]
		if !exists {
			groupByColValCnt[key] = cnt
		}
	}
}

func IsRankBySum(timechart *structs.TimechartExpr) bool {
	if timechart != nil && timechart.LimitExpr != nil && timechart.LimitExpr.LimitScoreMode == structs.LSMBySum {
		return true
	}
	return false
}

func ShouldAddRes(timechart *structs.TimechartExpr, tmLimitResult *structs.TMLimitResult, index int, eVal utils.CValueEnclosure,
	hllToMerge *hyperloglog.Sketch, aggFunc utils.AggregateFunctions, groupByColVal string, isOtherCol bool) bool {

	useAdditionForMerge := (tmLimitResult.OtherCValArr == nil)
	isRankBySum := IsRankBySum(timechart)

	// If true, current col's val will be added into 'other' col. So its val should not be added into res at this time
	if isOtherCol {
		otherCVal := tmLimitResult.OtherCValArr[index]
		MergeVal(otherCVal, eVal, tmLimitResult.Hll, hllToMerge, aggFunc, useAdditionForMerge)
		return false
	} else {
		if isRankBySum && tmLimitResult.OtherCValArr == nil {
			scoreVal := tmLimitResult.GroupValScoreMap[groupByColVal]
			MergeVal(scoreVal, eVal, tmLimitResult.Hll, hllToMerge, aggFunc, useAdditionForMerge)
			return false
		}
		return true
	}
}
