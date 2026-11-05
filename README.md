# idp-cli
CLI Tool for the IdP-in-a-Box service.

## CLI

The CLI can be used to check the status of the IdP. It can also be used to establish secondary resources
in a second AWS region, and to initiate a secondary region failover action.

Released builds are available in GitHub releases. To build for development, run `make cli`.

### Parameters

All parameters can be set in a config file or environment variables. Some can also be specified as command-line flags.
Command line options take precedence over environment variables, which take precedence over the config file (per
[spf13/viper](https://github.com/spf13/viper#why-viper)). The config file can be specified as a parameter, like
`idp --config idp.toml`. If not specified the current directory is searched for a file with the name `idp-cli.toml`.
The file can be in any of these formats: JSON, TOML, YAML, HCL, envfile. Change the file extension to match the format.
To set a parameter by environment variable, uppercase the parameter name, replace hyphens with underscores, and prefix
with `IDP_` (e.g. `tfc-token` becomes `IDP_TFC_TOKEN`).

## Login log

The `logins` commands query the login audit log that
[ssp-base](https://github.com/sil-org/ssp-base)'s `loginfinalizer:LogToDynamo` filter writes to DynamoDB: one record
per successful login, with the user, the originating SP, the Hub it came through (if any), the IdP, and the time.

```
idp-cli logins list    [filters] [--limit N] [--format table|csv|json]
idp-cli logins summary [filters] [--by sp|hub|idp|day|user] [--top N] [--format table|csv|json]
idp-cli logins tui     [filters]
```

`list` shows the newest 50 logins as a table, but every matching login as CSV or JSON, since those are usually
written to a file; `--limit` overrides either.

`tui` opens an interactive browser: switch between the raw logins and summaries by SP, Hub, IdP, day, and user; press
Enter on a summary row to drill into it (Backspace drills back out), `/` to search the visible rows, Enter on a login
for its full record, `e` to export the current tab to CSV, `f` to edit the filters, and `r` to rescan.

`e` exports what the current tab shows, in the same CSV format as `--format csv`: on the Logins tab, every login in
the current view (filters, drill-downs and search applied, including any beyond the 10,000 rows the table displays);
on a summary tab, its groups. It won't overwrite an existing file, and creates the file readable only by you, since it
contains user identifiers.

The TUI scans only by time range (`--since`/`--until`) and applies the other filters in memory, so changing the SP,
Hub, IdP, or user filter (or narrowing the time range) is instant. Only widening the time range beyond what's loaded
needs a rescan, and the filter form says which will happen before you press Enter. The Logins tab shows the newest
10,000 matching logins; counts and summary tabs always cover all of them.

### Filters

| Flag           | Meaning                                                                                     |
|----------------|---------------------------------------------------------------------------------------------|
| `--since`      | at/after: `24h`, `7d`, `2w`, `2026-10-01`, `"2026-10-01 13:45"`, or RFC 3339                |
| `--until`      | at/before, same forms; a date or a time without seconds includes that whole day or minute   |
| `--sp`         | exact SP entity ID                                                                          |
| `--hub`        | exact Hub entity ID, or `direct` for logins that didn't come through a Hub                  |
| `--idp-entity` | exact IdP entity ID                                                                         |
| `--user`       | exact EmployeeNumber, eduPersonPrincipalName, or CN                                         |

All times are UTC, matching what is stored.

### Examples

```
idp-cli logins summary --since 30d --by hub
idp-cli logins summary --since 2026-10-01 --until 2026-10-31 --by day
idp-cli logins summary --since 7d --by user --top 20
idp-cli logins list --user 10001 --since 90d --format csv > user-10001.csv
idp-cli logins tui --since 7d
idp-cli logins summary --profile idp-audit --region us-east-1 --since 7d --by sp
```

### Parameters

| Parameter         |  Default  | Notes                                                                         |
| ----------------- | --------- | ----------------------------------------------------------------------------- |
| `log-table`       |           | required; the `DYNAMO_LOG_TABLE` the IdP writes to                            |
| `region`          |           | AWS region of the table (or set `AWS_REGION` / use your AWS profile's)        |
| `profile`         |           | AWS profile from `~/.aws/config` / `~/.aws/credentials`, like `aws --profile` |
| `max-rcu`         | `500`     | read capacity units per second the scan may consume; `0` = unlimited          |
| `max-records`     | `1000000` | stop with an error if more logins than this match; `0` = no limit             |
| `scan-segments`   | `4`       | parallel scan workers                                                         |
| `dynamo-endpoint` |           | only for dynamodb-local, e.g. `http://localhost:8000`                         |

AWS credentials come from `--profile` (or `IDP_PROFILE` / `profile` in the config file) when set, and otherwise from
the standard AWS chain (`AWS_PROFILE`, environment variables, SSO, ...). They need `dynamodb:Scan` on the table. With an
SSO profile, run `aws sso login --profile <name>` first.

### Cost and performance

The table is keyed only by a random ID, so **every query is a full-table Scan**. Filters reduce what is transferred,
not the read capacity consumed. For example, a table of about 470k records (one year at about 1,300 logins a day, with
the IdP's one-year expiry) is about 80 MB. A full scan of it consumes about 9,800 RCU, roughly $0.001 on an on-demand
table, and takes about 20 seconds at the default `--max-rcu` of 500.

`--max-rcu` exists to protect **provisioned-capacity** tables, where a fast scan could consume the capacity the IdPs
need to write new login records. On an **on-demand** (`PAY_PER_REQUEST`) table there is no such capacity to protect,
so set `max-rcu = 0` (and, for speed, `scan-segments = 16`) in `idp-cli.toml`. Check the billing mode with
`aws dynamodb describe-table`.

### Memory

Every matching login is held in memory, at about 300 bytes each: about 150 MB for 500k logins, with the process
peaking at about 250 MB. `--max-records` (default 1,000,000) stops a scan that matches more than that with an error
asking you to narrow the time range, rather than exhausting memory. Out-of-memory failures kill the process outright
(on Linux, without an error message) and, in the TUI, leave the terminal in a bad state (run `reset`).

### Local testing

With ssp-base's local dynamodb-local running (`docker compose up -d dynamo` and `docker compose run --rm init-dynamo`
in ssp-base):

```
AWS_ACCESS_KEY_ID=local AWS_SECRET_ACCESS_KEY=local \
  idp-cli logins tui --region us-east-1 --log-table sildisco_local_user-log --dynamo-endpoint http://localhost:8000
```
