---
status: accepted
---

# MaaS Seedance is a separate product, not another source of doubao Seedance

MaaS serves Seedance 2.x from Volcengine Ark (its result links point at Ark's `doubao-seedance-2-5` storage) under its own model names and an OpenAI-compatible `/v1/videos` API. We considered selling it as one more source of the doubao plugin's Seedance models: the same model names, shared prices, and channels that back each other up. We rejected that. MaaS takes a different request format, bills in its own currency at its own token prices, and offers a different set of inputs (no asset references from callers, no audio inputs yet), so a shared model would need one price for two cost bases and would fail differently depending on which channel won. The mgtv plugin instead declares its own public model names (`seedance-2.5`, `seedance-2.0-pro`, `seedance-2.0-fast`, `seedance-2.0-mini`), priced on their own, as the mulerouter plugin does (ADR-0002).

## Consequences

- A `doubao-seedance-*` request never reaches a MaaS channel and a `seedance-*` request never reaches Ark; neither backs up the other.
- MaaS channels list only the public model names and carry no `model_mapping`; a mapping to a MaaS model name is refused, an alias to a public model name works.
- A failed task's reason is MaaS's `error.message` as it is, so callers can tell whether to retry. That message is written by MaaS and may give the source away; filtering it is out of scope.
- The plugin key `mgtv` still shows in a task's `platform`, as for mulerouter (ADR-0002).
