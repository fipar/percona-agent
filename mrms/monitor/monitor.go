/*
   Copyright (c) 2014-2015, Percona LLC and/or its affiliates. All rights reserved.

   This program is free software: you can redistribute it and/or modify
   it under the terms of the GNU Affero General Public License as published by
   the Free Software Foundation, either version 3 of the License, or
   (at your option) any later version.

   This program is distributed in the hope that it will be useful,
   but WITHOUT ANY WARRANTY; without even the implied warranty of
   MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
   GNU Affero General Public License for more details.

   You should have received a copy of the GNU Affero General Public License
   along with this program.  If not, see <http://www.gnu.org/licenses/>
*/

package monitor

import (
	"sync"
	"time"

	"github.com/percona/percona-agent/mrms"
	"github.com/percona/percona-agent/mysql"
	"github.com/percona/percona-agent/pct"
)

const (
	MONITOR_NAME = "mrms-monitor"
)

type Monitor struct {
	logger           *pct.Logger
	mysqlConnFactory mysql.ConnectionFactory
	// --
	mysqlInstances map[string]*MysqlInstance
	sync.RWMutex
	// --
	status     *pct.Status
	sync       *pct.SyncChan
	globalChan chan string
}

func NewMonitor(logger *pct.Logger, mysqlConnFactory mysql.ConnectionFactory) mrms.Monitor {
	m := &Monitor{
		logger:           logger,
		mysqlConnFactory: mysqlConnFactory,
		// --
		mysqlInstances: make(map[string]*MysqlInstance),
		// --
		status:     pct.NewStatus([]string{MONITOR_NAME}),
		sync:       pct.NewSyncChan(),
		globalChan: make(chan string, 100),
	}
	return m
}

/////////////////////////////////////////////////////////////////////////////
// Interface
/////////////////////////////////////////////////////////////////////////////

/**
 * Monitor for MySQL restart every *interval*
 */
func (m *Monitor) Start(interval time.Duration) error {
	m.logger.Debug("Start:call")
	defer m.logger.Debug("Start:return")

	go m.run(interval)
	return nil
}

func (m *Monitor) Stop() error {
	m.logger.Debug("Stop:call")
	defer m.logger.Debug("Stop:return")

	m.sync.Stop()
	m.sync.Wait()
	return nil
}

func (m *Monitor) Status() map[string]string {
	return m.status.All()
}

func (m *Monitor) Add(dsn string) (c <-chan bool, err error) {
	m.logger.Debug("Add:call:" + mysql.HideDSNPassword(dsn))
	defer m.logger.Debug("Add:return:" + mysql.HideDSNPassword(dsn))

	m.Lock()
	defer m.Unlock()

	mysqlInstance, ok := m.mysqlInstances[dsn]
	if !ok {
		mysqlInstance, err = m.createMysqlInstance(dsn)
		if err != nil {
			return nil, err
		}
		m.mysqlInstances[dsn] = mysqlInstance
	}

	c = mysqlInstance.Subscribers.Add()
	return c, nil
}

func (m *Monitor) GlobalSubscribe() (chan string, error) {
	m.logger.Debug("GlobalSubscribe:call")
	defer m.logger.Debug("GlobalSubscribe:return")

	m.Lock()
	defer m.Unlock()
	var err error

	for _, instance := range m.mysqlInstances {
		err = instance.Subscribers.GlobalAdd(m.globalChan, instance.mysqlConn.DSN())
		if err != nil {
			return nil, err
		}
	}
	return m.globalChan, nil
}

func (m *Monitor) Remove(dsn string, c <-chan bool) {
	m.logger.Debug("Remove:call:" + mysql.HideDSNPassword(dsn))
	defer m.logger.Debug("Remove:return:" + mysql.HideDSNPassword(dsn))

	m.Lock()
	defer m.Unlock()

	if mysqlInstance, ok := m.mysqlInstances[dsn]; ok {
		mysqlInstance.Subscribers.Remove(c)
		if mysqlInstance.Subscribers.Empty() {
			delete(m.mysqlInstances, dsn)
		}
		mysqlInstance.Subscribers.GlobalRemove(dsn)
	}
}

func (m *Monitor) Check() {
	m.logger.Debug("Check:call")
	defer m.logger.Debug("Check:return")

	m.RLock()
	defer m.RUnlock()

	for _, mysqlInstance := range m.mysqlInstances {
		wasRestarted, err := mysqlInstance.CheckIfMysqlRestarted()
		if err != nil {
			m.logger.Error(err)
			continue
		}
		if wasRestarted {
			m.logger.Debug("Check:restarted:" + mysql.HideDSNPassword(mysqlInstance.DSN()))
			mysqlInstance.Subscribers.Notify()
		}
	}
}

/////////////////////////////////////////////////////////////////////////////
// Implementation
/////////////////////////////////////////////////////////////////////////////

func (m *Monitor) run(interval time.Duration) {
	m.logger.Debug("run:call")
	defer m.logger.Debug("run:return")

	defer func() {
		if err := recover(); err != nil {
			m.logger.Error("MySQL Restart Monitor Service (MRMS) crashsed: ", err)
		}
		m.status.Update(MONITOR_NAME, "Stopped")
		m.sync.Done()
	}()

	for {
		// Immediately run first check...
		m.status.Update(MONITOR_NAME, "Checking")
		m.Check()

		// ...and after that idle for *interval* until next check,
		// or until monitor is stopped
		m.status.Update(MONITOR_NAME, "Idle")
		select {
		case <-time.After(interval):
		case <-m.sync.StopChan:
			return
		}
	}
}

func (m *Monitor) createMysqlInstance(dsn string) (mi *MysqlInstance, err error) {
	m.logger.Debug("createMysqlInstance:call:" + mysql.HideDSNPassword(dsn))
	defer m.logger.Debug("createMysqlInstance:return:" + mysql.HideDSNPassword(dsn))

	mysqlConn := m.mysqlConnFactory.Make(dsn)
	// todo: fix
	logger := pct.NewLogger(m.logger.LogChan(), "mrms-monitor-mysql")
	subscribers := NewSubscribers(logger)
	return NewMysqlInstance(logger, mysqlConn, subscribers)
}
