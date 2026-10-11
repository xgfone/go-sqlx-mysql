// Copyright 2026 xgfone
// SPDX-License-Identifier: Apache-2.0

package mysql

import (
	"context"
	"database/sql"
	"maps"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/xgfone/go-sqlx"
	"github.com/xgfone/go-sqlx/dialect"
)

func mustTimezone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestMySQLTimezoneDSN(t *testing.T) {
	for _, tc := range []struct{ name, session string }{
		{"UTC", "'+00:00'"},
		{"Etc/UTC", "'Etc/UTC'"},
		{"Asia/Shanghai", "'Asia/Shanghai'"},
		{"America/New_York", "'America/New_York'"},
		{"Etc/GMT-8", "'Etc/GMT-8'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loc := mustTimezone(t, tc.name)
			for _, parseTime := range []bool{false, true} {
				original := ("user:pa?ss/w://ord@unix(/tmp/mysql.sock)/billing" +
					"%2Ftest?charset=utf8mb4,latin1&timeout=3s&sql_mode=" +
					url.QueryEscape("'STRICT_ALL_TABLES'"))
				if parseTime {
					original += "&parseTime=true"
				}

				dsn, err := prepareMySQLTimezoneDSN(original, loc)
				if err != nil {
					t.Fatal(err)
				}

				cfg, err := mysql.ParseDSN(dsn)
				if err != nil {
					t.Fatal(err)
				}

				if cfg.User != "user" ||
					cfg.Net != "unix" ||
					cfg.Addr != "/tmp/mysql.sock" ||
					cfg.Passwd != "pa?ss/w://ord" ||
					cfg.DBName != "billing/test" ||
					cfg.ParseTime != parseTime ||
					cfg.Timeout != 3*time.Second ||
					!strings.Contains(dsn, "charset=utf8mb4,latin1") ||
					cfg.Params["sql_mode"] != "'STRICT_ALL_TABLES'" ||
					cfg.Params["time_zone"] != tc.session ||
					cfg.Loc.String() != loc.String() {
					t.Fatalf("settings were not preserved/prepared: %q", dsn)
				}

				// Preparing an already prepared DSN must not change it.
				again, err := prepareMySQLTimezoneDSN(dsn, loc)
				if err != nil || again != dsn {
					t.Fatalf("non-idempotent preparation: %q, %v", again, err)
				}
			}
		})
	}
}

func TestMySQLApplicationTimezoneOverridesDSN(t *testing.T) {
	for _, tc := range []struct{ app, originalLoc, originalZone, session string }{
		{"UTC", "Asia/Shanghai", "'+08:00'", "'+00:00'"},
		{"Asia/Shanghai", "UTC", "'+00:00'", "'Asia/Shanghai'"},
		{"America/New_York", "Asia/Hong_Kong", "'-05:00'", "'America/New_York'"},
		{"UTC", "Local", "'SYSTEM'", "'+00:00'"},
		{"UTC", "Etc/UTC", "CONCAT('+08:00')", "'+00:00'"},
	} {
		t.Run(tc.app+"/"+tc.originalZone, func(t *testing.T) {
			dsn := "user:secret@tcp(host)/db?loc=" + url.QueryEscape(tc.originalLoc) +
				"&time_zone=" + url.QueryEscape(tc.originalZone) + "&autocommit=1"
			prepared, err := prepareMySQLTimezoneDSN(dsn, mustTimezone(t, tc.app))
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := mysql.ParseDSN(prepared)
			if err != nil || cfg.Loc.String() != tc.app ||
				cfg.Params["time_zone"] != tc.session ||
				cfg.Params["autocommit"] != "1" {
				t.Fatalf("application time zone did not override DSN: %v", err)
			}
			if tc.app == "UTC" && strings.Contains(prepared, "loc=") {
				t.Fatal("UTC should use the official formatter's implicit default")
			}
		})
	}

	// Let the driver parse duplicates; replace the resulting case variants.
	const dsn = "/db?loc=Asia%2FShanghai&loc=UTC&time_zone='UTC'&TIME_ZONE='SYSTEM'"
	prepared, err := prepareMySQLTimezoneDSN(dsn, time.UTC)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := mysql.ParseDSN(prepared)
	if err != nil || len(cfg.Params) != 1 || cfg.Params["time_zone"] != "'+00:00'" {
		t.Fatalf("old time_zone settings survived: %v", err)
	}
}

func TestMySQLUnsupportedApplicationTimezone(t *testing.T) {
	for _, loc := range []*time.Location{
		new(time.Location),
		time.FixedZone("+08:00", 8*3600),
		time.FixedZone("", 8*3600),
		time.FixedZone("Unknown/Timezone", 8*3600),
	} {
		t.Run(loc.String(), func(t *testing.T) {
			result, err := ConfigureConnection(dialect.ConnectionRequest{
				Driver: "mysql", DSN: "/db",
				Options: dialect.ConnectionOptions{dialect.OptionLocation: loc},
			})
			if err == nil || len(result.Handled) != 0 || result.DSN != "/db" ||
				!strings.Contains(err.Error(), "IANA") {
				t.Fatalf("unsupported location was handled: %+v, %v", result, err)
			}
		})
	}
}

func TestMySQLLocalTimezonePreservesSessionSettings(t *testing.T) {
	previous := time.Local
	t.Cleanup(func() { time.Local = previous })
	local := systemLocation
	time.Local = local

	for _, original := range []string{
		"/db?parseTime=true&autocommit=1",
		"/db?loc=UTC&time_zone=%27%2B00%3A00%27&parseTime=true",
		"/db?loc=Asia%2FShanghai&time_zone=%27SYSTEM%27&sql_mode=%27STRICT_ALL_TABLES%27",
		"/db?time_zone=%27UTC%27&TIME_ZONE=%27SYSTEM%27",
	} {
		t.Run(original, func(t *testing.T) {
			before, err := mysql.ParseDSN(original)
			if err != nil {
				t.Fatal(err)
			}

			result, err := ConfigureConnection(dialect.ConnectionRequest{
				Driver: "mysql", DSN: original,
				Options: dialect.ConnectionOptions{dialect.OptionLocation: local},
			})

			if err != nil || len(result.Handled) != 1 ||
				result.Handled[0] != dialect.OptionLocation {
				t.Fatalf("Local was not handled: %+v, %v", result, err)
			}

			cfg, err := mysql.ParseDSN(result.DSN)
			if err != nil || cfg.Loc != local ||
				cfg.ParseTime != before.ParseTime ||
				!maps.Equal(cfg.Params, before.Params) ||
				!strings.Contains(result.DSN, "loc=Local") {
				t.Fatalf("Local changed native session/driver settings: %q, %v",
					result.DSN, err)
			}

			again, err := prepareMySQLTimezoneDSN(result.DSN, local)
			if err != nil || again != result.DSN {
				t.Fatalf("non-idempotent Local preparation: %q, %v", again, err)
			}
		})
	}
}

func TestMySQLLocalDSNResolvesCurrentProcessLocation(t *testing.T) {
	previous := time.Local
	t.Cleanup(func() { time.Local = previous })
	// Capturing a location pointer does not freeze the driver's loc=Local alias.
	initialLocal := systemLocation
	for _, name := range []string{"UTC", "Asia/Shanghai"} {
		t.Run(name, func(t *testing.T) {
			time.Local = mustTimezone(t, name)
			prepared, err := prepareMySQLTimezoneDSN("/db?time_zone=%27SYSTEM%27",
				initialLocal)
			if err != nil {
				t.Fatal(err)
			}

			cfg, err := mysql.ParseDSN(prepared)
			if err != nil || cfg.Loc != time.Local ||
				!strings.Contains(prepared, "loc=Local") ||
				cfg.Params["time_zone"] != "'SYSTEM'" {
				t.Fatalf("Local did not retain its native alias semantics: %q, %v",
					prepared, err)
			}
		})
	}
}

func TestMySQLInitialLocalNameDoesNotSelectSessionPolicy(t *testing.T) {
	previousLocal, previousSystem := time.Local, systemLocation
	t.Cleanup(func() { time.Local, systemLocation = previousLocal, previousSystem })
	for _, tc := range []struct {
		name    string
		offset  int
		session string
	}{
		{"UTC", 0, "'+00:00'"},
		{"Asia/Shanghai", 8 * 3600, "'Asia/Shanghai'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Model the initial system location having a UTC or IANA name.
			systemLocation = time.FixedZone(tc.name, tc.offset)
			time.Local = time.UTC
			const original = "/db?time_zone=%27SYSTEM%27"
			prepared, err := prepareMySQLTimezoneDSN(original, systemLocation)
			if err != nil {
				t.Fatal(err)
			}

			cfg, err := mysql.ParseDSN(prepared)
			if err != nil || !strings.Contains(prepared, "loc=Local") ||
				cfg.Loc != time.Local || cfg.Params["time_zone"] != "'SYSTEM'" {
				t.Fatalf("initial Local name changed session policy: %q, %v", prepared, err)
			}

			// Another pointer with the same name must synchronize both settings.
			explicit := mustTimezone(t, tc.name)
			prepared, err = prepareMySQLTimezoneDSN(original, explicit)
			if err != nil {
				t.Fatal(err)
			}

			cfg, err = mysql.ParseDSN(prepared)
			if err != nil || cfg.Loc.String() != tc.name ||
				cfg.Params["time_zone"] != tc.session {
				t.Fatalf("explicit same-name location was treated as Local: %q, %v", prepared, err)
			}
		})
	}
}

func TestMySQLReassignedLocalNameIsNotSystemLocal(t *testing.T) {
	previous := time.Local
	t.Cleanup(func() { time.Local = previous })
	time.Local = time.FixedZone("Local", 8*3600)
	result, err := ConfigureConnection(dialect.ConnectionRequest{
		Driver: "mysql", DSN: "/db",
		Options: dialect.ConnectionOptions{dialect.OptionLocation: time.Local},
	})
	if err == nil || len(result.Handled) != 0 || result.DSN != "/db" ||
		!strings.Contains(err.Error(), "initial time.Local") {
		t.Fatalf("a replacement named Local was accepted: %+v, %v", result, err)
	}
}

func TestMySQLConfiguredProcessLocation(t *testing.T) {
	previous := time.Local
	t.Cleanup(func() { time.Local = previous })
	// Billing sets time.Local to an explicitly selected location at startup.
	time.Local = mustTimezone(t, "Asia/Shanghai")
	prepared, err := prepareMySQLTimezoneDSN("/db?loc=UTC", time.Local)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := mysql.ParseDSN(prepared)
	if err != nil || cfg.Loc.String() != "Asia/Shanghai" ||
		cfg.Params["time_zone"] != "'Asia/Shanghai'" {
		t.Fatalf("configured process location was not applied: %v", err)
	}
}

func TestMySQLInvalidNativeDSN(t *testing.T) {
	for _, dsn := range []string{
		"user:secret@tcp(host)",
		"user:secret@tcp(host)/db?parseTime=secret",
		"user:secret@tcp(host)/db?loc=Unknown%2FTimezone",
		"user:secret@tcp(host)/db?loc=%zz",
	} {
		result, err := ConfigureConnection(dialect.ConnectionRequest{
			Driver: "mysql", DSN: dsn,
			Options: dialect.ConnectionOptions{dialect.OptionLocation: time.UTC},
		})
		if err == nil || strings.Contains(err.Error(), "secret") ||
			len(result.Handled) != 0 || result.DSN != dsn {
			t.Fatalf("invalid DSN was accepted or exposed: %v", err)
		}
	}
}

// Opt in with a test server; no schema is modified. Multiple physical
// connections, including replacements, must receive the same session setting.
func TestMySQLTimezoneConnections(t *testing.T) {
	dsn := os.Getenv("SQLDB_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("SQLDB_TEST_MYSQL_DSN is not set")
	}

	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid test DSN")
	}

	cfg.Loc = mustTimezone(t, "Asia/Shanghai")
	if cfg.Params == nil {
		cfg.Params = make(map[string]string)
	}
	cfg.Params["time_zone"] = "'+08:00'"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	db, err := sqlx.OpenWithOptions("mysql", cfg.FormatDSN(), sqlx.OpenOptions{
		Connection: dialect.ConnectionOptions{dialect.OptionLocation: time.UTC},
		Required:   []string{dialect.OptionLocation},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck

	pool, ok := sqlx.AsExecutor[*sql.DB](db.Executor)
	if !ok {
		t.Fatal("missing sql.DB")
	}
	if err := pool.PingContext(ctx); err != nil {
		t.Fatal(err)
	}

	pool.SetMaxIdleConns(0)
	var connections []*sql.Conn
	for range 2 {
		conn, err := pool.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}

		connections = append(connections, conn)
		t.Cleanup(func() { _ = conn.Close() })
		var zone string
		err = conn.QueryRowContext(ctx, "SELECT @@session.time_zone").Scan(&zone)
		if err != nil {
			t.Fatal(err)
		}
		if zone != "+00:00" {
			t.Fatalf("session time_zone = %q", zone)
		}
	}

	// Release both connections, then force another physical connection.
	// MaxIdleConns=0 ensures Close actually drops them.
	for _, conn := range connections {
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}

	var zone string
	err = pool.QueryRowContext(ctx, "SELECT @@session.time_zone").Scan(&zone)
	if err != nil || zone != "+00:00" {
		t.Fatalf("replacement connection: %q, %v", zone, err)
	}
}

func TestConnectionOptionsContract(t *testing.T) {
	for _, options := range []dialect.ConnectionOptions{
		nil,
		{},
		{"future": 42},
		{dialect.OptionLocation: nil},
		{dialect.OptionLocation: (*time.Location)(nil)},
	} {
		result, err := ConfigureConnection(dialect.ConnectionRequest{
			Driver:  "mysql",
			DSN:     "opaque",
			Options: options,
		})
		if err != nil || result.DSN != "opaque" || len(result.Handled) != 0 {
			t.Fatalf("unspecified/unsupported options changed DSN: %+v, %v", result, err)
		}
	}

	_, err := ConfigureConnection(dialect.ConnectionRequest{
		Driver:  "mysql",
		DSN:     "/db",
		Options: dialect.ConnectionOptions{dialect.OptionLocation: "UTC"},
	})
	if err == nil {
		t.Fatal("invalid location type accepted")
	}

	options := dialect.ConnectionOptions{
		dialect.OptionLocation: time.UTC,
		"future":               42,
	}
	result, err := ConfigureConnection(dialect.ConnectionRequest{
		Driver:  "mysql",
		DSN:     "/db",
		Options: options,
	})
	if err != nil || len(result.Handled) != 1 || len(options) != 2 ||
		result.Handled[0] != dialect.OptionLocation {
		t.Fatalf("location was not handled independently: %+v, %v", result, err)
	}

	// Registration uses the actual driver, so sharing MySQL's SQL dialect does
	// not apply this adapter to an unrelated native DSN format.
	untouched, err := dialect.MySQL.ConfigureConnection(dialect.ConnectionRequest{
		Driver:  "unrelated-driver",
		DSN:     "opaque",
		Options: options,
	})
	if err != nil || untouched.DSN != "opaque" || len(untouched.Handled) != 0 {
		t.Fatalf("adapter applied to wrong driver: %+v, %v", untouched, err)
	}
	if err := Register("mysql-adapter-test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		dialect.UnregisterConnectionConfigurer("mysql", "mysql-adapter-test")
	})

	configured, err := dialect.WithFeatures(dialect.WithVersion(dialect.MySQL, 8, 0, 31), nil, nil).
		ConfigureConnection(dialect.ConnectionRequest{
			Driver:  "mysql-adapter-test",
			DSN:     "/db",
			Options: options,
		})
	if err != nil || len(configured.Handled) != 1 {
		t.Fatalf("decorated dialect lost connection configuration: %+v, %v", configured, err)
	}
}

func prepareMySQLTimezoneDSN(dsn string, loc *time.Location) (string, error) {
	result, err := ConfigureConnection(dialect.ConnectionRequest{
		Options: dialect.ConnectionOptions{dialect.OptionLocation: loc},
		Driver:  "mysql", DSN: dsn,
	})
	return result.DSN, err
}
