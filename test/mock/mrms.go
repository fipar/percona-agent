/*
   Copyright (c) 2014, Percona LLC and/or its affiliates. All rights reserved.

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

package mock

import (
	"github.com/percona/cloud-protocol/proto"
)

type MRMS struct {
}

func NewMRMS() *MRMS {
	m := &MRMS{}
	return m
}

func (m *MRMS) Add(dsn string) (c chan bool, err error) {
	c = make(chan bool, 10)
	return c, nil
}

func (m *MRMS) Remove(dsn string, c chan bool) {
}

func (m *MRMS) Run() {
}

func (m *MRMS) Start() error {
	return nil
}

func (m *MRMS) Stop() error {
	return nil
}

func (m *MRMS) GetConfig() ([]proto.AgentConfig, []error) {
	return nil, nil
}

func (m *MRMS) Handle(cmd *proto.Cmd) *proto.Reply {
	return cmd.Reply(nil)
}

func (m *MRMS) Status() map[string]string {
	return nil
}
