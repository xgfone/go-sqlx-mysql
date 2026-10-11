// Copyright 2026 xgfone
// SPDX-License-Identifier: Apache-2.0

package mysql

import (
	"errors"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/xgfone/go-sqlx/dialect"
)

// Capture the system location before application startup replaces time.Local.
var systemLocation = time.Local

// Importing this package registers go-sql-driver/mysql's database/sql driver
// and its connection configurer for the built-in MySQL dialect and driver "mysql".
func init() {
	if err := Register("mysql"); err != nil {
		panic(err)
	}
}

// Register adds this configurer for another driver using the same native DSN and
// per-connection loc/time_zone semantics as go-sql-driver/mysql. It does not
// register the SQL driver itself. Duplicate registrations fail.
func Register(driver string) error {
	return dialect.RegisterConnectionConfigurer("mysql", driver, ConfigureConnection)
}

// ConfigureConnection handles OptionLocation and ignores all other options.
// A nil location leaves the DSN unchanged; no process-wide default is assumed.
// A supplied UTC or named IANA location overrides the native loc and time_zone.
// The initial time.Local pointer sets loc=Local and preserves time_zone settings,
// regardless of its name or later assignments to time.Local.
func ConfigureConnection(request dialect.ConnectionRequest) (dialect.ConnectionResult, error) {
	result := dialect.ConnectionResult{DSN: request.DSN}
	value, specified := request.Options[dialect.OptionLocation]
	if !specified || value == nil {
		return result, nil
	}

	loc, ok := value.(*time.Location)
	if !ok {
		return result, errors.New("location option must be a *time.Location")
	}

	if loc == nil {
		return result, nil
	}

	dsn, err := mysqlTimezoneDSN(request.DSN, loc)
	if err != nil {
		return result, err
	}

	return dialect.ConnectionResult{
		DSN: dsn,
		Handled: []string{
			dialect.OptionLocation,
		},
	}, nil
}

func mysqlTimezoneDSN(dsn string, loc *time.Location) (newdsn string, err error) {
	isLocal := loc == systemLocation
	name := loc.String()
	if name == "" {
		return "", errors.New("time zone must have a name: the initial time.Local, UTC, or an explicit IANA name such as Asia/Shanghai")
	}

	// The location name is the driver's DSN representation. Let Go resolve it
	// instead of examining system files or comparing time zone histories.
	resolved := loc
	if !isLocal {
		if name == "Local" {
			return "", errors.New("time zone named Local must use the initial time.Local")
		}

		if resolved, err = time.LoadLocation(name); err != nil {
			return "", errors.New("time zone must be the initial time.Local, UTC, or a loadable IANA name such as Asia/Shanghai")
		}
	}

	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return "", errors.New("invalid native MySQL DSN")
	}

	if isLocal {
		// This location is only a FormatDSN token, never a driver time zone.
		// Force loc=Local even if the captured system location has an IANA name.
		// The driver resolves the alias to time.Local when parsing the DSN.
		cfg.Loc = time.FixedZone("Local", 0)
		return cfg.FormatDSN(), nil
	}

	cfg.Loc = resolved
	if cfg.Params == nil {
		cfg.Params = make(map[string]string, 1)
	}

	// MySQL system variable names are case-insensitive; replace case variants
	// too so an old TIME_ZONE cannot compete with the application setting.
	for key := range cfg.Params {
		if strings.EqualFold(key, "time_zone") {
			delete(cfg.Params, key)
		}
	}

	zone := name
	if resolved == time.UTC {
		zone = "+00:00"
	}

	cfg.Params["time_zone"] = "'" + zone + "'"
	return cfg.FormatDSN(), nil
}
