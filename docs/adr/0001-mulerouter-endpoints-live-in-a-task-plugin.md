---
status: accepted
---

# MuleRouter endpoints live in a task plugin, not in channel configuration

MuleRouter was kept as the one built-in Go task adaptor beside the task plugin system so that its MuleRouter endpoints could stay channel configuration (specifications/implemented/mulerouter-async-task-gateway.md §12). We are moving it onto a built-in task plugin and writing the endpoint list, with each endpoint's bounded billing fields, into the plugin's code. The Go adaptor touched upstream hot spots (distributor, task relay, controller, adaptor lookup, a channel type, the channel form), and every rebase onto upstream paid for it. A plugin cannot read channel-level settings, so keeping the list in channel configuration would have meant patching the plugin host, which has the same rebase cost. Adding a MuleRouter endpoint therefore becomes a reviewed code change instead of a form edit.

## Consequences

- Pricing stays per-call by MuleRouter model name times request-field ratios; the plugin reproduces the ratios, so charges do not change.
- Clients reach MuleRouter only through the unified task route. The vendor-native `/vendors/...` path is dropped, and vendor parameters must be sent under `metadata`, because the plugin host passes only the unified request fields to a plugin on that route.
