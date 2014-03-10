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

package pct

import (
	"math"
	"math/rand"
	"time"
)

type Backoff struct {
	try         int
	lastSuccess time.Time
	resetAfter  time.Duration
	NowFunc     func() time.Time
}

func NewBackoff(resetAfter time.Duration) *Backoff {
	b := &Backoff{
		resetAfter: resetAfter,
		NowFunc:    time.Now,
	}
	return b
}

func (b *Backoff) Wait() time.Duration {
	var t int
	if b.try == 0 {
		t = 0
		b.try++
	} else if b.try < 7 {
		// 1s, 3s, 7s, 15s, 31s, 1m3s = 2m
		t = int(math.Pow(2, float64(b.try)) - 1)
		b.try++
	} else {
		// [1m30s, 3m)
		t = int(90 + (90 * rand.Float64()))
	}
	return time.Duration(t) * time.Second
}

func (b *Backoff) Success() {
	if b.lastSuccess.IsZero() {
		// First success, don't reset backoff yet because if the remote end
		// is flapping, there maybe be other tries real soon, so we want the
		// backoff wait to take effect.
		b.lastSuccess = time.Now()
	} else if b.lastSuccess.Sub(b.NowFunc()) > b.resetAfter {
		// If it's been > 5m since the last success and this success,
		// then the remote end was flapping at least stopped for 5 minutes,
		// so we reset the backoff.
		b.lastSuccess = time.Now()
		b.try = 0
	}
}