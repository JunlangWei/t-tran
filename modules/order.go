package modules

import (
	"errors"
	"time"
)

const (
	constOneDayOrderCancelLimit = 3 // 单日订单取消上限数

	// 订单状态
	constOrderStatusUnpay           = iota // 未支付
	constOrderStatusTimeout                // 未支付超时
	constOrderStatusCancelled              // 取消未支付订单
	constOrderStatusPaid                   // 已支付
	constOrderStatusRefund                 // 已退票
	constOrderStatusChanged                // 已改签
	constOrderStatusChangeUnPay            // 改签票未支付
	constOrderStatusChangeTimeout          // 改签票支付超时
	constOrderStatusChangeCancelled        // 改签票取消支付
	constOrderStatusChangePaid             // 改签票已支付
	constOrderStatusIssued                 // 已出票
	constOrderStatusExpired                // 已过期（旅程已结束）
)

var (
	// 未支付订单
	unpayOrders []*Order
	// 已支付且未乘车的订单
	validOrders []*Order
)

// OrderCancelTimes 订单取消次数模型
type OrderCancelTimes struct {
	UserID      uint64    // 用户ID
	Date        time.Time // 日期
	CancelTimes uint8     // 取消订单的次数
}

// 判断用户当天取消订单的次数是否已达上限
func isCancelTimesInLimit(userID uint64) bool {
	date := time.Now().Format(ConstYmdFormat)
	oct := &OrderCancelTimes{}
	db.Where("user_id = ? and date = ?", userID, date).Attrs(OrderCancelTimes{CancelTimes: 0}).FirstOrCreate(oct)
	return oct.CancelTimes < constOneDayOrderCancelLimit
}

// hasUnpayOrder 判断是否有未支付订单
func hasUnpayOrder(userID uint64) bool {
	count := 0
	db.Model(&Order{}).Where("user_id = ? and status = 0", userID).Count(&count)
	return count == 0
}

// hasTimeConflict 判断乘车人的乘车时间是否冲突
func hasTimeConflict(passengerID uint64, depTime, arrTime time.Time) bool {
	count := 0
	availOrdreStatus := []uint8{constOrderStatusUnpay, constOrderStatusPaid, constOrderStatusChangeUnPay, constOrderStatusChangePaid, constOrderStatusIssued}
	db.Model(&Order{}).Where("passenger_id = ? and status in (?) and ((dep_time < ? and ? < arr_time) or (dep_time < ? and ? < arr_time) or (? < dep_time and arr_time < ?))",
		passengerID, availOrdreStatus, depTime, depTime, arrTime, arrTime, depTime, arrTime).Count(&count)
	return count != 0
}

// hasTimeConflictInChange 改签时，判断乘车人的乘车时间是否冲突
func hasTimeConflictInChange(passengerID, orderID uint64, depTime, arrTime time.Time) bool {
	count := 0
	availOrdreStatus := []uint8{constOrderStatusUnpay, constOrderStatusPaid, constOrderStatusChangeUnPay, constOrderStatusChangePaid, constOrderStatusIssued}
	// 相较于hasTimeConflict，多了一个orderID的限制
	db.Model(&Order{}).Where("passenger_id = ? and status in (?) and order_id != ? and ((dep_time < ? and ? < arr_time) or (dep_time < ? and ? < arr_time) or (? < dep_time and arr_time < ?))",
		passengerID, availOrdreStatus, orderID, depTime, depTime, arrTime, arrTime, depTime, arrTime).Count(&count)
	return count != 0
}

// Order 订单
type Order struct {
	ID              uint64
	OrderNum        string    // 订单号
	UserID          uint64    // 用户ID
	PassengerID     uint64    // 乘客ID
	TranDepDate     string    // 列车发车日期
	TranNum         string    // 车次号
	CarNum          uint8     // 车厢号
	SeatNum         string    // 座位号
	SeatType        string    // 座位类型
	CheckTicketGate string    // 检票口
	DepStation      string    // 出发站
	DepStationIdx   uint8     // 出发站在路线中的索引
	DepTime         time.Time `gorm:"type:datetime"` // 出发时间
	ArrStation      string    // 到达站
	ArrStationIdx   uint8     // 到达站在路线中的索引
	ArrTime         time.Time `gorm:"type:datetime"` // 到达时间
	Price           float32   // 票价
	BookTime        time.Time `gorm:"type:datetime"` // 订票时间
	PayTime         time.Time `gorm:"type:datetime"` // 支付时间
	PayType         uint      // 支付类型
	PayAccount      string    // 支付账户
	Status          uint8     // 订单状态 1.未支付 2.超时未支付 3.已支付 4.已退票 5.已改签 6.已出票
	changeOrderID   uint64    // 改签票订单ID
	sourceOrderID   uint64    // 原订单ID
}

// SubmitOrder 订票
func SubmitOrder(tranNum string, date time.Time, depIdx, arrIdx uint8, userID, contactID uint64, isStudent bool, seatType string) error {
	// 判断是否已达单日取消订单次数上限
	if !isCancelTimesInLimit(userID) {
		return errors.New("您单日取消订单次数已达上限")
	}
	// 判断是否有未支付的订单，有未支付的订单，则不进行下一步 可考虑从数据库中查询
	if hasUnpayOrder(userID) {
		return errors.New("您有未完成的订单，请先完成订单")
	}
	tran, exist := getTranInfo(tranNum, date)
	if !exist {
		return errors.New("车次信息不存在")
	}
	// 判断当前乘坐人在乘车时间上是否冲突 可考虑从数据库中查询
	depTime := tran.Timetable[depIdx].DepTime
	arrTime := tran.Timetable[arrIdx].ArrTime
	if hasTimeConflict(contactID, depTime, arrTime) {
		return errors.New("乘车人时间冲突")
	}
	// 排班信息
	scheduleTran := getScheduleTran(tranNum, date.Format(ConstYmdFormat))
	carIdxList, exist := scheduleTran.carTypeIdxMap[seatType]
	if !exist {
		return errors.New("所选席别无效")
	}
	// 锁定座位，创建订单
	var car *ScheduleCar
	var seat *ScheduleSeat
	ok, isMedley := false, false
	seatBit := countSeatBit(depIdx, arrIdx)
	// 优先席位票
	for _, carIdx := range carIdxList {
		if seat, ok = scheduleTran.Cars[carIdx].getAvailableSeat(depIdx, arrIdx, seatBit, isStudent); ok {
			car = &scheduleTran.Cars[carIdx]
			break
		}
	}
	// 无席位票，则考虑站票
	if !ok {
		for _, carIdx := range carIdxList {
			if seat, ok = scheduleTran.Cars[carIdx].getAvailableNoSeat(depIdx, arrIdx, seatBit); ok {
				car = &scheduleTran.Cars[carIdx]
				isMedley = true
				break
			}
		}
	}
	// 无票
	if !ok {
		return errors.New("没有足够的票")
	}
	o := &Order{
		//ID:              1,  // TODO: ID生成器需返回一个订单全局唯一ID
		OrderNum:        "", // TODO: 订单号生成器需返回一个全局唯一订单号
		UserID:          userID,
		PassengerID:     contactID,
		TranDepDate:     scheduleTran.DepartureDate,
		TranNum:         tran.TranNum,
		CarNum:          car.CarNum,
		SeatNum:         seat.SeatNum,
		SeatType:        car.SeatType,
		CheckTicketGate: tran.Timetable[depIdx].CheckTicketGate,
		DepStation:      tran.Timetable[depIdx].StationName,
		DepStationIdx:   depIdx,
		DepTime:         tran.Timetable[depIdx].DepTime,
		ArrStation:      tran.Timetable[arrIdx].StationName,
		ArrStationIdx:   arrIdx,
		ArrTime:         tran.Timetable[arrIdx].ArrTime,
		// 根据价格表，各路段累加
		Price:    tran.getOrderPrice(car.SeatType, seat.SeatNum, depIdx, arrIdx),
		BookTime: time.Now(),
		Status:   1,
	}
	// 拼凑的只能是站票
	if isMedley {
		o.SeatType = constSeatTypeNoSeat
	}
	unpayOrders = append(unpayOrders, o)
	time.AfterFunc(constUnpayOrderAvaliableTime*time.Minute, func() {
		if o.Status == constOrderStatusUnpay {
			seatBit := countSeatBit(o.DepStationIdx, o.ArrStationIdx)
			o.Status = constOrderStatusTimeout
			seat.Release(seatBit)
			car.releaseSeat(o.DepStationIdx, o.ArrStationIdx)
		}
	})
	return nil
}

// CancelOrder 取消订单
func (o *Order) CancelOrder() error {
	st := getScheduleTran(o.TranNum, o.TranDepDate)
	var car *ScheduleCar
	for i := 0; i < len(st.Cars); i++ {
		if o.CarNum == st.Cars[i].CarNum {
			car = &st.Cars[i]
			break
		}
	}
	if o.SeatType != constSeatTypeNoSeat {
		seatBit := countSeatBit(o.DepStationIdx, o.ArrStationIdx)
		for i := 0; i < len(car.Seats); i++ {
			if car.Seats[i].SeatNum == o.SeatNum {
				car.Seats[i].Release(seatBit)
				break
			}
		}
	}
	car.releaseSeat(o.DepStationIdx, o.ArrStationIdx)
	o.Status = constOrderStatusCancelled
	return nil
}

// Payment 订单支付
func (o *Order) Payment(payType uint, payAccount string, price float32) error {
	if o.Status != constOrderStatusUnpay {
		switch o.Status {
		case constOrderStatusPaid:
			return errors.New("订单已支付")
		case constOrderStatusTimeout:
			return errors.New("订单已过期")
		case constOrderStatusChanged:
			return errors.New("订单已改签")
		case constOrderStatusIssued:
			return errors.New("已出票")
		}
	}
	if o.Price != price {
		return errors.New("支付金额错误")
	}
	if err := payment(o.ID, o.UserID, payType, payAccount, o.Price); err != nil {
		return err
	}
	o.PayType = payType
	o.PayAccount = payAccount
	o.PayTime = time.Now()
	o.Status = constOrderStatusPaid
	// TODO：将此订单从未支付列表移至已支付列表
	return nil
}

// Refund 退票（已支付订单）
func (o *Order) Refund() error {
	if err := o.CancelOrder(); err != nil {
		return err
	}
	if err := refund(o.ID, o.UserID, o.PayType, o.PayAccount, o.Price); err != nil {
		return err
	}
	return nil
}

// Change 改签
func (o *Order) Change(tranNum string, date time.Time, depIdx, arrIdx uint8, userID, passengerID uint64, isStudent bool, seatType string) error {
	// 当前订单已有改签记录或者当前订单已是改签票，则不能再改签
	if o.changeOrderID != 0 || o.sourceOrderID != 0 {
		return errors.New("已经改签，无法再次改签")
	}

	// 判断是否有未支付的订单，有未支付的订单，则不进行下一步 可考虑从数据库中查询
	if hasUnpayOrder(userID) {
		return errors.New("您有未完成的订单，请先完成订单")
	}

	tran, exist := getTranInfo(tranNum, date)
	if !exist {
		return errors.New("改签的车次不存在")
	}
	// 判断当前乘坐人在乘车时间上是否冲突 可考虑从数据库中查询
	depTime := tran.Timetable[depIdx].DepTime
	arrTime := tran.Timetable[arrIdx].ArrTime
	if hasTimeConflictInChange(passengerID, o.ID, depTime, arrTime) {
		return errors.New("乘车人时间冲突")
	}
	scheduleTran := getScheduleTran(tranNum, date.Format(ConstYmdFormat))
	// 锁定座位，创建订单
	carIdxList, exist := scheduleTran.carTypeIdxMap[seatType]
	if !exist {
		return errors.New("所选席别无效")
	}
	var car *ScheduleCar
	var seat *ScheduleSeat
	ok, isMedley, carIdx := false, false, -1
	seatBit := countSeatBit(depIdx, arrIdx)
	// 优先席位票
	for _, carIdx := range carIdxList {
		if seat, ok = scheduleTran.Cars[carIdx].getAvailableSeat(depIdx, arrIdx, seatBit, isStudent); ok {
			car = &scheduleTran.Cars[carIdx]
			break
		}
	}
	// 无席位票，则考虑站票
	if !ok {
		for _, carIdx := range carIdxList {
			if seat, ok = scheduleTran.Cars[carIdx].getAvailableNoSeat(depIdx, arrIdx, seatBit); ok {
				car = &scheduleTran.Cars[carIdx]
				isMedley = true
				break
			}
		}
	}
	// 无票
	if !ok {
		return errors.New("没有足够的票")
	}
	newOrder := &Order{
		//ID:              1,  // TODO: ID生成器需返回一个订单全局唯一ID
		OrderNum:        "", // TODO: 订单号生成器需返回一个全局唯一订单号
		UserID:          userID,
		PassengerID:     passengerID,
		TranDepDate:     scheduleTran.DepartureDate,
		TranNum:         tran.TranNum,
		CarNum:          scheduleTran.Cars[carIdx].CarNum,
		SeatNum:         seat.SeatNum,
		SeatType:        scheduleTran.Cars[carIdx].SeatType,
		CheckTicketGate: tran.Timetable[depIdx].CheckTicketGate,
		DepStation:      tran.Timetable[depIdx].StationName,
		DepStationIdx:   depIdx,
		DepTime:         tran.Timetable[depIdx].DepTime,
		ArrStation:      tran.Timetable[arrIdx].StationName,
		ArrStationIdx:   arrIdx,
		ArrTime:         tran.Timetable[arrIdx].ArrTime,
		// 根据价格表，各路段累加
		Price:    tran.getOrderPrice(car.SeatType, seat.SeatNum, depIdx, arrIdx),
		BookTime: time.Now(),
		Status:   1,
	}
	// 拼凑的只能是站票
	if isMedley {
		newOrder.SeatType = constSeatTypeNoSeat
	}
	o.changeOrderID = newOrder.ID
	// 原票价高于改签后的票价则需设置改签票为已支付状态，且需退还差额；否则改签票保持未支付状态，且用户需补交差额
	if o.Price >= newOrder.Price {
		// 退款
		refund(o.ID, o.UserID, o.PayType, o.PayAccount, o.Price-newOrder.Price)
		newOrder.Status = constOrderStatusPaid
		o.Status = constOrderStatusChanged
	} else {
		newOrder.Price -= o.Price
		time.AfterFunc(constUnpayOrderAvaliableTime*time.Minute, func() {
			if newOrder.Status == constOrderStatusUnpay {
				seatBit := countSeatBit(newOrder.DepStationIdx, newOrder.ArrStationIdx)
				newOrder.Status = constOrderStatusTimeout
				seat.Release(seatBit)
				scheduleTran.Cars[carIdx].releaseSeat(newOrder.DepStationIdx, newOrder.ArrStationIdx)
			}
		})
	}
	return nil
}

// CheckIn 取票
func (o *Order) CheckIn() {
	if o.Status == constOrderStatusPaid || o.Status == constOrderStatusChangePaid {
		o.Status = constOrderStatusIssued
	}
}
