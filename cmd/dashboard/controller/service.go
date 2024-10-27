package controller

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jinzhu/copier"

	"github.com/naiba/nezha/model"
	"github.com/naiba/nezha/service/singleton"
	"gorm.io/gorm"
)

// List service
// @Summary List service
// @Security BearerAuth
// @Schemes
// @Description List service
// @Tags common
// @Produce json
// @Success 200 {object} model.CommonResponse[model.ServiceResponse]
// @Router /service [get]
func listService(c *gin.Context) (*model.ServiceResponse, error) {
	res, err, _ := requestGroup.Do("list-service", func() (interface{}, error) {
		singleton.AlertsLock.RLock()
		defer singleton.AlertsLock.RUnlock()
		var stats map[uint64]model.ServiceResponseItem
		var statsStore map[uint64]model.CycleTransferStats
		copier.Copy(&stats, singleton.ServiceSentinelShared.LoadStats())
		copier.Copy(&statsStore, singleton.AlertsCycleTransferStatsStore)
		_, isMember := c.Get(model.CtxKeyAuthorizedUser)
		authorized := isMember // TODO || isViewPasswordVerfied
		for k, service := range stats {
			if !service.Service.EnableShowInService {
				delete(stats, k)
			}
			if !authorized {
				service.Service = &model.Service{Name: service.Service.Name}
				stats[k] = service
			}
		}
		return []interface {
		}{
			stats, statsStore,
		}, nil
	})
	if err != nil {
		return nil, err
	}

	return &model.ServiceResponse{
		Services:           res.([]interface{})[0].(map[uint64]model.ServiceResponseItem),
		CycleTransferStats: res.([]interface{})[1].(map[uint64]model.CycleTransferStats),
	}, nil
}

// List service histories by server id
// @Summary List service histories by server id
// @Security BearerAuth
// @Schemes
// @Description List service histories by server id
// @Tags common
// @param id path uint true "Server ID"
// @Produce json
// @Success 200 {object} model.CommonResponse[[]model.ServiceInfos]
// @Router /service/{id} [get]
func listServiceHistory(c *gin.Context) ([]*model.ServiceInfos, error) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		return nil, err
	}

	singleton.ServerLock.RLock()
	server, ok := singleton.ServerList[id]
	if !ok {
		return nil, errors.New("server not found")
	}

	_, isMember := c.Get(model.CtxKeyAuthorizedUser)
	authorized := isMember // TODO || isViewPasswordVerfied

	if server.HideForGuest && !authorized {
		return nil, errors.New("unauthorized")
	}
	singleton.ServerLock.RUnlock()

	var serviceHistories []*model.ServiceHistory
	if err := singleton.DB.Model(&model.ServiceHistory{}).Select("service_id, created_at, server_id, avg_delay").
		Where("server_id = ?", id).Where("created_at >= ?", time.Now().Add(-24*time.Hour)).Order("service_id, created_at").
		Scan(&serviceHistories).Error; err != nil {
		return nil, err
	}

	singleton.ServiceSentinelShared.ServicesLock.RLock()
	defer singleton.ServiceSentinelShared.ServicesLock.RUnlock()
	singleton.ServerLock.RLock()
	defer singleton.ServerLock.RUnlock()

	var sortedServiceIDs []uint64
	resultMap := make(map[uint64]*model.ServiceInfos)
	for _, history := range serviceHistories {
		infos, ok := resultMap[history.ServiceID]
		if !ok {
			infos = &model.ServiceInfos{
				ServiceID:   history.ServiceID,
				ServerID:    history.ServerID,
				ServiceName: singleton.ServiceSentinelShared.Services[history.ServiceID].Name,
				ServerName:  singleton.ServerList[history.ServerID].Name,
			}
			resultMap[history.ServiceID] = infos
			sortedServiceIDs = append(sortedServiceIDs, history.ServiceID)
		}
		infos.CreatedAt = append(infos.CreatedAt, history.CreatedAt.Truncate(time.Minute).Unix()*1000)
		infos.AvgDelay = append(infos.AvgDelay, history.AvgDelay)
	}

	ret := make([]*model.ServiceInfos, 0, len(sortedServiceIDs))
	for _, id := range sortedServiceIDs {
		ret = append(ret, resultMap[id])
	}

	return ret, nil
}

// List server with service
// @Summary List server with service
// @Security BearerAuth
// @Schemes
// @Description List server with service
// @Tags common
// @Produce json
// @Success 200 {object} model.CommonResponse[[]uint64]
// @Router /service/server [get]
func listServerWithServices(c *gin.Context) ([]uint64, error) {
	var serverIdsWithService []uint64
	if err := singleton.DB.Model(&model.ServiceHistory{}).
		Select("distinct(server_id)").
		Where("server_id != 0").
		Find(&serverIdsWithService).Error; err != nil {
		return nil, newGormError("%v", err)
	}

	_, isMember := c.Get(model.CtxKeyAuthorizedUser)
	authorized := isMember // TODO || isViewPasswordVerfied

	var ret []uint64
	for _, id := range serverIdsWithService {
		singleton.ServerLock.RLock()
		server, ok := singleton.ServerList[id]
		if !ok {
			singleton.ServerLock.RUnlock()
			return nil, errors.New("server not found")
		}

		if !server.HideForGuest || authorized {
			ret = append(ret, id)
		}
		singleton.ServerLock.RUnlock()
	}

	return ret, nil
}

// Create service
// @Summary Create service
// @Security BearerAuth
// @Schemes
// @Description Create service
// @Tags auth required
// @Accept json
// @param request body model.ServiceForm true "Service Request"
// @Produce json
// @Success 200 {object} model.CommonResponse[uint64]
// @Router /service [post]
func createService(c *gin.Context) (uint64, error) {
	var mf model.ServiceForm
	if err := c.ShouldBindJSON(&mf); err != nil {
		return 0, err
	}

	var m model.Service
	m.Name = mf.Name
	m.Target = strings.TrimSpace(mf.Target)
	m.Type = mf.Type
	m.SkipServers = mf.SkipServers
	m.Cover = mf.Cover
	m.Notify = mf.Notify
	m.NotificationGroupID = mf.NotificationGroupID
	m.Duration = mf.Duration
	m.LatencyNotify = mf.LatencyNotify
	m.MinLatency = mf.MinLatency
	m.MaxLatency = mf.MaxLatency
	m.EnableShowInService = mf.EnableShowInService
	m.EnableTriggerTask = mf.EnableTriggerTask
	m.RecoverTriggerTasks = mf.RecoverTriggerTasks
	m.FailTriggerTasks = mf.FailTriggerTasks

	if err := singleton.DB.Create(&m).Error; err != nil {
		return 0, err
	}

	var skipServers []uint64
	for k := range m.SkipServers {
		skipServers = append(skipServers, k)
	}

	var err error
	if m.Cover == 0 {
		err = singleton.DB.Unscoped().Delete(&model.ServiceHistory{}, "service_id = ? and server_id in (?)", m.ID, skipServers).Error
	} else {
		err = singleton.DB.Unscoped().Delete(&model.ServiceHistory{}, "service_id = ? and server_id not in (?)", m.ID, skipServers).Error
	}
	if err != nil {
		return 0, err
	}

	return m.ID, singleton.ServiceSentinelShared.OnServiceUpdate(m)
}

// Update service
// @Summary Update service
// @Security BearerAuth
// @Schemes
// @Description Update service
// @Tags auth required
// @Accept json
// @param id path uint true "Service ID"
// @param request body model.ServiceForm true "Service Request"
// @Produce json
// @Success 200 {object} model.CommonResponse[any]
// @Router /service/{id} [patch]
func updateService(c *gin.Context) (any, error) {
	strID := c.Param("id")
	id, err := strconv.ParseUint(strID, 10, 64)
	if err != nil {
		return nil, err
	}
	var mf model.ServiceForm
	if err := c.ShouldBindJSON(&mf); err != nil {
		return nil, err
	}
	var m model.Service
	if err := singleton.DB.First(&m, id).Error; err != nil {
		return nil, fmt.Errorf("service id %d does not exist", id)
	}
	m.Name = mf.Name
	m.Target = strings.TrimSpace(mf.Target)
	m.Type = mf.Type
	m.SkipServers = mf.SkipServers
	m.Cover = mf.Cover
	m.Notify = mf.Notify
	m.NotificationGroupID = mf.NotificationGroupID
	m.Duration = mf.Duration
	m.LatencyNotify = mf.LatencyNotify
	m.MinLatency = mf.MinLatency
	m.MaxLatency = mf.MaxLatency
	m.EnableShowInService = mf.EnableShowInService
	m.EnableTriggerTask = mf.EnableTriggerTask
	m.RecoverTriggerTasks = mf.RecoverTriggerTasks
	m.FailTriggerTasks = mf.FailTriggerTasks

	if err := singleton.DB.Save(&m).Error; err != nil {
		return nil, err
	}

	var skipServers []uint64
	for k := range m.SkipServers {
		skipServers = append(skipServers, k)
	}

	if m.Cover == 0 {
		err = singleton.DB.Unscoped().Delete(&model.ServiceHistory{}, "service_id = ? and server_id in (?)", m.ID, skipServers).Error
	} else {
		err = singleton.DB.Unscoped().Delete(&model.ServiceHistory{}, "service_id = ? and server_id not in (?)", m.ID, skipServers).Error
	}
	if err != nil {
		return nil, err
	}

	return nil, singleton.ServiceSentinelShared.OnServiceUpdate(m)
}

// Batch delete service
// @Summary Batch delete service
// @Security BearerAuth
// @Schemes
// @Description Batch delete service
// @Tags auth required
// @Accept json
// @param request body []uint true "id list"
// @Produce json
// @Success 200 {object} model.CommonResponse[any]
// @Router /batch-delete/service [post]
func batchDeleteService(c *gin.Context) (any, error) {
	var ids []uint64
	if err := c.ShouldBindJSON(&ids); err != nil {
		return nil, err
	}
	err := singleton.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Delete(&model.Service{}, "id in (?)", ids).Error; err != nil {
			return err
		}
		return tx.Unscoped().Delete(&model.ServiceHistory{}, "service_id in (?)", ids).Error
	})
	if err != nil {
		return nil, err
	}
	singleton.ServiceSentinelShared.OnServiceDelete(ids)
	return nil, nil
}
