# new-api

An AI API gateway that relays client requests to many upstream AI providers and bills each request against the caller's quota.

## Asynchronous tasks

**Task plugin**:
A sandboxed JavaScript module that drives one upstream provider's asynchronous task API (submit, poll, settle) on behalf of the gateway.
_Avoid_: Task adaptor (the built-in Go form it replaced)

**Plugin route**:
An HTTP entry point on this gateway that a task plugin declares, through which clients reach that plugin directly in the provider's own wire format.
_Avoid_: Route (unqualified), endpoint

**MuleRouter endpoint**:
One upstream MuleRouter operation, addressed by vendor, model and action, together with the request fields it prices or bounds: a field that scales its price, or one whose value it limits without pricing it. Each MuleRouter endpoint is billed as exactly one model.
_Avoid_: Route, route row, MuleRouter route

**MuleRouter model name**:
The upstream identity of a MuleRouter endpoint, spelled `{vendor}/{model}/{action}`. Clients never send or see it; they reach it through a public model name.
_Avoid_: Internal model name, full model name

**Public model name**:
The short, vendor-neutral name a client sends for a model that a task plugin declares and maps to the upstream's own name internally: a MuleRouter endpoint in the mulerouter plugin, such as `wan2.2-i2v-spicy`, or a MaaS model in the mgtv plugin, such as `seedance-2.5`. Prices and model permissions are keyed by it.
_Avoid_: Alias, short name, client model

**MaaS**:
MGTV Cloud's model service at `maas-api-pub.cloud.mgtv.com`, which the mgtv task plugin drives through its OpenAI-compatible `/v1/videos` API. Its video models are Seedance 2.x from Volcengine Ark under MaaS model names such as `msd-2-5-260628`, which clients never send or see; the gateway sells them as their own product, apart from the doubao plugin's Seedance models (ADR-0003).
_Avoid_: MGTV (the company), MSD, Mango

**Vendor parameter**:
A request field specific to one MuleRouter endpoint (such as width or prompt_extend), outside the gateway's unified task vocabulary. Clients send vendor parameters at the top level of the request or under `metadata`; both count.
_Avoid_: Vendor field, extra parameter
