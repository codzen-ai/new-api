---
status: accepted
---

# The MuleRouter plugin declares public model names instead of a channel model mapping

A channel `model_mapping` from each public model name to its MuleRouter model name made the host record the MuleRouter model name as the task's upstream model, and the host shows that value to the caller in the task's properties and in the usage log (#27). Hiding it there would mean patching host code that upstream owns, so the plugin now declares the public model names itself and maps each one to its MuleRouter endpoint internally, and MuleRouter channels carry no `model_mapping`. The plugin accepts only public model names, with no fallback to MuleRouter model names: the change shipped while only staging test tasks existed, so tasks still running at cutover were allowed to fail.

## Consequences

- Renaming a public model name is now a plugin code change, like adding a MuleRouter endpoint already was (ADR-0001).
- A channel that maps a public model name to a MuleRouter model name is refused, because the plugin does not declare MuleRouter model names. A mapping from an alias to a public model name still works and exposes only the public model name.
- `GET /v1/video/generations/{id}` and the plugin key in `platform` still expose MuleRouter; both need host or migration work and are out of scope here.
