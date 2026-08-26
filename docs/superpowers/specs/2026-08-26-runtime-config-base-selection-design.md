# Runtime Configuration and Base Selection Design

## Goal

Use the persisted application configuration during startup, make `--base` select a configured notes base, and derive the default configuration directory through the operating system's XDG-aware configuration lookup.

The setup wizard, runtime base switching, Git synchronization, and per-base metadata databases are outside this change.

## Configuration Location

The `--config` flag continues to represent a configuration directory. When it is explicitly provided, the application uses that directory and reads `<directory>/config.json`.

When `--config` is omitted, the application calls `os.UserConfigDir()` and uses `<user-config-dir>/igonotes/config.json`. On Unix this honors `XDG_CONFIG_HOME` when it is set and otherwise uses the platform default. It also gives Windows and macOS their native user configuration locations.

Failure to determine the user configuration directory stops startup with a descriptive error. An explicit `--config` value does not require that lookup.

## Startup Resolver

A startup resolver in `internal/service` coordinates configuration initialization and base selection. `ConfigService` remains responsible only for loading and saving JSON. `cmd/api/main.go` remains responsible for parsing flags and wiring dependencies.

The resolver receives:

- the `ConfigService` for the selected `config.json`;
- the optional value of `--base`;
- the application data directory used for first-run defaults.

It returns the absolute or configured filesystem path of the selected notes base, or an error that prevents server startup.

## First-Run Initialization

When `config.json` does not exist or is empty, the resolver creates a usable default configuration. It creates the default base directory and saves the following logical values, using absolute paths rather than a literal `~`:

```json
{
  "base_dir": "/home/user/.igonotes/bases",
  "bases": [
    {
      "name": "default",
      "path": "/home/user/.igonotes/bases/default",
      "auto_sync": false
    }
  ],
  "current_base": "default"
}
```

Only this first-run default directory is created automatically. Paths referenced by an existing configuration are treated as deliberate user settings and are never created implicitly.

Malformed, unreadable, or otherwise invalid existing configuration is not replaced by the default configuration.

## Base Selection

Selection follows this precedence:

1. A non-empty `--base NAME` selects the entry in `config.bases` whose `name` exactly equals `NAME`.
2. Otherwise, `config.current_base` selects the entry with that name.

Base names are case-sensitive and must be unique. `config.base_dir` is persisted for configuration and future UI use, but it is not used to synthesize paths for named entries. Each selected base must have its own non-empty `path`.

The selected path is passed to `service.NewNoteService`; the temporary hard-coded `bases/default` path in `main.go` is removed.

The selected CLI base is an invocation-only override. Startup does not modify `current_base` in the persisted configuration.

## Validation and Errors

Startup fails before the HTTP server is started when:

- `config.json` contains invalid JSON;
- multiple configured bases have the same name;
- `--base` names no configured base;
- `current_base` is empty or names no configured base when `--base` is absent;
- the selected base has an empty name or path;
- the selected path does not exist;
- the selected path exists but is not a directory;
- the selected path cannot be inspected due to a filesystem error.

An unknown `--base` error includes the requested name and the available configured names. Other validation errors identify the relevant configuration field or path. Errors from startup resolution are wrapped with enough context for `main.go` to report them through `log.Fatal`.

## Existing Behavior

The SQLite metadata database remains at `~/.igonotes/metadata.db`, and the first-run notes base remains under `~/.igonotes/bases/default`. The current application data directory behavior is unchanged; only the configuration directory becomes platform- and XDG-aware.

The `/api/config` endpoint and JSON schema remain unchanged. Saving configuration through that endpoint does not switch the active `NoteService` during the current process; the new selection takes effect on the next application start.

## Testing

Unit tests cover configuration directory resolution and startup base resolution with temporary directories:

- explicit `--config` precedence;
- default `os.UserConfigDir()/igonotes` composition;
- creation and persistence of the first-run default configuration;
- initialization from an empty configuration file;
- selection by `--base` over `current_base`;
- selection by `current_base` without `--base`;
- errors for unknown CLI and configured base names;
- errors for duplicate configured base names;
- errors for empty selected names and paths;
- errors for missing paths and non-directory paths;
- propagation of malformed JSON without overwriting it;
- preservation of an existing valid configuration.

Repository verification consists of `go test ./...`, `go vet ./...`, and a full project build. No HTTP API or frontend behavior changes are required for this feature.
