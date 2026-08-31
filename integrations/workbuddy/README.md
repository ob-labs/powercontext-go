# WorkBuddy integration

The WorkBuddy adapter recalls bounded project context before a submitted user
prompt and captures that prompt as a PowerContext Source. It also registers the
PowerContext Streamable HTTP MCP endpoint. Hook failures always leave the host
prompt flow intact.

Install the checked-in adapter with:

```sh
powercontext setup workbuddy
```

Installation writes a credential-free configuration at
`$WORKBUDDY_HOME/powercontext.json` (or `~/.workbuddy/powercontext.json`). It
contains the Server URL, scope mode, request limits, and the name of the
optional authorization environment variable. The authorization value itself is
read only when the Hook or MCP client runs and is never written by setup.

Run `powercontext doctor workbuddy` to check the installed Python Hook,
configuration, MCP registration, Skill ownership, and the configured Server
health endpoints. It reports fixed diagnostic categories and does not print
configuration credentials, prompt content, retrieved context, or scope IDs.

Adapter-only tests run without a WorkBuddy installation:

```sh
uv run --with pytest pytest integrations/workbuddy/tests
```
