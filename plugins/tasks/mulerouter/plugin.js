// MuleRouter endpoints, keyed by MuleRouter model name ({vendor}/{model}/{action}).
// A channel model_mapping turns the public model name into one of these keys;
// prices stay keyed by the public model name, and the host charges that price
// times the multipliers the endpoint reports.
//
// Each endpoint declares what it produces (output: "image" or "video") and, in
// fields, every request field that affects the price or must be bounded, keyed
// by the field name the client and MuleRouter use:
// - type "enum": the value, read as a string, must be a key of values.
// - type "integer": an integer, written as a number or a numeric string, that
//   must be a key of values when values is given, else lie in [min, max].
// - A priced field names its multiplier and maps each allowed value to the
//   multiplier it reports in values; an omitted or null priced field bills
//   default. A field without a multiplier is only bounded.
const ENDPOINTS = {
  "carrothub/z-image-spicy/generation": {
    output: "image",
    fields: {
      // MuleRouter adds a flat fee for prompt rewriting: $0.001 on a $0.013 image.
      prompt_extend: { type: "enum", values: { true: 1.076923, false: 1 }, default: true, multiplier: "prompt_extend" },
      width: { type: "integer", min: 256, max: 1536 },
      height: { type: "integer", min: 256, max: 1536 },
    },
  },
  // Image edit: one flat price per call.
  "carrothub/qwen-image-edit-spicy/generation": { output: "image", fields: {} },
  // Image to video, priced per second of output and by resolution.
  "carrothub/wan2.2-i2v-spicy/generation": {
    output: "video",
    fields: {
      duration: { type: "integer", values: { 5: 5, 8: 8 }, default: 5, multiplier: "seconds" },
      resolution: { type: "enum", values: { "480p": 1, "720p": 2 }, default: "480p", multiplier: "resolution" },
    },
  },
};

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
  version: "1.1.0",
  author: { name: "codzen" },
  baseUrl: "https://api.mulerouter.ai",
  upstreams: ["vendor"],
  models: Object.keys(ENDPOINTS),
  fetchMode: "per_task",
  // The OpenAI video API serves only the endpoints that produce videos.
  protocols: [{ name: "openai_video", models: Object.keys(ENDPOINTS).filter((name) => ENDPOINTS[name].output === "video") }],
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
  return { name: name, fields: ENDPOINTS[name].fields };
}

// The upstream body is the request minus the gateway's own fields, with vendor
// parameters from metadata merged over it.
function upstreamParams(requestBody) {
  const params = {};
  const body = requestBody || {};
  for (const key of Object.keys(body)) params[key] = body[key];
  // On POST /v1/videos taskRequestBody copies a lone image into images
  // ([image]), as the host's task request did for the Go channel, and the Go
  // adaptor sent only the image the client wrote; dropping the copy here sends
  // the same body. POST /v1/tasks/mulerouter hands over the client's body
  // unchanged, so there the rule only matters to a client that itself sends
  // image and images with the same single URL: the Go adaptor sent both, the
  // plugin sends only image. Both name the same single input image and
  // neither field is priced, so the charge is the same.
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

function hasKey(object, key) {
  return Object.prototype.hasOwnProperty.call(object, key);
}

// allowedValue checks a field value against the endpoint's capabilities and
// returns the key of values it is priced by.
function allowedValue(field, name, raw) {
  if (field.type === "enum") {
    const key = String(raw);
    if (!hasKey(field.values, key)) {
      throw new Error("invalid value " + quoted(key) + " for " + name + ", allowed: " + Object.keys(field.values).sort().join(", "));
    }
    return key;
  }
  let value = raw;
  if (typeof raw === "string") {
    const text = raw.trim();
    if (!/^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$/.test(text)) {
      throw new Error("invalid value for " + name + ": " + quoted(raw) + " is not a number");
    }
    value = Number(text);
  } else if (typeof raw !== "number") {
    throw new Error("invalid value for " + name + ": " + String(raw) + " is not a number");
  }
  if (!Number.isInteger(value)) throw new Error(name + " must be an integer, got " + String(raw));
  if (field.values) {
    if (!hasKey(field.values, String(value))) {
      const allowed = Object.keys(field.values).map(Number).sort((a, b) => a - b);
      throw new Error("invalid value " + String(value) + " for " + name + ", allowed: " + allowed.join(", "));
    }
    return String(value);
  }
  if (value < field.min) throw new Error(name + " must be >= " + String(field.min));
  if (value > field.max) throw new Error(name + " must be <= " + String(field.max));
  return String(value);
}

// billingRatios validates the body that will be sent upstream against the
// endpoint's capabilities and returns the multipliers it contributes, so
// metadata cannot carry a multiplier past it.
function billingRatios(endpoint, params) {
  const ratios = {};
  for (const name of Object.keys(endpoint.fields)) {
    const field = endpoint.fields[name];
    let raw = params[name];
    if (raw === undefined || raw === null) {
      if (!field.multiplier) continue;
      raw = field.default;
    }
    const key = allowedValue(field, name, raw);
    if (!field.multiplier) continue;
    const ratio = field.values[key];
    if (!(ratio > 0) || !Number.isFinite(ratio)) throw new Error(name + " produced a non-positive billing multiplier");
    ratios[field.multiplier] = ratio;
  }
  const undeclared = Object.keys(params)
    .filter((key) => !hasKey(endpoint.fields, key) && params[key] !== null && params[key] !== undefined && COST_FIELDS.indexOf(key) >= 0)
    .sort();
  if (undeclared.length) {
    throw new Error(
      "parameter(s) " + undeclared.join(", ") + " affect the price of this model and are not enabled on this endpoint; remove them"
    );
  }
  return ratios;
}

// Every MuleRouter endpoint takes a prompt. buildSubmitRequest applies it on every entry,
// before quota is reserved; the openai_video decoder applies it again so POST
// /v1/videos refuses the request already at channel selection.
function requirePrompt(body) {
  if (typeof body.prompt !== "string" || body.prompt.trim() === "") throw new Error("prompt is required");
}

export function buildSubmitRequest(ctx) {
  const endpoint = endpointFor(ctx);
  const params = upstreamParams(ctx.requestBody);
  requirePrompt(params);
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

function videoURL(data) {
  return data && Array.isArray(data.videos) && typeof data.videos[0] === "string" ? data.videos[0] : "";
}

function imageURLs(data) {
  if (!data || !Array.isArray(data.images)) return [];
  return data.images.filter((url) => typeof url === "string" && url !== "");
}

// An image artifact key is this prefix and the image's 1-based position, with
// no leading zeros: image-1, image-2, ... listArtifacts writes it and
// buildContentRequest reads it back.
const IMAGE_ARTIFACT_PREFIX = "image-";

// A completed task's results are its artifacts: every image as image-1,
// image-2, ... in MuleRouter's order, or the first video as video, which
// GET /v1/videos/{task_id}/content also streams. The URLs are MuleRouter's
// public result links and need no credentials. The host refuses to load a
// plugin that claims openai_video without these two hooks.
export function listArtifacts(task) {
  if (task.status !== "SUCCESS") return [];
  const images = imageURLs(task.data);
  if (images.length) return images.map((_, index) => ({ key: IMAGE_ARTIFACT_PREFIX + (index + 1), type: "image" }));
  return videoURL(task.data) ? [{ key: "video", type: "video" }] : [];
}

export function buildContentRequest(ctx) {
  const key = String(ctx.artifactKey || "");
  let url = "";
  if (key === "video") url = videoURL(ctx.data);
  else if (key.startsWith(IMAGE_ARTIFACT_PREFIX)) {
    const position = key.slice(IMAGE_ARTIFACT_PREFIX.length);
    if (/^[1-9][0-9]*$/.test(position)) url = imageURLs(ctx.data)[Number(position) - 1] || "";
  }
  if (!url) throw new Error("artifact_not_found");
  return { url: url, method: ctx.clientRequest.method, credentialless: true };
}

// TASK_REQUEST_STRINGS and taskRequestBody keep, of a POST /v1/videos body,
// the fields of the host's task request (TaskSubmitReq in
// relay/common/relay_info.go), which the Go adaptor's task route accepted:
// only those fields survive, so on this API vendor parameters reach the
// endpoint only through metadata. POST /v1/tasks/mulerouter instead hands the
// plugin the client's whole body.
const TASK_REQUEST_STRINGS = ["model", "mode", "image", "size", "seconds", "input_reference"];

// A duration that cannot be read as an integer is dropped, as the host drops
// it from a task request.
function taskDuration(raw) {
  if (typeof raw === "number") return Number.isInteger(raw) ? raw : 0;
  if (typeof raw === "string" && /^[+-]?\d+$/.test(raw)) return Number(raw);
  return 0;
}

function taskMetadata(raw) {
  let metadata = raw;
  if (typeof raw === "string" && raw !== "") {
    try {
      metadata = JSON.parse(raw);
    } catch (_error) {
      return null;
    }
  }
  if (!metadata || typeof metadata !== "object" || Array.isArray(metadata) || !Object.keys(metadata).length) return null;
  return metadata;
}

function taskRequestBody(value) {
  const body = { prompt: value.prompt };
  for (const key of TASK_REQUEST_STRINGS) {
    const field = value[key];
    if (field === undefined || field === null) continue;
    if (typeof field !== "string") throw new Error(key + " must be a string");
    if (field !== "") body[key] = field;
  }
  if (value.images !== undefined && value.images !== null) {
    if (!Array.isArray(value.images) || value.images.some((image) => typeof image !== "string")) {
      throw new Error("images must be an array of strings");
    }
    if (value.images.length) body.images = value.images.slice();
  }
  // The host's task request copies a lone image into images.
  if (!body.images && typeof body.image === "string" && body.image.trim() !== "") body.images = [body.image];
  const duration = taskDuration(value.duration);
  if (duration !== 0) body.duration = duration;
  const metadata = taskMetadata(value.metadata);
  if (metadata) body.metadata = utils.json.clone(metadata);
  return body;
}

export const protocols = {
  openai_video: {
    // POST /v1/videos takes the task request body and hands the endpoint the
    // request the Go adaptor built from it, so it is sent and charged as the
    // Go channel sent and charged it: vendor parameters go under metadata,
    // and the OpenAI seconds and size fields are cost fields the endpoint
    // refuses.
    decodeRequest(ctx) {
      const body = ctx.body;
      if (!body || body.kind !== "json") throw new Error("a JSON request body is required");
      const value = body.value;
      if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("the request body must be a JSON object");
      requirePrompt(value);
      const requestBody = taskRequestBody(value);
      requestBody.model = ctx.model;
      // The Go adaptor filed every MuleRouter task under image_to_video.
      return { kind: "submit", model: ctx.model, action: "image_to_video", requestBody: requestBody };
    },
    // The host fills in id, object, model, status, progress and the
    // timestamps; the plugin adds the result URL and the failure.
    render(_ctx, task) {
      const data = task.data || {};
      for (const key of ["images", "audios"]) {
        if (Array.isArray(data[key]) && data[key].length) throw new Error("this model does not produce videos");
      }
      const video = {};
      const url = videoURL(data);
      if (url) video.metadata = { url: url };
      if (task.status === "FAILURE") video.error = { message: task.fail_reason || "", code: "task_failed" };
      return video;
    },
  },
};
