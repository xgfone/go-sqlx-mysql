# MySQL connection adapter

`github.com/xgfone/go-sqlx-mysql` is an independent MySQL connection adapter for
[SQLx](https://github.com/xgfone/go-sqlx). It uses `github.com/go-sql-driver/mysql`
directly, while SQLx's core module has no third-party dependencies. Importing this
adapter registers both the official `database/sql` MySQL driver and a connection
configurer for SQLx's built-in `mysql` dialect and actual `mysql` driver.

Go 1.27 or newer is required. Consumers import and require
`github.com/xgfone/go-sqlx-mysql`; its package name is `mysql`.

```go
import (
    "time"

    "github.com/xgfone/go-sqlx"
    "github.com/xgfone/go-sqlx/dialect"
    _ "github.com/xgfone/go-sqlx-mysql"
)

pool, err := sqlx.OpenWithOptions("", "mysql://user:password@tcp(host:3306)/billing", sqlx.OpenOptions{
    Connection: dialect.ConnectionOptions{dialect.OptionLocation: time.UTC},
    Required: []string{dialect.OptionLocation},
})
```

The adapter uses the official driver's `ParseDSN` and `Config.FormatDSN` itself;
no function injection or separate driver import is needed. SQLx passes the native
DSN without its metadata prefix. Driver settings, including `parseTime`, are
preserved rather than enabled implicitly.

## Time zone policy

`ConfigureConnection` handles only `OptionLocation` (`*time.Location`). Unknown
keys, missing locations, and nil locations leave the native DSN untouched and do
not appear in `Handled`. An invalid value type is an error. Nothing implicitly
selects `time.Local` or changes application/scanner time settings.

A supplied application location always overrides native `loc`. The adapter
privately captures the initial `time.Local` pointer during package initialization.
Pointer equality with that captured value selects the Local policy; other
locations use their names:

| Location value               | Driver `loc`                             | Session `time_zone`                                   |
| ---------------------------- | ---------------------------------------- | ----------------------------------------------------- |
| Initial `time.Local` pointer | Explicit `Local`, regardless of its name | Preserve existing values; do not add a missing value. |
| `UTC`                        | Driver's UTC default                     | Override with `'+00:00'`.                             |
| Explicit IANA name           | That name                                | Override with the quoted name.                        |
| Empty or unsupported name    | Error                                    | Error                                                 |

The adapter parses the DSN with the official driver, updates its configuration,
then calls `Config.FormatDSN`. UTC and IANA names overwrite conflicting existing
`time_zone` values, including case variants of that plain parameter. Local
preserves all session parameters as parsed, including their case variants. Time
zone aliases and historical rules are not compared. Use the driver's standard
parameter names rather than alternative SQL variable spellings.

Automatic synchronization accepts `time.UTC` and explicit IANA locations loaded
with `time.LoadLocation`, such as `Asia/Shanghai` or `America/New_York`. The
location is represented and resolved by its name. The captured system location
only configures the driver; matching the database session time zone is the
caller's responsibility. If its `time_zone` is absent, MySQL uses its normal
session default. Unnamed zones and fixed offsets without a loadable IANA name are
unsupported. A separately created location named `Local` is rejected; its name
does not identify it as the initial system location. No system files or `TZ`
values are inspected to discover a name.

Reassigning `time.Local` does not change the captured pointer. The Local branch
explicitly serializes `loc=Local`, even if the captured location's name is UTC or
an IANA name. A different pointer with the same UTC/IANA name receives automatic
synchronization, including one assigned to `time.Local` by application startup.
The adapter never compares against the mutable current `time.Local` to identify
Local. Names are still validated; an empty name is always an error.

The driver's `loc=Local` alias resolves the current `time.Local` when it parses
the DSN; capturing a pointer does not freeze that alias. Configure the process
location before opening pools and retain the setting throughout their lifetime.

UTC uses the driver's default UTC location and session `time_zone='+00:00'`,
requiring no MySQL time zone tables. `FormatDSN` normally omits `loc=UTC`;
that default is preserved. Every other IANA name is used unchanged for both
settings, including `Etc/UTC` and `Etc/GMT-8`, and needs populated MySQL time
zone tables. Named zones are never reduced to today's numeric offset.

For domestic deployments, configure `Asia/Shanghai`; overseas deployments can
use `UTC`. Prefer setting only the application location and leaving native time
zone parameters out of the DSN. Other driver options, including `parseTime`, are
preserved. The original DSN must still be parseable: an invalid native `loc` or
other malformed driver parameter fails before overrides can be applied. Errors
from native parsing do not expose the DSN or credentials.

Call `Register(driverName)` for a wrapper driver with the same native DSN and
per-connection `loc`/`time_zone` semantics as the official MySQL driver. This
does not register that wrapper SQL driver. Sharing only MySQL's SQL syntax is
insufficient; unregistered driver names receive no MySQL DSN transformation.

Settings are encoded in the native DSN and applied by the driver on every new
physical connection, including reconnects. `OpenWithOptions` creates a lazy pool;
applications should ping it before completing startup so missing time zone tables
or other server rejection is reported before serving requests.
