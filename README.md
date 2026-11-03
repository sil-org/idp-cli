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
