// MuleRouter endpoints, keyed by MuleRouter model name ({vendor}/{model}/{action}).
// A channel model_mapping turns the public model name into one of these keys;
// prices stay keyed by the public model name.
//
// Each endpoint lists its billing fields with the semantics of the Go channel's
// billing_vars, so a request is charged exactly what the Go channel charges:
// - int: an integer bounded by min/max or enum; the multiplier is value / divisor,
//   and no divisor means the field is bounds-checked but not priced.
// - enum: the stringified value looks up its multiplier in values.
// - default (a string) bills an omitted or null field; source names the request
//   field when it differs from the multiplier name.
const ENDPOINTS = {
  "carrothub/z-image-spicy/generation": {
    billing: [
      // MuleRouter adds a flat fee for prompt rewriting: $0.001 on a $0.013 image.
      { name: "prompt_extend", kind: "enum", values: { true: 1.076923, false: 1 }, default: "true" },
      { name: "width", kind: "int", min: 256, max: 1536, default: "1024" },
      { name: "height", kind: "int", min: 256, max: 1536, default: "1536" },
    ],
  },
  // Image edit: one flat price per call.
  "carrothub/qwen-image-edit-spicy/generation": { billing: [] },
  // Image to video, priced per second of output and by resolution.
  "carrothub/wan2.2-i2v-spicy/generation": {
    billing: [
      // An omitted duration bills 5 seconds, as in the Go channel. The host
      // decodes a top-level duration into an integer and drops one it cannot
      // read (5.5, "eight", 0), so such a request also bills 5 seconds and
      // sends no duration, where the Go channel refuses it.
      { name: "seconds", source: "duration", kind: "int", enum: [5, 8], default: "5", divisor: 1 },
      { name: "resolution", kind: "enum", values: { "480p": 1, "720p": 2 }, default: "480p" },
    ],
  },
};

// The gateway's startup migration reads this table from the loaded plugin and
// moves a Go MuleRouter channel onto the plugin only when every MuleRouter
// endpoint the channel configures is declared here with the same billing
// fields. No hook reads it.
export const muleRouterEndpoints = ENDPOINTS;

// Request fields known to scale MuleRouter's cost. An endpoint that does not
// declare one of them refuses it, so an unpriced dimension never reaches the
// vendor as an unbounded multiplier.
const COST_FIELDS = [
  "n",
  "count",
  "num_images",
  "num_outputs",
  "batch_size",
  "duration",
  "seconds",
  "resolution",
  "size",
  "width",
  "height",
  "quality",
  "fps",
  "frames",
  "num_frames",
  "steps",
  "sample_count",
];

// Fields that configure the gateway, never the vendor endpoint.
const CONTROL_FIELDS = ["model", "metadata"];

export const meta = {
  apiVersion: 1,
  key: "mulerouter",
  name: "MuleRouter",
  description: {
    en: "Asynchronous image and video generation through MuleRouter",
    zh: "通过 MuleRouter 异步生成图片和视频",
  },
  version: "1.0.0",
  author: { name: "codzen" },
  baseUrl: "https://api.mulerouter.ai",
  upstreams: ["vendor"],
  models: Object.keys(ENDPOINTS),
  fetchMode: "per_task",
};

function upstreamPath(modelName) {
  const parts = String(modelName || "")
    .trim()
    .split("/");
  if (parts.length !== 3 || parts.some((part) => part === "")) return "";
  return "/vendors/" + parts[0] + "/v1/" + parts[1] + "/" + parts[2];
}

// Error messages reach the caller, who must not learn the vendor or the
// MuleRouter model name behind the public model name.
function endpointFor(ctx) {
  const name = ctx.upstreamModel || ctx.model;
  if (!Object.prototype.hasOwnProperty.call(ENDPOINTS, name)) throw new Error("this model is not available on this channel");
  return { name: name, billing: ENDPOINTS[name].billing };
}

// The upstream body is the request minus the gateway's own fields, with vendor
// parameters from metadata merged over it.
function upstreamParams(requestBody) {
  const params = {};
  const body = requestBody || {};
  for (const key of Object.keys(body)) params[key] = body[key];
  // The host copies a lone image into images ([image]) before the plugin runs.
  // MuleRouter reads the field the client wrote, so only image goes upstream.
  // A client that itself sends both with the same single URL loses images,
  // which it cannot be told apart from; neither field is priced.
  const images = params.images;
  if (typeof params.image === "string" && Array.isArray(images) && images.length === 1 && images[0] === params.image) {
    delete params.images;
  }
  const metadata = body.metadata;
  if (metadata && typeof metadata === "object" && !Array.isArray(metadata)) {
    for (const key of Object.keys(metadata)) params[key] = metadata[key];
  }
  for (const key of CONTROL_FIELDS) delete params[key];
  return params;
}

function quoted(value) {
  return JSON.stringify(String(value));
}

// fieldRatio bounds one billing field, read from the request field source,
// and returns its multiplier; 0 means the field is valid but does not affect
// the price.
function fieldRatio(field, source, raw) {
  if (field.kind === "enum") {
    const key = String(raw);
    if (!Object.prototype.hasOwnProperty.call(field.values, key)) {
      throw new Error("invalid value " + quoted(key) + " for " + source + ", allowed: " + Object.keys(field.values).sort().join(", "));
    }
    return field.values[key];
  }
  let value = raw;
  if (typeof raw === "string") {
    const text = raw.trim();
    if (!/^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$/.test(text)) {
      throw new Error("invalid value for " + source + ": " + quoted(raw) + " is not a number");
    }
    value = Number(text);
  } else if (typeof raw !== "number") {
    throw new Error("invalid value for " + source + ": " + String(raw) + " is not a number");
  }
  if (!Number.isInteger(value)) throw new Error(source + " must be an integer, got " + String(raw));
  if (field.enum && field.enum.indexOf(value) < 0) {
    throw new Error("invalid value " + String(value) + " for " + source + ", allowed: " + field.enum.map(String).join(", "));
  }
  if (field.min !== undefined && value < field.min) throw new Error(source + " must be >= " + String(field.min));
  if (field.max !== undefined && value > field.max) throw new Error(source + " must be <= " + String(field.max));
  if (!field.divisor) return 0;
  const ratio = value / field.divisor;
  if (!(ratio > 0) || !Number.isFinite(ratio)) throw new Error(source + " produced a non-positive billing multiplier");
  return ratio;
}

// billingRatios validates the body that will be sent upstream and returns the
// multipliers it contributes, so metadata cannot carry a multiplier past it.
function billingRatios(endpoint, params) {
  const declared = {};
  const ratios = {};
  for (const field of endpoint.billing) {
    const source = field.source || field.name;
    declared[source] = true;
    let raw = params[source];
    if (raw === undefined || raw === null) raw = field.default;
    const ratio = fieldRatio(field, source, raw);
    if (ratio > 0) ratios[field.name] = ratio;
  }
  const undeclared = Object.keys(params)
    .filter((key) => !declared[key] && params[key] !== null && params[key] !== undefined && COST_FIELDS.indexOf(key) >= 0)
    .sort();
  if (undeclared.length) {
    throw new Error(
      "parameter(s) " + undeclared.join(", ") + " affect the price of this model and are not enabled on this endpoint; remove them"
    );
  }
  return ratios;
}

export function buildSubmitRequest(ctx) {
  const endpoint = endpointFor(ctx);
  const params = upstreamParams(ctx.requestBody);
  billingRatios(endpoint, params);
  return {
    url: ctx.baseUrl + upstreamPath(endpoint.name),
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json", Authorization: "Bearer " + ctx.apiKey },
    body: params,
  };
}

// Per-call pricing: the public model's fixed price times these multipliers.
export function extractUsage(ctx) {
  if (ctx.usagePurpose !== "billing_ratios") return null;
  return billingRatios(endpointFor(ctx), upstreamParams(ctx.requestBody));
}

function errorMessage(error) {
  if (!error) return "";
  if (error.title && error.detail) return error.title + ": " + error.detail;
  return error.title || error.detail || "";
}

export function parseSubmitResponse(_ctx, resp) {
  const info = resp.body && resp.body.task_info;
  if (!info || !info.id) throw new Error("upstream returned no task id");
  if (info.status === "failed") throw new Error("task failed: " + errorMessage(info.error));
  return { taskId: info.id, taskData: resp.body };
}

// MuleRouter files a task under its model's path, so the stored model name
// rebuilds the query URL.
export function buildQueryRequest(ctx) {
  const path = upstreamPath(ctx.upstreamModel || ctx.model);
  if (!path) throw new Error("cannot rebuild the task query URL");
  return {
    url: ctx.baseUrl + path + "/" + ctx.taskId,
    method: "GET",
    headers: { Accept: "application/json", Authorization: "Bearer " + ctx.apiKey },
  };
}

const TASK_STATUSES = { pending: "SUBMITTED", processing: "IN_PROGRESS", completed: "SUCCESS", failed: "FAILURE" };

// A completed task carries its results under images, videos or audios.
function resultURL(body) {
  for (const key of ["images", "videos", "audios"]) {
    if (Array.isArray(body[key]) && body[key].length) return body[key][0];
  }
  return "";
}

export function parseTaskResult(_ctx, body) {
  const info = (body && body.task_info) || {};
  const status = TASK_STATUSES[info.status];
  if (!status) return { status: "UNKNOWN", reason: "unknown task status: " + String(info.status || "") };
  if (status === "SUCCESS") {
    const url = resultURL(body);
    return url ? { status: status, url: url } : { status: status };
  }
  if (status === "FAILURE") return { status: status, reason: errorMessage(info.error) || "task failed" };
  return { status: status };
}
