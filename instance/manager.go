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

package instance

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/percona/percona-agent/agent"

	"github.com/percona/cloud-protocol/proto"
	"github.com/percona/percona-agent/mrms"
	"github.com/percona/percona-agent/mysql"
	"github.com/percona/percona-agent/pct"
)

type empty struct{}

type Manager struct {
	logger    *pct.Logger
	configDir string
	api       pct.APIConnector
	// --
	status         *pct.Status
	repo           *Repo
	stopChan       chan empty
	mrm            mrms.Monitor
	mrmChans       map[string]<-chan bool
	mrmsGlobalChan chan string
	agentConfig    *agent.Config
}

func NewManager(logger *pct.Logger, configDir string, api pct.APIConnector, mrm mrms.Monitor) *Manager {
	repo := NewRepo(pct.NewLogger(logger.LogChan(), "instance-repo"), configDir, api)
	m := &Manager{
		logger:    logger,
		configDir: configDir,
		api:       api,
		// --
		status:         pct.NewStatus([]string{"instance", "instance-repo", "instance-mrms"}),
		repo:           repo,
		mrm:            mrm,
		mrmChans:       make(map[string]<-chan bool),
		mrmsGlobalChan: make(chan string, 100), // monitor up to 100 instances
	}
	return m
}

/////////////////////////////////////////////////////////////////////////////
// Interface
/////////////////////////////////////////////////////////////////////////////

// @goroutine[0]
func (m *Manager) Start() error {
	m.status.Update("instance", "Starting")
	if err := m.repo.Init(); err != nil {
		return err
	}
	m.logger.Info("Started")
	m.status.Update("instance", "Running")

	mrmsGlobalChan, err := m.mrm.GlobalSubscribe()
	if err != nil {
		return err
	}

	for _, instance := range m.GetMySQLInstances() {
		ch, err := m.mrm.Add(instance.Properties["dsn"])
		if err != nil {
			m.logger.Error("Cannot add instance to the monitor:", err)
			continue
		}
		safeDSN := mysql.HideDSNPassword(instance.Properties["dsn"])
		m.status.Update("instance", "Getting info "+safeDSN)
		if err := GetMySQLInfo(instance); err != nil {
			m.logger.Warn(fmt.Sprintf("Failed to get MySQL info %s: %s", safeDSN, err))
			continue
		}
		m.status.Update("instance", "Updating info "+safeDSN)
		m.pushInstanceInfo(instance)
		// Store the channel to be able to remove it from mrms
		m.mrmChans[instance.Properties["dsn"]] = ch
	}
	go m.monitorInstancesRestart(mrmsGlobalChan)
	return nil
}

// @goroutine[0]
func (m *Manager) Stop() error {
	// Can't stop the instance manager.
	return nil
}

/////////////////////////////////////////////////////////////////////////////
// Implementation
/////////////////////////////////////////////////////////////////////////////

// Adds a MySQL instance to MRM
func (m *Manager) mrmMySQL(inst proto.Instance) error {
	itDSN, ok := inst.Properties["dns"]
	if !ok {
		return errors.New("Missing DSN in MySQL instance " + inst.UUID)
	}
	ch, err := m.mrm.Add(itDSN)
	if err != nil {
		return err
	}
	m.mrmChans[itDSN] = ch

	safeDSN := mysql.HideDSNPassword(itDSN)
	m.status.Update("instance", "Getting info "+safeDSN)
	if err := GetMySQLInfo(inst); err != nil {
		m.logger.Warn(fmt.Sprintf("Failed to get MySQL info %s: %s", safeDSN, err))
	}
	m.status.Update("instance", "Updating info "+safeDSN)
	err = m.pushInstanceInfo(inst)
	if err != nil {
		return err
	}
	return nil
}

// Auxiliary function to add MySQL instances on MRM
func (m *Manager) mrmAddedMySQL(added []proto.Instance) {
	// Process added instances
	for _, addIt := range added {
		if err := m.mrmMySQL(addIt); err != nil {
			m.logger.Error(err)
		}
	}
}

// Auxiliary function to remove deleted MySQL instances from MRM
func (m *Manager) mrmDeletedMySQL(deleted []proto.Instance) {
	for _, dltIt := range deleted {
		itDSN, ok := dltIt.Properties["dns"]
		if !ok {
			err := errors.New("Missing DSN in deleted MySQL instance " + dltIt.UUID)
			m.logger.Error(err)
			continue
		}
		m.mrm.Remove(itDSN, m.mrmChans[itDSN])
	}
}

// Auxiliary function to update MySQL instances on MRM
func (m *Manager) mrmUpdatedMySQL(updated []proto.Instance) {
	// For now updates means deleting and then adding DSN to MRM
	m.mrmDeletedMySQL(updated)
	m.mrmAddedMySQL(updated)
}

// @goroutine[0]
func (m *Manager) Handle(cmd *proto.Cmd) *proto.Reply {
	m.status.UpdateRe("instance", "Handling", cmd)
	defer m.status.Update("instance", "Running")

	var it *proto.Instance = nil
	if err := json.Unmarshal(cmd.Data, &it); err != nil {
		return cmd.Reply(nil, err)
	}

	switch cmd.Cmd {
	case "Update":
		added := make([]proto.Instance, 0)
		deleted := make([]proto.Instance, 0)
		updated := make([]proto.Instance, 0)
		err := m.repo.UpdateTree(*it, &added, &deleted, &updated, true) // true = write to disk
		if err != nil {
			return cmd.Reply(nil, err)
		}
		// For the following block we only care about MySQL instances
		// From former code logic, we don't actually care about if there was an error
		// while updating MRM. TODO: really?
		m.mrmAddedMySQL(onlyMySQLInsts(added))
		m.mrmDeletedMySQL(onlyMySQLInsts(deleted))
		m.mrmUpdatedMySQL(onlyMySQLInsts(updated))
		return cmd.Reply(nil, nil)
	case "GetInfo":
		err := m.handleGetInfo(*it)
		return cmd.Reply(it, err)
	default:
		return cmd.Reply(nil, pct.UnknownCmdError{Cmd: cmd.Cmd})
	}
}

func (m *Manager) Status() map[string]string {
	list := m.repo.List()
	uuids := make([]string, len(list))
	for _, it := range list {
		uuids = append(uuids, it.UUID)
	}
	m.status.Update("instance-repo", strings.Join(uuids, " "))
	return m.status.All()
}

func (m *Manager) GetConfig() ([]proto.AgentConfig, []error) {
	return nil, nil
}

func (m *Manager) Repo() *Repo {
	return m.repo
}

func (m *Manager) handleGetInfo(it proto.Instance) error {
	if !isMySQLInst(it) {
		return fmt.Errorf("Don't know how to get info for %s instance", it.UUID)
	}
	return GetMySQLInfo(it)
}

func GetMySQLInfo(it proto.Instance) error {
	conn := mysql.NewConnection(it.Properties["dsn"])
	if err := conn.Connect(1); err != nil {
		return err
	}
	defer conn.Close()
	sql := "SELECT /* percona-agent */" +
		" CONCAT_WS('.', @@hostname, IF(@@port='3306',NULL,@@port)) AS Hostname," +
		" @@version_comment AS Distro," +
		" @@version AS Version"
	// Need auxiliary vars because can't get map attribute addresses
	var hostname, distro, version string
	err := conn.DB().QueryRow(sql).Scan(
		&hostname,
		&distro,
		&version,
	)
	if err != nil {
		return err
	}
	it.Properties["hostname"] = hostname
	it.Properties["distro"] = distro
	it.Properties["version"] = version
	return nil
}

func (m *Manager) GetMySQLInstances() []proto.Instance {
	m.logger.Debug("GetMySQLInstances:call")
	defer m.logger.Debug("GetMySQLInstances:return")
	return m.Repo().GetMySQLInstances()
}

func (m *Manager) monitorInstancesRestart(ch chan string) {
	m.logger.Debug("monitorInstancesRestart:call")
	defer func() {
		if err := recover(); err != nil {
			m.logger.Error("MySQL connection crashed: ", err)
			m.status.Update("instance-mrms", "Crashed")
		} else {
			m.status.Update("instance-mrms", "Stopped")
		}
		m.logger.Debug("monitorInstancesRestart:return")
	}()

	ch, err := m.mrm.GlobalSubscribe()
	if err != nil {
		m.logger.Error(fmt.Sprintf("Failed to get MySQL restart monitor global channel: %s", err))
		return
	}

	for {
		m.status.Update("instance-mrms", "Idle")
		select {
		case dsn := <-ch:
			safeDSN := mysql.HideDSNPassword(dsn)
			m.logger.Debug("mrms:restart:" + safeDSN)
			m.status.Update("instance-mrms", "Updating "+safeDSN)

			// Get the updated instances list. It should be updated every time since
			// the Add method can add new instances to the list.
			for _, instance := range m.GetMySQLInstances() {
				if instance.Properties["dsn"] != dsn {
					continue
				}
				m.status.Update("instance-mrms", "Getting info "+safeDSN)
				if err := GetMySQLInfo(instance); err != nil {
					m.logger.Warn(fmt.Sprintf("Failed to get MySQL info %s: %s", safeDSN, err))
					break
				}
				m.status.Update("instance-mrms", "Updating info "+safeDSN)
				err := m.pushInstanceInfo(instance)
				if err != nil {
					m.logger.Warn(err)
				}
				break
			}
		}
	}
}

func (m *Manager) pushInstanceInfo(inst proto.Instance) error {
	// Subsystems will be ignored, don't send them
	inst.Subsystems = make([]proto.Instance, 0)
	uri := fmt.Sprintf("%s/%s", m.api.EntryLink("insts"), inst.UUID)
	data, err := json.Marshal(&inst)
	if err != nil {
		m.logger.Error(err)
		return err
	}
	resp, body, err := m.api.Put(m.api.ApiKey(), uri, data)
	if err != nil {
		return err
	}
	// Sometimes the API returns only a status code for an error, without a message
	// so body = nil and in that case string(body) will fail.
	if body == nil {
		body = []byte{}
	}
	if resp != nil && resp.StatusCode != 200 {
		return fmt.Errorf("Failed to PUT: %d, %s", resp.StatusCode, string(body))
	}
	return nil
}
