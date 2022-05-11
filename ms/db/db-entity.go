package db

import "time"

type ApiRequest struct {
	ID            int64      `gorm:"column:id"`
	BusinessTxnId string     `gorm:"column:business_txn_id"`
	ApiRequestKey string     `gorm:"column:api_request_key"`
	StartTime     *time.Time `gorm:"column:start_time"`
	EndTime       *time.Time `gorm:"column:end_time"`
	Ver           int32      `gorm:"column:ver"`
	ApiUrl        string     `gorm:"column:api_url"`
	Status        string     `gorm:"column:status"`
}
