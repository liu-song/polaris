/**
 * Tencent is pleased to support the open source community by making Polaris available.
 *
 * Copyright (C) 2019 THL A29 Limited, a Tencent company. All rights reserved.
 *
 * Licensed under the BSD 3-Clause License (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * https://opensource.org/licenses/BSD-3-Clause
 *
 * Unless required by applicable law or agreed to in writing, software distributed
 * under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR
 * CONDITIONS OF ANY KIND, either express or implied. See the License for the
 * specific language governing permissions and limitations under the License.
 */

package healthcheck

import (
	"context"
	api "github.com/polarismesh/polaris-server/common/api/v1"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// eventInterval, trigger after instance change event
	eventInterval = 5 * time.Second
	// ensureInterval, trigger when timeout
	ensureInterval = 61 * time.Second
)

// Dispatcher dispatch all instances using consistent hash ring
type Dispatcher struct {
	healthCheckInstancesChanged uint32
	selfServiceInstancesChanged uint32
	managedInstances            map[string]*InstanceWithChecker

	selfServiceBuckets map[Bucket]bool //  这个Bucket的作用
	continuum          *Continuum
	mutex              *sync.Mutex
}

func newDispatcher(ctx context.Context) *Dispatcher {
	dispatcher := &Dispatcher{
		mutex: &sync.Mutex{},
	}
	dispatcher.startDispatchingJob(ctx)
	return dispatcher
}

// UpdateStatusByEvent 更新变更状态
func (d *Dispatcher) UpdateStatusByEvent(event CacheEvent) {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	if event.selfServiceInstancesChanged {
		atomic.StoreUint32(&d.selfServiceInstancesChanged, 1)
	}
	if event.healthCheckInstancesChanged {
		atomic.StoreUint32(&d.healthCheckInstancesChanged, 1)
	}
}

// startDispatchingJob start job to dispatch instances
func (d *Dispatcher) startDispatchingJob(ctx context.Context) {
	go func() {
		eventTicker := time.NewTicker(eventInterval)
		defer eventTicker.Stop()
		ensureTicker := time.NewTicker(ensureInterval)
		defer ensureTicker.Stop()

		for {
			select {
			case <-eventTicker.C:
				d.processEvent()
			case <-ensureTicker.C:
				d.processEnsure()
			case <-ctx.Done():
				return
			}
		}
	}()
}

const weight = 100

func compareBuckets(src map[Bucket]bool, dst map[Bucket]bool) bool {
	if len(src) != len(dst) {
		return false
	}
	if len(src) == 0 {
		return false
	}
	for bucket := range dst {
		if _, ok := src[bucket]; !ok {
			return false
		}
	}
	return true
}

func (d *Dispatcher) reloadSelfContinuum() bool {
	nextBuckets := make(map[Bucket]bool)
	server.cacheProvider.RangeSelfServiceInstances(func(instance *api.Instance) {
		if instance.GetIsolate().GetValue() || !instance.GetHealthy().GetValue() {
			return
		}
		nextBuckets[Bucket{
			Host:   instance.GetHost().GetValue(),
			Weight: weight,
		}] = true
	})
	originBucket := d.selfServiceBuckets
	log.Debugf("[Health Check][Dispatcher]reload continuum by %v, origin is %v", nextBuckets, originBucket)
	if compareBuckets(originBucket, nextBuckets) {
		return false
	}
	d.selfServiceBuckets = nextBuckets
	d.continuum = New(d.selfServiceBuckets)
	return true
}

func (d *Dispatcher) reloadManagedInstances() {
	//  nextInstances 和 originInstances  的意思
	nextInstances := make(map[string]*InstanceWithChecker)
	var totalCount int
	if nil != d.continuum {
		server.cacheProvider.RangeHealthCheckInstances(func(instance *InstanceWithChecker) {
			instanceId := instance.instance.ID()
			host := d.continuum.Hash(instance.hashValue)
			if host == server.localHost {
				nextInstances[instanceId] = instance
			}
			totalCount++
		})
	}
	log.Infof("[Health Check][Dispatcher]count %d instances has been dispatched to %s, total is %d",
		len(nextInstances), server.localHost, totalCount)
	originInstances := d.managedInstances
	d.managedInstances = nextInstances
	if len(nextInstances) > 0 {
		for id, instance := range nextInstances {
			if len(originInstances) == 0 {
				server.checkScheduler.AddInstance(instance)
				continue
			}
			if _, ok := originInstances[id]; !ok {
				server.checkScheduler.AddInstance(instance)
			}
		}
	}
	if len(originInstances) > 0 {
		for id, instance := range originInstances {
			if len(nextInstances) == 0 {
				server.checkScheduler.DelInstance(instance)
				continue
			}
			if _, ok := nextInstances[id]; !ok {
				server.checkScheduler.DelInstance(instance)
			}
		}
	}
}

func (d *Dispatcher) processEvent() {
	var selfContinuumReloaded bool
	// 标记清楚这两个分别是什么时候触发 selfServiceInstancesChanged
	if atomic.CompareAndSwapUint32(&d.selfServiceInstancesChanged, 1, 0) {
		selfContinuumReloaded = d.reloadSelfContinuum()
	}

	// 标记清楚这两个分别是什么时候触发 healthCheckInstancesChanged
	if selfContinuumReloaded || atomic.CompareAndSwapUint32(&d.healthCheckInstancesChanged, 1, 0) {
		d.reloadManagedInstances()
	}
}

func (d *Dispatcher) processEnsure() {
	d.reloadSelfContinuum()
	d.reloadManagedInstances()
}
