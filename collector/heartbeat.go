// Copyright 2018 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Scrape heartbeat data.

package collector

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// heartbeat is the Metric subsystem we use.
	heartbeat = "heartbeat"
	// heartbeatQuery is the query used to fetch the stored and current
	// timestamps. %s will be replaced by the database and table name.
	// The second column allows gets the server timestamp at the exact same
	// time the query is run.
	heartbeatQuery = "SELECT UNIX_TIMESTAMP(ts), UNIX_TIMESTAMP(%s), server_id from `%s`.`%s`"
)

// Arg definitions.
var (
	heartbeatDatabase = "database"
	heartbeatTable    = "table"
	heartbeatUtc      = "utc"

	heartbeatArgDefs = []*argDef{
		{
			name:         heartbeatDatabase,
			help:         "Database from where to collect heartbeat data",
			defaultValue: "heartbeat",
		},
		{
			name:         heartbeatTable,
			help:         "Database from where to collect heartbeat data",
			defaultValue: "heartbeat",
		},
		{
			name:         heartbeatUtc,
			help:         "Use UTC for timestamps of the current server (`pt-heartbeat` is called with `--utc`)",
			defaultValue: false,
		},
	}
)

// Metric descriptors.
var (
	HeartbeatStoredDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, heartbeat, "stored_timestamp_seconds"),
		"Timestamp stored in the heartbeat table.",
		[]string{"server_id"}, nil,
	)
	HeartbeatNowDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, heartbeat, "now_timestamp_seconds"),
		"Timestamp of the current server.",
		[]string{"server_id"}, nil,
	)
)

// ScrapeHeartbeat scrapes from the heartbeat table.
// This is mainly targeting pt-heartbeat, but will work with any heartbeat
// implementation that writes to a table with two columns:
// CREATE TABLE heartbeat (
//
//	ts                    varchar(26) NOT NULL,
//	server_id             int unsigned NOT NULL PRIMARY KEY,
//
// );
type ScrapeHeartbeat struct {
	sync.RWMutex

	database string
	enabled  atomic.Bool
	table    string
	utc      bool
}

// Name of the Scraper. Should be unique.
func (*ScrapeHeartbeat) Name() string {
	return "heartbeat"
}

// Help describes the role of the Scraper.
func (*ScrapeHeartbeat) Help() string {
	return "Collect from heartbeat"
}

// Version of MySQL from which scraper is available.
func (*ScrapeHeartbeat) Version() float64 {
	return 5.1
}

// Enabled describes if the Scraper is currently enabled.
func (s *ScrapeHeartbeat) Enabled() bool {
	return s.enabled.Load()
}

// SetEnabled enables or disables the Scraper.
func (s *ScrapeHeartbeat) SetEnabled(enabled bool) {
	s.enabled.Store(enabled)
}

// ArgDefs describes the args the Scraper accepts.
func (s *ScrapeHeartbeat) Args() []Arg {
	s.RLock()
	defer s.RUnlock()
	return []Arg{
		&arg{
			name:  heartbeatDatabase,
			value: s.database,
		},
		&arg{
			name:  heartbeatTable,
			value: s.table,
		},
		&arg{
			name:  heartbeatUtc,
			value: s.utc,
		},
	}
}

// Configure modifies the runtime behavior of the scraper via accepted args.
func (s *ScrapeHeartbeat) Configure(args ...Arg) error {
	s.Lock()
	defer s.Unlock()
	for _, arg := range args {
		switch arg.Name() {
		case heartbeatDatabase:
			database, ok := arg.Value().(string)
			if !ok {
				return wrongArgTypeError(s.Name(), arg.Name(), arg.Value())
			}
			s.database = database
		case heartbeatTable:
			table, ok := arg.Value().(string)
			if !ok {
				return wrongArgTypeError(s.Name(), arg.Name(), arg.Value())
			}
			s.table = table
		case heartbeatUtc:
			utc, ok := arg.Value().(bool)
			if !ok {
				return wrongArgTypeError(s.Name(), arg.Name(), arg.Value())
			}
			s.utc = utc
		default:
			return unknownArgError(s.Name(), arg.Name())
		}
	}
	return nil
}

// nowExpr returns a current timestamp expression.
func nowExpr(utc bool) string {
	if utc {
		return "UTC_TIMESTAMP(6)"
	}
	return "NOW(6)"
}

// Scrape collects data from database connection and sends it over channel as prometheus metric.
func (s *ScrapeHeartbeat) Scrape(ctx context.Context, db *sql.DB, ch chan<- prometheus.Metric, logger log.Logger) error {
	s.RLock()
	defer s.RUnlock()

	query := fmt.Sprintf(heartbeatQuery, nowExpr(s.utc), s.database, s.table)
	heartbeatRows, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer heartbeatRows.Close()

	var (
		now, ts  sql.RawBytes
		serverId int
	)

	for heartbeatRows.Next() {
		if err := heartbeatRows.Scan(&ts, &now, &serverId); err != nil {
			return err
		}

		tsFloatVal, err := strconv.ParseFloat(string(ts), 64)
		if err != nil {
			return err
		}

		nowFloatVal, err := strconv.ParseFloat(string(now), 64)
		if err != nil {
			return err
		}

		serverId := strconv.Itoa(serverId)

		ch <- prometheus.MustNewConstMetric(
			HeartbeatNowDesc,
			prometheus.GaugeValue,
			nowFloatVal,
			serverId,
		)
		ch <- prometheus.MustNewConstMetric(
			HeartbeatStoredDesc,
			prometheus.GaugeValue,
			tsFloatVal,
			serverId,
		)
	}

	return nil
}

// check interface
var _ Scraper = &ScrapeHeartbeat{}

func init() {
	onRegistryInit(func(registerScraper registerScraperFn) {
		registerScraper(&ScrapeHeartbeat{}, heartbeatArgDefs...)
	})
}
