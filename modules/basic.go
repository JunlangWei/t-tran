package modules

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// ConstHmFormat 时间格式 小时:分钟
	ConstHmFormat = "15:04"
	// ConstHmsFormat 时间格式 小时:分钟:秒
	ConstHmsFormat = "15:04:05"
	// ConstYmdFormat 时间格式 年-月-日
	ConstYmdFormat = "2006-01-02"
	// ConstYMdHmFormat 时间格式 年-月-日 小时:分钟
	ConstYMdHmFormat = "2006-01-02 15:04"
	// ConstYMdHmsFormat 时间格式 年-月-日 小时:分钟:秒
	ConstYMdHmsFormat = "2006-01-02 15:04:05"
	// ConstStrNullTime 时刻表出发和到站时间为空时的字符串
	ConstStrNullTime = "----"

	constOneDayDuration = 24 * time.Hour // 一天的长度
	constTranCount      = 13000          // 车次数量
	constCityCount      = 620            // 有火车经过的城市数量
	// 各类座位的名称
	constSeatTypeSpecial             = "S"   // 商务座
	constSeatTypeFristClass          = "FC"  // 一等座
	constSeatTypeSecondClass         = "SC"  // 二等座
	constSeatTypeAdvancedSoftSleeper = "ASS" // 高级软卧
	constSeatTypeSoftSleeper         = "SS"  // 软卧
	constSeatTypeEMUSleeper          = "DS"  // 动车组卧铺
	constSeatTypeMoveSleeper         = "MS"  // 动卧(普快、直达、特快等车次，下铺床位改座位)
	constSeatTypeHardSleeper         = "HS"  // 硬卧
	constSeatTypeSoftSeat            = "SST" // 软座
	constSeatTypeHardSeat            = "HST" // 硬座
	constSeatTypeNoSeat              = "NST" // 无座
)

type tranCfgs []TranInfo

func (t tranCfgs) Len() int {
	return len(t)
}
func (t tranCfgs) Less(i, j int) bool {
	if t[i].TranNum < t[j].TranNum {
		return true
	}
	return t[i].EnableStartDate.Before(t[j].EnableStartDate)
}
func (t tranCfgs) Swap(i, j int) {
	t[i], t[j] = t[j], t[i]
}

var (
	// 所有车厢，存于内存，便于组装
	carMap map[int](Car)
	// 所有车次信息，不参与订票，用于查询列车的时刻表和各路段各座次的价格
	tranInfos tranCfgs
	// 各城市与经过该城市的列车映射
	cityTranMap map[string]([]*TranInfo)
)

func initTranInfo() {
	initCarMap()
	initTranInfos()
	initCityTranMap()
}

func initCarMap() {
	start := time.Now()
	var cars []Car
	db.Find(&cars)
	carMap = make(map[int](Car), len(cars))
	for i := 0; i < len(cars); i++ {
		db.Where("car_id = ?", cars[i].ID).Find(&cars[i].Seats)
		carMap[cars[i].ID] = cars[i]
	}
	fmt.Println("init car map complete, cost time:", time.Now().Sub(start).Seconds(), "(s)")
}

func initTranInfos() {
	start := time.Now()
	today, lastDate := time.Now().Format(ConstYmdFormat), time.Now().AddDate(0, 0, constDays).Format(ConstYmdFormat)
	db.Where("enable_end_date >= ? and ? >= enable_start_date", today, lastDate).Find(&tranInfos)
	goPool := newGoPool(120) // mysql 默认的最大连接数为151
	var wg sync.WaitGroup
	for i := 0; i < len(tranInfos); i++ {
		goPool.Take()
		wg.Add(1)
		go func(idx int) {
			tranInfos[idx].getFullInfo()
			goPool.Return()
			wg.Done()
		}(i)
	}
	wg.Wait()
	goPool.Close()
	sort.Sort(tranInfos)
	fmt.Println("init tran infos complete, cost time:", time.Now().Sub(start).Seconds(), "(s)")
}

func initCityTranMap() {
	start := time.Now()
	cityTranMap = make(map[string]([]*TranInfo), constCityCount)
	for i := 0; i < len(tranInfos); i++ {
		for j := 0; j < len(tranInfos[i].Timetable); j++ {
			cityCode := tranInfos[i].Timetable[j].CityCode
			tranPtrs, exist := cityTranMap[cityCode]
			if exist {
				tranPtrs = append(tranPtrs, &tranInfos[i])
			} else {
				tranPtrs = []*TranInfo{&tranInfos[i]}
			}
			cityTranMap[cityCode] = tranPtrs
		}
	}
	fmt.Println("init city tran map complete, cost time:", time.Now().Sub(start).Seconds(), "(s)")
}

func getTranInfo(tranNum string, date time.Time) (*TranInfo, bool) {
	idx := sort.Search(len(tranInfos), func(i int) bool {
		if tranInfos[i].TranNum < tranNum ||
			tranInfos[i].EnableEndDate.Before(date) ||
			tranInfos[i].EnableStartDate.After(date) {
			return false
		}
		return true
	})
	if idx == -1 {
		return nil, false
	}
	// 该车次在所选日期不发车
	if int(date.Sub(tranInfos[idx].EnableStartDate).Hours())%(tranInfos[idx].ScheduleDays*24) != 0 {
		return nil, false
	}
	return &tranInfos[idx], true
}

func getViaTrans(depS, arrS *Station) (result []*TranInfo) {
	// 获取经过出发站所在城市的所有车次
	depTrans, exist := cityTranMap[depS.CityCode]
	if !exist {
		return
	}
	// 获取经过目的站所在城市的所有车次
	arrTrans, exist := cityTranMap[arrS.CityCode]
	if !exist {
		return
	}
	if len(arrTrans) < len(depTrans) {
		arrTrans, depTrans = depTrans, arrTrans
	}
	// 用map取经过出发站城市车次和目的站城市车次的交集
	m := make(map[string](bool), len(depTrans))
	for _, t := range depTrans {
		m[t.TranNum+t.EnableStartDate.Format(ConstYmdFormat)] = true
	}
	for idx, t := range arrTrans {
		// 属于交集，则放置结果集中
		if _, exist := m[t.TranNum+t.EnableStartDate.Format(ConstYmdFormat)]; exist {
			result = append(result, arrTrans[idx])
		}
	}
	return
}

// TranInfo 列车信息结构体 ===============================start
type TranInfo struct {
	ID                int       `json:"id"`
	TranNum           string    `gorm:"index:main;type:varchar(10)" json:"tranNum"` // 车次号
	RouteDepCrossDays int       `json:"durationDays"`                               // 路段出发跨天数：最后一个路段的发车时间与起点站发车时间的间隔天数
	ScheduleDays      int       `gorm:"default:1" json:"scheduleDays"`              // 间隔多少天发一趟车，绝大多数是1天
	IsSaleTicket      bool      `json:"isSaleTicket"`                               // 是否售票
	SaleTicketTime    time.Time `json:"saleTicketTime"`                             // 售票时间，不需要日期部分，只取时间部分
	NonSaleRemark     string    `gorm:"type:varchar(100)" json:"nonSaleRemark"`     // 不售票说明
	// 生效开始日期 默认零值
	EnableStartDate time.Time `gorm:"index:query;type:datetime;default:'1970-01-01 00:00:00'" json:"enableStartDate"`
	// 生效截止日期 默认最大值
	EnableEndDate time.Time            `gorm:"index:query;type:datetime;default:'9999-12-31 23:59:59'" json:"enableEndDate"`
	CarIds        string               `gorm:"type:varchar(100)" json:"carIds"` // 车厢ID及其数量，格式如：32:1;12:2; ...
	carCount      uint8                // 车厢数量
	carTypeIdxMap map[string]([]uint8) // 各座次类型及其对应的车厢索引集合
	Timetable     []Route              `gorm:"-" json:"timetable"`    // 时刻表
	SeatPriceMap  map[string]([]int)   `gorm:"-" json:"seatPriceMap"` // 各类席位在各路段的价格
}

// 是否为城际车次，城际车次在同一个城市内可能会有多个站，情况相对特殊
func (t *TranInfo) isIntercity() bool {
	return strings.Index(t.TranNum, "C") == 0
}

func (t *TranInfo) getFullInfo() {
	// 获取时刻表信息
	db.Where("tran_id = ?", t.ID).Order("station_index").Find(&t.Timetable)
	// 获取各席别在各路段的价格，大多数车次只有三类席别（无座不考虑）
	t.SeatPriceMap = make(map[string]([]int), 3)
	var routePrices []RoutePrice
	db.Where("tran_id = ?", t.ID).Order("seat_type, route_index").Find(&routePrices)
	count := len(routePrices) + 1
	for start, end := 0, 1; end < count; end++ {
		if end == count-1 || routePrices[start].SeatType != routePrices[end].SeatType {
			arr := routePrices[start:end]
			prices := make([]int, len(arr))
			for idx, rp := range arr {
				prices[idx] = rp.Price
			}
			t.SeatPriceMap[routePrices[start].SeatType] = prices
			start = end
		}
	}
	// 设置车厢信息
	t.carTypeIdxMap = make(map[string]([]uint8))
	// 车厢ID及其数量，格式如：32:1;12:2; ...
	carSettings, carIdx := strings.Split(t.CarIds, ";"), uint8(0)
	for i := 0; i < len(carSettings); i++ {
		setting := strings.Split(carSettings[i], ":")
		if len(setting) == 2 {
			id, _ := strconv.Atoi(setting[0])
			count, _ := strconv.Atoi(setting[1])
			car := carMap[id]
			idxs := make([]uint8, count)
			for k := 0; k < count; k++ {
				idxs[k] = carIdx
				carIdx++
			}
			// 当t.CarIds中某ID配置了多次，应该以追加的形式得到索引结果集，否则就会得到覆盖的错误结果
			// 如："16:2;18:5;16:1"，则ID为16的车厢，应为seatTypeOf_16:[0,1,7]，而不是seatTypeOf_16:[7]
			if existIdxs, ok := t.carTypeIdxMap[car.SeatType]; ok {
				idxs = append(existIdxs, idxs...)
			}
			t.carTypeIdxMap[car.SeatType] = idxs
		}
	}
	t.carCount = carIdx
}

// 要严格与getFullInfo中设置车厢信息部分的逻辑一致
func (t *TranInfo) getScheduleCars() []ScheduleCar {
	// 获取排班的车厢信息
	result := make([]ScheduleCar, t.carCount)
	carIdx, routeCount := uint8(0), len(t.Timetable)-1
	carSettings := strings.Split(t.CarIds, ";")
	for i := 0; i < len(carSettings); i++ {
		setting := strings.Split(carSettings[i], ":")
		if len(setting) == 2 {
			id, _ := strconv.Atoi(setting[0])
			count, _ := strconv.Atoi(setting[1])
			sc := scheduleCarMap[id]
			for k := 0; k < count; k++ {
				result[carIdx] = ScheduleCar{
					SeatType:    sc.SeatType,
					CarNum:      carIdx + 1,
					NoSeatCount: sc.NoSeatCount,
					Seats:       sc.Seats,
					EachRouteTravelerCount: make([]uint8, routeCount), //sc.EachRouteTravelerCount,
				}
				carIdx++
			}
		}
	}
	return result
}

// Save 保存到数据库
func (t *TranInfo) Save() (bool, string) {
	t.initTimetable()
	t.EnableEndDate = t.EnableEndDate.Add(24*time.Hour - time.Second)
	if t.ID == 0 {
		db.Create(t)
	} else {
		db.Save(t)
		db.Delete(Route{}, "tran_id = ?", t.ID)
		db.Delete(RoutePrice{}, "tran_id = ?", t.ID)
	}
	for i, r := range t.Timetable {
		r.TranID = t.ID
		r.TranNum = t.TranNum
		r.StationIndex = uint8(i + 1)
		db.Create(&r)
	}
	for k, v := range t.SeatPriceMap {
		for i, p := range v {
			rp := &RoutePrice{TranID: t.ID, SeatType: k, RouteIndex: uint8(i), Price: p}
			db.Create(rp)
		}
	}
	return true, ""
}

// 重置列车所经站点的时间，默认从0001-01-01开始，终点站的到达时间0001-01-0N, 其中‘N’表示该列车运行的跨天数
func (t *TranInfo) initTimetable() {
	// 所跨天数
	day, routeCount := 0, len(t.Timetable)
	// Note: 这里默认有一个规则，所有列车在任一路段，运行时间不会超过24小时；当有例外时，下面的代码需要调整逻辑
	// 起点站无需重置出发时间和到达时间，终点站无需重置出发时间
	for i := 1; i < routeCount; i++ {
		t.Timetable[i].ArrTime = t.Timetable[i].ArrTime.AddDate(0, 0, day)
		if t.Timetable[i].ArrTime.Before(t.Timetable[i-1].DepTime) {
			day++
			t.Timetable[i].ArrTime = t.Timetable[i].ArrTime.AddDate(0, 0, 1)
		}
		if i == routeCount-1 {
			break
		}
		t.Timetable[i].DepTime = t.Timetable[i].DepTime.AddDate(0, 0, day)
		if t.Timetable[i].DepTime.Before(t.Timetable[i].ArrTime) {
			day++
			t.Timetable[i].DepTime = t.Timetable[i].DepTime.AddDate(0, 0, 1)
		}
	}
	// 时刻表中的第一站出发日期，默认都为0001-01-01，所以最后一个路段的发车日期 - 1，就是路段出发间隔天数
	t.RouteDepCrossDays = t.Timetable[len(t.Timetable)-1].DepTime.YearDay() - 1
}

// 根据起止站获取各类座位的票价
func (t *TranInfo) getSeatPrice(depIdx, arrIdx uint8) (result map[string]float32) {
	result = make(map[string]float32)
	for seatType, eachRoutePrice := range t.SeatPriceMap {
		length := uint8(len(eachRoutePrice))
		if arrIdx > length-1 {
			arrIdx = length - 1
		}
		price := 0
		for i := depIdx; i < arrIdx; i++ {
			price += eachRoutePrice[i]
		}
		result[seatType] = float32(price) / 100
	}
	return
}

// IsMatchQuery 判断当前车次在日期上是否匹配
func (t *TranInfo) IsMatchQuery(depS, arrS *Station, queryDate time.Time) (depIdx, arrIdx uint8, depDate string, ok bool) {
	// 查询的日期需在车次配置的有效期内
	if queryDate.Before(t.EnableStartDate) || queryDate.After(t.EnableEndDate.AddDate(0, 0, t.RouteDepCrossDays)) {
		return
	}
	depI, timetableLen := -1, len(t.Timetable)
	// 非城际车次
	if !t.isIntercity() {
		for i := 0; i < timetableLen-1; i++ {
			if t.Timetable[i].CityCode == depS.CityCode {
				depI = i
				if t.Timetable[i].StationCode == depS.StationCode {
					depI = i
				}
				continue
			}
			if depI != -1 && t.Timetable[i].CityCode != depS.CityCode {
				break
			}
		}
		depIdx = uint8(depI)
		for i := depI + 1; i < timetableLen; i++ {
			if t.Timetable[i].CityCode == arrS.CityCode {
				arrIdx = uint8(i)
				if t.Timetable[i].StationCode == arrS.StationCode {
					arrIdx = uint8(i)
					break
				}
				continue
			}
			if depIdx < arrIdx && t.Timetable[i].CityCode != arrS.CityCode {
				break
			}
		}
		if depIdx >= arrIdx {
			return
		}
	} else { // 城际车次
		for i := 0; i < timetableLen; i++ {
			if depI == -1 && t.Timetable[i].StationCode == depS.StationCode {
				depI = i
				depIdx = uint8(i)
				continue
			}
			if depI != -1 && t.Timetable[i].StationCode == arrS.StationCode {
				arrIdx = uint8(i)
				break
			}
		}
	}
	// 计算当前车次信息的出发站发车日期
	date := queryDate.AddDate(0, 0, 1-t.Timetable[depIdx].DepTime.Day())
	// 不是每天发车的车次，要判断发车日期是否有效
	if t.ScheduleDays > 1 {
		if date.Sub(t.EnableStartDate).Hours()/float64(24*t.ScheduleDays) != 0 {
			return
		}
	}
	depDate = date.Format(ConstYmdFormat)
	ok = true
	return
}

// getOrderPrice 获取订单价格
func (t *TranInfo) getOrderPrice(seatType, seatNum string, depIdx, arrIdx uint8) float32 {
	var priceSlice []int
	switch seatType {
	case constSeatTypeAdvancedSoftSleeper, constSeatTypeSoftSleeper, constSeatTypeHardSleeper:
		priceSlice = t.SeatPriceMap[seatType+strings.Split(seatNum, "-")[1]][depIdx : arrIdx-depIdx]
	default:
		priceSlice = t.SeatPriceMap[seatType][depIdx : arrIdx-depIdx]
	}
	price := 0
	for _, p := range priceSlice {
		price += p
	}
	return float32(price) / 100
}

// getDepAndArrTime 获取出发和到站时间
func (t *TranInfo) getDepAndArrTime(date string, depIdx, arrIdx uint8) (time.Time, time.Time) {
	depTime, arrTime := t.Timetable[depIdx].DepTime, t.Timetable[arrIdx].ArrTime
	dt, _ := time.Parse(ConstYmdFormat, date)
	y, m, d := dt.Date()
	depTime = depTime.AddDate(y, int(m), d)
	arrTime = arrTime.AddDate(y, int(m), d)
	return depTime, arrTime
}

// Route 时刻表信息
type Route struct {
	ID              uint64
	TranID          int     // 车次ID
	TranNum         string  `gorm:"index:main;type:varchar(10)"` // 车次号
	StationIndex    uint8   // 车站索引
	StationName     string  `gorm:"type:nvarchar(20)" json:"stationName"`    // 车站名
	StationCode     string  `gorm:"type:varchar(10)"`                        // 车站编码
	CityCode        string  `gorm:"type:varchar(20)"`                        // 城市编码
	CheckTicketGate string  `gorm:"type:varchar(10)" json:"checkTicketGate"` // 检票口
	Platform        uint8   `json:"platform"`                                // 乘车站台
	MileageNext     float32 `json:"mileageNext"`                             // 距下一站的里程
	// 出发时间
	DepTime time.Time `gorm:"type:datetime" json:"depTime"`
	// 到达时间
	ArrTime time.Time `gorm:"type:datetime" json:"arrTime"`
}

func (r *Route) getStrDepTime() string {
	return r.DepTime.Format(ConstHmFormat)
}

func (r *Route) getStrArrTime() string {
	return r.ArrTime.Format(ConstHmFormat)
}

func (r *Route) getStrStayTime() string {
	return strconv.FormatFloat(r.DepTime.Sub(r.ArrTime).Minutes(), 'f', 0, 64)
}

// RoutePrice 各路段价格
type RoutePrice struct {
	ID         uint64
	TranID     int    `gorm:"index:main"`      // 车次ID
	SeatType   string `gorm:"type:varchar(5)"` // 座次类型
	RouteIndex uint8  // 路段索引
	Price      int    // 价格, 单位：分
}

// Car 车厢信息结构体
type Car struct {
	ID          int    `json:"id"`
	TranType    string `gorm:"type:varchar(20)" json:"tranType"` // 车次类型 高铁、动车、直达等
	SeatType    string `gorm:"type:varchar(5)" json:"seatType"`  // 车厢的座位类型
	SeatCount   uint8  // 车厢内座位数(或床位数)
	NoSeatCount uint8  `json:"noSeatCount"`                     // 车厢内站票数
	Remark      string `gorm:"type:nvarchar(50)" json:"remark"` // 说明
	Seats       []Seat `json:"seats"`                           // 车厢的所有座位
}

// Save 保存车厢信息到数据库
func (c *Car) Save() (bool, string) {
	c.SeatCount = uint8(len(c.Seats))
	if c.ID == 0 {
		db.Create(c)
	} else {
		db.Save(c)
		db.Delete(Seat{}, "car_id = ?", c.ID)
	}
	for i := 0; i < len(c.Seats); i++ {
		c.Seats[i].CarID = c.ID
		db.Create(&c.Seats[i])
	}
	return true, ""
}

// Seat 座位信息结构体
type Seat struct {
	CarID     int    `gorm:"index:main"`                     // 车厢ID
	SeatNum   string `gorm:"type:varchar(5)" json:"seatNum"` // 座位号
	IsStudent bool   `json:"isStudent"`                      // 是否学生票
}
