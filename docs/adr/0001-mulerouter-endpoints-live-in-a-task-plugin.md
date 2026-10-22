---
status: accepted
---

# MuleRouter endpoints live in a task plugin, not in channel configuration

MuleRouter was kept as the one built-in Go task adaptor beside the task plugin system so that its MuleRouter endpoints could stay channel configuration (specifications/implemented/mulerouter-async-task-gateway.md §12). We are moving it onto a built-in task plugin and writing the endpoint list, with each endpoint's bounded billing fields, into the plugin's code. The Go adaptor touched upstream hot spots (distributor, task relay, controller, adaptor lookup, a channel type, the channel form), and every rebase onto upstream paid for it. A plugin cannot read channel-level settings, so keeping the list in channel configuration would have meant patching the plugin host, which has the same rebase cost. Adding a MuleRouter endpoint therefore becomes a reviewed code change instead of a form edit.

## Consequences

- Pricing stays per-call by MuleRouter model name times request-field ratios; the plugin reproduces the ratios, so charges do not change.
- Clients submit MuleRouter tasks on `POST /v1/tasks/mulerouter` (videos also on `POST /v1/videos`) and read them on `GET /v1/tasks/{id}` and its artifacts. `POST /v1/video/generations` does not reach a Task Plugin channel, because the host does not pin a plugin on it before choosing a channel. The vendor-native `/vendors/...` path is dropped. Vendor parameters belong under `metadata`: `/v1/tasks/mulerouter` hands the plugin the whole body and also reads them from the top level, but `/v1/videos` keeps only the task request fields.
- Changed by spec #31: `/v1/videos` no longer keeps only the task request fields. It reads vendor parameters at the top level and under `metadata` alike, takes the OpenAI fields `seconds`, `size` and `input_reference`, and refuses fields the MuleRouter endpoint does not take. The rest of this decision stands.
